//go:build sakura_server_inprocess

package main

// Opt-in SDK wire/decoder integration only. Not host-token authentication,
// high-level service orchestration, VM execution, Terraform or real Sakura.
// Use a reviewed local workspace containing this module and the candidate root;
// select only ^TestServerSDKInProcess to avoid unrelated listener tests.
import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/sacloud/sacloud-sdk-go/api/iaas"
	"github.com/sacloud/sacloud-sdk-go/common/saclient"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	common "github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
)

const serverSDKAuthority = "127.0.0.1:42300" // inert URL only, never bound or dialed
const serverSDKPrefix = "/cloud/zone/is1a/api/cloud/1.1/"

type serverSDKTransport struct {
	// Nil interface embedding supplies unused SDK configuration methods. No
	// concrete SDK profile/credential/environment chain is constructed or called.
	saclient.ClientAPI
	handler http.Handler
	calls   []string
}

var _ http.RoundTripper = (*serverSDKTransport)(nil)

func (s *serverSDKTransport) Do(r *http.Request) (*http.Response, error) { return s.RoundTrip(r) }
func (s *serverSDKTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r == nil || r.URL == nil || r.URL.Scheme != "http" || r.URL.Host != serverSDKAuthority || r.URL.User != nil || r.URL.Fragment != "" || r.URL.EscapedPath() != r.URL.Path || (r.Host != "" && r.Host != serverSDKAuthority) {
		return nil, fmt.Errorf("unowned synthetic target")
	}
	path := r.URL.Path
	allowed := false
	for _, resource := range []string{"server", "disk"} {
		base := serverSDKPrefix + resource
		if path == base {
			allowed = r.Method == "GET" || r.Method == "POST"
		} else if strings.HasPrefix(path, base+"/") {
			tail := strings.TrimPrefix(path, base+"/")
			digits := len(tail) > 0 && tail[0] != '0'
			for _, c := range tail {
				if c < '0' || c > '9' {
					digits = false
				}
			}
			allowed = digits && (r.Method == "GET" || r.Method == "PUT" || r.Method == "DELETE")
		}
		if allowed {
			break
		}
	}
	if !allowed || r.Header.Get("Authorization") != "" {
		return nil, fmt.Errorf("unexpected synthetic request")
	}
	// This is the existing trusted library test seam, not a host-auth bypass.
	actor := common.Actor{CredentialIdentity: "learner", ExerciseIdentity: "learner", CredentialRole: common.CredentialParticipant}
	req := r.Clone(common.WithActor(r.Context(), actor))
	req.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.calls = append(s.calls, r.Method+" "+path)
	s.handler.ServeHTTP(w, req)
	result := w.Result()
	if result.StatusCode >= 300 && result.StatusCode < 400 {
		result.Body.Close()
		return nil, fmt.Errorf("redirect refused")
	}
	return result, nil
}
func TestServerSDKInProcessCRUD(t *testing.T) {
	old := sdk.SakuraCloudAPIRoot
	sdk.SakuraCloudAPIRoot = "http://" + serverSDKAuthority + "/cloud/zone"
	t.Cleanup(func() { sdk.SakuraCloudAPIRoot = old })
	store, e := core.New(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	h, e := sakura.NewProvider(store.Database(), sakura.Config{Environment: "team-a", Project: "practice", Zone: "is1a", Owner: "learner"})
	if e != nil {
		t.Fatal(e)
	}
	transport := &serverSDKTransport{handler: h}
	client := sdk.NewClientFromSaclient(transport)
	servers := sdk.NewServerOp(client)
	disks := sdk.NewDiskOp(client)
	ctx := context.Background()
	sentinel, e := disks.Create(ctx, "is1a", &sdk.DiskCreateRequest{DiskPlanID: 4, SizeMB: 20480, Name: "sentinel", Description: "unchanged"}, nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	check := func(v *sdk.Server, name, desc string) {
		t.Helper()
		if v == nil || v.ID == 0 || v.Name != name || v.Description != desc || v.CPU != 2 || v.MemoryMB != 4096 || v.Availability != "" {
			t.Fatalf("wrong decoded metadata: %+v", v)
		}
	}
	created, e := servers.Create(ctx, "is1a", &sdk.ServerCreateRequest{CPU: 2, MemoryMB: 4096, Name: "sdk-server", Description: "original"})
	if e != nil {
		t.Fatal(e)
	}
	check(created, "sdk-server", "original")
	id := created.ID
	read, e := servers.Read(ctx, "is1a", id)
	if e != nil {
		t.Fatal(e)
	}
	check(read, "sdk-server", "original")
	if read.ID != id {
		t.Fatal("read identity")
	}
	listed, e := servers.Find(ctx, "is1a", &sdk.FindCondition{Count: 10, From: 0})
	if e != nil {
		t.Fatal(e)
	}
	if listed.Total != 1 || listed.Count != 1 || len(listed.Servers) != 1 || listed.Servers[0].ID != id {
		t.Fatal("list cardinality/identity")
	}
	check(listed.Servers[0], "sdk-server", "original")
	updated, e := servers.Update(ctx, "is1a", id, &sdk.ServerUpdateRequest{Name: "updated", Description: ""})
	if e != nil {
		t.Fatal(e)
	}
	check(updated, "updated", "")
	if updated.ID != id {
		t.Fatal("update identity")
	}
	read, e = servers.Read(ctx, "is1a", id)
	if e != nil {
		t.Fatal(e)
	}
	check(read, "updated", "")
	if e = servers.Delete(ctx, "is1a", id); e != nil {
		t.Fatal(e)
	}
	if _, e = servers.Read(ctx, "is1a", id); !sdk.IsNotFoundError(e) {
		t.Fatal("removed ID not decoded as 404", e)
	}
	listed, e = servers.Find(ctx, "is1a", nil)
	if e != nil || listed.Total != 0 || listed.Count != 0 || len(listed.Servers) != 0 {
		t.Fatal("delete was not observed before cleanup", e)
	}
	fresh, e := servers.Create(ctx, "is1a", &sdk.ServerCreateRequest{CPU: 2, MemoryMB: 4096, Name: "fresh", Description: "new"})
	if e != nil {
		t.Fatal(e)
	}
	check(fresh, "fresh", "new")
	if fresh.ID == id {
		t.Fatal("reused identity")
	}
	if _, e = servers.Read(ctx, "is1a", id); !sdk.IsNotFoundError(e) {
		t.Fatal("old ID resurrected", e)
	}
	if _, e = servers.Update(ctx, "is1a", id, &sdk.ServerUpdateRequest{Name: "stale"}); !sdk.IsNotFoundError(e) {
		t.Fatal("stale update", e)
	}
	read, e = servers.Read(ctx, "is1a", fresh.ID)
	if e != nil {
		t.Fatal(e)
	}
	check(read, "fresh", "new")
	if e = servers.Delete(ctx, "is1a", fresh.ID); e != nil {
		t.Fatal(e)
	}
	listed, e = servers.Find(ctx, "is1a", nil)
	if e != nil || listed.Total != 0 || len(listed.Servers) != 0 {
		t.Fatal("final server cleanup", e)
	}
	kept, e := disks.Read(ctx, "is1a", sentinel.ID)
	if e != nil || kept.Name != "sentinel" || kept.Description != "unchanged" || kept.SizeMB != 20480 {
		t.Fatal("Disk sentinel changed", e)
	}
	if len(transport.calls) != 16 {
		t.Fatalf("unexpected call count %d", len(transport.calls))
	}
	t.Logf("16 actual in-process SDK/handler calls; stable Disk sentinel; no socket or VM")
}
func TestServerSDKInProcessTransportBoundary(t *testing.T) {
	calls := 0
	s := &serverSDKTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "https://example.invalid")
		w.WriteHeader(302)
	})}
	for _, target := range []string{"https://127.0.0.1:42300" + serverSDKPrefix + "server", "http://localhost:42300" + serverSDKPrefix + "server", "http://127.0.0.1:42301" + serverSDKPrefix + "server", "http://u@127.0.0.1:42300" + serverSDKPrefix + "server", "http://127.0.0.1:42300" + serverSDKPrefix + "server/1/power", "http://127.0.0.1:42300" + serverSDKPrefix + "server%2F1", "http://127.0.0.1:42300" + serverSDKPrefix + "server/01"} {
		r, e := http.NewRequest("GET", target, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.RoundTrip(r); e == nil {
			t.Fatal("unexpected target accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unowned request reached handler")
	}
	r, e := http.NewRequest("GET", "http://"+serverSDKAuthority+serverSDKPrefix+"server", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RoundTrip(r); e == nil || calls != 1 {
		t.Fatal("redirect followed or accepted")
	}
}
