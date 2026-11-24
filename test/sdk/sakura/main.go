// Development-only compatibility client; never linked into the simulator.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sacloud/sacloud-sdk-go/api/iaas"
	"github.com/sacloud/sacloud-sdk-go/common/saclient"
)

type config struct {
	Origin         string
	Token          string
	ServerMetadata bool
}

// This terminal middleware never pulls the SDK's stock credential/HTTP chain.
// It preserves the real SDK serializer, query and response/error decoder.
func localTransport(c config) (saclient.Middleware, func(), error) {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || c.Token == "" {
		return nil, nil, errors.New("invalid owned endpoint")
	}
	target := u.Host
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != target {
				return nil, errors.New("unowned destination refused")
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		},
		ResponseHeaderTimeout: 5 * time.Second,
	}
	return func(req *http.Request, _ func() (saclient.Middleware, bool)) (*http.Response, error) {
		path := "/cloud/zone/is1a/api/cloud/1.1/disk"
		if c.ServerMetadata {
			path = "/cloud/zone/is1a/api/cloud/1.1/server"
			if !serverMetadataRoute(req, path) {
				return nil, errors.New("unexpected Server metadata route refused")
			}
		}
		if req.URL.Scheme != "https" || req.URL.Host != "secure.sakura.ad.jp" || (req.URL.Path != path && !strings.HasPrefix(req.URL.Path, path+"/")) {
			return nil, errors.New("unexpected SDK route refused")
		}
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = u.Scheme, u.Host
		clone.URL.Path = "/_simulator/providers/sakura" + req.URL.Path
		clone.Host = u.Host
		clone.SetBasicAuth("TCSIMSAKURA", "dummy-local-only")
		clone.Header.Set("X-Simulator-Token", c.Token)
		clone.Header.Set("X-Simulator-Principal", "learner")
		clone.Header.Set("Content-Type", "application/json")
		// RoundTrip does not follow redirects; no ambient proxy or DNS is used.
		response, err := transport.RoundTrip(clone)
		if c.ServerMetadata && err == nil && response.StatusCode >= 300 && response.StatusCode < 400 {
			response.Body.Close()
			return nil, errors.New("Server metadata redirect refused")
		}
		return response, err
	}, transport.CloseIdleConnections, nil
}

func serverMetadataRoute(req *http.Request, base string) bool {
	u := req.URL
	if u.User != nil || u.Fragment != "" || u.EscapedPath() != u.Path {
		return false
	}
	if u.Path == base {
		return (req.Method == "GET" || req.Method == "POST") && (u.RawQuery == "" || req.Method == "GET")
	}
	id, ok := strings.CutPrefix(u.Path, base+"/")
	if !ok || id == "" || id[0] == '0' || u.RawQuery != "" {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return req.Method == "GET" || req.Method == "PUT" || req.Method == "DELETE"
}

func serverMetadata(ctx context.Context, client *saclient.Client) {
	servers := iaas.NewServerOp(iaas.NewClientFromSaclient(client))
	check := func(v *iaas.Server, name, description string) bool {
		return v != nil && v.ID > 0 && v.Name == name && v.Description == description && v.CPU == 2 && v.MemoryMB == 4096 && v.Availability == ""
	}
	v, err := servers.Create(ctx, "is1a", &iaas.ServerCreateRequest{CPU: 2, MemoryMB: 4096, Name: "sdk-server", Description: "original"})
	require(err == nil && check(v, "sdk-server", "original"), "Server create metadata")
	id := v.ID
	removed := false
	defer func() {
		if !removed {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = servers.Delete(cleanup, "is1a", id)
		}
	}()
	v, err = servers.Read(ctx, "is1a", id)
	require(err == nil && check(v, "sdk-server", "original") && v.ID == id, "Server read")
	page, err := servers.Find(ctx, "is1a", &iaas.FindCondition{Count: 10, From: 0})
	require(err == nil && page != nil && page.Total == 1 && page.Count == 1 && len(page.Servers) == 1 && check(page.Servers[0], "sdk-server", "original") && page.Servers[0].ID == id, "Server list identity")
	v, err = servers.Update(ctx, "is1a", id, &iaas.ServerUpdateRequest{Name: "updated", Description: ""})
	require(err == nil && check(v, "updated", "") && v.ID == id, "Server update")
	v, err = servers.Read(ctx, "is1a", id)
	require(err == nil && check(v, "updated", "") && v.ID == id, "Server persisted metadata")
	require(servers.Delete(ctx, "is1a", id) == nil, "Server delete")
	removed = true
	_, err = servers.Read(ctx, "is1a", id)
	require(iaas.IsNotFoundError(err), "Server deleted 404 decoder")
	page, err = servers.Find(ctx, "is1a", nil)
	require(err == nil && page != nil && page.Total == 0 && page.Count == 0 && len(page.Servers) == 0, "Server empty after cleanup")
}

func require(ok bool, phase string) {
	if !ok {
		panic(phase)
	}
}

func run(c config) {
	middleware, closeTransport, err := localTransport(c)
	require(err == nil, "transport configuration")
	defer closeTransport()
	var sa saclient.Client
	require(sa.SetEnviron([]string{}) == nil, "empty SDK environment")
	require(sa.SetWith(saclient.WithoutProfile(), saclient.WithoutRetry(), saclient.WithoutAPIRequestRateLimit(), saclient.WithMiddleware(middleware)) == nil, "SDK configuration")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if c.ServerMetadata {
		serverMetadata(ctx, &sa)
		return
	}
	disks := iaas.NewDiskOp(iaas.NewClientFromSaclient(&sa))
	created, err := disks.Create(ctx, "is1a", &iaas.DiskCreateRequest{DiskPlanID: 4, SizeMB: 20480, Name: "sdk-created", Description: "saved"}, nil, 0)
	require(err == nil && created != nil && created.ID > 0 && created.Name == "sdk-created" && created.SizeMB == 20480, "SDK create / neutral Icon")
	read, err := disks.Read(ctx, "is1a", created.ID)
	require(err == nil && read.ID == created.ID && read.Description == "saved", "SDK read")
	listed, err := disks.Find(ctx, "is1a", &iaas.FindCondition{From: 1, Count: 1})
	require(err == nil && listed.Total == 2 && listed.From == 1 && listed.Count == 1 && len(listed.Disks) == 1 && listed.Disks[0].ID == created.ID, "SDK JSON pagination")
	updated, err := disks.Update(ctx, "is1a", created.ID, &iaas.DiskUpdateRequest{Name: "sdk-updated", Description: ""})
	require(err == nil && updated.Name == "sdk-updated" && updated.Description == "", "SDK update / clear description")
	_, err = disks.Create(ctx, "is1a", &iaas.DiskCreateRequest{DiskPlanID: 4, SizeMB: 20480, Name: "unsupported", IconID: 99}, nil, 0)
	var apiError iaas.APIError
	require(errors.As(err, &apiError) && apiError.ResponseCode() == 501, "SDK unsupported Icon error decoder")
	read, err = disks.Read(ctx, "is1a", created.ID)
	require(err == nil && read.Name == "sdk-updated" && read.Description == "", "SDK read persisted update")
	require(disks.Delete(ctx, "is1a", created.ID) == nil, "SDK delete")
	_, err = disks.Read(ctx, "is1a", created.ID)
	require(iaas.IsNotFoundError(err), "SDK deleted 404 decoder")
	listed, err = disks.Find(ctx, "is1a", nil)
	require(err == nil && listed.Total == 1 && len(listed.Disks) == 1 && listed.Disks[0].Name == "sentinel", "SDK cleanup and sentinel preservation")
}

func main() {
	// Credentials arrive only over stdin, never command arguments or output.
	defer func() {
		if recover() != nil {
			fmt.Fprintln(os.Stderr, "Sakura SDK acceptance failed; request and credentials redacted")
			os.Exit(1)
		}
	}()
	var c config
	require(json.NewDecoder(os.Stdin).Decode(&c) == nil, "input")
	run(c)
	if c.ServerMetadata {
		fmt.Println("Sakura SDK: 8 real Server metadata calls passed")
		return
	}
	fmt.Println("Sakura SDK: 9 real Disk calls passed")
}
