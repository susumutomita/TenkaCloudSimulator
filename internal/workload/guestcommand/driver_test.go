package guestcommand

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
)

type peerRequest struct {
	Execute   string          `json:"execute"`
	Arguments json.RawMessage `json:"arguments"`
	ID        int64           `json:"id"`
}
type peer struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (p *peer) receive(name string) (peerRequest, error) {
	line, err := p.reader.ReadBytes('\n')
	if err != nil {
		return peerRequest{}, err
	}
	var r peerRequest
	if err = json.Unmarshal(line, &r); err != nil {
		return r, err
	}
	if r.Execute != name {
		return r, fmt.Errorf("unexpected RPC %q", r.Execute)
	}
	return r, nil
}
func (p *peer) reply(r peerRequest, result any) error {
	data, err := json.Marshal(map[string]any{"id": r.ID, "return": result})
	if err != nil {
		return err
	}
	_, err = p.conn.Write(append(data, '\n'))
	return err
}
func (p *peer) reject(r peerRequest) error {
	_, err := fmt.Fprintf(p.conn, "{\"id\":%d,\"error\":{\"class\":\"GenericError\",\"desc\":\"rejected\"}}\n", r.ID)
	return err
}
func (p *peer) start(pid int) error {
	r, err := p.receive("guest-exec")
	if err != nil {
		return err
	}
	return p.reply(r, map[string]int{"pid": pid})
}
func (p *peer) status(result any) error {
	r, err := p.receive("guest-exec-status")
	if err != nil {
		return err
	}
	return p.reply(r, result)
}
func (p *peer) untilClosed() error { _, _ = io.Copy(io.Discard, p.reader); return nil }

// The only transport used here is net.Pipe: no OS socket, guest or process.
func control(t *testing.T, script func(*peer) error, wrap func(net.Conn) qga.Transport) *qga.Client {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(10 * time.Second))
		p := &peer{conn: server, reader: bufio.NewReader(server)}
		b, err := p.reader.ReadByte()
		if err != nil || b != 0xff {
			done <- fmt.Errorf("missing sync sentinel: %x %v", b, err)
			return
		}
		r, err := p.receive("guest-sync-delimited")
		if err != nil {
			done <- err
			return
		}
		var args struct {
			ID int64 `json:"id"`
		}
		if err = json.Unmarshal(r.Arguments, &args); err != nil {
			done <- err
			return
		}
		if _, err = server.Write([]byte{0xff}); err != nil {
			done <- err
			return
		}
		if err = p.reply(r, args.ID); err != nil {
			done <- err
			return
		}
		done <- script(p)
	}()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("memory peer: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("memory peer did not stop")
		}
	})
	var transport qga.Transport = client
	if wrap != nil {
		transport = wrap(client)
	}
	c, err := qga.New(context.Background(), transport, qga.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func driver(t *testing.T, script func(*peer) error) *Driver {
	t.Helper()
	d, err := New(control(t, script, nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}
func start(t *testing.T, d *Driver, timeout time.Duration) Handle {
	t.Helper()
	h, s, err := d.Start(context.Background(), qga.Command{Path: "/bin/true"}, timeout)
	if err != nil || s.State != Accepted || s.CleanupRequired || s.Result.Exited {
		t.Fatalf("start: %+v, %v", s, err)
	}
	return h
}
func exited(code int64) map[string]any {
	return map[string]any{"exited": true, "exitcode": code, "out-data": "b2s=", "err-data": "ZXJy"}
}

func TestObservedLifecycleAndOpaqueResultCopies(t *testing.T) {
	d := driver(t, func(p *peer) error {
		r, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		var args struct {
			Path  string   `json:"path"`
			Args  []string `json:"arg"`
			Input string   `json:"input-data"`
		}
		if err = json.Unmarshal(r.Arguments, &args); err != nil {
			return err
		}
		// The trusted caller supplies the shell and unchanged command bytes;
		// this adapter has no AWS document or problem-specific translation.
		if args.Path != "/bin/sh" || len(args.Args) != 1 || args.Args[0] != "-s" || args.Input != "c3lzdGVtY3RsIHN0b3AgbmdpbnggfHwgdHJ1ZQo=" {
			return fmt.Errorf("command bytes changed: %+v", args)
		}
		if err = p.reply(r, map[string]int{"pid": 42}); err != nil {
			return err
		}
		if err = p.status(map[string]bool{"exited": false}); err != nil {
			return err
		}
		if err = p.status(exited(-2147483648)); err != nil {
			return err
		}
		if err = p.start(42); err != nil {
			return err
		}
		return p.status(map[string]any{"exited": true, "signal": -1073741819})
	})
	h, s, err := d.Start(context.Background(), qga.Command{Path: "/bin/sh", Args: []string{"-s"}, Stdin: []byte("systemctl stop nginx || true\n")}, time.Second)
	if err != nil || s.State != Accepted {
		t.Fatalf("start: %+v %v", s, err)
	}
	s, err = d.Poll(context.Background(), h)
	if err != nil || s.State != Accepted || s.Polls != 1 || s.Result.Exited {
		t.Fatalf("pending: %+v %v", s, err)
	}
	s, err = d.Poll(context.Background(), h)
	if !errors.Is(err, ErrPollTooSoon) || s.CleanupRequired || s.Polls != 1 {
		t.Fatalf("rate limit: %+v %v", s, err)
	}
	time.Sleep(MinPollInterval)
	s, err = d.Poll(context.Background(), h)
	if err != nil || s.State != Exited || !s.Result.Exited || *s.Result.ExitCode != -2147483648 || s.Polls != 2 {
		t.Fatalf("exit: %+v %v", s, err)
	}
	s.Result.Stdout[0] = 'X'
	s.Result.Stderr[0] = 'X'
	*s.Result.ExitCode = 999
	s, err = d.Inspect(h)
	if err != nil || string(s.Result.Stdout) != "ok" || string(s.Result.Stderr) != "err" || *s.Result.ExitCode != -2147483648 {
		t.Fatalf("snapshot alias: %+v %v", s, err)
	}
	second := start(t, d, time.Second)
	// A delayed timer or cancellation for an old job cannot close the next job.
	d.quarantine(h.job, DeadlineExpired)
	if s, err = d.RequestCancellation(h); err != nil || s.State != Exited {
		t.Fatalf("retired cancellation: %+v %v", s, err)
	}
	if s, err = d.Poll(context.Background(), h); err != nil || s.State != Exited {
		t.Fatalf("retired poll: %+v %v", s, err)
	}
	s, err = d.Poll(context.Background(), second)
	if err != nil || s.State != Exited || s.Result.Signal == nil || *s.Result.Signal != -1073741819 {
		t.Fatalf("next job: %+v %v", s, err)
	}
	*s.Result.Signal = 0
	s, _ = d.Inspect(second)
	if *s.Result.Signal != -1073741819 {
		t.Fatal("signal alias")
	}
}

func TestAdmissionAndDefiniteRejectionsNeedNoCleanup(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil client accepted")
	}
	d := driver(t, func(p *peer) error {
		r, err := p.receive("guest-exec")
		if err != nil {
			return err
		}
		if err = p.reject(r); err != nil {
			return err
		}
		if err = p.start(1); err != nil {
			return err
		}
		return p.status(exited(0))
	})
	for _, timeout := range []time.Duration{0, -1, MaxExecutionTimeout + 1} {
		if _, _, err := d.Start(context.Background(), qga.Command{Path: "x"}, timeout); !errors.Is(err, ErrInvalidTimeout) {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := d.Start(ctx, qga.Command{Path: "x"}, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	h, s, err := d.Start(context.Background(), qga.Command{}, time.Second)
	if !errors.Is(err, qga.ErrInvalidCommand) || s.State != Rejected || s.CleanupRequired {
		t.Fatalf("invalid input: %+v %v", s, err)
	}
	if s, err = d.Poll(context.Background(), h); err != nil || s.State != Rejected {
		t.Fatal(s, err)
	}
	_, s, err = d.Start(context.Background(), qga.Command{Path: "x"}, time.Second)
	var remote *qga.RemoteError
	var uncertain *qga.StartUncertainError
	if !errors.As(err, &remote) || errors.As(err, &uncertain) || s.State != Rejected || s.CleanupRequired {
		t.Fatalf("remote rejection: %+v %v", s, err)
	}
	h = start(t, d, time.Second)
	if s, err = d.Poll(ctx, h); !errors.Is(err, context.Canceled) || s.State != Accepted || s.CleanupRequired || s.Polls != 0 {
		t.Fatalf("cancelled unsent poll: %+v %v", s, err)
	}
	if s, err = d.Poll(context.Background(), h); err != nil || s.State != Exited {
		t.Fatal(s, err)
	}
}

type cancelBeforeWrite struct {
	net.Conn
	cancel context.CancelFunc
	armed  atomic.Bool
}

func (c *cancelBeforeWrite) SetDeadline(deadline time.Time) error {
	if c.armed.Load() {
		c.cancel()
	}
	return c.Conn.SetDeadline(deadline)
}

func TestProvenUnsentStartNeverRequiresGuestCleanup(t *testing.T) {
	for _, mode := range []string{"cancel-before-write", "expired-before-write", "encoded-limit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var transport *cancelBeforeWrite
			c := control(t, func(p *peer) error { return p.untilClosed() }, func(conn net.Conn) qga.Transport {
				transport = &cancelBeforeWrite{Conn: conn, cancel: cancel}
				return transport
			})
			d, _ := New(c)
			t.Cleanup(func() { _ = d.Close() })
			command := qga.Command{Path: "x"}
			timeout := time.Second
			switch mode {
			case "cancel-before-write":
				transport.armed.Store(true)
			case "expired-before-write":
				timeout = time.Nanosecond
			case "encoded-limit":
				command.Args = []string{strings.Repeat("\x01", qga.MaxArgumentBytes-1)}
				command.Stdin = make([]byte, qga.MaxStdinBytes)
			}
			h, s, err := d.Start(ctx, command, timeout)
			if err == nil || s.State != Rejected || s.CleanupRequired || s.Result.Exited || errors.Is(err, ErrCleanupRequired) {
				t.Fatalf("unsent start falsely needs guest cleanup: %+v %v", s, err)
			}
			var uncertain *qga.StartUncertainError
			if errors.As(err, &uncertain) {
				t.Fatal("unsent request marked uncertain")
			}
			s, err = d.Inspect(h)
			if err != nil || s.State != Rejected || s.CleanupRequired {
				t.Fatal(s, err)
			}
		})
	}
}

func TestUncertainStartIsStickyAndNeverRetried(t *testing.T) {
	d := driver(t, func(p *peer) error { _, err := p.receive("guest-exec"); return err })
	h, s, err := d.Start(context.Background(), qga.Command{Path: "x"}, time.Second)
	var uncertain *qga.StartUncertainError
	if !errors.Is(err, ErrCleanupRequired) || !errors.As(err, &uncertain) || s.State != Uncertain || !s.CleanupRequired || s.Reason != ControlLost || s.Result.Exited {
		t.Fatal(s, err)
	}
	if _, _, err = d.Start(context.Background(), qga.Command{Path: "x"}, time.Second); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if s, err = d.Poll(context.Background(), h); !errors.Is(err, ErrCleanupRequired) || s.State != Uncertain {
		t.Fatal(s, err)
	}
	if s, err = d.RequestCancellation(h); !errors.Is(err, ErrCleanupRequired) || s.Reason != ControlLost {
		t.Fatal(s, err)
	}
}

func TestDeadlineActsWithoutPollingAndDoesNotClaimProcessStopped(t *testing.T) {
	for _, mode := range []string{"accepted", "starting"} {
		t.Run(mode, func(t *testing.T) {
			d := driver(t, func(p *peer) error {
				r, err := p.receive("guest-exec")
				if err != nil {
					return err
				}
				if mode == "accepted" {
					if err = p.reply(r, map[string]int{"pid": 1}); err != nil {
						return err
					}
				}
				return p.untilClosed()
			})
			h, s, err := d.Start(context.Background(), qga.Command{Path: "x"}, 30*time.Millisecond)
			if mode == "accepted" {
				if err != nil || s.State != Accepted {
					t.Fatal(s, err)
				}
				time.Sleep(60 * time.Millisecond)
			} else {
				if !errors.Is(err, ErrCleanupRequired) {
					t.Fatal(s, err)
				}
			}
			s, err = d.Inspect(h)
			if err != nil || s.State != Uncertain || !s.CleanupRequired || s.Reason != DeadlineExpired || s.Result.Exited {
				t.Fatalf("deadline claimed a stopped process: %+v %v", s, err)
			}
			if _, _, err = d.Start(context.Background(), qga.Command{Path: "x"}, time.Second); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}

func TestCancellationRetiresControlButNotGuestProcess(t *testing.T) {
	d := driver(t, func(p *peer) error {
		if err := p.start(1); err != nil {
			return err
		}
		return p.untilClosed()
	})
	h := start(t, d, time.Second)
	s, err := d.RequestCancellation(h)
	if !errors.Is(err, ErrCleanupRequired) || s.State != Uncertain || !s.CleanupRequired || s.Reason != CancellationRequested || s.Result.Exited {
		t.Fatal(s, err)
	}
	_ = d.Close()
	s, err = d.Inspect(h)
	if err != nil || s.Reason != CancellationRequested || !s.CleanupRequired {
		t.Fatal(s, err)
	}
}

func TestPollFailuresAndTruncation(t *testing.T) {
	for _, mode := range []string{"remote", "malformed", "closed", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			d := driver(t, func(p *peer) error {
				if err := p.start(1); err != nil {
					return err
				}
				r, err := p.receive("guest-exec-status")
				if err != nil {
					return err
				}
				switch mode {
				case "remote":
					return p.reject(r)
				case "malformed":
					_, err = p.conn.Write([]byte("bad\n"))
					return err
				case "closed":
					return nil
				case "truncated":
					if err = p.reply(r, map[string]any{"exited": true, "exitcode": 0, "out-truncated": true}); err != nil {
						return err
					}
					if err = p.start(1); err != nil {
						return err
					}
					return p.status(exited(0))
				}
				return nil
			})
			h := start(t, d, time.Second)
			s, err := d.Poll(context.Background(), h)
			if mode == "truncated" {
				if !errors.Is(err, qga.ErrOutputTruncated) || s.State != OutputUnavailable || s.CleanupRequired || s.Result.Exited || s.Result.Stdout != nil {
					t.Fatal(s, err)
				}
				if s, err = d.Poll(context.Background(), h); err != nil || s.State != OutputUnavailable {
					t.Fatal(s, err)
				}
				h = start(t, d, time.Second)
				if s, err = d.Poll(context.Background(), h); err != nil || s.State != Exited {
					t.Fatal(s, err)
				}
			} else {
				if !errors.Is(err, ErrCleanupRequired) || s.State != Uncertain || !s.CleanupRequired || s.Result.Exited {
					t.Fatal(s, err)
				}
				if _, _, err = d.Start(context.Background(), qga.Command{Path: "x"}, time.Second); !errors.Is(err, ErrClosed) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestConcurrentOperationsAndClose(t *testing.T) {
	received := make(chan struct{})
	d := driver(t, func(p *peer) error {
		if err := p.start(1); err != nil {
			return err
		}
		if _, err := p.receive("guest-exec-status"); err != nil {
			return err
		}
		close(received)
		return p.untilClosed()
	})
	h := start(t, d, time.Second)
	if _, _, err := d.Start(context.Background(), qga.Command{Path: "x"}, time.Second); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		s, err := d.Poll(context.Background(), h)
		if s.State != Uncertain || s.Result.Exited {
			done <- fmt.Errorf("closed poll claimed outcome: %+v", s)
			return
		}
		done <- err
	}()
	<-received
	if s, err := d.Poll(context.Background(), h); !errors.Is(err, ErrBusy) || s.Polls != 1 {
		t.Fatal(s, err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() { _ = d.Close() })
	}
	wg.Wait()
	if err := <-done; !errors.Is(err, ErrCleanupRequired) {
		t.Fatal(err)
	}
	s, err := d.Inspect(h)
	if err != nil || s.Reason != DriverClosed || !s.CleanupRequired {
		t.Fatal(s, err)
	}
}

func TestPollBudgetAndHandleIsolation(t *testing.T) {
	d := driver(t, func(p *peer) error {
		if err := p.start(1); err != nil {
			return err
		}
		return p.untilClosed()
	})
	h := start(t, d, time.Second)
	other := driver(t, func(p *peer) error { return p.untilClosed() })
	for _, invalid := range []Handle{{}, h} {
		if _, err := other.Poll(context.Background(), invalid); !errors.Is(err, ErrInvalidHandle) {
			t.Fatal(err)
		}
		if _, err := other.Inspect(invalid); !errors.Is(err, ErrInvalidHandle) {
			t.Fatal(err)
		}
		if _, err := other.RequestCancellation(invalid); !errors.Is(err, ErrInvalidHandle) {
			t.Fatal(err)
		}
	}
	d.mu.Lock()
	h.job.snapshot.Polls = MaxPolls
	d.mu.Unlock()
	s, err := d.Poll(context.Background(), h)
	if !errors.Is(err, ErrPollLimit) || !errors.Is(err, ErrCleanupRequired) || s.State != Uncertain || s.Reason != PollBudgetExhausted || !s.CleanupRequired {
		t.Fatal(s, err)
	}
}

func TestExternallyLostControlDoesNotBecomeSuccess(t *testing.T) {
	c := control(t, func(p *peer) error {
		if err := p.start(1); err != nil {
			return err
		}
		return p.untilClosed()
	}, nil)
	d, _ := New(c)
	t.Cleanup(func() { _ = d.Close() })
	h := start(t, d, time.Second)
	_ = c.Close() // Simulate lifecycle/channel failure, not permitted client sharing.
	s, err := d.Poll(context.Background(), h)
	if !errors.Is(err, ErrCleanupRequired) || !s.CleanupRequired || s.State != Uncertain || s.Result.Exited {
		t.Fatal(s, err)
	}
}

func TestDeadlineChecksDoNotDependOnTimerScheduling(t *testing.T) {
	for _, mode := range []string{"before-poll", "during-poll"} {
		t.Run(mode, func(t *testing.T) {
			d := driver(t, func(p *peer) error {
				if err := p.start(1); err != nil {
					return err
				}
				if mode == "during-poll" {
					if _, err := p.receive("guest-exec-status"); err != nil {
						return err
					}
				}
				return p.untilClosed()
			})
			h := start(t, d, 50*time.Millisecond)
			d.mu.Lock()
			h.job.timer.Stop() // Model a delayed timer callback; RPC checks still apply.
			if mode == "before-poll" {
				h.job.snapshot.Deadline = time.Now().Add(-time.Second)
			}
			d.mu.Unlock()
			s, err := d.Poll(context.Background(), h)
			if !errors.Is(err, ErrCleanupRequired) || s.State != Uncertain || s.Reason != DeadlineExpired || !s.CleanupRequired || s.Result.Exited {
				t.Fatal(s, err)
			}
		})
	}
	if cleanupError(nil) != ErrCleanupRequired {
		t.Fatal("cleanup without an underlying IO error was lost")
	}
}

func TestCloseDuringStartKeepsUnknownOutcomeAndFirstReason(t *testing.T) {
	received := make(chan struct{})
	d := driver(t, func(p *peer) error {
		if _, err := p.receive("guest-exec"); err != nil {
			return err
		}
		close(received)
		return p.untilClosed()
	})
	type answer struct {
		handle   Handle
		snapshot Snapshot
		err      error
	}
	done := make(chan answer, 1)
	go func() {
		h, s, err := d.Start(context.Background(), qga.Command{Path: "x"}, time.Second)
		done <- answer{h, s, err}
	}()
	<-received
	d.mu.Lock()
	j := d.active
	if !d.quarantineLocked(j, DriverClosed) || d.quarantineLocked(j, DeadlineExpired) {
		t.Error("starting abort reason was not sticky")
	}
	if j.snapshot.CleanupRequired {
		t.Error("cleanup inferred before Start classified transmission")
	}
	d.mu.Unlock()
	_ = d.Close()
	got := <-done
	if !errors.Is(got.err, ErrCleanupRequired) || got.snapshot.State != Uncertain || !got.snapshot.CleanupRequired || got.snapshot.Reason != DriverClosed {
		t.Fatal(got.snapshot, got.err)
	}
}

func TestAcceptedCommandDoesNotBorrowRequestContextLifetime(t *testing.T) {
	d := driver(t, func(p *peer) error {
		if err := p.start(1); err != nil {
			return err
		}
		return p.status(exited(0))
	})
	ctx, cancel := context.WithCancel(context.Background())
	h, s, err := d.Start(ctx, qga.Command{Path: "/bin/true"}, time.Second)
	if err != nil || s.State != Accepted {
		t.Fatal(s, err)
	}
	cancel() // The initiating HTTP request may finish; it is not process cancellation.
	s, err = d.Poll(context.Background(), h)
	if err != nil || s.State != Exited || s.CleanupRequired || !s.Result.Exited {
		t.Fatal(s, err)
	}
}
