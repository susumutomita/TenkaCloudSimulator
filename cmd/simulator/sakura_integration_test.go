package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura/iaas"
)

type sakuraStartupOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	ready chan string
	once  sync.Once
}

func (o *sakuraStartupOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, _ := o.data.Write(p)
	for _, line := range strings.Split(o.data.String(), "\n") {
		if strings.HasPrefix(line, "Console http://") {
			u, e := url.Parse(strings.TrimPrefix(line, "Console "))
			if e == nil {
				o.once.Do(func() { o.ready <- u.Scheme + "://" + u.Host })
			}
		}
	}
	return n, nil
}
func TestSakuraCLIFlagPreflightPrecedesListenerAndFiles(t *testing.T) {
	occupied, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer occupied.Close()
	for _, extra := range [][]string{{"--enable-sakura-disks"}, {"--sakura-owner="}, append(enabledSakuraArgs(), "--unsafe-no-auth")} {
		dir := t.TempDir()
		state, token := filepath.Join(dir, "state.db"), filepath.Join(dir, "token")
		args := []string{"--listen", occupied.Addr().String(), "--state", state, "--token-file", token}
		args = append(args, extra...)
		e = run(context.Background(), args, &bytes.Buffer{})
		if e == nil {
			t.Fatal("invalid invocation started")
		}
		if len(extra) == 1 && extra[0] == "--enable-sakura-disks" && !errors.Is(e, iaas.ErrInvalid) {
			t.Fatal("invalid scope hit listener before preflight", e)
		}
		if strings.Contains(e.Error(), "address already in use") {
			t.Fatal("preflight attempted listener")
		}
		for _, p := range []string{state, token} {
			if _, e = os.Stat(p); !os.IsNotExist(e) {
				t.Fatal("preflight created startup file")
			}
		}
	}
}
func TestSakuraCLIActualRunDefaultAndOptInUseRealHTTP(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-disabled", true: "explicit-enabled"}[enabled], func(t *testing.T) {
			dir := t.TempDir()
			state, tokenPath := filepath.Join(dir, "state.db"), filepath.Join(dir, "token")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output := &sakuraStartupOutput{ready: make(chan string, 1)}
			done := make(chan error, 1)
			args := []string{"--listen", "127.0.0.1:0", "--state", state, "--token-file", tokenPath}
			if enabled {
				args = append(args, enabledSakuraArgs()...)
			}
			go func() { done <- run(ctx, args, output) }()
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					cancel()
					select {
					case <-done:
					case <-time.After(8 * time.Second):
						t.Error("owned CLI run did not stop")
					}
				}
			})
			var origin string
			select {
			case origin = <-output.ready:
			case e := <-done:
				stopped = true
				t.Fatalf("CLI exited before readiness: %T", e)
			case <-time.After(15 * time.Second):
				t.Fatal("CLI startup deadline")
			}
			data, e := os.ReadFile(tokenPath)
			if e != nil {
				t.Fatal(e)
			}
			token := strings.TrimSpace(string(data))
			if len(token) < 32 {
				t.Fatal("missing generated token")
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			invoke := func(method, path, body string, status int) map[string]any {
				r, e := http.NewRequest(method, origin+path, strings.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				r.Header.Set("X-Simulator-Token", token)
				r.Header.Set("X-Simulator-Principal", "learner")
				r.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
				r.Header.Set("Content-Type", "application/json")
				response, e := client.Do(r)
				if e != nil {
					t.Fatalf("owned actual CLI HTTP failed: %T", e)
				}
				defer response.Body.Close()
				if response.StatusCode != status {
					t.Fatalf("CLI HTTP status%d wanted%d (body redacted)", response.StatusCode, status)
				}
				var v map[string]any
				if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
					if e = json.NewDecoder(response.Body).Decode(&v); e != nil {
						t.Fatal(e)
					}
				}
				return v
			}
			path := "/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/disk"
			caps := invoke("GET", "/_simulator/v1/capabilities", "", 200)
			if enabled {
				if caps["sakura"] == nil {
					t.Fatal("enabled capability missing")
				}
				v := invoke("POST", path, `{"Disk":{"Name":"cli-config","Plan":{"ID":4},"SizeMB":20480}}`, 201)
				disk := v["Disk"].(map[string]any)
				if disk["ID"] != "1" {
					t.Fatal("CLI create not real")
				}
				if _, ok := disk["Availability"]; ok {
					t.Fatal("invented readiness")
				}
				invoke("DELETE", path+"/1", "", 200)
			} else {
				if _, ok := caps["sakura"]; ok {
					t.Fatal("default capability advertised")
				}
				invoke("GET", path, "", 404)
			}
			cancel()
			select {
			case e = <-done:
				stopped = true
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("actual CLI shutdown deadline")
			}
			output.mu.Lock()
			leaked := strings.Contains(output.data.String(), token)
			output.mu.Unlock()
			if leaked {
				t.Fatal("CLI printed generated token")
			}
			// This executes run and real sockets in one Go test process; the independent
			// subprocess driver, not this test, establishes two operating-system processes.
		})
	}
}
