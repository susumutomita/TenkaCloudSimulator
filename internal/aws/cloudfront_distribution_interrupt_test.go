package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

func cfInterruptFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var frame []string
	for _, query := range []string{
		"SELECT json_array('distribution',id,caller,revision,bucket,bucket_created,bucket_region,document) FROM aws_cloudfront_distributions ORDER BY id",
		"SELECT json_array('receipt',distribution,id,caller,document) FROM aws_cloudfront_invalidations ORDER BY distribution,id",
		"SELECT json_array('cache',distribution,revision,principal,object_key,expires,inserted,content_type,etag,hex(data)) FROM aws_cloudfront_cache ORDER BY distribution,revision,principal,object_key",
		"SELECT json_array('bucket',name,region,created) FROM buckets ORDER BY name",
		"SELECT json_array('object',bucket,key,content_type,etag,modified,hex(data)) FROM objects ORDER BY bucket,key",
	} {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err = rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			frame = append(frame, row)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return frame
}

type cfStagedObservation struct {
	document, id            string
	targetCache, otherCache int
	contextErr              error
}

// Native network cancellation requires the client to disconnect after the
// SQLite write starts. A handler deadline instead expires while the client
// is still waiting for a response. Both drive the ordinary Gateway entry.
func cfInterruptRequest(t *testing.T, h *cfDistHarness, method, path, body, match, trigger string, cause error) cfStagedObservation {
	t.Helper()
	staged := make(chan cfStagedObservation, 1)
	var calls atomic.Int32
	var active context.Context
	conn, err := h.store.Database().Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = conn.Raw(func(raw any) error {
		return raw.(driver.Conn).Raw().CreateFunction("observe_cf_write", 4, 0, func(fn sqlite3.Context, args ...sqlite3.Value) {
			if calls.Add(1) == 1 {
				staged <- cfStagedObservation{args[0].Text(), args[1].Text(), args[2].Int(), args[3].Int(), active.Err()}
			}
			<-active.Done()
			fn.ResultNull()
		})
	})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	_, err = conn.ExecContext(t.Context(), trigger)
	closeErr := conn.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}

	type outcome struct {
		response *httptest.ResponseRecorder
		reason   error
	}
	finished := make(chan outcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		active = r.Context()
		if cause == context.DeadlineExceeded {
			var cancel context.CancelFunc
			active, cancel = context.WithTimeout(active, time.Second)
			defer cancel()
			r = r.WithContext(active)
		}
		result := httptest.NewRecorder()
		h.gateway.ServeHTTP(result, r)
		finished <- outcome{result, active.Err()}
		for name, values := range result.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(result.Code)
		_, _ = w.Write(result.Body.Bytes())
	}))
	defer server.Close()
	clientCtx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(clientCtx, method, server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Simulator-Service", "cloudfront")
	if match != "" {
		req.Header.Set("If-Match", match)
	}
	type transport struct {
		status int
		err    error
	}
	clientDone := make(chan transport, 1)
	go func() {
		response, err := server.Client().Do(req)
		if err != nil {
			clientDone <- transport{err: err}
			return
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		response.Body.Close()
		clientDone <- transport{response.StatusCode, readErr}
	}()
	var observed cfStagedObservation
	select {
	case observed = <-staged:
	case done := <-finished:
		t.Fatalf("handler returned HTTP %d before the transaction stage", done.response.Code)
	case <-clientCtx.Done():
		t.Fatal("request did not stage the write")
	}
	if cause == context.Canceled {
		cancel()
	}
	result := <-finished
	if calls.Load() != 1 || observed.contextErr != nil {
		t.Fatalf("expected one SQL write on a live request, observed %d with context %v", calls.Load(), observed.contextErr)
	}
	if !errors.Is(result.reason, cause) || result.response.Code != 500 || result.response.Header().Get("ETag") != "" || result.response.Header().Get("Location") != "" {
		t.Fatalf("failure response carried success metadata or lost cause: %d, %v", result.response.Code, result.reason)
	}
	var failure struct {
		Code string `xml:"Error>Code"`
	}
	if err := xml.Unmarshal(result.response.Body.Bytes(), &failure); err != nil || failure.Code != "InternalError" {
		t.Fatal("missing native error response", err)
	}
	client := <-clientDone
	if cause == context.Canceled && !errors.Is(client.err, context.Canceled) || cause == context.DeadlineExceeded && (client.err != nil || client.status != 500) {
		t.Fatalf("client returned HTTP %d, %v", client.status, client.err)
	}
	return observed
}

func TestCloudFrontInterruptedDistributionWrites(t *testing.T) {
	for _, op := range []string{"UpdateDistribution", "CreateInvalidation"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(op+"/"+cause.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.sqlite")
				h := cfDistHarnessFor(t, path)
				h.seed("cf-owner-origin", "original bytes")
				d, etag := h.create("owner", "cf-owner-origin")
				sibling, _ := h.create("sibling", "cf-owner-origin")
				native := cfDistributionPath + "/" + d.Id
				view := cfDeliveryPath + d.Id + "/objects/asset.txt"
				siblingView := cfDeliveryPath + sibling.Id + "/objects/asset.txt"
				h.call("POST", native+"/invalidation", cfInvalidate("prior", "/unrelated.txt"), "", "", 201)
				for _, route := range []string{view, siblingView} {
					h.call("GET", route, "", "", "", 200)
				}
				h.put("cf-owner-origin", "replacement bytes")

				peer := cfDistHarnessFor(t, filepath.Join(t.TempDir(), "peer.sqlite"))
				peer.seed("cf-owner-origin", "independent peer")
				peerDistribution, _ := peer.create("owner", "cf-owner-origin")
				peer.call("GET", cfDeliveryPath+peerDistribution.Id+"/objects/asset.txt", "", "", "", 200)
				before, peerBefore := cfInterruptFrame(t, h.store.Database()), cfInterruptFrame(t, peer.store.Database())

				method, route, match := "POST", native+"/invalidation", ""
				body := cfInvalidate("refresh-original", "/*")
				trigger := `CREATE TEMP TRIGGER stop_cf AFTER INSERT ON aws_cloudfront_invalidations BEGIN
					SELECT observe_cf_write(
					 (SELECT document FROM aws_cloudfront_invalidations WHERE distribution=NEW.distribution AND id=NEW.id),NEW.id,
					 (SELECT count(*) FROM aws_cloudfront_cache WHERE distribution=NEW.distribution),
					 (SELECT count(*) FROM aws_cloudfront_cache WHERE distribution<>NEW.distribution)); END`
				if op == "UpdateDistribution" {
					method, route, match = "PUT", native+"/config", etag
					body = strings.Replace(cfDistConfig("owner", "cf-owner-origin", true, 60), "local only", "updated locally", 1)
					trigger = `CREATE TEMP TRIGGER stop_cf AFTER UPDATE ON aws_cloudfront_distributions BEGIN
						SELECT observe_cf_write(
						 (SELECT document FROM aws_cloudfront_distributions WHERE id=NEW.id),NEW.revision,
						 (SELECT count(*) FROM aws_cloudfront_cache WHERE distribution=NEW.id),
						 (SELECT count(*) FROM aws_cloudfront_cache WHERE distribution<>NEW.id)); END`
				}
				staged := cfInterruptRequest(t, h, method, route, body, match, trigger, cause)
				if staged.targetCache != 0 || staged.otherCache != 1 {
					t.Fatal("target cache was not selectively evicted in the open transaction")
				}
				if op == "UpdateDistribution" {
					var changed cfDistribution
					if err := json.Unmarshal([]byte(staged.document), &changed); err != nil || changed.Id != d.Id || changed.DistributionConfig.Comment != "updated locally" || staged.id == "" || staged.id == etag {
						t.Fatal("did not observe actual new distribution configuration and revision", err)
					}
				} else {
					var attempted cfInvalidation
					if err := json.Unmarshal([]byte(staged.document), &attempted); err != nil || attempted.Status != "Completed" || !cfInvalidationID.MatchString(staged.id) || attempted.Id != staged.id || attempted.InvalidationBatch.CallerReference != "refresh-original" || !slices.Equal(attempted.InvalidationBatch.Paths.Items, []string{"/*"}) {
						t.Fatal("did not observe actual proposed receipt", err)
					}
				}
				if err := h.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened := cfDistHarnessFor(t, path)
				if !slices.Equal(before, cfInterruptFrame(t, reopened.store.Database())) || !slices.Equal(peerBefore, cfInterruptFrame(t, peer.store.Database())) {
					t.Fatal("interrupted request changed persisted owner or peer")
				}
				old := reopened.call("GET", view, "", "", "", 200)
				if old.Body.String() != "original bytes" || !strings.HasPrefix(old.Header().Get("X-Cache"), "Hit") {
					t.Fatal("interrupted request lost actual cached object")
				}
				if op == "UpdateDistribution" {
					got := reopened.call(method, route, body, "", match, 200)
					current := reopened.call("GET", native+"/config", "", "", "", 200)
					if got.Header().Get("ETag") == "" || got.Header().Get("ETag") == etag || current.Header().Get("ETag") != got.Header().Get("ETag") || !strings.Contains(current.Body.String(), "<Comment>updated locally</Comment>") {
						t.Fatal("clean update did not persist one new revision")
					}
				} else {
					reopened.call("GET", native+"/invalidation/"+staged.id, "", "", "", 404)
					got := reopened.call(method, route, body, "", match, 201)
					var receipt cfInvalidation
					if err := xml.Unmarshal(got.Body.Bytes(), &receipt); err != nil || receipt.Id == staged.id || !cfInvalidationID.MatchString(receipt.Id) || receipt.Status != "Completed" {
						t.Fatal("retry did not persist a fresh invalidation", err)
					}
					reopened.call("GET", native+"/invalidation/"+receipt.Id, "", "", "", 200)
					again := reopened.call(method, route, body, "", match, 201)
					var count int
					if err := reopened.store.Database().QueryRowContext(t.Context(), "SELECT count(*) FROM aws_cloudfront_invalidations WHERE distribution=?", d.Id).Scan(&count); err != nil || count != 2 {
						t.Fatal("expected the original and one new invalidation", count, err)
					}
					if got.Header().Get("Location") == "" || again.Header().Get("Location") != got.Header().Get("Location") {
						t.Fatal("idempotent retry created another receipt")
					}
				}
				fresh := reopened.call("GET", view, "", "", "", 200)
				retained := reopened.call("GET", siblingView, "", "", "", 200)
				if fresh.Body.String() != "replacement bytes" || !strings.HasPrefix(fresh.Header().Get("X-Cache"), "Miss") || retained.Body.String() != "original bytes" || !strings.HasPrefix(retained.Header().Get("X-Cache"), "Hit") || !slices.Equal(peerBefore, cfInterruptFrame(t, peer.store.Database())) {
					t.Fatal("retry did not refresh only the target")
				}
			})
		}
	}
}
