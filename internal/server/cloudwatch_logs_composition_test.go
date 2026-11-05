package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real composition entry points, never a manually assembled Gateway.
func TestLogsDefaultCompositionUsesAuthenticatedPersistentSQLite(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "New"
		if configured {
			name = "NewWithCloudStorage"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			var h http.Handler
			var closeStore func() error
			var err error
			if configured {
				h, closeStore, err = NewWithCloudStorage(path, DefaultCloudStorageConfig())
			} else {
				h, closeStore, err = New(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeStore(); err != nil {
					t.Error(err)
				}
			}()
			token := strings.Repeat("a", 64)
			access, err := NewAccess(h, token, false)
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(access)
			defer srv.Close()
			headers := map[string]string{"Content-Type": "application/x-amz-json-1.1", "X-Amz-Target": "Logs_20140328.CreateLogGroup", "X-Simulator-Service": "logs", "X-Simulator-Region": "us-east-1"}
			r, _ := call(t, srv, "POST", "/", `{"logGroupName":"composed"}`, headers)
			if r.StatusCode != 401 {
				t.Fatalf("missing local token status %d", r.StatusCode)
			}
			headers["X-Simulator-Token"] = token
			r, body := call(t, srv, "POST", "/", `{"logGroupName":"composed"}`, headers)
			if r.StatusCode != 200 {
				t.Fatalf("real composed CreateLogGroup status %d: %s", r.StatusCode, body)
			}
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM aws_logs_groups WHERE region='us-east-1' AND name='composed'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("same SQLite count %d error %v", count, err)
			}
			headers["X-Amz-Target"] = "Logs_20140328.DescribeLogGroups"
			r, body = call(t, srv, "POST", "/_simulator/providers/aws/", `{}`, headers)
			var listed struct {
				LogGroups []struct {
					LogGroupName string `json:"logGroupName"`
				} `json:"logGroups"`
			}
			if r.StatusCode != 200 || json.Unmarshal(body, &listed) != nil || len(listed.LogGroups) != 1 || listed.LogGroups[0].LogGroupName != "composed" {
				t.Fatalf("provider route did not read actual group status %d: %s", r.StatusCode, body)
			}
			headers["X-Amz-Target"] = "Logs_20140328.DeleteLogGroup"
			r, body = call(t, srv, "POST", "/", `{"logGroupName":"composed"}`, headers)
			if r.StatusCode != 200 {
				t.Fatalf("cleanup %d: %s", r.StatusCode, body)
			}
			if err = db.QueryRow(`SELECT count(*) FROM aws_logs_groups`).Scan(&count); err != nil || count != 0 {
				t.Fatal("cleanup did not affect same SQLite", count, err)
			}
		})
	}
}
