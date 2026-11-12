package guestvm

import (
	"context"
	"errors"
	"testing"
)

// This exercises adapter wiring only. The context is canceled before Launch;
// guestprocess must return before its pidfd probe or any process creation. Real
// helper-process lifecycle tests live in guestprocess, not this VM plan test.
func TestPreparedLaunchCanceledBeforeAnyExecution(t *testing.T) {
	requireLinux(t)
	b, layout := testBundle(t, imageHeader())
	p, err := Prepare(context.Background(), shortDir(t), b, layout, acquire(), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	process, err := p.Launch(ctx)
	if process != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled plan accepted: %v", err)
	}
}
