package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

type localSSOReadyWriter struct{ ready chan string }

func (w localSSOReadyWriter) Write(b []byte) (int, error) {
	if !bytes.Contains(b, []byte("Local SSO sample ready")) {
		return 0, errors.New("unexpected launcher output")
	}
	w.ready <- string(b)
	return len(b), nil
}

var localSSOTestInput = regexp.MustCompile(`<input type="hidden" name="([A-Za-z]+)" value="([^"]+)">`)
var localSSOTestForm = regexp.MustCompile(`<form(?: id="return")? method="post" action="([^"]+)">`)
var localSSOTestContinue = regexp.MustCompile(`<a id="callback" rel="noreferrer" href="([^"]+)">`)

func TestLocalSSOActualSignedLoopbackFlow(t *testing.T) {
	private := t.TempDir()
	t.Setenv("TMPDIR", private)
	t.Setenv("TMP", private)
	t.Setenv("TEMP", private)
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan struct{})
	var err error
	var addresses []string
	go func() {
		defer close(done)
		err = runLocalSSOWithListener(parent, nil, localSSOReadyWriter{ready}, func(network, address string) (net.Listener, error) {
			l, e := net.Listen(network, address)
			if e == nil {
				addresses = append(addresses, l.Addr().String())
			}
			return l, e
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("launcher did not cleanly stop")
		}
	})
	var lines []string
	select {
	case output := <-ready:
		lines = strings.Split(strings.TrimRight(output, "\n"), "\n")
	case <-done:
		t.Fatalf("launcher stopped before delivering readiness: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("launcher did not become ready")
	}
	if len(lines) != 5 || len(addresses) != 4 {
		t.Fatal("unexpected local launcher readiness projection")
	}
	root := "https://" + addresses[3]
	cognito := "https://" + addresses[1]
	idp := "https://" + addresses[2]
	if lines[1] != "Open: "+root+"/" {
		t.Fatal("browser origin is not the reserved sample listener")
	}
	caFile, hasCA := strings.CutPrefix(lines[2], "Trust this per-run CA only in an isolated browser profile: ")
	codeFile, hasCode := strings.CutPrefix(lines[3], "One-shot Entra code file (private): ")
	if !hasCA || !hasCode || filepath.Dir(caFile) != filepath.Dir(codeFile) ||
		filepath.Dir(filepath.Dir(caFile)) != private {
		t.Fatal("private material was not placed under the run directory")
	}
	roots := x509.NewCertPool()
	pem, e := os.ReadFile(caFile)
	if e != nil || !roots.AppendCertsFromPEM(pem) {
		t.Fatal("could not load the actual launcher trust root")
	}
	codeBytes, e := os.ReadFile(codeFile)
	if e != nil {
		t.Fatal("could not load the one-time fixture code")
	}
	code := strings.TrimSpace(string(codeBytes))
	if !localSSOOpaque(code) || strings.Contains(strings.Join(lines, "\n"), code) {
		t.Fatal("one-time fixture code is invalid or present in stdout")
	}
	for _, f := range []string{caFile, codeFile} {
		info, e := os.Stat(f)
		if e != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			t.Fatal("per-run trust/code file does not have private mode")
		}
	}
	if conn, e := net.DialTimeout("tcp4", addresses[0], time.Second); e == nil {
		_ = conn.Close()
		t.Fatal("bootstrap listener is still open after readiness")
	}
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		DisableKeepAlives: true, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			for _, allowed := range addresses[1:] {
				if network == "tcp" && addr == allowed {
					return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", addr)
				}
			}
			return nil, errors.New("test refused a destination outside the own fixture")
		}}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, Jar: jar, Timeout: 6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	call := func(stage, method, address string, values url.Values, origin string, status int) (string, http.Header) {
		t.Helper()
		var body io.Reader
		if values != nil {
			body = strings.NewReader(values.Encode())
		}
		req, e := http.NewRequestWithContext(t.Context(), method, address, body)
		if e != nil {
			t.Fatalf("%s: request creation failed", stage)
		}
		if values != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, e := httpClient.Do(req)
		if e != nil {
			t.Fatalf("%s: strict local HTTPS failed", stage)
		}
		defer resp.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
		if e != nil || len(raw) > 256<<10 || resp.StatusCode != status {
			t.Fatalf("%s: unexpected response status %d", stage, resp.StatusCode)
		}
		return string(raw), resp.Header
	}
	target := func(stage, address, origin, suffix string) string {
		t.Helper()
		x, e := url.Parse(html.UnescapeString(address))
		if e != nil || x.Scheme+"://"+x.Host != origin || x.User != nil || x.Fragment != "" ||
			!strings.HasSuffix(x.Path, suffix) {
			t.Fatalf("%s: unexpected form action or redirect origin", stage)
		}
		return x.String()
	}
	markup := func(stage, body string, pattern *regexp.Regexp) string {
		t.Helper()
		m := pattern.FindStringSubmatch(body)
		if len(m) != 2 {
			t.Fatalf("%s: expected guarded native HTML form/link was absent", stage)
		}
		return m[1]
	}
	fields := func(stage, body string, names ...string) url.Values {
		t.Helper()
		out := make(url.Values)
		for _, match := range localSSOTestInput.FindAllStringSubmatch(body, -1) {
			out.Add(match[1], html.UnescapeString(match[2]))
		}
		if len(out) != len(names) {
			t.Fatalf("%s: unexpected form fields", stage)
		}
		for _, name := range names {
			if len(out[name]) != 1 || out.Get(name) == "" {
				t.Fatalf("%s: missing or duplicate form field", stage)
			}
		}
		return out
	}
	body, _ := call("sample", http.MethodGet, root+"/", nil, "", http.StatusOK)
	if !strings.Contains(body, "Sign in with local Entra") {
		t.Fatal("native sample start link absent")
	}
	_, headers := call("start", http.MethodGet, root+"/start", nil, "", http.StatusSeeOther)
	authorize := target("start", headers.Get("Location"), cognito, "/oauth2/authorize")
	_, headers = call("Cognito authorize", http.MethodGet, authorize, nil, "", http.StatusFound)
	login := target("authorize", headers.Get("Location"), idp, "/saml2")
	body, _ = call("local sign-in prompt", http.MethodGet, login, nil, "", http.StatusOK)
	form := target("prompt", markup("prompt", body, localSSOTestForm), idp, "/saml2")
	values := fields("prompt", body, "flow")
	values.Set("code", code)
	body, _ = call("one-time identity", http.MethodPost, form, values, idp, http.StatusOK)
	consume := target("identity", markup("identity", body, localSSOTestForm), cognito, "/saml2/idpresponse")
	body, _ = call("Cognito ACS", http.MethodPost, consume, fields("identity", body, "SAMLResponse", "RelayState"), idp, http.StatusOK)
	callback := target("ACS", markup("ACS", body, localSSOTestContinue), root, "/callback")
	body, headers = call("validated application callback", http.MethodGet, callback, nil, "", http.StatusOK)
	if !strings.Contains(body, "Local sign-in verified") || headers.Get("Cache-Control") != "no-store" ||
		strings.Contains(body, "id_token") || strings.Contains(body, code) {
		t.Fatal("sample did not show only the verified acknowledgement")
	}
	body, _ = call("browser reload of finished callback", http.MethodGet, callback, nil, "", http.StatusBadRequest)
	if strings.Contains(body, "Local sign-in verified") {
		t.Fatal("a completed one-use callback was accepted again")
	}
	cancel()
	select {
	case <-done:
		if err != nil {
			t.Fatalf("native launcher did not shut down successfully: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("native launcher cleanup timed out")
	}
	files, e := os.ReadDir(private)
	if e != nil || len(files) != 0 {
		t.Fatal("actual successful run did not remove private state")
	}
	for i, addr := range addresses {
		if conn, e := net.DialTimeout("tcp4", addr, time.Second); e == nil {
			_ = conn.Close()
			t.Errorf("run left listener %d accepting connections", i)
		}
	}
}

type localSSOFailOutput struct{ err error }

func (w localSSOFailOutput) Write([]byte) (int, error) { return 0, w.err }

func TestLocalSSOReadinessOutputFailureTearsDownInitializedRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	closedOutput := errors.New("fixture output is unavailable")
	parent, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	var bound []string
	err := runLocalSSOWithListener(parent, nil, localSSOFailOutput{closedOutput}, func(network, host string) (net.Listener, error) {
		l, e := net.Listen(network, host)
		if e == nil {
			bound = append(bound, l.Addr().String())
		}
		return l, e
	})
	if !errors.Is(err, closedOutput) || len(bound) != 4 {
		t.Fatalf("initialized launch did not report its output failure; listeners=%d", len(bound))
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("failed readiness publication left its private files")
	}
	for i, addr := range bound {
		if conn, err := net.DialTimeout("tcp4", addr, time.Second); err == nil {
			_ = conn.Close()
			t.Errorf("failed readiness publication left listener %d", i)
		}
	}
}
