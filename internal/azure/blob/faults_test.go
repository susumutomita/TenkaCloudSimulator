package blob

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func sqlCount(t *testing.T, s *Service, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func execSQL(t *testing.T, s *Service, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func TestAzureBlobSQLFailuresAndIgnoredWritesNeverAck(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	tag := put(t, s, "box", "old", "prior", map[string]string{"x-ms-meta-label": "prior"}, 201).Header().Get("ETag")
	execSQL(t, s, "CREATE TRIGGER blob_write_fault BEFORE UPDATE ON azure_blob_objects BEGIN SELECT RAISE(ABORT,'private-write-fault'); END")
	out := put(t, s, "box", "old", "new", nil, 500)
	if strings.Contains(out.Body.String(), "private-write-fault") {
		t.Fatal("database diagnostic leaked")
	}
	execSQL(t, s, "DROP TRIGGER blob_write_fault")
	get := request(t, s, "GET", "/accountone/box/old", nil, nil, learner(), 200)
	if get.Body.String() != "prior" || get.Header().Get("ETag") != tag || get.Header().Get("x-ms-meta-label") != "prior" {
		t.Fatal("failed overwrite changed saved object")
	}
	execSQL(t, s, "CREATE TRIGGER blob_ignore BEFORE INSERT ON azure_blob_objects BEGIN SELECT RAISE(IGNORE); END")
	put(t, s, "box", "phantom", "not saved", nil, 500)
	if sqlCount(t, s, "azure_blob_objects") != 1 {
		t.Fatal("ignored insert acknowledged")
	}
	execSQL(t, s, "DROP TRIGGER blob_ignore")
	put(t, s, "box", "second", "second", nil, 201)
	execSQL(t, s, "CREATE TRIGGER blob_delete_fault BEFORE DELETE ON azure_blob_objects WHEN old.name='second' BEGIN SELECT RAISE(ABORT,'private-delete-fault'); END")
	request(t, s, "DELETE", "/accountone/box?restype=container", nil, nil, learner(), 500)
	if sqlCount(t, s, "azure_blob_objects") != 2 || sqlCount(t, s, "azure_blob_containers") != 1 {
		t.Fatal("partial container cascade committed")
	}
	execSQL(t, s, "DROP TRIGGER blob_delete_fault")
	execSQL(t, s, "CREATE TRIGGER blob_delete_ignore BEFORE DELETE ON azure_blob_objects BEGIN SELECT RAISE(IGNORE); END")
	request(t, s, "DELETE", "/accountone/box/old", nil, nil, learner(), 500)
	if sqlCount(t, s, "azure_blob_objects") != 2 {
		t.Fatal("ignored delete changed graph")
	}
	execSQL(t, s, "DROP TRIGGER blob_delete_ignore")
	request(t, s, "DELETE", "/accountone/box?restype=container", nil, nil, learner(), 202)
	if sqlCount(t, s, "azure_blob_objects") != 0 {
		t.Fatal("Azure delete did not cascade")
	}
}
func TestAzureBlobRequestAndPersistentCorruptionBoundaries(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	for _, suffix := range []string{"?comp=block", "?snapshot=x", "?versionid=1", "?sig=not-a-sas", "?restype=blob", "?timeout=0"} {
		request(t, s, "GET", "/accountone/box/key"+suffix, nil, nil, learner(), map[bool]int{true: 501, false: 400}[suffix == "?comp=block" || suffix == "?restype=blob"])
	}
	request(t, s, "GET", "/accountone/box?restype=container&comp=list&prefix=%00", nil, nil, learner(), 400)
	for _, headers := range []map[string]string{{"x-ms-lease-id": "unimplemented"}, {"x-ms-range-get-content-crc64": "true"}, {"If-Match": "W/\"weak\""}, {"If-Match": `"one"`, "If-None-Match": "*"}, {"x-ms-version": "wrong"}} {
		request(t, s, "GET", "/accountone/box/key", nil, headers, learner(), 400)
	}
	put(t, s, "box", "body-limit", strings.Repeat("x", MaxBlobBytes+1), nil, 413)
	put(t, s, "box", "invalid-metadata", "x", map[string]string{"x-ms-meta-bad-name": "value"}, 400)
	put(t, s, "box", "bad-hash", "x", map[string]string{"Content-MD5": "wrong"}, 400)
	put(t, s, "box", "empty", "", nil, 201)
	if w := request(t, s, "GET", "/accountone/box/empty", nil, nil, learner(), 200); w.Body.Len() != 0 {
		t.Fatal("empty blob changed")
	}
	put(t, s, "box", "healthy", "saved", nil, 201)
	execSQL(t, s, "UPDATE azure_blob_objects SET metadata='not-json' WHERE name='healthy'")
	request(t, s, "GET", "/accountone/box/healthy", nil, nil, learner(), 500)
	request(t, s, "GET", "/accountone/box?restype=container&comp=list", nil, nil, learner(), 500)
	if sqlCount(t, s, "azure_blob_objects") != 2 {
		t.Fatal("corrupt state silently dropped")
	}
	execSQL(t, s, "UPDATE azure_blob_objects SET metadata='{}' WHERE name='healthy'")
	execSQL(t, s, "UPDATE azure_blob_accounts SET owner='foreign-owner'")
	request(t, s, "GET", "/accountone/box/healthy", nil, nil, learner(), 500)
}
func TestAzureBlobPhysicalQuotaLastSlotAndLegacyCleanup(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "seed", "x", nil, 201)
	execSQL(t, s, `WITH RECURSIVE seed(n) AS(SELECT 1 UNION ALL SELECT n+1 FROM seed WHERE n<9998)
INSERT INTO azure_blob_objects SELECT environment,account,container,printf('seed-%05d',n),body,etag,modified,metadata,content_type,md5 FROM seed CROSS JOIN azure_blob_objects WHERE name='seed'`)
	start := make(chan struct{})
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("PUT", fmt.Sprintf("/accountone/box/last-%d", i), strings.NewReader("x"))
			r.Header.Set("x-ms-version", APIVersion)
			r.Header.Set("x-ms-blob-type", "BlockBlob")
			r = r.WithContext(WithActor(r.Context(), *learner()))
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			codes <- w.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(codes)
	results := map[int]int{}
	for c := range codes {
		results[c]++
	}
	if results[201] != 1 || results[409] != 1 || sqlCount(t, s, "azure_blob_objects") != MaxBlobs {
		t.Fatal(results)
	}
	// Legitimate old rows above the current quota survive startup/read and can be
	// explicitly deleted; constructor must not discard/adopt them to get under cap.
	execSQL(t, s, `INSERT INTO azure_blob_objects SELECT environment,account,container,'legacy-extra',body,etag,modified,metadata,content_type,md5 FROM azure_blob_objects WHERE name='seed'`)
	reopened, err := New(s.db, s.config, permitted)
	if err != nil {
		t.Fatal(err)
	}
	request(t, reopened, "GET", "/accountone/box/legacy-extra", nil, nil, learner(), 200)
	put(t, reopened, "box", "over-quota", "x", nil, 409)
	request(t, reopened, "DELETE", "/accountone/box?restype=container", nil, nil, learner(), 202)
	if sqlCount(t, s, "azure_blob_objects") != 0 {
		t.Fatal("legacy cleanup blocked")
	}
}
func TestAzureBlobPhysicalByteBudgetCountsPeerAccountsAndRollback(t *testing.T) {
	db, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	peer, err := New(db.Database(), config("accounttwo", "owner"), permitted)
	if err != nil {
		t.Fatal(err)
	}
	create(t, peer, "box")
	put(t, peer, "box", "seed", "x", nil, 201)
	// Eight individually legal physical blobs nearly fill the actual 64 MiB
	// retained payload budget. No production limit or test driver is overridden.
	payload := strings.Repeat("x", MaxBlobBytes-128)
	sum := md5.Sum([]byte(payload))
	execSQL(t, s, "UPDATE azure_blob_objects SET body=?,md5=? WHERE account='accounttwo'", []byte(payload), base64.StdEncoding.EncodeToString(sum[:]))
	execSQL(t, s, `WITH RECURSIVE seed(n) AS(SELECT 1 UNION ALL SELECT n+1 FROM seed WHERE n<7)
INSERT INTO azure_blob_objects SELECT environment,account,container,printf('large-%d',n),body,etag,modified,metadata,content_type,md5 FROM seed CROSS JOIN azure_blob_objects WHERE name='seed' AND account='accounttwo'`)
	var used int64
	if err = s.db.QueryRow(`SELECT (SELECT SUM(length(body)+length(CAST(metadata AS BLOB))+length(CAST(name AS BLOB))+length(CAST(content_type AS BLOB))) FROM azure_blob_objects)+(SELECT SUM(length(CAST(name AS BLOB))+length(CAST(metadata AS BLOB))) FROM azure_blob_containers)`).Scan(&used); err != nil {
		t.Fatal(err)
	}
	remaining := MaxStoredBytes - used
	if remaining <= 100 || remaining > 4096 {
		t.Fatal("unexpected fixture", remaining)
	}
	// New value contains multibyte UTF-8 plus serialized metadata overhead; the
	// body is rejected if its actual stored representation would exceed cap.
	put(t, s, "box", "too-large", strings.Repeat("é", int(remaining)), map[string]string{"x-ms-meta-label": "budget"}, 409)
	if sqlCount(t, s, "azure_blob_objects") != 8 {
		t.Fatal("failed byte admission changed peer rows")
	}
	put(t, s, "box", "fits", "x", nil, 201)
	execSQL(t, s, "CREATE TRIGGER azure_budget_fault BEFORE INSERT ON azure_blob_objects BEGIN SELECT RAISE(ABORT,'private-budget-fault'); END")
	put(t, s, "box", "failed", "x", nil, 500)
	if sqlCount(t, s, "azure_blob_objects") != 9 {
		t.Fatal("failed SQL insert consumed budget")
	}
	execSQL(t, s, "DROP TRIGGER azure_budget_fault")
}
func TestAzureBlobCursorCannotConferAuthority(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "one", "one", nil, 201)
	put(t, s, "box", "two", "two", nil, 201)
	w := request(t, s, "GET", "/accountone/box?restype=container&comp=list&maxresults=1", nil, nil, learner(), 200)
	var p struct {
		Next string `xml:"NextMarker"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Next == "" {
		t.Fatal(err, p)
	}
	request(t, s, "GET", "/accountone/box?restype=container&comp=list&maxresults=1&marker="+url.QueryEscape(p.Next), nil, nil, nil, 403)
	// Correctly signed cursors still fail if account ownership/secret drifts.
	execSQL(t, s, "UPDATE azure_blob_accounts SET cursor_key=x'00'")
	request(t, s, "GET", "/accountone/box?restype=container&comp=list&marker="+url.QueryEscape(p.Next), nil, nil, learner(), 500)
}

func TestAzureBlobConstructorDoesNotAdoptOrphanedContainer(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	execSQL(t, s, "PRAGMA foreign_keys=OFF")
	execSQL(t, s, "DELETE FROM azure_blob_accounts")
	execSQL(t, s, "PRAGMA foreign_keys=ON")
	if _, err := New(s.db, s.config, permitted); err == nil {
		t.Fatal("orphaned container adopted by new synthetic owner")
	}
	if sqlCount(t, s, "azure_blob_accounts") != 0 || sqlCount(t, s, "azure_blob_containers") != 1 {
		t.Fatal("constructor changed orphan evidence")
	}
}
