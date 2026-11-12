// Package probeclock owns a process-incarnation monotonic clock. Samples are
// authenticated transport data, not proof that a remote machine shares a clock.
package probeclock

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

const MaxValidity = 30 * time.Second
const Protocol = 2

var ErrClock = errors.New("unqualified probe clock")

type Clock struct {
	domain string
	key    [32]byte
	anchor time.Time
	ticks  int64
}
type Bootstrap struct {
	Version int    `json:"version"`
	Domain  string `json:"domain"`
	Key     string `json:"key"`
	Clock   string `json:"clock"`
}
type Sample struct {
	Version      int    `json:"version"`
	Domain       string `json:"domain"`
	Nonce        string `json:"nonce"`
	TicksMs      int64  `json:"ticksMs"`
	ValidUntilMs int64  `json:"validUntilMs"`
	MAC          string `json:"mac"`
}

// bootstrap must be the launcher-inherited private pipe, never stdout, a
// participant connection, configuration file, or an HTTP response.
func (c *Clock) Bootstrap() Bootstrap {
	return Bootstrap{Protocol, c.domain, hex.EncodeToString(c.key[:]), "linux-clock-monotonic"}
}
func (c *Clock) Domain() string { return c.domain }
func newClock(anchor time.Time, ticks int64, entropy io.Reader) (*Clock, error) {
	if anchor == anchor.Round(0) || ticks < 1 || ticks > 1<<53-1 {
		return nil, ErrClock
	}
	c := &Clock{anchor: anchor, ticks: ticks}
	var id [16]byte
	if _, e := io.ReadFull(entropy, id[:]); e != nil {
		return nil, e
	}
	if _, e := io.ReadFull(entropy, c.key[:]); e != nil {
		return nil, e
	}
	c.domain = "mono-" + hex.EncodeToString(id[:])
	return c, nil
}
func (c *Clock) NowMs() int64 { return c.ticks + time.Since(c.anchor).Milliseconds() }
func (c *Clock) TimeAt(ms int64) (time.Time, error) {
	if ms < 1 || ms > 1<<53-1 {
		return time.Time{}, ErrClock
	}
	delta := ms - c.ticks
	if delta > int64((24*time.Hour)/time.Millisecond) || delta < -int64((24*time.Hour)/time.Millisecond) {
		return time.Time{}, ErrClock
	}
	return c.anchor.Add(time.Duration(delta) * time.Millisecond), nil
}

// Millis refuses wall-only timestamps. Rounding a time or decoding it from JSON
// destroys its monotonic component and cannot establish operation timing.
func (c *Clock) Millis(t time.Time) (int64, error) {
	if t == t.Round(0) {
		return 0, ErrClock
	}
	d := t.Sub(c.anchor)
	n := c.ticks + int64(d/time.Millisecond)
	if d < 0 && d%time.Millisecond != 0 {
		n--
	}
	if n < 1 || n > 1<<53-1 {
		return 0, ErrClock
	}
	return n, nil
}
func (c *Clock) Sample(nonce string) (Sample, error) {
	raw, e := hex.DecodeString(nonce)
	if e != nil || len(raw) != 32 || hex.EncodeToString(raw) != nonce {
		return Sample{}, ErrClock
	}
	now := c.NowMs()
	s := Sample{Version: Protocol, Domain: c.domain, Nonce: nonce, TicksMs: now, ValidUntilMs: now + MaxValidity.Milliseconds()}
	mac := hmac.New(sha256.New, c.key[:])
	fmt.Fprintf(mac, "TCS-CLOCK-2\n%s\n%s\n%d\n%d\n", s.Domain, s.Nonce, s.TicksMs, s.ValidUntilMs)
	s.MAC = hex.EncodeToString(mac.Sum(nil))
	return s, nil
}

var randomSource io.Reader = rand.Reader
