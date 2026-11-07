package server

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func renewalHeaders() map[string]string {
	return map[string]string{"Origin": "http://127.0.0.1:4566", "Sec-Fetch-Site": "same-origin", "X-Simulator-Session-Action": "renew"}
}
func renewBrowserForTest(a *Access, cookie *http.Cookie) int {
	return browserRequest(a, "POST", "/_simulator/session/renew", "", "", cookie, renewalHeaders()).Code
}
func TestBrowserRenewalIdleAndAbsoluteCap(t *testing.T) {
	a, operator, clock := loginExperienceAccess(t)
	start := clock.now()
	cookie := browserSignIn(t, a, operator, nil)
	for _, minute := range []int{14, 28, 42, 56, 59} {
		clock.set(start.Add(time.Duration(minute) * time.Minute))
		r := browserRequest(a, "POST", "/_simulator/session/renew", "", "", cookie, renewalHeaders())
		if r.Code != 200 {
			t.Fatal("live operator renewal refused")
		}
		var value map[string]time.Time
		if json.Unmarshal(r.Body.Bytes(), &value) != nil || len(value) != 2 || !value["hardExpiresAt"].Equal(start.Add(time.Hour)) {
			t.Fatal("renewal metadata must preserve original hard deadline")
		}
		expected := clock.now().Add(15 * time.Minute)
		if expected.After(start.Add(time.Hour)) {
			expected = start.Add(time.Hour)
		}
		if !value["expiresAt"].Equal(expected) || !loginExperienceDeadline(a, cookie).Equal(expected) {
			t.Fatal("idle deadline not bounded by initial hard deadline")
		}
		got := r.Result().Cookies()
		if len(got) != 1 || got[0].Value != cookie.Value || !got[0].HttpOnly || got[0].SameSite != http.SameSiteStrictMode || got[0].Path != "/" {
			t.Fatal("renew changed cookie authority or flags")
		}
	}
	clock.set(start.Add(time.Hour))
	if renewBrowserForTest(a, cookie) != 401 || browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 401 {
		t.Fatal("exact hard deadline must deny use and renewal")
	}
	if browserRequest(a, "GET", "/", "", operator, nil, nil).Code != 204 {
		t.Fatal("browser cap changed SDK authority")
	}
}
func TestBrowserRenewalExactIdleBoundary(t *testing.T) {
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		t.Run(delta.String(), func(t *testing.T) {
			a, operator, c := loginExperienceAccess(t)
			start := c.now()
			cookie := browserSignIn(t, a, operator, nil)
			c.set(start.Add(15*time.Minute + delta))
			want := 401
			if delta < 0 {
				want = 200
			}
			if renewBrowserForTest(a, cookie) != want {
				t.Fatal("idle boundary changed")
			}
		})
	}
}
func TestBrowserRenewalNoRevivalAfterIdleLogoutOrRestart(t *testing.T) {
	for _, kind := range []string{"idle", "logout", "restart", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			a, operator, c := loginExperienceAccess(t)
			cookie := browserSignIn(t, a, operator, nil)
			switch kind {
			case "idle":
				c.set(c.now().Add(15 * time.Minute))
			case "logout":
				browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, nil)
			case "restart":
				a, _ = NewAccess(a.next, operator, false)
				a.now = c.now
			case "unknown":
				cookie = &http.Cookie{Name: cookie.Name, Value: strings.Repeat("x", 64)}
			}
			if renewBrowserForTest(a, cookie) != 401 {
				t.Fatal("renewal revived missing authority")
			}
			if len(a.browsers) != 0 {
				t.Fatal("renewal minted a new browser slot")
			}
		})
	}
}
func TestBrowserRenewalParticipantsRemainFixedAndParentBound(t *testing.T) {
	for _, kind := range []string{"live", "expired", "revoked", "changed"} {
		t.Run(kind, func(t *testing.T) {
			a, operator, c := loginExperienceAccess(t)
			token := participantCredential(t, a, operator)
			cookie := browserSignIn(t, a, token, nil)
			original := loginExperienceDeadline(a, cookie)
			want := 403
			switch kind {
			case "expired":
				c.set(c.now().Add(15 * time.Minute))
				want = 401
			case "revoked":
				delete(a.sessions, sha256.Sum256([]byte(token)))
				want = 401
			case "changed":
				parent := a.sessions[sha256.Sum256([]byte(token))]
				parent.principal = "different"
				a.sessions[sha256.Sum256([]byte(token))] = parent
				want = 401
			}
			if renewBrowserForTest(a, cookie) != want {
				t.Fatal("participant renewal admitted or parent binding ignored")
			}
			if kind == "live" && !loginExperienceDeadline(a, cookie).Equal(original) {
				t.Fatal("participant deadline extended")
			}
		})
	}
}
func TestBrowserRenewalTwoBrowsersAndConcurrentTabs(t *testing.T) {
	a, operator, c := loginExperienceAccess(t)
	start := c.now()
	one := browserSignIn(t, a, operator, nil)
	two := browserSignIn(t, a, operator, nil)
	c.set(start.Add(14 * time.Minute))
	var wg sync.WaitGroup
	codes := make(chan int, 20)
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- renewBrowserForTest(a, one) }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal("concurrent renewal failed")
		}
	}
	if !loginExperienceDeadline(a, one).Equal(start.Add(29*time.Minute)) || !loginExperienceDeadline(a, two).Equal(start.Add(15*time.Minute)) || len(a.browsers) != 2 {
		t.Fatal("concurrency drifted deadline, quota or unrelated browser")
	}
}
func TestBrowserRenewalOriginMethodAndSDKRefusals(t *testing.T) {
	a, operator, _ := loginExperienceAccess(t)
	cookie := browserSignIn(t, a, operator, nil)
	original := loginExperienceDeadline(a, cookie)
	for _, kind := range []string{"origin-missing", "foreign-origin", "port", "scheme", "same-site", "cross-site", "marker-missing", "sdk", "no-cookie", "get"} {
		t.Run(kind, func(t *testing.T) {
			headers := renewalHeaders()
			method := "POST"
			sdk := ""
			current := cookie
			want := 403
			switch kind {
			case "origin-missing":
				delete(headers, "Origin")
			case "foreign-origin":
				headers["Origin"] = "http://localhost:4566"
			case "port":
				headers["Origin"] = "http://127.0.0.1:4567"
			case "scheme":
				headers["Origin"] = "https://127.0.0.1:4566"
			case "same-site":
				headers["Sec-Fetch-Site"] = "same-site"
			case "cross-site":
				headers["Sec-Fetch-Site"] = "cross-site"
			case "marker-missing":
				delete(headers, "X-Simulator-Session-Action")
			case "sdk":
				sdk = operator
			case "no-cookie":
				current = nil
				want = 401
			case "get":
				method = "GET"
				want = 405
			}
			if browserRequest(a, method, "/_simulator/session/renew", "", sdk, current, headers).Code != want {
				t.Fatal("renewal boundary accepted invalid request")
			}
			if !loginExperienceDeadline(a, cookie).Equal(original) {
				t.Fatal("rejected renewal changed stored deadline")
			}
		})
	}
}
func TestBrowserRenewalSeparateTeamsCannotRenew(t *testing.T) {
	one, operator, c := loginExperienceAccess(t)
	cookie := browserSignIn(t, one, operator, nil)
	two, _ := NewAccess(one.next, strings.Repeat("z", 64), false)
	two.now = c.now
	if renewBrowserForTest(two, cookie) != 401 || len(two.browsers) != 0 {
		t.Fatal("browser renewed across independent team authority")
	}
}
func TestBrowserRenewalClockRollbackCannotDriftOrShorten(t *testing.T) {
	a, operator, c := loginExperienceAccess(t)
	start := c.now()
	cookie := browserSignIn(t, a, operator, nil)
	c.set(start.Add(14 * time.Minute))
	if renewBrowserForTest(a, cookie) != 200 {
		t.Fatal("initial renewal failed")
	}
	c.set(start.Add(time.Minute))
	if renewBrowserForTest(a, cookie) != 200 || !loginExperienceDeadline(a, cookie).Equal(start.Add(29*time.Minute)) {
		t.Fatal("backwards clock shortened or drifted deadline")
	}
	a.mu.Lock()
	session := a.browsers[sha256.Sum256([]byte(cookie.Value))]
	a.mu.Unlock()
	if !session.hardExpires.Equal(start.Add(time.Hour)) {
		t.Fatal("absolute issuance cap changed")
	}
}
func TestBrowserRenewalConcurrentLogoutCannotLeaveReplayAuthority(t *testing.T) {
	a, operator, _ := loginExperienceAccess(t)
	cookie := browserSignIn(t, a, operator, nil)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); renewBrowserForTest(a, cookie) }()
	go func() { defer wg.Done(); browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, nil) }()
	wg.Wait()
	if renewBrowserForTest(a, cookie) != 401 || browserRequest(a, "GET", "/", "", "", cookie, nil).Code != 401 {
		t.Fatal("concurrent logout left replay authority")
	}
}

func TestBrowserRenewalModuleExactPublicBootstrap(t *testing.T) {
	a, _, _ := loginExperienceAccess(t)
	for _, path := range []string{"/_simulator/console/session-activity.mjs", "/_simulator/console/other.mjs"} {
		want := 401
		if path == "/_simulator/console/session-activity.mjs" {
			want = 204
		}
		if browserRequest(a, "GET", path, "", "", nil, nil).Code != want {
			t.Fatal("static allowlist broadened or module cannot bootstrap")
		}
	}
	for _, headers := range []map[string]string{{"Origin": "http://localhost:4566"}, {"Sec-Fetch-Site": "cross-site"}} {
		if browserRequest(a, "GET", "/_simulator/console/session-activity.mjs", "", "", nil, headers).Code != 403 {
			t.Fatal("public module bypassed host/origin checks")
		}
	}
	if browserRequest(a, "POST", "/_simulator/console/session-activity.mjs", "", "", nil, nil).Code != 401 {
		t.Fatal("module allowance accepted non-GET request")
	}
}
