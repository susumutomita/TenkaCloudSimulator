//go:build linux && amd64

package rdsruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	loopback  = "127.0.0.1"
	ownedSlot = "managed-postgres-15"
	probeSQL  = `SELECT current_setting('cluster_name'), current_setting('server_version_num'),
current_user, (current_database() = 'postgres'
 AND inet_server_addr() = inet '127.0.0.1' AND inet_client_addr() = inet '127.0.0.1'
 AND current_setting('listen_addresses') = '127.0.0.1'
 AND current_setting('unix_socket_directories') = ''
 AND current_setting('password_encryption') = 'scram-sha-256'
 AND EXISTS (SELECT 1 FROM pg_catalog.pg_authid WHERE rolname=current_user
             AND rolpassword LIKE 'SCRAM-SHA-256$%'))`
)

var (
	ErrStart       = errors.New("owned PostgreSQL workload did not start")
	ErrNotReady    = errors.New("owned PostgreSQL workload is not ready")
	ErrCleanupDebt = errors.New("owned PostgreSQL cleanup unconfirmed; private workspace retained")
)

// Runtime is a single, process-owned workload, not an RDS resource or a client
// capability. No caller-supplied SQL, address, port or password is accepted.
type Runtime struct {
	mu       sync.Mutex // serializes bounded authenticated probes with shutdown
	bundle   *Bundle
	home     string
	homeID   os.FileInfo
	port     string
	role     string
	identity string
	witness  string
	child    *os.Process
	done     chan struct{}
	waitErr  error // published by close(done)
	revoked  atomic.Bool
	closed   bool
	closeErr error
}

// CheckIdleWorkRoot confirms the explicit provider-owned work root is private
// and empty. It never removes a retained/unknown workload or its data.
func CheckIdleWorkRoot(workRoot string) error {
	if _, err := ownedPath(workRoot, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil || len(entries) != 0 {
		return ErrWork
	}
	return nil
}

// Start starts a newly initialized workload in an existing private provider-owned
// directory. It returns only after a real authenticated PostgreSQL identity
// check. Ignoring or cancelling the startup context does not authorize orphaning
// a known child; Start attempts bounded cleanup on every post-launch failure.
func Start(ctx context.Context, b *Bundle, workRoot string) (*Runtime, error) {
	if ctx == nil {
		return nil, ErrWork
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.verify(); err != nil {
		return nil, err
	}
	if os.Geteuid() == 0 {
		return nil, ErrWork
	}
	if _, err := ownedPath(workRoot, true); err != nil {
		return nil, err
	}
	// This owner starts at most one workload per trusted work root. A
	// previously retained slot is debt, not a cluster we may reopen.
	home := filepath.Join(workRoot, ownedSlot)
	if err := os.Mkdir(home, 0700); err != nil {
		return nil, ErrWork
	}
	homeID, err := ownedPath(home, true)
	if err != nil {
		return nil, ErrCleanupDebt
	}
	r := &Runtime{bundle: b, home: home, homeID: homeID, done: make(chan struct{})}
	password, err := r.prepare(ctx)
	if err != nil {
		if errors.Is(err, ErrCleanupDebt) {
			return nil, err
		}
		return nil, errors.Join(err, r.removeWorkspace())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, r.removeWorkspace())
	}
	port, err := availablePort()
	if err != nil {
		return nil, errors.Join(ErrStart, r.removeWorkspace())
	}
	r.port = port
	if err := r.writePassfile(password); err != nil {
		return nil, errors.Join(ErrStart, r.removeWorkspace())
	}
	program, err := b.executable(1)
	if err != nil {
		return nil, errors.Join(err, r.removeWorkspace())
	}
	command := exec.Command(program, "-D", r.dataPath(), "-h", loopback, "-k", "", "-p", r.port,
		"-c", "ssl=off", "-c", "password_encryption=scram-sha-256", "-c", "log_min_error_statement=panic",
		"-c", "logging_collector=off", "-c", "max_connections=8")
	command.Dir, command.Env = r.home, r.env()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return nil, errors.Join(ErrStart, ErrCleanupDebt)
	}
	r.child = command.Process
	go func() {
		r.waitErr = command.Wait()
		close(r.done)
	}()

	bounded, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := r.await(bounded); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	if err := r.Probe(bounded); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return r, nil
}

func (r *Runtime) dataPath() string { return filepath.Join(r.home, "data") }
func (r *Runtime) passPath() string { return filepath.Join(r.home, "pgpass") }

func (r *Runtime) env() []string {
	return []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "TZ=UTC", "HOME=" + r.home, "TMPDIR=" + r.home,
		"LD_LIBRARY_PATH=" + filepath.Join(r.bundle.dir, "lib")}
}

func (r *Runtime) prepare(ctx context.Context) (string, error) {
	var entropy [64]byte
	_, _ = rand.Read(entropy[:])
	r.identity = "tcsim_" + hex.EncodeToString(entropy[:16])
	r.role = "tcsim_boot_" + hex.EncodeToString(entropy[16:32])
	password := hex.EncodeToString(entropy[32:])
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	program, err := r.bundle.executable(0)
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(bounded, program, "-D", r.dataPath(), "-U", r.role,
		"--auth-local=reject", "--auth-host=scram-sha-256", "--encoding=UTF8", "--no-locale", "--pwfile=/dev/stdin")
	command.Dir, command.Env = r.home, r.env()
	command.Stdin = strings.NewReader(password + "\n")
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		// initdb may run a bootstrap postgres child. A cancelled initdb does
		// not prove all descendants are gone, so do not remove its data.
		return "", errors.Join(ErrStart, ErrCleanupDebt)
	}
	root, err := os.OpenRoot(r.home)
	if err != nil {
		return "", ErrStart
	}
	defer root.Close()
	conf, err := root.OpenFile("data/postgresql.conf", os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return "", ErrStart
	}
	_, writeErr := fmt.Fprintf(conf, "\ncluster_name = '%s'\n", r.identity)
	if closeErr := conf.Close(); writeErr != nil || closeErr != nil {
		return "", ErrStart
	}
	return password, nil
}

func (r *Runtime) writePassfile(password string) error {
	root, err := os.OpenRoot(r.home)
	if err != nil {
		return ErrStart
	}
	defer root.Close()
	// The private credential is scoped to its one numeric host, port, DB and
	// synthetic role. It is not returned, logged or passed as an argument.
	return root.WriteFile("pgpass", []byte(loopback+":"+r.port+":postgres:"+r.role+":"+password+"\n"), 0600)
}

func availablePort() (string, error) {
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return "", ErrStart
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	return port, l.Close()
}

func (r *Runtime) childExited() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *Runtime) readyLock() (string, error) {
	if r.child == nil || r.childExited() {
		return "", ErrNotReady
	}
	homeID, err := ownedPath(r.home, true)
	if err != nil || !os.SameFile(homeID, r.homeID) {
		return "", ErrNotReady
	}
	root, err := os.OpenRoot(r.home)
	if err != nil {
		return "", ErrNotReady
	}
	defer root.Close()
	f, err := root.OpenFile("data/postmaster.pid", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", ErrNotReady
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !trustedOwner(info) || info.Mode().Perm() != 0600 {
		return "", ErrNotReady
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 || r.childExited() {
		return "", ErrNotReady
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 9 || strings.TrimSpace(lines[7]) != "ready" ||
		lines[0] != strconv.Itoa(r.child.Pid) || lines[1] != r.dataPath() ||
		lines[3] != r.port || lines[4] != "" || lines[5] != loopback {
		return "", ErrNotReady
	}
	if start, err := strconv.ParseInt(lines[2], 10, 64); err != nil || start <= 0 {
		return "", ErrNotReady
	}
	return lines[2], nil
}

func (r *Runtime) await(ctx context.Context) error {
	tick := time.NewTicker(35 * time.Millisecond)
	defer tick.Stop()
	for {
		if witness, err := r.readyLock(); err == nil {
			r.witness = witness
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(ErrNotReady, ctx.Err())
		case <-r.done:
			return ErrNotReady
		case <-tick.C:
		}
	}
}

type boundedOutput struct{ buf bytes.Buffer }

func (w *boundedOutput) Write(data []byte) (int, error) {
	if len(data) > 512-w.buf.Len() {
		return 0, io.ErrShortWrite
	}
	return w.buf.Write(data)
}

// Probe confirms the current child and its authenticated identity. It is a
// fixed read-only operation, not a SQL or TCP proxy.
func (r *Runtime) Probe(ctx context.Context) error {
	if r == nil || ctx == nil || r.revoked.Load() {
		return ErrNotReady
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revoked.Load() || r.witness == "" {
		return ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.bundle.verify(); err != nil {
		return err
	}
	if witness, err := r.readyLock(); err != nil || witness != r.witness {
		return ErrNotReady
	}
	bounded, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	program, err := r.bundle.executable(2)
	if err != nil {
		return err
	}
	command := exec.CommandContext(bounded, program, "-XqwAt", "-v", "ON_ERROR_STOP=1",
		"-h", loopback, "-p", r.port, "-U", r.role, "-d", "postgres", "-c", probeSQL)
	command.Dir = r.home
	command.Env = append(r.env(), "PGPASSFILE="+r.passPath(), "PGSSLMODE=disable", "PGGSSENCMODE=disable", "PGCONNECT_TIMEOUT=2")
	command.WaitDelay = time.Second
	var output boundedOutput
	command.Stdout = &output
	if err := command.Run(); err != nil ||
		output.buf.String() != r.identity+"|150020|"+r.role+"|t\n" {
		return ErrNotReady
	}
	if witness, err := r.readyLock(); err != nil || witness != r.witness || r.revoked.Load() || ctx.Err() != nil {
		return ErrNotReady
	}
	return nil
}

// Close revokes the handle before signalling only its exact direct child. It
// confirms clean exit and removal of the private postmaster lock before deleting
// data. Uncertain cleanup is sticky across repeat calls.
func (r *Runtime) Close() error {
	if r == nil {
		return ErrNotReady
	}
	r.revoked.Store(true)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	r.closeErr = r.stop()
	return r.closeErr
}

func (r *Runtime) stop() error {
	if r.child == nil {
		return ErrNotReady
	}
	if !r.childExited() {
		if err := r.child.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return ErrCleanupDebt
		}
	}
	if !r.wait(12 * time.Second) {
		_ = r.child.Signal(syscall.SIGQUIT)
		if !r.wait(5 * time.Second) {
			_ = r.child.Kill()
			_ = r.wait(5 * time.Second)
		}
		return ErrCleanupDebt
	}
	if r.waitErr != nil {
		return ErrCleanupDebt
	}
	if _, err := os.Lstat(filepath.Join(r.dataPath(), "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		return ErrCleanupDebt
	}
	return r.removeWorkspace()
}

func (r *Runtime) wait(duration time.Duration) bool {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	select {
	case <-r.done:
		return true
	case <-deadline.C:
		return false
	}
}

func (r *Runtime) removeWorkspace() error {
	homeID, err := ownedPath(r.home, true)
	if err != nil || !os.SameFile(homeID, r.homeID) || os.RemoveAll(r.home) != nil {
		return ErrCleanupDebt
	}
	return nil
}
