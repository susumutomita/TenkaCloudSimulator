// Package qga implements a bounded command-only QEMU Guest Agent client over a
// transport supplied by a trusted lifecycle controller. It does not create or
// contain guests, authorize commands, or cancel guest processes.
package qga

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxArgs           = 64
	MaxPathBytes      = 4096
	MaxArgumentBytes  = 64 << 10 // Combined path and argument bytes.
	MaxStdinBytes     = 256 << 10
	MaxRequestBytes   = 512 << 10 // Including sentinel/newline when present.
	MaxFrameBytes     = 1 << 20   // Excluding the terminating newline.
	MaxSyncBytes      = 2 << 20   // All bytes, including stale data and sentinels.
	MaxOutputBytes    = 256 << 10 // Combined decoded stdout and stderr.
	MaxCommands       = 32        // Active, not yet reaped handles per client.
	DefaultRPCTimeout = 2 * time.Second
	MaxRPCTimeout     = 5 * time.Second
)

var (
	ErrClosed          = errors.New("guest control client is closed")
	ErrInvalidCommand  = errors.New("invalid guest command")
	ErrInvalidHandle   = errors.New("invalid or retired guest command handle")
	ErrCapacity        = errors.New("guest command handle capacity reached")
	ErrLimit           = errors.New("guest control byte limit exceeded")
	ErrProtocol        = errors.New("invalid guest control response")
	ErrOutputTruncated = errors.New("guest command output was truncated")
)

// Transport is an already-authorized, private, exclusively owned QGA stream.
// Close and SetDeadline must be safe concurrently with Read/Write. Close must
// promptly unblock them, and deadlines must bound both reads and writes. These
// are trusted requirements, not guarantees a client can impose on arbitrary IO.
// No other reader/writer may use a transport after passing it to New.
type Transport interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}

// Options sets the hard timeout; zero selects DefaultRPCTimeout.
// The timeout includes waiting for the client's serial RPC slot, not just IO.
type Options struct{ RPCTimeout time.Duration }

// Command contains guest-local input, never host paths or authority. No shell
// is inserted. A caller that requests a shell explicitly owns its authorization.
// The caller must not mutate Args or Stdin while StartCommand uses them.
type Command struct {
	Path  string
	Args  []string
	Stdin []byte
}

// Handle is opaque and belongs to one Client. Its zero value is invalid. Copies
// refer to the same command and are all invalid after an exited status is read.
// Handles are not serializable, reconnectable, or transferable between clients.
type Handle struct {
	owner *Client
	key   *handleKey
}

// A nonzero-sized allocation gives each handle a distinct pointer identity.
type handleKey struct{ marker byte }

// Status contains complete output only after exit. Exactly one of ExitCode or
// Signal is then set. Values preserve QAPI signed int64 status, including Windows
// high-bit codes. An exited status reaps the QGA record and retires its handle.
type Status struct {
	Exited   bool
	ExitCode *int64
	Signal   *int64
	Stdout   []byte
	Stderr   []byte
}

// RemoteError is an explicit, correlated QGA rejection, unlike a lost response.
// Class and Description are untrusted guest text. Never treat them as authority.
type RemoteError struct{ Class, Description string }

func (e *RemoteError) Error() string {
	return "guest agent rejected request: " + e.Class + ": " + e.Description
}

// StartUncertainError means some request bytes were transmitted without a
// trustworthy successful/rejected outcome. The command may be running. Never
// retry it automatically. Closing this client does not cancel that process.
type StartUncertainError struct{ Err error }

func (e *StartUncertainError) Error() string {
	return "guest command may have started: " + e.Err.Error()
}
func (e *StartUncertainError) Unwrap() error { return e.Err }

// Client must not be copied. Calls are serialized and bounded; Close may run
// concurrently with any call. A transport/protocol failure permanently closes
// the client, so a timed-out stream is never reused or automatically retried.
type Client struct {
	transport Transport
	reader    *bufio.Reader
	timeout   time.Duration
	gate      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	// These fields are used only while holding gate.
	nextID  int64
	handles map[*handleKey]int64
}

// New takes ownership of a non-nil transport even on error, and synchronizes it
// with guest-sync-delimited before exposing commands. It never dials, discovers
// a guest, starts a process, opens files, or changes transport permissions.
func New(ctx context.Context, transport Transport, options Options) (*Client, error) {
	if transport == nil {
		return nil, errors.New("guest control transport is required")
	}
	c := &Client{transport: transport, reader: bufio.NewReaderSize(transport, 4096),
		timeout: options.RPCTimeout, gate: make(chan struct{}, 1), done: make(chan struct{}),
		handles: make(map[*handleKey]int64)}
	if c.timeout == 0 {
		c.timeout = DefaultRPCTimeout
	}
	if c.timeout < 0 || c.timeout > MaxRPCTimeout {
		_ = c.Close()
		return nil, errors.New("guest control RPC timeout must be positive and at most five seconds")
	}
	callCtx, release, err := c.acquire(ctx)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	defer release()
	var nonceBytes [8]byte
	_, _ = rand.Read(nonceBytes[:])
	nonce := int64(binary.BigEndian.Uint64(nonceBytes[:]) & (1<<63 - 1))
	_, _, err = c.exchange(callCtx, "guest-sync-delimited", map[string]int64{"id": nonce}, &nonce)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// Close only closes the control stream, not the VM or any guest command. The
// lifecycle owner must stop/reclaim the VM or implement real process cancellation.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.done); c.closeErr = c.transport.Close() })
	return c.closeErr
}

func (c *Client) acquire(ctx context.Context) (context.Context, func(), error) {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	select {
	case <-callCtx.Done():
		cancel()
		return nil, nil, callCtx.Err()
	case <-c.done:
		cancel()
		return nil, nil, ErrClosed
	case c.gate <- struct{}{}:
	}
	release := func() { <-c.gate; cancel() }
	if err := callCtx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	select {
	case <-c.done:
		release()
		return nil, nil, ErrClosed
	default:
	}
	return callCtx, release, nil
}

func validateCommand(command Command) error {
	if len(command.Path) == 0 || len(command.Path) > MaxPathBytes || len(command.Args) > MaxArgs || len(command.Stdin) > MaxStdinBytes {
		return ErrInvalidCommand
	}
	size := 0
	for _, value := range append([]string{command.Path}, command.Args...) {
		if len(value) > MaxArgumentBytes-size || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return ErrInvalidCommand
		}
		size += len(value)
	}
	return nil
}

// StartCommand sends exactly one guest-exec request, capturing stdout and stderr.
// A valid handle means the agent accepted a PID, not that the command completed.
// Input or admission errors send nothing. A StartUncertainError must be escalated
// to the lifecycle controller rather than retried. No command runtime limit or
// guest-process cancellation is provided by this RPC timeout.
func (c *Client) StartCommand(ctx context.Context, command Command) (Handle, error) {
	if err := validateCommand(command); err != nil {
		return Handle{}, err
	}
	callCtx, release, err := c.acquire(ctx)
	if err != nil {
		return Handle{}, err
	}
	defer release()
	if len(c.handles) >= MaxCommands {
		return Handle{}, ErrCapacity
	}
	data, sent, err := c.exchange(callCtx, "guest-exec", execArguments(command), nil)
	if err != nil {
		var remote *RemoteError
		if sent && !errors.As(err, &remote) {
			err = &StartUncertainError{Err: err}
		}
		return Handle{}, err
	}
	pid, err := parsePID(data)
	if err == nil {
		for _, activePID := range c.handles {
			if pid == activePID {
				err = fmt.Errorf("%w: duplicate active PID", ErrProtocol)
				break
			}
		}
	}
	if err != nil {
		_ = c.Close()
		return Handle{}, &StartUncertainError{Err: err}
	}
	key := &handleKey{marker: 1}
	c.handles[key] = pid
	return Handle{owner: c, key: key}, nil
}

// CommandStatus checks only a live handle from this client. Exited statuses
// retire all copies, including when output is truncated. Errors never imply
// guest-process cancellation. A remote rejection also retires the handle:
// callers cannot use it repeatedly to probe a PID that may have been reused.
func (c *Client) CommandStatus(ctx context.Context, handle Handle) (Status, error) {
	if handle.owner != c || handle.key == nil {
		return Status{}, ErrInvalidHandle
	}
	callCtx, release, err := c.acquire(ctx)
	if err != nil {
		return Status{}, err
	}
	defer release()
	pid, ok := c.handles[handle.key]
	if !ok {
		return Status{}, ErrInvalidHandle
	}
	data, _, err := c.exchange(callCtx, "guest-exec-status", map[string]int64{"pid": pid}, nil)
	if err != nil {
		var remote *RemoteError
		if errors.As(err, &remote) {
			delete(c.handles, handle.key)
		}
		return Status{}, err
	}
	status, err := parseStatus(data)
	if status.Exited {
		delete(c.handles, handle.key)
	}
	if err != nil {
		if !errors.Is(err, ErrOutputTruncated) {
			_ = c.Close()
		}
		return Status{}, err
	}
	return status, nil
}
