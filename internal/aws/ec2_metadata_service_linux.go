//go:build linux

package aws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
)

// This bounded instance metadata subset runs on the actual private userspace TCP
// listener. It issues instance-local IMDSv2 tokens, not AWS IAM credentials.
// IAM credential/identity-signature endpoints remain unsupported, not simulated.
type ec2MetadataService struct {
	*guesttcp.Listener
	server *http.Server
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	tokens map[string]time.Time
	owner  EC2GuestOwner
	ip     string
}

func newEC2MetadataService(parent context.Context, d guesttcp.OwnedProbeDescriptor, o EC2GuestOwner, deadline time.Time) (*ec2MetadataService, error) {
	ctx, cancel := context.WithDeadline(parent, deadline)
	l, e := guesttcp.NewListenerUntil(ctx, guesttcp.ListenConfig{LocalIP: d.Passive.LocalIP, PeerIP: d.Passive.PeerIP, LocalMAC: d.Passive.LocalMAC, PeerMAC: d.Passive.PeerMAC, LocalPort: d.Passive.LocalPort}, deadline)
	if e != nil {
		cancel()
		return nil, e
	}
	s := &ec2MetadataService{Listener: l, ctx: ctx, cancel: cancel, done: make(chan struct{}), tokens: map[string]time.Time{}, owner: o, ip: d.Config.NIC.PeerIP.String()}
	s.server = &http.Server{Handler: s, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192}
	go func() { defer close(s.done); _ = s.server.Serve(l) }()
	return s, nil
}
func (s *ec2MetadataService) Lifetime() context.Context { return s.ctx }
func (s *ec2MetadataService) Close() error {
	s.cancel()
	err := s.server.Close()
	err = errors.Join(err, s.Listener.Close())
	<-s.done
	return err
}
func (s *ec2MetadataService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.ctx.Err() != nil {
		http.Error(w, "instance unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/latest/api/token" {
		ttl, e := strconv.Atoi(r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds"))
		if r.Method != "PUT" || r.Header.Get("X-Forwarded-For") != "" || e != nil || ttl < 1 || ttl > 21600 {
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}
		var bytes [32]byte
		if _, e = rand.Read(bytes[:]); e != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		token := hex.EncodeToString(bytes[:])
		expires := time.Now().Add(time.Duration(ttl) * time.Second)
		if d, _ := s.ctx.Deadline(); expires.After(d) {
			expires = d
		}
		s.mu.Lock()
		for k, v := range s.tokens {
			if !time.Now().Before(v) {
				delete(s.tokens, k)
			}
		}
		if len(s.tokens) >= 8 {
			s.mu.Unlock()
			http.Error(w, "token capacity", http.StatusServiceUnavailable)
			return
		}
		s.tokens[token] = expires
		s.mu.Unlock()
		w.Header().Set("X-aws-ec2-metadata-token-ttl-seconds", strconv.Itoa(ttl))
		_, _ = w.Write([]byte(token))
		return
	}
	s.mu.Lock()
	expires, ok := s.tokens[r.Header.Get("X-aws-ec2-metadata-token")]
	s.mu.Unlock()
	if !ok || !time.Now().Before(expires) {
		http.Error(w, "token required", http.StatusUnauthorized)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var value string
	switch r.URL.Path {
	case "/latest/meta-data/instance-id":
		value = s.owner.Instance
	case "/latest/meta-data/public-hostname":
		value = EC2InstancePublicDNSName(s.owner.Instance)
	case "/latest/meta-data/local-ipv4":
		value = s.ip
	case "/latest/meta-data/placement/region":
		value = s.owner.Region
	default:
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(value))
}
