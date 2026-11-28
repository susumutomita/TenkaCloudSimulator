package aws

import (
	"encoding/xml"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

func cfRecoveryError(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var response struct {
		XMLName xml.Name
		Code    string `xml:"Error>Code"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil || response.XMLName.Local != "ErrorResponse" || response.Code != code {
		t.Fatalf("expected native %s error, received %v (%v)", code, response, err)
	}
	for _, field := range []string{"ETag", "Location", "X-Cache", "Content-Disposition"} {
		if w.Header().Get(field) != "" {
			t.Fatalf("failed operation returned %s", field)
		}
	}
}

// A late fault is armed by a physical row change, so a refused readback also
// verifies rollback of the preceding mutation in the native transaction.
type cfRecoveryFault struct {
	operation string
	action    sqlite3.AuthorizerActionCode
	table     string
	column    string
	after     string
}

func (f cfRecoveryFault) install(t *testing.T, h *cfDistHarness) func() {
	t.Helper()
	configure := func(fn func(*sqlite3.Conn) error) {
		t.Helper()
		c, err := h.store.Database().Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := c.Raw(func(raw any) error { return fn(raw.(driver.Conn).Raw()) }); err != nil {
			t.Fatal(err)
		}
	}
	armed, fired := f.after == "", false
	configure(func(c *sqlite3.Conn) error {
		c.UpdateHook(func(_ sqlite3.AuthorizerActionCode, _, table string, _ int64) {
			if table == f.after {
				armed = true
			}
		})
		return c.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
			if armed && action == f.action && table == f.table && (f.column == "*" || column == f.column) {
				fired = true
				return sqlite3.AUTH_DENY
			}
			return sqlite3.AUTH_OK
		})
	})
	return func() {
		configure(func(c *sqlite3.Conn) error {
			c.UpdateHook(nil)
			return c.SetAuthorizer(nil)
		})
		if !fired {
			t.Fatal("requested physical storage fault was never reached")
		}
	}
}

func TestCloudFrontDistributionStorageRecovery(t *testing.T) {
	const dist = "aws_cloudfront_distributions"
	const cache = "aws_cloudfront_cache"
	const receipts = "aws_cloudfront_invalidations"
	for _, fault := range []cfRecoveryFault{
		{"deliver", sqlite3.AUTH_TRANSACTION, "BEGIN", "*", ""},
		{"deliver", sqlite3.AUTH_READ, cache, "content_type", ""},
		{"deliver", sqlite3.AUTH_DELETE, cache, "*", ""},
		{"deliver", sqlite3.AUTH_READ, "objects", "data", cache},
		{"deliver", sqlite3.AUTH_FUNCTION, "", "sum", cache},
		{"deliver", sqlite3.AUTH_INSERT, cache, "*", ""},
		{"deliver", sqlite3.AUTH_READ, "buckets", "created", ""},
		{"create", sqlite3.AUTH_READ, dist, "", ""},
		{"create", sqlite3.AUTH_READ, dist, "caller", ""},
		{"create", sqlite3.AUTH_READ, dist, "document", dist},
		{"update", sqlite3.AUTH_READ, dist, "document", ""},
		{"update", sqlite3.AUTH_DELETE, cache, "*", ""},
		{"update", sqlite3.AUTH_READ, cache, "distribution", cache},
		{"invalidate", sqlite3.AUTH_READ, dist, "document", ""},
		{"invalidate", sqlite3.AUTH_READ, receipts, "document", ""},
		{"invalidate", sqlite3.AUTH_READ, receipts, "document", receipts},
		{"invalidate", sqlite3.AUTH_DELETE, cache, "*", ""},
		{"delete", sqlite3.AUTH_READ, dist, "document", ""},
		{"delete", sqlite3.AUTH_DELETE, cache, "*", ""},
		{"delete", sqlite3.AUTH_DELETE, receipts, "*", ""},
		{"delete", sqlite3.AUTH_DELETE, dist, "*", receipts},
		{"delete", sqlite3.AUTH_READ, dist, "id", dist},
	} {
		t.Run(strings.Join([]string{fault.operation, fault.table, fault.column, fault.after}, "/"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.db")
			h := cfDistHarnessFor(t, path)
			h.seed("owned-origin", "old origin bytes")
			d, etag := h.create("target", "owned-origin")
			sibling, _ := h.create("sibling", "owned-origin")
			native := cfDistributionPath + "/" + d.Id
			view := cfDeliveryPath + d.Id + "/objects/asset.txt"
			peerView := cfDeliveryPath + sibling.Id + "/objects/asset.txt"
			h.call("POST", native+"/invalidation", cfInvalidate("existing", "/asset.txt"), "", "", 201)
			h.call("GET", view, "", "", "", 200)
			h.call("GET", peerView, "", "", "", 200)
			h.put("owned-origin", "fresh origin bytes")
			method, address, body, match, status := "GET", view, "", "", 200
			switch fault.operation {
			case "deliver":
				if _, err := h.store.Database().Exec("UPDATE aws_cloudfront_cache SET expires=0 WHERE distribution=?", d.Id); err != nil {
					t.Fatal(err)
				}
			case "create":
				method, address, status = "POST", cfDistributionPath, 201
				body = cfDistConfig("distinct", "owned-origin", true, 10)
			case "update":
				method, address, match = "PUT", native+"/config", etag
				body = cfDistConfig("target", "owned-origin", true, 10)
			case "invalidate":
				method, address, status = "POST", native+"/invalidation", 201
				body = cfInvalidate("new-refresh", "/asset.txt")
			case "delete":
				got := h.call("PUT", native+"/config", cfDistConfig("target", "owned-origin", false, 60), "", etag, 200)
				method, address, match, status = "DELETE", native, got.Header().Get("ETag"), 204
			}
			before := cfInterruptFrame(t, h.store.Database())
			clear := fault.install(t, h)
			got := h.call(method, address, body, "", match, 500)
			clear()
			cfRecoveryError(t, got, "InternalError")
			if err := h.store.Close(); err != nil {
				t.Fatal(err)
			}
			h = cfDistHarnessFor(t, path)
			if !slices.Equal(before, cfInterruptFrame(t, h.store.Database())) {
				t.Fatal("failed operation changed persisted distribution, origin, invalidation or cache")
			}
			retry := h.call(method, address, body, "", match, status)
			switch fault.operation {
			case "create", "update":
				var result cfDistribution
				if err := xml.Unmarshal(retry.Body.Bytes(), &result); err != nil || result.DistributionConfig.DefaultCacheBehavior.DefaultTTL != 10 || retry.Header().Get("ETag") == "" {
					t.Fatalf("retry did not save requested configuration: %v", err)
				}
				saved := h.call("GET", cfDistributionPath+"/"+result.Id, "", "", "", 200)
				if saved.Header().Get("ETag") != retry.Header().Get("ETag") || saved.Body.String() != retry.Body.String() {
					t.Fatal("persisted distribution differs from acknowledged retry")
				}
			case "invalidate":
				var inv cfInvalidation
				if err := xml.Unmarshal(retry.Body.Bytes(), &inv); err != nil || inv.InvalidationBatch.CallerReference != "new-refresh" {
					t.Fatalf("invalid retry receipt: %v", err)
				}
				stored := h.call("GET", native+"/invalidation/"+inv.Id, "", "", "", 200)
				if stored.Body.String() != retry.Body.String() || h.call("GET", view, "", "", "", 200).Body.String() != "fresh origin bytes" {
					t.Fatal("retry receipt or refreshed origin bytes were not retained")
				}
			case "deliver":
				if retry.Body.String() != "fresh origin bytes" || !strings.HasPrefix(retry.Header().Get("X-Cache"), "Miss") {
					t.Fatal("retry did not fetch the saved replacement")
				}
			case "delete":
				cfRecoveryError(t, h.call("GET", native, "", "", "", 404), "NoSuchDistribution")
			}
			if w := h.call("GET", peerView, "", "", "", 200); w.Body.String() != "old origin bytes" || !strings.HasPrefix(w.Header().Get("X-Cache"), "Hit") {
				t.Fatal("target failure or retry changed sibling cache")
			}
		})
	}
}

func TestCloudFrontDistributionDamagedSavedDocuments(t *testing.T) {
	for _, damage := range []string{"invalid distribution JSON", "another distribution ID", "invalid invalidation JSON", "another invalidation ID"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "reopen.db")
			h := cfDistHarnessFor(t, path)
			h.seed("doc-origin", "retained")
			target, _ := h.create("published", "doc-origin")
			sibling, _ := h.create("another", "doc-origin")
			native := cfDistributionPath + "/" + target.Id
			h.call("GET", cfDeliveryPath+sibling.Id+"/objects/asset.txt", "", "", "", 200)
			batch := cfInvalidate("known", "/asset.txt")
			created := h.call("POST", native+"/invalidation", batch, "", "", 201)
			var inv cfInvalidation
			if err := xml.Unmarshal(created.Body.Bytes(), &inv); err != nil {
				t.Fatal(err)
			}
			isReceipt := strings.Contains(damage, "invalidation")
			table, id, other, address := "aws_cloudfront_distributions", target.Id, sibling.Id, native
			if isReceipt {
				table, id, other, address = "aws_cloudfront_invalidations", inv.Id, "I000000000000000000000000", native+"/invalidation/"+inv.Id
			}
			healthy := cfInterruptFrame(t, h.store.Database())
			var original string
			if err := h.store.Database().QueryRow("SELECT document FROM "+table+" WHERE id=?", id).Scan(&original); err != nil {
				t.Fatal(err)
			}
			damaged := "{"
			if strings.HasPrefix(damage, "another") {
				damaged = strings.Replace(original, id, other, 1)
				if damaged == original {
					t.Fatal("test did not change the saved identifier")
				}
			}
			write := func(document string) {
				t.Helper()
				if _, err := h.store.Database().Exec("UPDATE "+table+" SET document=? WHERE id=?", document, id); err != nil {
					t.Fatal(err)
				}
			}
			write(damaged)
			before := cfInterruptFrame(t, h.store.Database())
			cfRecoveryError(t, h.call("GET", address, "", "", "", 500), "InternalError")
			if !isReceipt {
				cfRecoveryError(t, h.call("GET", cfDistributionPath, "", "", "", 500), "InternalError")
				cfRecoveryError(t, h.call("GET", cfDeliveryPath+target.Id+"/objects/asset.txt", "", "", "", 500), "InternalError")
			} else if damage == "invalid invalidation JSON" {
				cfRecoveryError(t, h.call("POST", native+"/invalidation", batch, "", "", 500), "InternalError")
			}
			if err := h.store.Close(); err != nil {
				t.Fatal(err)
			}
			h = cfDistHarnessFor(t, path)
			if !slices.Equal(before, cfInterruptFrame(t, h.store.Database())) {
				t.Fatal("refusal changed the damaged row or its siblings")
			}
			write(original)
			if !slices.Equal(healthy, cfInterruptFrame(t, h.store.Database())) {
				t.Fatal("restoring only the original bytes did not recover the original frame")
			}
			retry := h.call("GET", address, "", "", "", 200)
			if isReceipt {
				if retry.Body.String() != created.Body.String() {
					t.Fatal("restored receipt disagrees with original acknowledgement")
				}
			} else {
				var actual cfDistribution
				if err := xml.Unmarshal(retry.Body.Bytes(), &actual); err != nil || actual.Id != target.Id || actual.ARN != target.ARN {
					t.Fatal("restored distribution mismatch", err)
				}
			}
		})
	}
}

func TestCloudFrontDistributionCacheAndObjectLimits(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("capacity-origin", "unchanged sibling bytes")
	target, _ := h.create("bounded", "capacity-origin")
	view := cfDeliveryPath + target.Id + "/objects/"
	put := func(key string, body []byte) {
		t.Helper()
		if _, err := h.gateway.S3.PutContext(t.Context(), "capacity-origin", key, "application/octet-stream", body, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	cfRecoveryError(t, h.call("GET", view+"unwritten", "", "", "", 404), "NoSuchKey")
	oneMiB := strings.Repeat("x", 1<<20)
	put("sized", []byte(oneMiB+"y"))
	cfRecoveryError(t, h.call("GET", view+"sized", "", "", "", 502), "ResponseLimitExceeded")
	put("sized", []byte(oneMiB))
	boundary := h.call("GET", view+"sized", "", "", "", 200)
	if boundary.Body.String() != oneMiB || !strings.HasPrefix(boundary.Header().Get("X-Cache"), "Miss") {
		t.Fatal("one MiB boundary did not return exact origin bytes")
	}
	for i := 0; i < 127; i++ {
		key := "file-" + strconv.Itoa(i)
		put(key, []byte(key))
		if w := h.call("GET", view+key, "", "", "", 200); w.Body.String() != key {
			t.Fatal("cache seed read mismatch")
		}
	}
	put("overflow", []byte("new entry"))
	before := cfInterruptFrame(t, h.store.Database())
	cfRecoveryError(t, h.call("GET", view+"overflow", "", "", "", 503), "CapacityExceeded")
	if !slices.Equal(before, cfInterruptFrame(t, h.store.Database())) {
		t.Fatal("refused cache fill changed durable state")
	}
	if w := h.call("GET", view+"file-0", "", "", "", 200); w.Body.String() != "file-0" || !strings.HasPrefix(w.Header().Get("X-Cache"), "Hit") {
		t.Fatal("full cache did not serve existing snapshot")
	}
	h.call("POST", cfDistributionPath+"/"+target.Id+"/invalidation", cfInvalidate("one-slot", "/file-0"), "", "", 201)
	if w := h.call("GET", view+"overflow", "", "", "", 200); w.Body.String() != "new entry" || !strings.HasPrefix(w.Header().Get("X-Cache"), "Miss") {
		t.Fatal("freeing one cache slot did not admit previous failed read")
	}
}
