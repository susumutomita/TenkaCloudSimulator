//go:build linux

package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

const battleCapabilitiesPath = "/_simulator/v1/battle-capabilities"

// BattleDeployment is Host correlation, not provider authority. Registration
// checks actual provider provenance and binds it once to a canonical receipt.
type BattleDeployment struct {
	EventID    string `json:"eventId"`
	TeamID     string `json:"teamId"`
	JobID      string `json:"jobId"`
	UnitHash   string `json:"unitHash"`
	DeployedAt int64  `json:"deployedAt"`
	StackID    string `json:"stackId"`
	InstanceID string `json:"instanceId"`
}
type battleLease struct {
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
}
type battleBinding struct {
	Owner     BattleDeployment  `json:"owner"`
	Lease     battleLease       `json:"lease"`
	Endpoints map[string]string `json:"endpoints"`
}
type battleTarget struct {
	LocalIP  string `json:"localIP"`
	PeerIP   string `json:"peerIP"`
	LocalMAC string `json:"localMAC"`
	PeerMAC  string `json:"peerMAC"`
	PeerPort uint16 `json:"peerPort"`
	Path     string `json:"path"`
}
type battleOperationBinding struct {
	Owner              BattleDeployment `json:"owner"`
	Lease              battleLease      `json:"lease"`
	Slot               string           `json:"slot"`
	BackendIncarnation string           `json:"backendIncarnation"`
	Target             battleTarget     `json:"target"`
}
type battleMapping struct {
	WireBinding guesttcp.HTTPProbeWireBinding `json:"wireBinding"`
	Service     guesttcp.HTTPProbeWireService `json:"service"`
	Binding     battleOperationBinding        `json:"binding"`
}
type battleReceipt struct {
	Version     int                      `json:"version"`
	Handle      string                   `json:"handle"`
	ClockDomain string                   `json:"clockDomain"`
	Binding     battleBinding            `json:"binding"`
	Slots       map[string]battleMapping `json:"slots"`
}
type battleReceiptLifetime interface {
	Ready() bool
	Seal(context.Context) error
	WaitCleanup(context.Context) error
}
type battleDispatch func([]byte) (*guesttcp.HTTPProbeWireSnapshot, error)
type battleEntry struct {
	receipt  battleReceipt
	source   battleReceiptLifetime
	dispatch map[string]battleDispatch
	sealed   bool
}

// BattleCapabilityRegistry cannot launch, reconstruct, or mint VM authority.
// The trusted provider composition registers its actual ready typed receipt.
// Entries/tombstones are bounded and never reused, including after shutdown.
type battleProviderReceipt struct {
	source   *aws.EC2VMProbeLaunch
	services map[string]string
}

type BattleCapabilityRegistry struct {
	monotonic *probeclock.Clock
	driver    *aws.EC2InstanceRuntime
	providers map[string]battleProviderReceipt
	mu        sync.Mutex
	origin    string
	clock     guesttcp.HTTPProbeCommandClock
	entries   map[string]*battleEntry
	byHandle  map[string]*battleEntry
	sources   map[*aws.EC2VMProbeLaunch]string
}

func NewBattleCapabilityRegistry(origin string, clock guesttcp.HTTPProbeCommandClock) (*BattleCapabilityRegistry, error) {
	u, e := url.Parse(origin)
	if e != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || clock.Domain == "" || clock.ValidFromMs < 1 || clock.ValidUntilMs <= clock.ValidFromMs {
		return nil, errors.New("invalid Battle provider configuration")
	}
	return &BattleCapabilityRegistry{origin: origin, clock: clock, providers: make(map[string]battleProviderReceipt), entries: make(map[string]*battleEntry), byHandle: make(map[string]*battleEntry), sources: make(map[*aws.EC2VMProbeLaunch]string)}, nil
}
func battleKey(owner BattleDeployment) string {
	h := sha256.New()
	for _, v := range []string{owner.EventID, owner.TeamID, owner.JobID, owner.UnitHash, strconv.FormatInt(owner.DeployedAt, 10), owner.StackID, owner.InstanceID} {
		_, _ = h.Write([]byte(strconv.Itoa(len([]byte(v))) + ":"))
		_, _ = h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func validBattleOwner(o BattleDeployment) bool {
	for _, s := range []string{o.EventID, o.TeamID, o.JobID, o.StackID, o.InstanceID} {
		if s == "" || len(s) > 1024 || !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.ContainsFunc(s, unicode.IsControl) || strings.ContainsRune(s, utf8.RuneError) {
			return false
		}
	}
	raw, err := hex.DecodeString(o.UnitHash)
	return err == nil && len(raw) == 32 && o.UnitHash == strings.ToLower(o.UnitHash) && o.DeployedAt > 0 && o.DeployedAt < 1<<53
}

// Reject duplicate (including escaped-equivalent) keys before typed decoding.
func uniqueBattleJSON(raw []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return errors.New("depth")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate")
				}
				seen[name] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return errors.New("object")
			}
			return nil
		}
		if delimiter == '[' {
			for d.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return errors.New("array")
			}
			return nil
		}
		return errors.New("delimiter")
	}
	if value(0) != nil {
		return false
	}
	_, e := d.Token()
	return e == io.EOF
}

// RegisterProviderReceipt is called by the configured EC2 runtime OnReady path.
// It derives selection provenance from the actual receipt, not callback strings.
func (r *BattleCapabilityRegistry) RegisterProviderReceipt(source *aws.EC2VMProbeLaunch) error {
	if r == nil || source == nil || !source.Ready() {
		return errors.New("provider receipt not ready")
	}
	issuance, err := source.Issuance()
	if err != nil {
		return err
	}
	descriptor, err := source.Descriptor()
	if err != nil {
		return err
	}
	services := map[string]string{}
	for _, service := range descriptor.Services {
		slot := ""
		if service.Name == "frontend" && service.Path == "/" {
			slot = "frontend"
		}
		if service.Name == "api" && service.Path == "/healthz" {
			slot = "api"
		}
		if slot != "" {
			if services[slot] != "" {
				return errors.New("ambiguous provider service")
			}
			services[slot] = service.Name
		}
	}
	if len(services) != 2 {
		return errors.New("provider services unavailable")
	}
	key := issuance.Lease.Owner.Deployment + "\x00" + issuance.Lease.Owner.Instance
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, ok := r.providers[key]; ok {
		if prior.source == source {
			return nil
		}
		return errors.New("provider generation already retained")
	}
	if len(r.providers) >= 1024 {
		return errors.New("provider receipt quota")
	}
	r.providers[key] = battleProviderReceipt{source, services}
	return nil
}

// Register is in-process only. A ready receipt cannot be adopted by another job
// or timestamp; exact repeat registration is idempotent. Slot names select only
// provider-declared immutable services, never caller addresses or URLs.
func (r *BattleCapabilityRegistry) Register(owner BattleDeployment, source *aws.EC2VMProbeLaunch, services map[string]string) error {
	if r == nil || !validBattleOwner(owner) || source == nil || len(services) != 2 {
		return errors.New("Battle receipt unavailable")
	}
	issuance, err := source.Issuance()
	if err != nil {
		return err
	}
	if issuance.Lease.Owner.Instance != owner.InstanceID || issuance.Lease.Owner.Deployment != owner.StackID {
		return errors.New("Battle deployment mismatch")
	}
	descriptor, err := source.Descriptor()
	if err != nil {
		return err
	}
	scope := source.ProbeScope()
	if scope == nil {
		return errors.New("Battle scope unavailable")
	}
	b, err := scope.Binding()
	if err != nil || b != descriptor.Binding {
		return errors.New("Battle scope mismatch")
	}
	wire := guesttcp.HTTPProbeWireBinding{Owner: hex.EncodeToString(b.Owner[:]), Deployment: hex.EncodeToString(b.Deployment[:]), Lease: hex.EncodeToString(b.Lease[:]), BackendIncarnation: hex.EncodeToString(b.BackendIncarnation[:])}
	lease := battleLease{issuance.Lease.Intent.Lease, wire.Lease}
	handleBytes := make([]byte, 32)
	if _, err = rand.Read(handleBytes); err != nil {
		return err
	}
	handle := hex.EncodeToString(handleBytes)
	entry := &battleEntry{source: source, dispatch: make(map[string]battleDispatch), receipt: battleReceipt{Version: 1, Handle: handle, ClockDomain: r.clock.Domain, Binding: battleBinding{owner, lease, make(map[string]string)}, Slots: make(map[string]battleMapping)}}
	for _, slot := range []string{"frontend", "api"} {
		name := services[slot]
		var selected *guesttcp.HTTPProbeService
		for _, s := range descriptor.Services {
			if s.Name == name {
				copy := s
				selected = &copy
			}
		}
		if selected == nil || (slot == "frontend" && selected.Path != "/") || (slot == "api" && selected.Path != "/healthz") {
			return errors.New("Battle service unavailable")
		}
		if r.monotonic != nil {
			d, e := guesttcp.NewHTTPProbeCommandDispatcherV2(scope, name, r.monotonic)
			if e != nil {
				return e
			}
			entry.dispatch[slot] = d.Dispatch
		} else {
			d, e := guesttcp.NewHTTPProbeCommandDispatcher(scope, name, r.clock)
			if e != nil {
				return e
			}
			entry.dispatch[slot] = d.Dispatch
		}
		nic := descriptor.Config.NIC
		entry.receipt.Slots[slot] = battleMapping{wire, guesttcp.HTTPProbeWireService{Name: name, Port: selected.Port, Path: selected.Path}, battleOperationBinding{owner, lease, slot, wire.BackendIncarnation, battleTarget{nic.LocalIP.String(), nic.PeerIP.String(), net.HardwareAddr(nic.LocalMAC[:]).String(), net.HardwareAddr(nic.PeerMAC[:]).String(), selected.Port, selected.Path}}}
		entry.receipt.Binding.Endpoints[slot] = r.origin + battleCapabilitiesPath + "/" + handle + "/" + slot
	}
	key := battleKey(owner)
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, ok := r.sources[source]; ok {
		if prior != key {
			return errors.New("Battle receipt already associated")
		}
		old := r.entries[key]
		for slot, d := range entry.receipt.Slots {
			if old.receipt.Slots[slot] != d {
				return errors.New("Battle service rebinding")
			}
		}
		return nil
	}
	if r.entries[key] != nil || len(r.entries) >= 1024 {
		return errors.New("Battle registration unavailable")
	}
	entry.sealed = !source.Ready()
	r.entries[key] = entry
	r.byHandle[handle] = entry
	r.sources[source] = key
	return nil
}
func battleJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func (r *BattleCapabilityRegistry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if r.monotonic != nil {
		r.monotonic.SignedHandler(http.HandlerFunc(r.serveHTTP)).ServeHTTP(w, request)
		return
	}
	r.serveHTTP(w, request)
}
func (r *BattleCapabilityRegistry) serveHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, ok := provider.ActorFromContext(request.Context())
	if !ok || actor.CredentialRole != provider.CredentialOperator {
		http.Error(w, "Operator authentication required", 403)
		return
	}
	if request.Method != "POST" || request.URL.RawQuery != "" || !strings.HasPrefix(request.URL.Path, battleCapabilitiesPath+"/") {
		http.Error(w, "Invalid Battle request", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, request.Body, 8192))
	if err != nil || !utf8.Valid(raw) || !uniqueBattleJSON(raw) {
		http.Error(w, "Invalid Battle body", 400)
		return
	}
	suffix := strings.TrimPrefix(request.URL.Path, battleCapabilitiesPath)
	if suffix == "/clock" {
		var input struct {
			Version int    `json:"version"`
			Nonce   string `json:"nonce"`
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if r.monotonic == nil || d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || input.Version != 2 {
			http.Error(w, "Clock unavailable", 400)
			return
		}
		sample, e := r.monotonic.Sample(input.Nonce)
		if e != nil {
			http.Error(w, "Invalid clock nonce", 400)
			return
		}
		battleJSON(w, sample)
		return
	}
	if suffix == "/availability" {
		var input struct {
			Version int `json:"version"`
		}
		d := json.NewDecoder(strings.NewReader(string(raw)))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(&struct{}{}) != io.EOF || input.Version != 1 {
			http.Error(w, "Invalid availability request", 400)
			return
		}
		r.mu.Lock()
		configured := r.driver != nil
		r.mu.Unlock()
		version := 1
		if r.monotonic != nil {
			version = 2
		}
		battleJSON(w, map[string]any{"configured": configured, "protocolVersion": version, "clockDomain": r.clock.Domain})
		return
	}
	if suffix == "/resolve" || suffix == "/seal" {
		var input struct {
			Version int              `json:"version"`
			Owner   BattleDeployment `json:"owner"`
			Key     string           `json:"key"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Version != 1 || !validBattleOwner(input.Owner) || input.Key != battleKey(input.Owner) {
			http.Error(w, "Invalid Battle correlation", 400)
			return
		}
		r.mu.Lock()
		entry := r.entries[input.Key]
		supplied, registered := r.providers[input.Owner.StackID+"\x00"+input.Owner.InstanceID]
		r.mu.Unlock()
		if entry == nil && registered {
			if e := r.Register(input.Owner, supplied.source, supplied.services); e != nil {
				http.Error(w, "Battle association unavailable", 409)
				return
			}
		}
		r.mu.Lock()
		entry = r.entries[input.Key]
		if entry == nil || entry.receipt.Binding.Owner != input.Owner {
			r.mu.Unlock()
			http.Error(w, "Battle receipt not registered", 409)
			return
		}
		if suffix == "/resolve" {
			if entry.sealed || !entry.source.Ready() {
				r.mu.Unlock()
				http.Error(w, "Battle receipt not ready", 409)
				return
			}
			receipt := entry.receipt
			r.mu.Unlock()
			battleJSON(w, receipt)
			return
		}
		entry.sealed = true
		r.mu.Unlock()
		drain, cancel := context.WithTimeout(request.Context(), 8*time.Second)
		defer cancel()
		sealErr := entry.source.Seal(drain)
		joinErr := entry.source.WaitCleanup(drain)
		battleJSON(w, map[string]bool{"sealed": sealErr == nil, "joined": sealErr == nil && joinErr == nil})
		return
	}
	pieces := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(pieces) != 3 || (pieces[2] != "start" && pieces[2] != "read") {
		http.NotFound(w, request)
		return
	}
	r.mu.Lock()
	entry := r.byHandle[pieces[0]]
	var dispatch battleDispatch
	if entry != nil {
		dispatch = entry.dispatch[pieces[1]]
	}
	blocked := entry != nil && (entry.sealed || !entry.source.Ready())
	r.mu.Unlock()
	if dispatch == nil {
		http.Error(w, "Unknown Battle receipt", 409)
		return
	}
	command, e := guesttcp.DecodeHTTPProbeCommand(raw)
	if e != nil || command.Command != pieces[2] {
		http.Error(w, "Invalid Battle command", 400)
		return
	}
	if pieces[2] == "start" && blocked {
		http.Error(w, "Battle admission sealed", 409)
		return
	}
	snapshot, e := dispatch(raw)
	if e != nil {
		http.Error(w, "Battle command outcome unavailable", 409)
		return
	}
	battleJSON(w, snapshot)
}

// NewWithBattleCapabilities is explicit trusted composition; NewAccess must wrap
// the returned handler. Default CLI/provider construction remains disabled.
func NewWithBattleCapabilities(path string, options ProviderConfig, registry *BattleCapabilityRegistry) (http.Handler, func() error, error) {
	if registry == nil {
		return nil, nil, errors.New("Battle registry required")
	}
	options.battleCapabilities = registry
	return NewWithProviders(path, options)
}
