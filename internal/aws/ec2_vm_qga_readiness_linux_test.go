//go:build linux

package aws

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
	"io"
	"syscall"
	"testing"
	"time"
)

type qgaBootMemoryTransport struct {
	closes int
	err    error
}

func (t *qgaBootMemoryTransport) Read([]byte) (int, error)    { return 0, io.EOF }
func (t *qgaBootMemoryTransport) Write(b []byte) (int, error) { return len(b), nil }
func (t *qgaBootMemoryTransport) SetDeadline(time.Time) error { return nil }
func (t *qgaBootMemoryTransport) Close() error                { t.closes++; return t.err }
func TestCanonicalQGAReadinessMemoryOnly(t *testing.T) {
	for _, mode := range []string{"timeout-ready", "exhaustion", "permission", "protocol", "cancel", "uncertain-close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			deadline, _ := ctx.Deadline()
			calls := 0
			handshakes := 0
			transports := []*qgaBootMemoryTransport{}
			ops := qgaBootOperations{dial: func(ctx context.Context, path string) (qga.Transport, error) {
				calls++
				if path != "/fixed/private/qga.sock" {
					t.Fatal("alternate path")
				}
				if d, _ := ctx.Deadline(); d.After(deadline) {
					t.Fatal("renewed deadline")
				}
				if mode == "permission" {
					return nil, syscall.EPERM
				}
				tr := &qgaBootMemoryTransport{}
				if mode == "uncertain-close" {
					tr.err = errors.New("unknown close")
				}
				transports = append(transports, tr)
				return tr, nil
			}, handshake: func(ctx context.Context, tr qga.Transport) (io.Closer, error) {
				handshakes++
				if mode == "protocol" {
					return nil, qga.ErrProtocol
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "timeout-ready" && handshakes == 2 {
					return tr, nil
				}
				return nil, context.DeadlineExceeded
			}, pause: func(ctx context.Context) error { return ctx.Err() }}
			client, err := awaitCanonicalQGA(ctx, "/fixed/private/qga.sock", ops)
			if mode == "timeout-ready" {
				if err != nil || client == nil || calls != 2 {
					t.Fatal(calls, err)
				}
				_ = client.Close()
			} else {
				if err == nil {
					t.Fatal("failure accepted")
				}
				if mode == "exhaustion" && calls != vmQGAMaxHandshakes {
					t.Fatal(calls)
				}
				if mode != "exhaustion" && calls != 1 {
					t.Fatal("terminal failure retried", calls)
				}
			}
			if mode == "uncertain-close" && client == nil {
				t.Fatal("lost uncertain transport")
			}
			for _, tr := range transports {
				if tr.closes != 1 {
					t.Fatal("close retried or dropped", tr.closes)
				}
			}
		})
	}
}
