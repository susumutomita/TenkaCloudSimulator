package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Break one real transport connection and close its owned listener. The
// preceding operations use ordinary verified TLS and signed native requests.
type localSSOEnrollmentInterruptedListener struct {
	*net.TCPListener
	stopAt int
	calls  atomic.Int32
}

func (l *localSSOEnrollmentInterruptedListener) Accept() (net.Conn, error) {
	conn, err := l.TCPListener.Accept()
	if err != nil {
		return nil, err
	}
	if int(l.calls.Add(1)) != l.stopAt {
		return conn, nil
	}
	_ = conn.Close()
	if err := l.TCPListener.Close(); err != nil {
		return nil, err
	}
	return l.TCPListener.Accept()
}

type localSSOEnrollmentReadyWriter struct {
	bytes.Buffer
	stop context.CancelFunc
}

func (w *localSSOEnrollmentReadyWriter) Write(p []byte) (int, error) {
	if !bytes.HasPrefix(p, []byte("Local SSO sample ready")) {
		return 0, errors.New("retry did not write readiness")
	}
	n, err := w.Buffer.Write(p)
	w.stop()
	return n, err
}

func TestLocalSSORetriesAfterPrivateEnrollmentListenerStops(t *testing.T) {
	for _, tc := range []struct {
		operation  string
		connection int
	}{
		{"CreateIdentityProvider", 2},
		{"CreateUserPoolClient", 3},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			root := t.TempDir()
			setLocalSSOStartupTemp(t, root)
			var acquired []*net.TCPListener
			t.Cleanup(func() {
				for _, l := range acquired {
					_ = l.Close()
				}
			})
			listen := func(network, address string) (net.Listener, error) {
				if network != "tcp4" || address != "127.0.0.1:0" {
					return nil, errors.New("non-isolated fixture listener")
				}
				l, err := net.Listen(network, address)
				if err != nil {
					return nil, err
				}
				tcp := l.(*net.TCPListener)
				acquired = append(acquired, tcp)
				return tcp, nil
			}
			clean := func(count int) {
				t.Helper()
				files, err := os.ReadDir(root)
				if err != nil || len(files) != 0 {
					t.Fatal("launcher left partial trust, code or state in the private fixture directory", err)
				}
				if len(acquired) != count {
					t.Fatalf("launcher opened %d listeners, want %d", len(acquired), count)
				}
				for i, l := range acquired {
					if !errors.Is(l.SetDeadline(time.Now()), net.ErrClosed) {
						t.Errorf("launcher left owned listener %d open", i)
					}
				}
			}
			var interrupted *localSSOEnrollmentInterruptedListener
			var output bytes.Buffer
			ctx, stop := context.WithTimeout(t.Context(), 15*time.Second)
			defer stop()
			err := runLocalSSOWithListener(ctx, nil, &output, func(network, address string) (net.Listener, error) {
				l, err := listen(network, address)
				if err != nil || len(acquired) != 1 {
					return l, err
				}
				// The first socket is the private management service. Its
				// client uses separate connections for its three ordered calls.
				interrupted = &localSSOEnrollmentInterruptedListener{TCPListener: acquired[0], stopAt: tc.connection}
				return interrupted, nil
			})
			want := "signed local Cognito " + tc.operation + " failed"
			if err == nil || err.Error() != want || output.Len() != 0 || interrupted == nil ||
				int(interrupted.calls.Load()) != tc.connection {
				t.Fatalf("partial enrollment was not refused at the intended phase: %v", err)
			}
			clean(4)

			retryCtx, stopRetry := context.WithTimeout(t.Context(), 15*time.Second)
			defer stopRetry()
			recovered := &localSSOEnrollmentReadyWriter{stop: stopRetry}
			if err := runLocalSSOWithListener(retryCtx, nil, recovered, listen); err != nil ||
				!bytes.Contains(recovered.Bytes(), []byte("Open: https://127.0.0.1:")) {
				t.Fatalf("fresh local restart did not reach verified readiness: %v", err)
			}
			clean(8)
		})
	}
}
