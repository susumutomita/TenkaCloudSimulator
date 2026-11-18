package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sacloud/sacloud-sdk-go/common/saclient"
)

func TestTransportRejectsUnownedOrigins(t *testing.T) {
	for _, origin := range []string{"https://127.0.0.1:1234", "http://localhost:1234", "http://example.com:80", "http://127.0.0.1", "http://user@127.0.0.1:1234", "http://127.0.0.1:1234/path", "http://127.0.0.1:1234?x=y"} {
		if _, _, err := localTransport(config{Origin: origin, Token: "dummy"}); err == nil {
			t.Fatalf("accepted unowned origin %q", origin)
		}
	}
}

func TestTerminalTransportRefusesRoutesAndDoesNotFollowRedirect(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, pass, ok := r.BasicAuth()
		if !ok || user != "TCSIMSAKURA" || pass != "dummy-local-only" || r.Header.Get("X-Simulator-Token") != "dummy" || r.Header.Get("X-Simulator-Principal") != "learner" {
			t.Error("missing local protocol authority")
		}
		if r.URL.Path != "/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/disk" || r.URL.RawQuery != "%7B%22Count%22:1%7D" {
			t.Error("SDK path/query changed")
		}
		w.Header().Set("Location", "https://example.com/cloud")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	middleware, closeTransport, err := localTransport(config{Origin: server.URL, Token: "dummy"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	pull := func() (saclient.Middleware, bool) { panic("stock chain reached") }
	for _, target := range []string{"http://secure.sakura.ad.jp/cloud/zone/is1a/api/cloud/1.1/disk", "https://example.com/cloud/zone/is1a/api/cloud/1.1/disk", "https://secure.sakura.ad.jp/cloud/zone/is1b/api/cloud/1.1/disk", "https://secure.sakura.ad.jp/cloud/zone/is1a/api/cloud/1.1/server"} {
		req, _ := http.NewRequest("GET", target, nil)
		if _, err := middleware(req, pull); err == nil {
			t.Errorf("accepted route %s", target)
		}
	}
	req, _ := http.NewRequest("GET", "https://secure.sakura.ad.jp/cloud/zone/is1a/api/cloud/1.1/disk?%7B%22Count%22:1%7D", nil)
	resp, err := middleware(req, pull)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || calls != 1 {
		t.Fatal("redirect followed or unowned request sent")
	}
}
