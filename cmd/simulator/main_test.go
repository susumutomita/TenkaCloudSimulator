package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

type readyOutput struct{ ready chan string }

func (w readyOutput) Write(data []byte) (int, error) { w.ready <- string(data); return len(data), nil }
func TestLifecycleServesRealRequestsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := readyOutput{make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--state", t.TempDir() + "/state.db"}, output)
	}()
	var message string
	select {
	case message = <-output.ready:
	case err := <-done:
		t.Fatalf("server failed before readiness: %v", err)
	// Cold SQLite/Wasm compilation under -race can exceed 10s on shared CI CPUs.
	case <-time.After(45 * time.Second):
		t.Fatal("server did not become ready")
	}
	_, endpoint, ok := strings.Cut(message, "S3 endpoint ")
	if !ok {
		t.Fatal(message)
	}
	endpoint = strings.TrimSpace(endpoint)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(endpoint + "/_simulator/health")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Contains(body, []byte(`"status":"ok"`)) {
		t.Fatalf("unhealthy listener: %d %s %v", response.StatusCode, body, err)
	}
	request, err := http.NewRequest("PUT", endpoint+"/cli-created", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
	if response, err = client.Get(endpoint + "/_simulator/health"); err == nil {
		response.Body.Close()
		t.Fatal("listener remained open after shutdown")
	}
}
func TestInvalidConfigurationDoesNotStartAListener(t *testing.T) {
	for _, args := range [][]string{{"--unknown-option"}, {"unexpected"}, {"--listen", "invalid"}, {"--listen", "0.0.0.0:0"}, {"--listen", "localhost:0"}} {
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("invalid arguments succeeded: %v", args)
		}
	}
	if err := run(context.Background(), []string{"--help"}, io.Discard); err != flag.ErrHelp {
		t.Fatalf("help returned %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = run(context.Background(), []string{"--listen", listener.Addr().String()}, io.Discard); err == nil {
		t.Fatal("occupied port succeeded")
	}
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	if err = run(context.Background(), []string{"--listen", address, "--state", t.TempDir()}, io.Discard); err == nil {
		t.Fatal("directory accepted as SQLite state")
	}
	recovered, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatal("failed state initialization leaked its listener", err)
	}
	recovered.Close()
}
