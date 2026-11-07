package server

import (
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type loginExperienceClock struct {
	mu sync.RWMutex
	at time.Time
}

func (c *loginExperienceClock) now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.at
}
func (c *loginExperienceClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}
func loginExperienceAccess(t *testing.T) (*Access, string, *loginExperienceClock) {
	t.Helper()
	a, operator := browserAccess(t)
	clock := &loginExperienceClock{at: time.Unix(1800000000, 0)}
	a.now = clock.now
	return a, operator, clock
}
func loginExperienceDeadline(a *Access, cookie *http.Cookie) time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.browsers[sha256.Sum256([]byte(cookie.Value))].expires
}

func TestLoginExperienceActivityDoesNotRenewTheFixedDeadline(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	cookie := browserSignIn(t, a, operator, nil)
	deadline := start.Add(15 * time.Minute)
	for _, minute := range []int{1, 5, 10, 14} {
		clock.set(start.Add(time.Duration(minute) * time.Minute))
		response := browserRequest(a, "GET", "/", "", "", cookie, nil)
		if response.Code != 204 || len(response.Result().Cookies()) != 0 || !loginExperienceDeadline(a, cookie).Equal(deadline) {
			t.Fatal("ordinary activity must not renew or replace the fixed session")
		}
	}
	clock.set(deadline)
	if browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 401 {
		t.Fatal("activity incorrectly extended browser access past its fixed deadline")
	}
	if browserRequest(a, "GET", "/", "", operator, nil, nil).Code != 204 {
		t.Fatal("browser expiry changed operator SDK authority")
	}
}

func TestLoginExperienceIdleAndActiveSessionsExpireTogether(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	idle := browserSignIn(t, a, operator, nil)
	active := browserSignIn(t, a, operator, nil)
	clock.set(start.Add(14 * time.Minute))
	if browserRequest(a, "GET", "/", "", "", active, nil).Code != 204 {
		t.Fatal("active session failed before expiry")
	}
	if !loginExperienceDeadline(a, idle).Equal(loginExperienceDeadline(a, active)) {
		t.Fatal("using one browser changed its absolute deadline")
	}
	clock.set(start.Add(15 * time.Minute))
	for _, cookie := range []*http.Cookie{idle, active} {
		if browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 401 {
			t.Fatal("idle or active browser escaped the same fixed deadline")
		}
	}
}

func TestLoginExperienceExactDeadlineBeforeAtAndAfter(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			a, operator, clock := loginExperienceAccess(t)
			start := clock.now()
			cookie := browserSignIn(t, a, operator, nil)
			clock.set(start.Add(15*time.Minute + delta))
			want := 401
			if delta < 0 {
				want = 204
			}
			if browserRequest(a, "GET", "/", "", "", cookie, nil).Code != want {
				t.Fatal("fixed expiry boundary changed")
			}
		})
	}
}

func TestLoginExperienceLateParticipantSignInDoesNotResetItsParent(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	participant := participantCredential(t, a, operator)
	clock.set(start.Add(14 * time.Minute))
	cookie := browserSignIn(t, a, participant, nil)
	if !loginExperienceDeadline(a, cookie).Equal(start.Add(15 * time.Minute)) {
		t.Fatal("late browser login renewed the participant's parent grant")
	}
	clock.set(start.Add(15 * time.Minute))
	if browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 401 || browserRequest(a, "GET", "/", "", participant, nil, nil).Code != 401 {
		t.Fatal("browser or SDK access outlived the participant grant")
	}
	if browserRequest(a, "GET", "/", "", operator, nil, nil).Code != 204 {
		t.Fatal("participant expiry revoked operator authority")
	}
}

func TestLoginExperienceIndependentBrowserDeadlinesAndRelogin(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	first := browserSignIn(t, a, operator, nil)
	clock.set(start.Add(5 * time.Minute))
	second := browserSignIn(t, a, operator, nil)
	clock.set(start.Add(15 * time.Minute))
	if browserRequest(a, "GET", "/", "", "", first, nil).Code != 401 || browserRequest(a, "GET", "/", "", "", second, nil).Code != 204 {
		t.Fatal("one browser's expiry affected another browser's later deadline")
	}
	fresh := browserSignIn(t, a, operator, first)
	if browserRequest(a, "GET", "/", "", "", first, nil).Code != 401 || browserRequest(a, "GET", "/", "", "", fresh, nil).Code != 204 {
		t.Fatal("explicit re-login did not reject the old cookie and accept the new one")
	}
	if browserRequest(a, "GET", "/", "", "", second, nil).Code != 204 {
		t.Fatal("re-login disturbed another browser")
	}
}

func TestLoginExperienceRestartRequiresReloginWithoutChangingSDKToken(t *testing.T) {
	a, operator, _ := loginExperienceAccess(t)
	old := browserSignIn(t, a, operator, nil)
	// A fresh authority with the SAME dummy startup token models a process
	// restart. Browser state is memory-only, even when the token file persists.
	restarted, sameOperator, clock := loginExperienceAccess(t)
	if sameOperator != operator || browserRequest(restarted, "GET", "/", "", "", old, nil).Code != 401 {
		t.Fatal("browser state unexpectedly survived a fresh Access instance")
	}
	if browserRequest(restarted, "GET", "/", "", operator, nil, nil).Code != 204 {
		t.Fatal("restart invalidated the unchanged operator SDK credential")
	}
	fresh := browserSignIn(t, restarted, operator, nil)
	if !loginExperienceDeadline(restarted, fresh).Equal(clock.now().Add(15*time.Minute)) || browserRequest(restarted, "GET", "/", "", "", fresh, nil).Code != 204 {
		t.Fatal("explicit re-login after restart failed")
	}
}

func TestLoginExperienceConcurrentActivityCannotRenewExpiry(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	cookie := browserSignIn(t, a, operator, nil)
	deadline := start.Add(15 * time.Minute)
	for _, phase := range []struct {
		at     time.Time
		status int
	}{{deadline.Add(-time.Nanosecond), 204}, {deadline, 401}} {
		clock.set(phase.at)
		results := make(chan int, 16)
		var group sync.WaitGroup
		for range 16 {
			group.Add(1)
			go func() {
				defer group.Done()
				results <- browserRequest(a, "GET", "/", "", "", cookie, nil).Code
			}()
		}
		group.Wait()
		close(results)
		for status := range results {
			if status != phase.status {
				t.Fatal("concurrent activity bypassed the unchanged expiry boundary")
			}
		}
	}
}

func TestLoginExperienceDifferentTeamAuthorityRejectsBrowserAndSDK(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	cookie := browserSignIn(t, a, operator, nil)
	otherOperator := strings.Repeat("p", 64)
	other, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), otherOperator, false)
	if err != nil {
		t.Fatal(err)
	}
	other.now = clock.now
	if browserRequest(other, "GET", "/", "", operator, nil, nil).Code != 401 || browserRequest(other, "GET", "/", "", "", cookie, nil).Code != 401 {
		t.Fatal("one team's credentials crossed its independent authority")
	}
	if browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 204 {
		t.Fatal("cross-team refusal revoked the source team's session")
	}
}
