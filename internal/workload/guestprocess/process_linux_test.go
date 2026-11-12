//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestOwnedHelper(t *testing.T) {
	mode := os.Getenv("GUESTPROCESS_TEST_HELPER")
	if mode == "" {
		return
	}
	if os.Getenv("GUESTPROCESS_MUST_NOT_INHERIT") != "" {
		os.Exit(41)
	}
	switch mode {
	case "exit":
		os.Exit(0)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "validate":
		// os.StartProcess must pass the entire literal argument unchanged.
		if len(os.Args) != 4 || os.Args[3] != "$(not-a-shell); *" {
			os.Exit(42)
		}
		for _, fd := range []int{0, 1, 2} {
			target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
			if err != nil || target != "/dev/null" {
				os.Exit(43)
			}
		}
		os.Exit(0)
	}
	os.Exit(44)
}

func helperSpec(t *testing.T, mode string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Executable: exe, Args: []string{"-test.run=^TestOwnedHelper$", "--", "$(not-a-shell); *"}, Env: []string{"GUESTPROCESS_TEST_HELPER=" + mode}}
}
func launchHelper(t *testing.T, mode string) *Process {
	t.Helper()
	p, err := Launch(context.Background(), helperSpec(t, mode))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := p.Kill(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return p
}

func TestNaturalExitAndRepeatedObservation(t *testing.T) {
	t.Setenv("GUESTPROCESS_MUST_NOT_INHERIT", "secret-test-sentinel")
	p := launchHelper(t, "validate")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := p.Wait(ctx)
	if err != nil || !result.Reaped || result.Code != 0 {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
	for range 3 {
		result, err = p.Kill(ctx)
		if err != nil || !result.Reaped {
			t.Fatalf("Kill after exit = %+v, %v", result, err)
		}
	}
}

func TestKillOwnedChildConcurrent(t *testing.T) {
	p := launchHelper(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			result, err := p.Kill(ctx)
			if err != nil || !result.Reaped {
				t.Errorf("Kill = %+v, %v", result, err)
			}
		})
	}
	wg.Wait()
}

func TestTimeoutDoesNotMeanExitOrContainment(t *testing.T) {
	p := launchHelper(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result, err := p.Wait(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || result.Reaped {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
	if err := p.Contain(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Contain = %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	result, err = p.Kill(ctx2)
	if !errors.Is(err, context.Canceled) || result.Reaped {
		t.Fatalf("canceled Kill = %+v, %v", result, err)
	}
	p.mu.Lock()
	reaped := p.result.Reaped
	p.mu.Unlock()
	if reaped {
		t.Fatal("canceled Kill unexpectedly observed reaping")
	}
}

func TestRejectInvalidBeforeLaunch(t *testing.T) {
	valid := helperSpec(t, "exit")
	cases := []Spec{{Executable: "relative", Env: []string{}}, {Executable: valid.Executable}, {Executable: valid.Executable, Env: []string{"NO_EQUALS"}}, {Executable: valid.Executable, Env: []string{"=empty"}}, {Executable: valid.Executable, Env: []string{"A=x", "A=y"}}, {Executable: valid.Executable, Env: []string{"A=\x00"}}, {Executable: valid.Executable, Env: []string{}, Args: []string{"\x00"}}}
	for _, spec := range cases {
		if p, err := Launch(context.Background(), spec); p != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid launch = %v, %v", p, err)
		}
	}
	if p, err := Launch(nil, valid); p != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context = %v, %v", p, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := Launch(ctx, valid); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch = %v, %v", p, err)
	}
	if p, err := Launch(context.Background(), Spec{Executable: "/nonexistent/guestprocess-test", Env: []string{}}); p != nil || err == nil {
		t.Fatalf("missing executable = %v, %v", p, err)
	}
}

func TestInvalidProcess(t *testing.T) {
	for _, p := range []*Process{nil, {}} {
		if _, err := p.Wait(context.Background()); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := p.Kill(context.Background()); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := p.Wait(nil); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if _, err := p.Kill(nil); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
		if err := p.Contain(nil); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestKillDoesNotAffectOtherOwnedChild(t *testing.T) {
	first := launchHelper(t, "sleep")
	second := launchHelper(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := first.Kill(ctx)
	if err != nil || !result.Reaped || result.Code != -1 {
		t.Fatalf("kill first = %+v, %v", result, err)
	}
	short, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	result, err = second.Wait(short)
	if !errors.Is(err, context.DeadlineExceeded) || result.Reaped {
		t.Fatalf("unrelated child affected = %+v, %v", result, err)
	}
}
