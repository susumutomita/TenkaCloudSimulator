package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

// Target and private channel are supplied only by the retained VM owner, never
// by request properties. Resolve must bind the current stack/instance incarnation.
type SSMSessionTarget struct{ Region, StackID, InstanceID string }
type SSMSessionBackend interface {
	ResolveSessionTarget(context.Context, string, string) (SSMSessionTarget, error)
	OpenSessionChannel(context.Context, SSMSessionTarget) (*guestsession.Driver, error)
}
type SSMSessionStore struct {
	db      *sql.DB
	backend SSMSessionBackend
	base    string
	mu      sync.Mutex
	entries map[string]*ssmLiveSession
	closed  bool
}
type ssmLiveSession struct {
	mu             sync.Mutex
	channelGate    sync.RWMutex
	id, region     string
	owner          ssmSessionCaller
	target         SSMSessionTarget
	driver         *guestsession.Driver
	cancelLifetime context.CancelFunc
	deadline       time.Time
	token          [32]byte
	tokenExpires   time.Time
	tokenUsed      bool
	generation     uint64
	channelCancel  context.CancelFunc
	state          string
}
type SSMSessionResponse struct {
	SessionID  string `json:"SessionId"`
	StreamURL  string `json:"StreamUrl"`
	TokenValue string `json:"TokenValue"`
}

func NewSSMSessionStore(db *sql.DB, backend SSMSessionBackend, streamBaseURL string) (*SSMSessionStore, error) {
	u, err := url.Parse(streamBaseURL)
	if db == nil || backend == nil || err != nil || u.Host == "" || (u.Scheme != "ws" && u.Scheme != "wss") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("configured private guest backend and channel URL required")
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS aws_ssm_sessions(id TEXT PRIMARY KEY,region TEXT NOT NULL,stack_id TEXT NOT NULL,instance_id TEXT NOT NULL,owner_tag TEXT NOT NULL,owner_ref TEXT NOT NULL,state TEXT NOT NULL,expires INTEGER NOT NULL)`); err != nil {
		return nil, err
	}
	return &SSMSessionStore{db: db, backend: backend, base: strings.TrimRight(streamBaseURL, "/"), entries: map[string]*ssmLiveSession{}}, nil
}
func sessionARN(region, id string) string {
	return "arn:aws:ssm:" + region + ":000000000000:session/" + id
}
func (s *SSMSessionStore) Start(r *http.Request, region, targetID, document string) (SSMSessionResponse, error) {
	var out SSMSessionResponse
	caller, err := bindSSMSessionCaller(r)
	if err != nil {
		return out, err
	}
	if err = checkTerminalBoundary(r, region, targetID); err != nil {
		return out, err
	}
	if caller.identity.Account != "000000000000" || len(caller.identity.UserID) > 79 {
		return out, fmt.Errorf("unsupported local session identity")
	}
	if document != "" && document != "SSM-SessionManagerRunShell" {
		return out, ssmFailure{"InvalidDocument", "Only the local shell session document is supported."}
	}
	permissions := []ssmSessionPermission{{"ssm:StartSession", networkARN(region, "instance", targetID)}, {"ssm:StartSession", "arn:aws:ssm:" + region + "::document/SSM-SessionManagerRunShell"}}
	if err = caller.authorizeAll(r.Context(), permissions, ""); err != nil {
		return out, err
	}
	target, err := s.backend.ResolveSessionTarget(r.Context(), region, targetID)
	if err != nil || target.Region != region || target.InstanceID != targetID || target.StackID == "" {
		return out, ssmFailure{"TargetNotConnected", "No retained interactive guest target."}
	}
	var id guestsession.ID
	if _, err = rand.Read(id[:]); err != nil {
		return out, err
	}
	sid := caller.identity.UserID + "-" + hex.EncodeToString(id[:8])
	deadline := time.Now().Add(15 * time.Minute)
	if caller.identity.Expires.Before(deadline) {
		deadline = caller.identity.Expires
	}
	lifetime, cancel := context.WithDeadline(context.Background(), deadline)
	entry := &ssmLiveSession{id: sid, region: region, owner: caller, target: target, cancelLifetime: cancel, deadline: deadline, state: "starting"}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return out, fmt.Errorf("session store shutting down")
	}
	for _, other := range s.entries {
		other.mu.Lock()
		busy := other.target == target && other.state != "terminated"
		other.mu.Unlock()
		if busy {
			s.mu.Unlock()
			cancel()
			return out, fmt.Errorf("target already has a retained session")
		}
	}
	if len(s.entries) >= 100 {
		s.mu.Unlock()
		cancel()
		return out, fmt.Errorf("local session quota reached")
	}
	s.entries[sid] = entry
	s.mu.Unlock()
	err = s.reserve(r.Context(), entry)
	if err != nil {
		cancel()
		s.mu.Lock()
		delete(s.entries, sid)
		s.mu.Unlock()
		return out, err
	}
	driver, err := s.backend.OpenSessionChannel(lifetime, target)
	if err == nil && driver == nil {
		err = fmt.Errorf("backend supplied no retained guest channel")
	}
	entry.mu.Lock()
	entry.driver = driver
	entry.mu.Unlock()
	if err == nil {
		err = driver.Open(r.Context(), id, 24, 80)
	}
	if err != nil {
		if driver != nil {
			_ = driver.Close()
		}
		cancel()
		entry.mu.Lock()
		entry.state = "failed"
		entry.mu.Unlock()
		_ = s.transition(context.Background(), entry, "starting", "failed")
		return out, err
	}
	if err = caller.authorizeAll(r.Context(), permissions, ""); err != nil {
		_ = driver.Close()
		cancel()
		entry.mu.Lock()
		entry.state = "failed"
		entry.mu.Unlock()
		_ = s.transition(context.Background(), entry, "starting", "failed")
		return out, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		driver.Revoke()
		cancel()
		return out, fmt.Errorf("session store shutting down")
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if !driver.Active() {
		cancel()
		entry.state = "failed"
		_ = s.transition(context.Background(), entry, "starting", "failed")
		return out, fmt.Errorf("guest session ended during start")
	}
	entry.state = "connected"
	if err = s.transition(r.Context(), entry, "starting", "connected"); err != nil {
		_ = driver.Close()
		cancel()
		entry.state = "failed"
		return out, err
	}
	response, err := s.rotateToken(entry)
	if err != nil {
		_ = driver.Close()
		cancel()
		entry.state = "failed"
		_ = s.transition(context.Background(), entry, "connected", "failed")
	}
	return response, err
}
func (s *SSMSessionStore) rotateToken(entry *ssmLiveSession) (SSMSessionResponse, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return SSMSessionResponse{}, err
	}
	token := hex.EncodeToString(raw[:])
	entry.token = sha256.Sum256([]byte(token))
	entry.tokenUsed = false
	entry.tokenExpires = time.Now().Add(time.Minute)
	if entry.deadline.Before(entry.tokenExpires) {
		entry.tokenExpires = entry.deadline
	}
	return SSMSessionResponse{SessionID: entry.id, StreamURL: s.base + "/" + url.PathEscape(entry.id), TokenValue: token}, nil
}
func (s *SSMSessionStore) owned(r *http.Request, region, id, action string) (*ssmLiveSession, error) {
	caller, err := bindSSMSessionCaller(r)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	entry := s.entries[id]
	s.mu.Unlock()
	if entry == nil || entry.region != region || !caller.owns(entry.owner) {
		return nil, ssmFailure{"AccessDeniedException", "The session is not owned by this participant credential."}
	}
	if err = checkTerminalBoundary(r, region, entry.target.InstanceID); err != nil {
		return nil, err
	}
	if err = caller.authorize(r.Context(), action, sessionARN(region, id), entry.owner.identity.UserID); err != nil {
		return nil, err
	}
	return entry, nil
}
func (s *SSMSessionStore) Resume(r *http.Request, region, id string) (SSMSessionResponse, error) {
	entry, err := s.owned(r, region, id, "ssm:ResumeSession")
	if err != nil {
		return SSMSessionResponse{}, err
	}
	entry.mu.Lock()
	if entry.state != "connected" {
		entry.mu.Unlock()
		return SSMSessionResponse{}, fmt.Errorf("session cannot be resumed")
	}
	// Close admission before joining the old reader. Otherwise a new token user
	// could enter after cancellation and indefinitely hold the reader gate.
	entry.state = "resuming"
	entry.tokenUsed = true
	entry.generation++
	if entry.channelCancel != nil {
		entry.channelCancel()
	}
	entry.mu.Unlock()
	entry.channelGate.Lock()
	defer entry.channelGate.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.state != "resuming" {
		return SSMSessionResponse{}, fmt.Errorf("session admission revoked during resume")
	}
	if entry.driver == nil || !entry.driver.Active() {
		entry.state = "failed"
		return SSMSessionResponse{}, fmt.Errorf("session cannot be resumed")
	}
	if err = entry.owner.authorize(r.Context(), "ssm:ResumeSession", sessionARN(region, id), entry.owner.identity.UserID); err != nil {
		entry.state = "connected"
		return SSMSessionResponse{}, err
	}
	entry.state = "connected"
	return s.rotateToken(entry)
}
func (s *SSMSessionStore) Terminate(r *http.Request, region, id string) error {
	entry, err := s.owned(r, region, id, "ssm:TerminateSession")
	if err != nil {
		return err
	}
	entry.mu.Lock()
	if entry.state != "connected" {
		entry.mu.Unlock()
		return fmt.Errorf("session is not connected")
	}
	if err = s.transition(r.Context(), entry, "connected", "closing"); err != nil {
		entry.mu.Unlock()
		return err
	}
	entry.state = "closing"
	entry.generation++
	if entry.channelCancel != nil {
		entry.channelCancel()
	}
	entry.mu.Unlock()
	entry.channelGate.Lock()
	defer entry.channelGate.Unlock()
	if entry.driver == nil {
		return fmt.Errorf("session has no actual guest channel")
	}
	if err = entry.driver.Terminate(r.Context()); err != nil {
		return err
	}
	entry.cancelLifetime()
	if err = entry.driver.WaitCleanup(r.Context()); err != nil {
		return err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if err = s.transition(r.Context(), entry, "closing", "terminated"); err != nil {
		return err
	}
	entry.state = "terminated"
	return nil
}

// Channel is authenticated by a single-use short-lived token and then checks the
// original native role generation/policy on every operation. Server code may not
// bypass this wrapper by returning the underlying Driver to an unauthenticated UI.
type SSMSessionChannel struct {
	entry      *ssmLiveSession
	generation uint64
	ctx        context.Context
}

func (s *SSMSessionStore) OpenDataChannel(r *http.Request, id, token string) (*SSMSessionChannel, error) {
	ctx := r.Context()
	caller, err := bindSSMSessionCaller(r)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	entry := s.entries[id]
	s.mu.Unlock()
	if entry == nil || !caller.owns(entry.owner) {
		return nil, fmt.Errorf("unknown or foreign session")
	}
	if err := checkTerminalBoundary(r, entry.region, entry.target.InstanceID); err != nil {
		return nil, err
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	hash := sha256.Sum256([]byte(token))
	if entry.state != "connected" || entry.tokenUsed || !time.Now().Before(entry.tokenExpires) || len(token) != 64 || subtle.ConstantTimeCompare(hash[:], entry.token[:]) != 1 {
		return nil, fmt.Errorf("invalid session channel token")
	}
	if err := entry.owner.authorize(ctx, "ssmmessages:OpenDataChannel", sessionARN(entry.region, id), entry.owner.identity.UserID); err != nil {
		return nil, err
	}
	if entry.driver == nil || !entry.driver.Active() {
		return nil, fmt.Errorf("guest channel is not active")
	}
	entry.tokenUsed = true
	channelCtx, cancel := context.WithCancel(ctx)
	entry.channelCancel = cancel
	return &SSMSessionChannel{entry: entry, generation: entry.generation, ctx: channelCtx}, nil
}
func (c *SSMSessionChannel) checked(action func(*guestsession.Driver) error) error {
	e := c.entry
	e.channelGate.RLock()
	defer e.channelGate.RUnlock()
	if err := c.ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	valid := e.state == "connected" && e.generation == c.generation
	driver := e.driver
	e.mu.Unlock()
	if !valid || driver == nil {
		return fmt.Errorf("channel generation revoked")
	}
	if err := e.owner.authorize(c.ctx, "ssmmessages:OpenDataChannel", sessionARN(e.region, e.id), e.owner.identity.UserID); err != nil {
		return err
	}
	return action(driver)
}
func (c *SSMSessionChannel) Input(b []byte) error {
	return c.checked(func(d *guestsession.Driver) error { return d.Input(b) })
}
func (c *SSMSessionChannel) Resize(rows, cols uint16) error {
	return c.checked(func(d *guestsession.Driver) error { return d.Resize(rows, cols) })
}
func (c *SSMSessionChannel) Next() ([]byte, error) {
	var out []byte
	err := c.checked(func(d *guestsession.Driver) error {
		var err error
		out, err = d.Next(c.ctx)
		if err != nil {
			return err
		}
		if err := c.ctx.Err(); err != nil {
			return err
		}
		return c.entry.owner.authorize(c.ctx, "ssmmessages:OpenDataChannel", sessionARN(c.entry.region, c.entry.id), c.entry.owner.identity.UserID)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Close detaches this consumer without terminating its retained guest PTY.
func (c *SSMSessionChannel) Close() error {
	e := c.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.generation == c.generation && e.channelCancel != nil {
		e.channelCancel()
	}
	return nil
}
