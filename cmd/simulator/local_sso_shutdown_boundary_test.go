package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// Model an operating system Close that closes the socket but cannot confirm
// completion to its caller. No request or TLS behavior is replaced.
type unconfirmedLocalSSOCloseListener struct {
	*net.TCPListener
	once sync.Once
}

func (l *unconfirmedLocalSSOCloseListener) Close() error {
	err := l.TCPListener.Close()
	if err == nil {
		l.once.Do(func() { err = errors.New("local fixture close acknowledgement lost") })
	}
	return err
}

func TestLocalSSONeverAdvertisesWhenBootstrapCloseIsUnconfirmed(t *testing.T) {
	root := t.TempDir()
	setLocalSSOStartupTemp(t, root)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var raw []*net.TCPListener
	t.Cleanup(func() {
		for _, l := range raw {
			_ = l.Close()
		}
	})
	var output bytes.Buffer
	err := runLocalSSOWithListener(ctx, nil, &output, func(network, address string) (net.Listener, error) {
		if network != "tcp4" || address != "127.0.0.1:0" {
			return nil, errors.New("non-isolated fixture listener")
		}
		l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return nil, err
		}
		raw = append(raw, l)
		if len(raw) == 1 {
			return &unconfirmedLocalSSOCloseListener{TCPListener: l}, nil
		}
		return l, nil
	})
	if !errors.Is(err, errLocalSSOCloseListener) || output.Len() != 0 || len(raw) != 4 {
		t.Fatalf("bootstrap close uncertainty was not surfaced before readiness (listeners=%d, error=%v)", len(raw), err)
	}
	for i, l := range raw {
		if !errors.Is(l.SetDeadline(time.Now()), net.ErrClosed) {
			t.Errorf("launcher left listener %d open", i)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatal("launcher retained temporary state after refusing readiness", err)
	}
}
