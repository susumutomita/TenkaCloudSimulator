package aws

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type cfDistHarness struct {
	t       *testing.T
	store   *core.Store
	gateway *Gateway
}

func cfDistHarnessFor(t *testing.T, path string) *cfDistHarness {
	t.Helper()
	store, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s3, err := NewS3Store(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	front, err := NewCloudFrontStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &cfDistHarness{t, store, &Gateway{Store: store, S3: s3, Identities: core.NewIdentities(store), CloudFront: front}}
}
func cfDistConfig(caller, bucket string, enabled bool, ttl int) string {
	return fmt.Sprintf(`<DistributionConfig xmlns="%s"><CallerReference>%s</CallerReference><Origins><Quantity>1</Quantity><Items><Origin><Id>saved</Id><DomainName>%s.s3.amazonaws.com</DomainName><S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig></Origin></Items></Origins><DefaultCacheBehavior><TargetOriginId>saved</TargetOriginId><ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy><AllowedMethods><Quantity>2</Quantity><Items><Method>GET</Method><Method>HEAD</Method></Items><CachedMethods><Quantity>2</Quantity><Items><Method>GET</Method><Method>HEAD</Method></Items></CachedMethods></AllowedMethods><ForwardedValues><QueryString>false</QueryString><Cookies><Forward>none</Forward></Cookies></ForwardedValues><MinTTL>0</MinTTL><DefaultTTL>%d</DefaultTTL><MaxTTL>86400</MaxTTL></DefaultCacheBehavior><Comment>local only</Comment><Enabled>%t</Enabled></DistributionConfig>`, cloudFrontNS, caller, bucket, ttl, enabled)
}
func (h *cfDistHarness) call(method, path, body, principal, match string, status int) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-Simulator-Service", "cloudfront")
	if principal != "" {
		r.Header.Set("X-Simulator-Principal", principal)
	}
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	if w.Code != status {
		h.t.Fatalf("%s %s expected%d got%d %s", method, path, status, w.Code, w.Body.String())
	}
	return w
}
func (h *cfDistHarness) create(caller, bucket string) (cfDistribution, string) {
	h.t.Helper()
	w := h.call("POST", cfDistributionPath, cfDistConfig(caller, bucket, true, 60), "", "", 201)
	var value cfDistribution
	if err := xml.Unmarshal(w.Body.Bytes(), &value); err != nil {
		h.t.Fatal(err)
	}
	if !cfDistributionID.MatchString(value.Id) || value.ARN != cfDistributionARN+value.Id || w.Header().Get("ETag") == "" {
		h.t.Fatal("invalid distribution metadata")
	}
	return value, w.Header().Get("ETag")
}
func (h *cfDistHarness) seed(bucket, body string) {
	h.t.Helper()
	if err := h.gateway.S3.Create(bucket, "us-east-1"); err != nil {
		h.t.Fatal(err)
	}
	h.put(bucket, body)
}
func (h *cfDistHarness) put(bucket, body string) {
	h.t.Helper()
	if _, err := h.gateway.S3.PutContext(h.t.Context(), bucket, "asset.txt", "text/plain", []byte(body), "", ""); err != nil {
		h.t.Fatal(err)
	}
}
func cfInvalidate(caller, path string) string {
	return fmt.Sprintf(`<InvalidationBatch xmlns="%s"><CallerReference>%s</CallerReference><Paths><Quantity>1</Quantity><Items><Path>%s</Path></Items></Paths></InvalidationBatch>`, cloudFrontNS, caller, path)
}

func TestCloudFrontDistributionNativeCacheInvalidationLifecycle(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "old")
	d, etag := h.create("first", "local-origin")
	native := cfDistributionPath + "/" + d.Id
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	h.call("POST", cfDistributionPath, cfDistConfig("first", "local-origin", true, 60), "", "", 409)
	check := h.call("GET", native+"/config", "", "", "", 200)
	if check.Header().Get("ETag") != etag {
		t.Fatal("config ETag changed")
	}
	miss := h.call("GET", view, "", "", "", 200)
	if miss.Body.String() != "old" || miss.Header().Get("X-Cache") != "Miss from local CloudFront simulator" || miss.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("not a real origin miss")
	}
	h.put("local-origin", "new")
	hit := h.call("GET", view, "", "", "", 200)
	if hit.Body.String() != "old" || !strings.HasPrefix(hit.Header().Get("X-Cache"), "Hit") {
		t.Fatal("cache did not retain snapshot")
	}
	head := h.call("HEAD", view, "", "", "", 200)
	if head.Body.Len() != 0 || head.Header().Get("Content-Length") != "3" || head.Header().Get("ETag") != hit.Header().Get("ETag") {
		t.Fatal("HEAD did not describe actual cached bytes")
	}
	created := h.call("POST", native+"/invalidation", cfInvalidate("refresh", "/asset.txt"), "", "", 201)
	var inv cfInvalidation
	if err := xml.Unmarshal(created.Body.Bytes(), &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Status != "Completed" || !cfInvalidationID.MatchString(inv.Id) {
		t.Fatal("invalidation not complete")
	}
	for i := 0; i < 2; i++ {
		retry := h.call("POST", native+"/invalidation", cfInvalidate("refresh", "/asset.txt"), "", "", 201)
		var again cfInvalidation
		_ = xml.Unmarshal(retry.Body.Bytes(), &again)
		if again.Id != inv.Id {
			t.Fatal("invalidation retry not idempotent")
		}
	}
	h.call("POST", native+"/invalidation", cfInvalidate("refresh", "/*"), "", "", 400)
	h.call("GET", native+"/invalidation/"+inv.Id, "", "", "", 200)
	updated := h.call("GET", view, "", "", "", 200)
	if updated.Body.String() != "new" || !strings.HasPrefix(updated.Header().Get("X-Cache"), "Miss") {
		t.Fatal("invalidation did not evict actual cache")
	}
	h.call("DELETE", native, "", "", etag, 409)
	h.call("PUT", native+"/config", cfDistConfig("first", "local-origin", false, 60), "", "wrong", 412)
	disabled := h.call("PUT", native+"/config", cfDistConfig("first", "local-origin", false, 60), "", etag, 200)
	next := disabled.Header().Get("ETag")
	if next == etag {
		t.Fatal("update did not change version")
	}
	h.call("GET", view, "", "", "", 403)
	h.call("DELETE", native, "", "", etag, 412)
	h.call("DELETE", native, "", "", next, 204)
	h.call("GET", native, "", "", "", 404)
	h.call("GET", view, "", "", "", 404)
	for _, table := range []string{"aws_cloudfront_cache", "aws_cloudfront_invalidations", "aws_cloudfront_distributions"} {
		var count int
		if err := h.store.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("distribution cleanup left rows", table, err)
		}
	}
	if _, err := h.gateway.S3.DeleteContext(t.Context(), "local-origin", "asset.txt", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.S3.DeleteBucket("local-origin"); err != nil {
		t.Fatal(err)
	}
}

func TestCloudFrontDistributionFreshJointPolicyAndOriginLifetime(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "kept")
	d, _ := h.create("first", "local-origin")
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	policy := func(statement string) {
		t.Helper()
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":` + statement + `}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = h.gateway.Identities.Set("reader", p); err != nil {
			t.Fatal(err)
		}
	}
	allow := fmt.Sprintf(`[{"Effect":"Allow","Action":"cloudfront:GetDistribution","Resource":%q},{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::local-origin/asset.txt"}]`, d.ARN)
	policy(allow)
	h.call("GET", view, "", "reader", "", 200)
	policy(fmt.Sprintf(`{"Effect":"Allow","Action":"cloudfront:GetDistribution","Resource":%q}`, d.ARN))
	h.call("GET", view, "", "reader", "", 403)
	policy(`{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::local-origin/asset.txt"}`)
	h.call("GET", view, "", "reader", "", 403)
	policy(strings.TrimSuffix(allow, "]") + `,{"Effect":"Deny","Action":"s3:GetObject","Resource":"*"}]`)
	h.call("GET", view, "", "reader", "", 403)
	h.call("GET", view, "", "missing", "", 403)
	policy(allow)
	if w := h.call("GET", view, "", "reader", "", 200); !strings.HasPrefix(w.Header().Get("X-Cache"), "Hit") {
		t.Fatal("permission changes incorrectly cleared cached bytes")
	}
	if _, err := h.gateway.S3.DeleteContext(t.Context(), "local-origin", "asset.txt", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.gateway.S3.DeleteBucket("local-origin"); err != nil {
		t.Fatal(err)
	}
	h.call("GET", view, "", "reader", "", 404)
	h.seed("local-origin", "replacement")
	h.call("GET", view, "", "reader", "", 404)
	config := h.call("GET", cfDistributionPath+"/"+d.Id+"/config", "", "", "", 200)
	h.call("PUT", cfDistributionPath+"/"+d.Id+"/config", config.Body.String(), "", config.Header().Get("ETag"), 200)
	if w := h.call("GET", view, "", "reader", "", 200); w.Body.String() != "replacement" {
		t.Fatal("explicit origin rebinding did not clear old cache")
	}
}

func TestCloudFrontDistributionTTLRestartAndPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	h := cfDistHarnessFor(t, path)
	h.seed("local-origin", "old")
	d, _ := h.create("first", "local-origin")
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	h.call("GET", view, "", "", "", 200)
	h.put("local-origin", "new")
	peer := cfDistHarnessFor(t, "")
	peer.seed("local-origin", "peer")
	other, _ := peer.create("first", "local-origin")
	otherView := cfDeliveryPath + other.Id + "/objects/asset.txt"
	peer.call("GET", view, "", "", "", 404)
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := cfDistHarnessFor(t, path)
	if w := reopened.call("GET", view, "", "", "", 200); w.Body.String() != "old" {
		t.Fatal("cache did not survive clean restart")
	}
	tx, err := reopened.store.Database().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := cfDeliver(t.Context(), tx, d.Id, core.Administrator, "asset.txt", func(string, string) string { return "allowed" }, time.Now().Add(61*time.Second))
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil || string(value.data) != "new" || value.cache != "Miss" {
		t.Fatal("TTL failed", err)
	}
	if w := peer.call("GET", otherView, "", "", "", 200); w.Body.String() != "peer" {
		t.Fatal("peer state changed")
	}
}

func TestCloudFrontDistributionStrictXMLAndAtomicFaults(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "old")
	good := cfDistConfig("first", "local-origin", true, 60)
	for _, bad := range []string{strings.Replace(good, "<Enabled>true</Enabled>", "", 1), strings.Replace(good, "<Enabled>true</Enabled>", "<Enabled>true</Enabled><Enabled>false</Enabled>", 1), strings.Replace(good, "<MinTTL>0</MinTTL>", "<MinTTL>-1</MinTTL>", 1), strings.Replace(good, "local-origin.s3.amazonaws.com", "127.0.0.1", 1), strings.Replace(good, "<QueryString>false</QueryString>", "<QueryString>true</QueryString>", 1), strings.Replace(good, "</DistributionConfig>", "<CachePolicyId>unknown</CachePolicyId></DistributionConfig>", 1)} {
		h.call("POST", cfDistributionPath, bad, "", "", 400)
	}
	if _, err := h.store.Database().Exec("CREATE TEMP TRIGGER fail_cf AFTER INSERT ON aws_cloudfront_distributions BEGIN UPDATE aws_cloudfront_distributions SET revision='unexpected' WHERE id=NEW.id; END"); err != nil {
		t.Fatal(err)
	}
	h.call("POST", cfDistributionPath, good, "", "", 500)
	var count int
	_ = h.store.Database().QueryRow("SELECT count(*) FROM aws_cloudfront_distributions").Scan(&count)
	if count != 0 {
		t.Fatal("failed insert leaked distribution")
	}
	_, _ = h.store.Database().Exec("DROP TRIGGER fail_cf")
	d, etag := h.create("first", "local-origin")
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	h.call("GET", view, "", "", "", 200)
	if _, err := h.store.Database().Exec("CREATE TEMP TRIGGER fail_invalidation BEFORE INSERT ON aws_cloudfront_invalidations BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	h.call("POST", cfDistributionPath+"/"+d.Id+"/invalidation", cfInvalidate("failed", "/*"), "", "", 500)
	h.put("local-origin", "new")
	if w := h.call("GET", view, "", "", "", 200); w.Body.String() != "old" {
		t.Fatal("failed invalidation evicted cache")
	}
	_, _ = h.store.Database().Exec("DROP TRIGGER fail_invalidation")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := httptest.NewRequest("PUT", cfDistributionPath+"/"+d.Id+"/config", strings.NewReader(cfDistConfig("first", "local-origin", false, 60))).WithContext(ctx)
	r.Header.Set("X-Simulator-Service", "cloudfront")
	r.Header.Set("If-Match", etag)
	w := httptest.NewRecorder()
	h.gateway.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatal("canceled update succeeded")
	}
	current := h.call("GET", cfDistributionPath+"/"+d.Id, "", "", "", 200)
	if current.Header().Get("ETag") != etag {
		t.Fatal("canceled update changed revision")
	}
	for _, extra := range []string{"?x=1", "/../asset.txt", "%2Fsecret"} {
		h.call("GET", view+extra, "", "", "", 400)
	}
}

func TestCloudFrontDistributionConcurrentETagAndScopedInvalidation(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "old")
	d, etag := h.create("first", "local-origin")
	other, _ := h.create("second", "local-origin")
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	peerView := cfDeliveryPath + other.Id + "/objects/asset.txt"
	h.call("GET", view, "", "", "", 200)
	h.call("GET", peerView, "", "", "", 200)
	h.put("local-origin", "new")
	h.call("POST", cfDistributionPath+"/"+d.Id+"/invalidation", cfInvalidate("only-first", "/*"), "", "", 201)
	if got := h.call("GET", view, "", "", "", 200); got.Body.String() != "new" {
		t.Fatal("target cache not invalidated")
	}
	if got := h.call("GET", peerView, "", "", "", 200); got.Body.String() != "old" || !strings.HasPrefix(got.Header().Get("X-Cache"), "Hit") {
		t.Fatal("invalidation changed another distribution's cache")
	}
	var wait sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for _, comment := range []string{"winner-a", "winner-b"} {
		wait.Add(1)
		go func(comment string) {
			defer wait.Done()
			body := strings.Replace(cfDistConfig("first", "local-origin", true, 60), "local only", comment, 1)
			r := httptest.NewRequest("PUT", cfDistributionPath+"/"+d.Id+"/config", strings.NewReader(body))
			r.Header.Set("X-Simulator-Service", "cloudfront")
			r.Header.Set("If-Match", etag)
			w := httptest.NewRecorder()
			h.gateway.ServeHTTP(w, r)
			results <- w
		}(comment)
	}
	wait.Wait()
	close(results)
	counts := map[int]int{}
	var winner cfDistribution
	var version string
	for result := range results {
		counts[result.Code]++
		if result.Code == 200 {
			if err := xml.Unmarshal(result.Body.Bytes(), &winner); err != nil {
				t.Fatal(err)
			}
			version = result.Header().Get("ETag")
		}
	}
	if counts[200] != 1 || counts[412] != 1 || version == "" || version == etag {
		t.Fatal("concurrent updates did not select exactly one ETag winner", counts)
	}
	current := h.call("GET", cfDistributionPath+"/"+d.Id, "", "", "", 200)
	var saved cfDistribution
	if err := xml.Unmarshal(current.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.DistributionConfig.Comment != winner.DistributionConfig.Comment || current.Header().Get("ETag") != version {
		t.Fatal("committed version does not match acknowledged winner")
	}
	var target, peer int
	if err := h.store.Database().QueryRow("SELECT count(*) FROM aws_cloudfront_cache WHERE distribution=?", d.Id).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Database().QueryRow("SELECT count(*) FROM aws_cloudfront_cache WHERE distribution=?", other.Id).Scan(&peer); err != nil {
		t.Fatal(err)
	}
	if target != 0 || peer != 1 {
		t.Fatal("update eviction escaped its distribution", target, peer)
	}
}

func TestCloudFrontDistributionManagementIAMAndNativePaging(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "saved")
	d, _ := h.create("first", "local-origin")
	_, _ = h.create("second", "local-origin")
	allow := func(action, resource string) {
		t.Helper()
		policy, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":%q,"Resource":%q}}`, action, resource)))
		if err != nil {
			t.Fatal(err)
		}
		if err = h.gateway.Identities.Set("manager", policy); err != nil {
			t.Fatal(err)
		}
	}
	native := cfDistributionPath + "/" + d.Id
	for _, op := range []struct{ action, method, path, body string }{
		{"CreateDistribution", "POST", cfDistributionPath, cfDistConfig("third", "local-origin", true, 60)},
		{"ListDistributions", "GET", cfDistributionPath, ""},
		{"GetDistribution", "GET", native, ""},
		{"GetDistributionConfig", "GET", native + "/config", ""},
		{"UpdateDistribution", "PUT", native + "/config", cfDistConfig("first", "local-origin", true, 60)},
		{"DeleteDistribution", "DELETE", native, ""},
		{"CreateInvalidation", "POST", native + "/invalidation", cfInvalidate("read-checked", "/*")},
		{"GetInvalidation", "GET", native + "/invalidation/I" + strings.Repeat("a", 24), ""},
	} {
		allow("cloudfront:"+op.action, "arn:aws:cloudfront::000000000000:distribution/another")
		if got := h.call(op.method, op.path, op.body, "manager", "", 403); got.Header().Get("X-Simulator-IAM-Decision") != "implicitDeny" {
			t.Fatal("wrong management IAM decision", op.action)
		}
	}
	allow("cloudfront:GetDistribution", d.ARN)
	h.call("GET", native, "", "manager", "", 200)
	h.call("GET", native+"/config", "", "manager", "", 403)
	allow("cloudfront:ListDistributions", "*")
	page := h.call("GET", cfDistributionPath+"?MaxItems=1", "", "manager", "", 200)
	var first cfDistributionList
	if err := xml.Unmarshal(page.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Quantity != 1 || !first.IsTruncated || first.NextMarker != first.Items[0].Id {
		t.Fatal("invalid first native page")
	}
	next := h.call("GET", cfDistributionPath+"?MaxItems=1&Marker="+first.NextMarker, "", "manager", "", 200)
	var second cfDistributionList
	if err := xml.Unmarshal(next.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if second.Quantity != 1 || second.IsTruncated || second.NextMarker != "" || second.Items[0].Id == first.Items[0].Id {
		t.Fatal("invalid last native page")
	}
	h.call("GET", cfDistributionPath+"?MaxItems=0", "", "manager", "", 400)
	h.call("GET", cfDistributionPath+"?MaxItems=1&MaxItems=2", "", "manager", "", 400)
	allow("cloudfront:GetDistribution", "*")
	h.call("GET", cfDistributionPath+"?MaxItems=1&Marker="+first.NextMarker, "", "manager", "", 403)
}

func TestCloudFrontInvalidationRequiresActualCacheEviction(t *testing.T) {
	for _, path := range []string{"/asset.txt", "/*"} {
		t.Run(path, func(t *testing.T) {
			h := cfDistHarnessFor(t, "")
			h.seed("local-origin", "old")
			d, _ := h.create("first", "local-origin")
			other, _ := h.create("second", "local-origin")
			kept := h.call("POST", cfDistributionPath+"/"+d.Id+"/invalidation", cfInvalidate("retained", "/absent.txt"), "", "", 201)
			var retained cfInvalidation
			if err := xml.Unmarshal(kept.Body.Bytes(), &retained); err != nil {
				t.Fatal(err)
			}
			view := cfDeliveryPath + d.Id + "/objects/asset.txt"
			otherView := cfDeliveryPath + other.Id + "/objects/asset.txt"
			h.call("GET", view, "", "", "", 200)
			h.call("GET", otherView, "", "", "", 200)
			h.put("local-origin", "new")
			if _, err := h.store.Database().Exec("CREATE TEMP TRIGGER ignore_cache_delete BEFORE DELETE ON aws_cloudfront_cache BEGIN SELECT RAISE(IGNORE); END"); err != nil {
				t.Fatal(err)
			}
			response := h.call("POST", cfDistributionPath+"/"+d.Id+"/invalidation", cfInvalidate("retry", path), "", "", 500)
			if strings.Contains(response.Body.String(), "Completed") || response.Header().Get("Location") != "" {
				t.Fatal("failed eviction acknowledged invalidation")
			}
			var receipts int
			if err := h.store.Database().QueryRow("SELECT count(*) FROM aws_cloudfront_invalidations").Scan(&receipts); err != nil || receipts != 1 {
				t.Fatal("failed eviction changed receipt set", err)
			}
			prior := h.call("GET", cfDistributionPath+"/"+d.Id+"/invalidation/"+retained.Id, "", "", "", 200)
			var still cfInvalidation
			if err := xml.Unmarshal(prior.Body.Bytes(), &still); err != nil || still.Id != retained.Id || still.CreateTime != retained.CreateTime || still.Status != retained.Status || still.InvalidationBatch.CallerReference != retained.InvalidationBatch.CallerReference || len(still.InvalidationBatch.Paths.Items) != 1 || still.InvalidationBatch.Paths.Items[0] != "/absent.txt" {
				t.Fatal("failed eviction changed prior receipt", err)
			}
			for _, target := range []string{view, otherView} {
				if got := h.call("GET", target, "", "", "", 200); got.Body.String() != "old" {
					t.Fatal("failed invalidation changed cached bytes")
				}
			}
			if _, err := h.store.Database().Exec("DROP TRIGGER ignore_cache_delete"); err != nil {
				t.Fatal(err)
			}
			h.call("POST", cfDistributionPath+"/"+d.Id+"/invalidation", cfInvalidate("retry", path), "", "", 201)
			if got := h.call("GET", view, "", "", "", 200); got.Body.String() != "new" {
				t.Fatal("retry did not evict cache")
			}
			if got := h.call("GET", otherView, "", "", "", 200); got.Body.String() != "old" {
				t.Fatal("retry evicted peer distribution")
			}
		})
	}
}

func TestCloudFrontRejectedViewerRequestsNeverFillCache(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("local-origin", "saved")
	d, _ := h.create("first", "local-origin")
	view := cfDeliveryPath + d.Id + "/objects/asset.txt"
	for _, method := range []string{"GET", "HEAD"} {
		for _, header := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
			r := httptest.NewRequest(method, view, nil)
			r.Header.Set(header, "unsupported")
			w := httptest.NewRecorder()
			h.gateway.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatal("conditional request was not refused", method, header, w.Code)
			}
		}
		r := httptest.NewRequest(method, view, strings.NewReader("unexpected"))
		r.ContentLength = -1
		w := httptest.NewRecorder()
		h.gateway.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("unknown-length body was not refused", method, w.Code)
		}
	}
	var rows int
	if err := h.store.Database().QueryRow("SELECT count(*) FROM aws_cloudfront_cache").Scan(&rows); err != nil || rows != 0 {
		t.Fatal("rejected request filled cache", err)
	}
}
