package aws

import (
	"encoding/xml"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cfNamespacedInvalidation(t *testing.T, body string, want cfInvalidation) cfInvalidation {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Space != cloudFrontNS {
			t.Fatalf("native invalidation element %s reset the official namespace", start.Name.Local)
		}
	}
	var value cfInvalidation
	if err := xml.Unmarshal([]byte(body), &value); err != nil {
		t.Fatal(err)
	}
	if !cfInvalidationID.MatchString(value.Id) || value.Status != "Completed" || value.CreateTime == "" ||
		value.InvalidationBatch.CallerReference != "wire-original" || value.InvalidationBatch.Paths.Quantity != 2 ||
		!reflect.DeepEqual(value.InvalidationBatch.Paths.Items, []string{"/asset.txt", "/*"}) {
		t.Fatal("native invalidation changed its concrete identity, caller reference or paths")
	}
	if want.Id != "" && !reflect.DeepEqual(value, want) {
		t.Fatal("read or idempotent replay changed the stored invalidation")
	}
	return value
}

func TestCloudFrontInvalidationStoredWireNamespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "native.sqlite")
	h := cfDistHarnessFor(t, path)
	h.seed("wire-origin", "saved bytes")
	distribution, _ := h.create("wire-distribution", "wire-origin")
	route := cfDistributionPath + "/" + distribution.Id + "/invalidation"
	batch := strings.Replace(cfInvalidate("wire-original", "/asset.txt"), "<Quantity>1</Quantity>", "<Quantity>2</Quantity>", 1)
	batch = strings.Replace(batch, "</Items>", "<Path>/*</Path></Items>", 1)
	created := cfNamespacedInvalidation(t, h.call("POST", route, batch, "", "", 201).Body.String(), cfInvalidation{})
	cfNamespacedInvalidation(t, h.call("GET", route+"/"+created.Id, "", "", "", 200).Body.String(), created)
	cfNamespacedInvalidation(t, h.call("POST", route, batch, "", "", 201).Body.String(), created)
	var before string
	if err := h.store.Database().QueryRowContext(t.Context(), "SELECT document FROM aws_cloudfront_invalidations WHERE distribution=? AND id=?", distribution.Id, created.Id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := cfDistHarnessFor(t, path)
	cfNamespacedInvalidation(t, reopened.call("GET", route+"/"+created.Id, "", "", "", 200).Body.String(), created)
	cfNamespacedInvalidation(t, reopened.call("POST", route, batch, "", "", 201).Body.String(), created)
	reopened.call("POST", route, cfInvalidate("wire-original", "/different.txt"), "", "", 400)
	var count int
	var after string
	if err := reopened.store.Database().QueryRowContext(t.Context(), "SELECT count(*) FROM aws_cloudfront_invalidations WHERE distribution=?", distribution.Id).Scan(&count); err != nil || count != 1 {
		t.Fatal("replay or conflict changed receipt cardinality", count, err)
	}
	if err := reopened.store.Database().QueryRowContext(t.Context(), "SELECT document FROM aws_cloudfront_invalidations WHERE distribution=? AND id=?", distribution.Id, created.Id).Scan(&after); err != nil || after != before {
		t.Fatal("read/replay/conflict changed the persisted payload", err)
	}
}
