package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real CLI writes several startup messages before the endpoint. Keep the
// writer independent of main_test's one-message unauthenticated fixture.
type cloudOwnerOutput struct{ lines chan string }

func (w cloudOwnerOutput) Write(p []byte) (int, error) { w.lines <- string(p); return len(p), nil }

func startCloudOwnerCLI(t *testing.T, state, tokenPath, owner string) (string, string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	output := cloudOwnerOutput{make(chan string, 8)}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--state", state, "--token-file", tokenPath, "--cloud-storage-owner", owner}, output)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("CLI shutdown: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("CLI shutdown exceeded existing 8s bound")
			}
		})
	}
	t.Cleanup(stop)
	ready := time.NewTimer(45 * time.Second)
	defer ready.Stop()
	for {
		select {
		case text := <-output.lines:
			if _, endpoint, ok := strings.Cut(text, "AWS simulation endpoint "); ok {
				data, err := os.ReadFile(tokenPath)
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(endpoint), strings.TrimSpace(string(data)), stop
			}
		case err := <-done:
			// Mark the already-consumed exit so cleanup cannot wait on it again.
			once.Do(func() { cancel() })
			t.Fatalf("CLI failed before endpoint: %v", err)
		case <-ready.C:
			t.Fatal("CLI endpoint not ready within existing 45s bound")
		}
	}
}

func TestCloudStorageOwnerCLIRejectsBeforeSideEffects(t *testing.T) {
	for _, owner := range []string{"", " ", "learner/name", "learner.name", " learner", "learner ", "学習者", strings.Repeat("a", 65), "line\nbreak", "zero\x00byte"} {
		t.Run(owner, func(t *testing.T) {
			directory := t.TempDir()
			state := filepath.Join(directory, "state.sqlite")
			token := filepath.Join(directory, "token")
			// An occupied address proves owner validation takes precedence over Listen.
			held, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			err = run(context.Background(), []string{"--listen", held.Addr().String(), "--state", state, "--token-file", token, "--cloud-storage-owner", owner}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "--cloud-storage-owner") {
				t.Fatalf("invalid owner did not fail before listener: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("invalid owner created persistent state or token files")
			}
		})
	}
}

func TestCloudStorageOwnerCLIParticipantLifecycleAndReassignmentRefusal(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, "state.sqlite")
	tokenPath := filepath.Join(directory, "token")
	endpoint, operator, stop := startCloudOwnerCLI(t, state, tokenPath, "learner")
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	call := func(method, path, body, token, selected string, azure bool, want int) []byte {
		t.Helper()
		request, err := http.NewRequest(method, endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("X-Simulator-Token", token)
		}
		if selected != "" {
			request.Header.Set("X-Simulator-Principal", selected)
		}
		request.Header.Set("Content-Type", "application/json")
		if azure {
			request.Header.Set("x-ms-version", "2023-11-03")
			if method == "PUT" && !strings.Contains(path, "restype=") {
				request.Header.Set("x-ms-blob-type", "BlockBlob")
			}
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status %d want %d", method, path, response.StatusCode, want)
		}
		return data
	}
	issue := func(principal string) string {
		t.Helper()
		data := call("POST", "/_simulator/v1/access", `{"Principal":"`+principal+`"}`, operator, "", false, 200)
		var issued struct{ AccessToken string }
		if json.Unmarshal(data, &issued) != nil || issued.AccessToken == "" {
			t.Fatal("participant token was not issued")
		}
		return issued.AccessToken
	}
	learner := issue("learner")
	reader := issue("reader")
	gcp := "/_simulator/providers/gcp/storage/v1/b"
	azure := "/_simulator/providers/azure/tenkacloudlocal/cli-container"
	// No AWS identity/policy is created. Cloud ownership grants no AWS access.
	awsBucket := "/_simulator/providers/aws/cli-aws-owner"
	awsObject := awsBucket + "/operator-evidence.txt"
	call("PUT", awsBucket, "", operator, "", false, 200)
	call("PUT", awsObject, "AWS operator bytes", operator, "", false, 200)
	verifyAWS := func() {
		t.Helper()
		call("GET", awsObject, "", learner, "", false, 403)
		call("PUT", awsObject, "participant overwrite", learner, "", false, 403)
		call("GET", awsObject, "", operator, "learner", false, 403)
		call("PUT", awsObject, "selected learner overwrite", operator, "learner", false, 403)
		if string(call("GET", awsObject, "", operator, "", false, 200)) != "AWS operator bytes" {
			t.Fatal("cloud ownership granted AWS access or denied writes changed S3 bytes")
		}
	}
	// The bound participant is the configured cloud exercise owner; the
	// operator credential itself is not a GCP/Azure owner grant.
	call("POST", gcp+"?project=tenkacloud-local", `{"name":"cli-owner-bucket"}`, operator, "", false, 403)
	call("PUT", azure+"?restype=container", "", operator, "", true, 403)
	call("POST", gcp+"?project=tenkacloud-local", `{"name":"cli-owner-bucket","location":"US"}`, learner, "", false, 200)
	call("POST", gcp+"?project=tenkacloud-local", `{"name":"cli-survivor-bucket","location":"US"}`, learner, "", false, 200)
	call("PUT", azure+"?restype=container", "", learner, "", true, 201)
	gcpObject := gcp + "/cli-owner-bucket/o/hello.txt"
	upload := "/_simulator/providers/gcp/upload/storage/v1/b/cli-owner-bucket/o?uploadType=media&name=hello.txt"
	call("POST", upload, "GCP participant bytes", learner, "", false, 200)
	call("PUT", azure+"/hello.txt", "Azure participant bytes", learner, "", true, 201)
	for _, selected := range []string{"simulator-admin", "reader"} {
		call("POST", upload, "spoofed", learner, selected, false, 403)
		call("PUT", azure+"/hello.txt", "spoofed", learner, selected, true, 403)
	}
	call("POST", upload, "reader mutation", reader, "", false, 403)
	call("PUT", azure+"/hello.txt", "reader mutation", reader, "", true, 403)
	verify := func() {
		t.Helper()
		verifyAWS()
		if string(call("GET", gcpObject+"?alt=media", "", learner, "", false, 200)) != "GCP participant bytes" {
			t.Fatal("GCP state changed or was not retained")
		}
		if string(call("GET", azure+"/hello.txt", "", learner, "", true, 200)) != "Azure participant bytes" {
			t.Fatal("Azure state changed or was not retained")
		}
		call("GET", gcpObject+"?alt=media", "", operator, "", false, 403)
		call("GET", azure+"/hello.txt", "", operator, "", true, 403)
	}
	verify()
	stop()
	savedToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	// A different valid host owner must not adopt either persisted cloud scope.
	rejectedCtx, rejectedCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer rejectedCancel()
	err = run(rejectedCtx, []string{"--listen", "127.0.0.1:0", "--state", state, "--token-file", tokenPath, "--cloud-storage-owner", "other-owner"}, io.Discard)
	if err == nil {
		t.Fatal("persisted cloud owner reassignment succeeded")
	}
	tokenAfter, err := os.ReadFile(tokenPath)
	if err != nil || string(tokenAfter) != string(savedToken) {
		t.Fatal("failed reassignment changed operator token")
	}
	endpoint, operator, stop = startCloudOwnerCLI(t, state, tokenPath, "learner")
	learner = issue("learner") // participant sessions are intentionally process-local
	verify()
	call("DELETE", gcpObject, "", learner, "", false, 204)
	call("DELETE", gcp+"/cli-owner-bucket", "", learner, "", false, 204)
	call("DELETE", azure+"/hello.txt", "", learner, "", true, 202)
	call("DELETE", azure+"?restype=container", "", learner, "", true, 202)
	// The owner-only factory conceals unknown bucket ownership with 403.
	// Prove deletion separately through the authenticated owned-project list.
	call("GET", gcp+"/cli-owner-bucket", "", learner, "", false, 403)
	var buckets struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	listed := call("GET", gcp+"?project=tenkacloud-local", "", learner, "", false, 200)
	if err := json.Unmarshal(listed, &buckets); err != nil {
		t.Fatalf("decode owned-project bucket list: %v", err)
	}
	if len(buckets.Items) != 1 || buckets.Items[0].Name != "cli-survivor-bucket" {
		t.Fatal("owned-project list lost the survivor or retained the deleted bucket")
	}
	for _, bucket := range buckets.Items {
		if bucket.Name == "cli-owner-bucket" {
			t.Fatal("deleted GCP bucket still present in owned-project list")
		}
	}
	call("DELETE", gcp+"/cli-survivor-bucket", "", learner, "", false, 204)
	call("HEAD", azure+"?restype=container", "", learner, "", true, 404)
	verifyAWS()
	call("DELETE", awsObject, "", operator, "", false, 204)
	call("GET", awsObject, "", operator, "", false, 404)
	call("DELETE", awsBucket, "", operator, "", false, 204)
	call("HEAD", awsBucket, "", operator, "", false, 404)
	stop()
}
