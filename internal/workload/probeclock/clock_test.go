package probeclock

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnchoredClockRejectsWallOnlyAndPreservesAbsoluteDeadline(t *testing.T) {
	a := time.Now()
	c, e := newClock(a, 100000, bytes.NewReader(bytes.Repeat([]byte{7}, 48)))
	if e != nil {
		t.Fatal(e)
	}
	deadline, e := c.TimeAt(107999)
	if e != nil {
		t.Fatal(e)
	}
	n, e := c.Millis(deadline)
	if e != nil || n != 107999 {
		t.Fatal(n, e)
	}
	if deadline.Sub(a) != 7999*time.Millisecond {
		t.Fatal("deadline rebased")
	}
	if _, e = c.Millis(deadline.Round(0)); e == nil {
		t.Fatal("wall-only evidence accepted")
	}
	if _, e = newClock(a.Round(0), 1000, bytes.NewReader(make([]byte, 48))); e == nil {
		t.Fatal("wall anchor")
	}
	if _, e = c.TimeAt(1 << 53); e == nil {
		t.Fatal("overflow")
	}
}
func TestSampleAuthenticationAndIncarnation(t *testing.T) {
	c, e := newClock(time.Now(), 100000, bytes.NewReader(bytes.Repeat([]byte{3}, 48)))
	if e != nil {
		t.Fatal(e)
	}
	nonce := strings.Repeat("ab", 32)
	s, e := c.Sample(nonce)
	if e != nil {
		t.Fatal(e)
	}
	m := hmac.New(sha256.New, c.key[:])
	fmt.Fprintf(m, "TCS-CLOCK-2\n%s\n%s\n%d\n%d\n", s.Domain, s.Nonce, s.TicksMs, s.ValidUntilMs)
	if s.MAC != hex.EncodeToString(m.Sum(nil)) || s.ValidUntilMs-s.TicksMs != 30000 {
		t.Fatal("sample contract")
	}
	if _, e = c.Sample("bad"); e == nil {
		t.Fatal("nonce accepted")
	}
	d, _ := newClock(time.Now(), 100000, bytes.NewReader(bytes.Repeat([]byte{4}, 48)))
	if c.Domain() == d.Domain() {
		t.Fatal("incarnation reused")
	}
}
func TestSignedResponseBindsPathStatusBody(t *testing.T) {
	c, _ := newClock(time.Now(), 100000, bytes.NewReader(bytes.Repeat([]byte{5}, 48)))
	h := c.SignedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202); _, _ = w.Write([]byte("retained")) }))
	r := httptest.NewRequest("POST", "http://localhost/probe/read", nil)
	r.Header.Set("X-Simulator-Clock-Nonce", strings.Repeat("ab", 32))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	req := sha256.Sum256(nil)
	m := hmac.New(sha256.New, c.key[:])
	fmt.Fprintf(m, "TCS-RESPONSE-3\n%s\n%s\nPOST\n/probe/read\n%x\n202\n8\nretained", c.Domain(), strings.Repeat("ab", 32), req)
	if w.Header().Get("X-Simulator-Clock-MAC") != hex.EncodeToString(m.Sum(nil)) {
		t.Fatal("response authentication")
	}
	if w.Body.String() != "retained" || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("reply body or media type changed")
	}
}
func TestSignedResponseNeverReflectsMarkupMediaType(t *testing.T) {
	c, _ := newClock(time.Now(), 100000, bytes.NewReader(bytes.Repeat([]byte{6}, 48)))
	for in, want := range map[string]string{"application/json": "application/json", "text/html; charset=utf-8": "text/plain; charset=utf-8", "": "text/plain; charset=utf-8"} {
		h := c.SignedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if in != "" {
				w.Header().Set("Content-Type", in)
			}
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("<b>x</b>"))
		}))
		r := httptest.NewRequest("POST", "http://localhost/probe/read", nil)
		r.Header.Set("X-Simulator-Clock-Nonce", strings.Repeat("cd", 32))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if got := w.Header().Get("Content-Type"); got != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("content type %q -> %q", in, got)
		}
	}
}
func TestMonotonicMillisBeyondNanosecondSafeInteger(t *testing.T) {
	// 105 days exceeds JavaScript's exact-integer nanosecond range. Wire values
	// are integer milliseconds, never a float64 conversion of nanoseconds.
	const ticks int64 = 105 * 24 * 60 * 60 * 1000
	c, e := newClock(time.Now(), ticks, bytes.NewReader(bytes.Repeat([]byte{8}, 48)))
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.TimeAt(ticks + 7999)
	if e != nil {
		t.Fatal(e)
	}
	n, e := c.Millis(v)
	if e != nil || n != ticks+7999 {
		t.Fatal(n, e)
	}
}

func TestPlatformClockIdentityIsExplicitAndLinuxWireUnchanged(t *testing.T) {
	entropy := func() *bytes.Reader { return bytes.NewReader(bytes.Repeat([]byte{9}, 48)) }
	linux, e := newClock(time.Now(), 1000, entropy())
	if e != nil {
		t.Fatal(e)
	}
	if linux.Bootstrap().Clock != "linux-clock-monotonic" || linux.Bootstrap().Version != 2 {
		t.Fatal("Linux wire changed")
	}
	darwin, e := newPlatformClock(time.Now(), 1000, entropy(), darwinUptimeRaw)
	if e != nil {
		t.Fatal(e)
	}
	if darwin.Bootstrap().Clock != "darwin-clock-uptime-raw" {
		t.Fatal("Darwin mislabelled")
	}
	for _, kind := range []string{"", "wall", "CLOCK_MONOTONIC", "linux-clock-monotonic-extra"} {
		if c, e := newPlatformClock(time.Now(), 1000, entropy(), kind); c != nil || e == nil {
			t.Fatal("unknown clock accepted")
		}
	}
}
