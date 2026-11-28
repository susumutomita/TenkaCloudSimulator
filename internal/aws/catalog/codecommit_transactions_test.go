package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func openGitTeam(t *testing.T, path string) (*core.Store, *harness) {
	t.Helper()
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, db: store.Database(), policy: &policy{deny: map[string]bool{}}}
	h.engine, err = New(h.db, Config{Regions: []string{"us-east-1"}, Account: "000000000000", DefaultPrincipal: "fixture", Authorize: h.policy.authorize}, []*Service{codeCommit})
	if err != nil {
		t.Fatal(err)
	}
	return store, h
}

func codeCommitSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rs, err := db.Query(`SELECT json_array('repo',arn,seq,generation,document,tags) FROM aws_catalog_resources WHERE service='codecommit'
UNION ALL SELECT json_array('ref',seq,repository,name,tip) FROM aws_codecommit_branches
UNION ALL SELECT json_array('object',repository,oid,kind,hex(body)) FROM aws_codecommit_objects ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var frames []string
	for rs.Next() {
		var v string
		if err := rs.Scan(&v); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, v)
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	return frames
}

func TestCodeCommitInterruptedGitWrite(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, existing := range []bool{false, true} {
			label := "first"
			if existing {
				label = "advance"
			}
			t.Run(cause.Error()+"/"+label, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.db")
				host, h := openGitTeam(t, path)
				_, peer := openGitTeam(t, filepath.Join(t.TempDir(), "peer.db"))
				put := map[string]any{"branchName": "main", "filePath": "test.bin", "fileContent": "YQ=="}
				for _, team := range []*harness{h, peer} {
					expect(t, gitCall(team, "target", "CreateRepository", nil), 200, "")
					expect(t, gitCall(team, "sibling", "CreateRepository", nil), 200, "")
					expect(t, gitCall(team, "sibling", "PutFile", put), 200, "")
				}
				expect(t, gitCall(peer, "target", "PutFile", put), 200, "")
				if existing {
					saved := gitCall(h, "target", "PutFile", put)
					expect(t, saved, 200, "")
					put["parentCommitId"] = saved.body["commitId"]
				}
				put["fileContent"] = "AP8="
				before, peerBefore := codeCommitSnapshot(t, h.db), codeCommitSnapshot(t, peer.db)
				var ctx context.Context
				var stop context.CancelFunc
				var staged string
				hits := 0
				conn, err := h.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("interrupt_git_write", 5, 0, func(rc sqlite3.Context, a ...sqlite3.Value) {
						hits++
						if a[0].Text() == "main" && a[1].Text() == a[2].Text() && len(a[1].Text()) == 40 && a[3].Int64() > 0 && a[4].Text() == "00FF" {
							staged = a[1].Text()
						}
						if cause == context.Canceled {
							stop()
						}
						select {
						case <-ctx.Done():
							rc.ResultNull()
						case <-time.After(5 * time.Second):
							rc.ResultError(errors.New("real request lifetime did not expire"))
						}
					})
				})
				if err == nil {
					_, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER staged_git AFTER UPDATE ON aws_catalog_resources
WHEN NEW.service='codecommit' AND NEW.name='target' BEGIN
SELECT interrupt_git_write(json_extract(NEW.document,'$.defaultBranch'),
(SELECT tip FROM aws_codecommit_branches WHERE repository=NEW.arn AND name='main'),
(SELECT oid FROM aws_codecommit_objects WHERE repository=NEW.arn AND kind='commit' AND oid=(SELECT tip FROM aws_codecommit_branches WHERE repository=NEW.arn AND name='main')),
(SELECT count(*) FROM aws_codecommit_objects WHERE repository=NEW.arn AND kind='tree'),
(SELECT hex(body) FROM aws_codecommit_objects WHERE repository=NEW.arn AND kind='blob' AND body=x'00ff')); END`)
				}
				closeErr := conn.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
				clientContext, cancelClient := context.WithCancel(t.Context())
				defer cancelClient()
				stop = cancelClient
				finished := make(chan *httptest.ResponseRecorder, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx = r.Context()
					if cause == context.DeadlineExceeded {
						var cancelDeadline context.CancelFunc
						ctx, cancelDeadline = context.WithTimeout(ctx, time.Second)
						defer cancelDeadline()
					}
					record := httptest.NewRecorder()
					h.engine.ServeHTTP(record, r.WithContext(ctx))
					w.WriteHeader(record.Code)
					_, _ = w.Write(record.Body.Bytes())
					finished <- record
				}))
				defer server.Close()
				put["repositoryName"] = "target"
				data, err := json.Marshal(put)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(clientContext, "POST", server.URL+"/", bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/x-amz-json-1.1")
				req.Header.Set("X-Amz-Target", "CodeCommit_20150413.PutFile")
				sign(req, "codecommit", "us-east-1")
				response, sendErr := server.Client().Do(req)
				if response != nil {
					response.Body.Close()
				}
				record := <-finished
				if cause == context.Canceled && !errors.Is(sendErr, context.Canceled) {
					t.Fatal("client did not disconnect", sendErr)
				}
				if cause == context.DeadlineExceeded && (sendErr != nil || response.StatusCode != record.Code) {
					t.Fatal("elapsed handler deadline did not reach client", sendErr)
				}
				var result map[string]any
				if err := json.Unmarshal(record.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if hits != 1 || staged == "" || !errors.Is(ctx.Err(), cause) || record.Code < 400 || result["__type"] == nil || result["commitId"] != nil {
					t.Fatalf("interruption phase hits=%d staged=%q cause=%v status=%d", hits, staged, ctx.Err(), record.Code)
				}
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				_, resumed := openGitTeam(t, path)
				if !slices.Equal(before, codeCommitSnapshot(t, resumed.db)) || !slices.Equal(peerBefore, codeCommitSnapshot(t, peer.db)) {
					t.Fatal("transaction changed retained Git objects, refs or metadata")
				}
				expect(t, gitCall(resumed, "target", "PutFile", put), 200, "")
				read := gitCall(resumed, "target", "GetFile", map[string]any{"filePath": "test.bin"})
				expect(t, read, 200, "")
				if read.body["fileContent"] != "AP8=" {
					t.Fatal(read.body)
				}
			})
		}
	}
}

func TestCodeCommitConcurrentParent(t *testing.T) {
	h := newHarness(t, codeCommit)
	expect(t, gitCall(h, "target", "CreateRepository", nil), 200, "")
	p := gitCall(h, "target", "PutFile", map[string]any{"filePath": "a", "branchName": "main", "fileContent": "YQ=="})
	expect(t, p, 200, "")
	start := make(chan struct{})
	done := make(chan reply, 2)
	for _, body := range []string{"Yg==", "Yw=="} {
		go func() {
			<-start
			done <- gitCall(h, "target", "PutFile", map[string]any{"branchName": "main", "filePath": "a", "fileContent": body, "parentCommitId": p.body["commitId"]})
		}()
	}
	close(start)
	a, b := <-done, <-done
	if a.status != 200 {
		a, b = b, a
	}
	expect(t, a, 200, "")
	expect(t, b, 400, "ParentCommitIdOutdatedException")
	ref := gitCall(h, "target", "GetBranch", map[string]any{"branchName": "main"})
	expect(t, ref, 200, "")
	if ref.body["branch"].(map[string]any)["commitId"] != a.body["commitId"] {
		t.Fatal(ref.body)
	}
	var commits int
	if err := h.db.QueryRow("SELECT count(*) FROM aws_codecommit_objects WHERE kind='commit'").Scan(&commits); err != nil || commits != 2 {
		t.Fatal(commits, err)
	}
}

func TestCodeCommitPendingAuthorizationCannotAdoptReplacement(t *testing.T) {
	h := newHarness(t, codeCommit)
	original := gitCall(h, "target", "CreateRepository", nil)
	expect(t, original, 200, "")
	originalID := original.body["repositoryMetadata"].(map[string]any)["repositoryId"]
	_, peer := openGitTeam(t, filepath.Join(t.TempDir(), "peer.db"))
	expect(t, gitCall(peer, "target", "CreateRepository", nil), 200, "")
	peerBefore := codeCommitSnapshot(t, peer.db)
	entered, release := make(chan struct{}), make(chan struct{})
	var pause sync.Once
	base := h.engine.cfg.Authorize
	h.engine.cfg.Authorize = func(ctx context.Context, principal, action, arn string) (string, error) {
		if action == "codecommit:PutFile" {
			pause.Do(func() { close(entered); <-release })
		}
		return base(ctx, principal, action, arn)
	}
	out := make(chan reply, 1)
	body := map[string]any{"filePath": "new", "branchName": "first", "fileContent": "AA=="}
	go func() { out <- gitCall(h, "target", "PutFile", body) }()
	<-entered
	expect(t, gitCall(h, "target", "DeleteRepository", nil), 200, "")
	replacement := gitCall(h, "target", "CreateRepository", nil)
	expect(t, replacement, 200, "")
	if replacement.body["repositoryMetadata"].(map[string]any)["repositoryId"] == originalID {
		t.Fatal("reused repository identity")
	}
	before := codeCommitSnapshot(t, h.db)
	close(release)
	stale := <-out
	if stale.status < 400 || stale.body["commitId"] != nil || !slices.Equal(before, codeCommitSnapshot(t, h.db)) || !slices.Equal(peerBefore, codeCommitSnapshot(t, peer.db)) {
		t.Fatalf("old request adopted another repository: HTTP %d", stale.status)
	}
	expect(t, gitCall(h, "target", "PutFile", body), 200, "")
}
