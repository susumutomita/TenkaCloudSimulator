//go:build darwin

package probeclock

import (
	"errors"
	"golang.org/x/sys/unix"
	"testing"
	"time"
)

func rawMilliseconds(t *testing.T) int64 {
	t.Helper()
	var ts unix.Timespec
	if e := unix.ClockGettime(unix.CLOCK_UPTIME_RAW, &ts); e != nil {
		t.Fatal(e)
	}
	return ts.Sec*1000 + ts.Nsec/1_000_000
}
func TestDarwinUsesActualUptimeRawWithoutEnablingExecutableLaunch(t *testing.T) {
	before := rawMilliseconds(t)
	clock, e := New()
	if e != nil {
		t.Fatal(e)
	}
	sample := clock.NowMs()
	after := rawMilliseconds(t)
	if sample < before || sample > after+1 {
		t.Fatalf("clock outside raw bracket: %d %d %d", before, sample, after)
	}
	if clock.Bootstrap().Clock != "darwin-clock-uptime-raw" {
		t.Fatal("mislabelled platform")
	}
	time.Sleep(2 * time.Millisecond)
	if clock.NowMs() <= sample {
		t.Fatal("clock did not advance")
	}
	if e := CloseInheritedExecutable(); !errors.Is(e, ErrClock) {
		t.Fatal("unqualified executable handling enabled")
	}
}
