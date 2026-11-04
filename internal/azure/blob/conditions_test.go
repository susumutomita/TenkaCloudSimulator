package blob

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type savedBlobRow struct {
	Environment, Account, Container, Name      string
	Body                                       []byte
	ETag, Modified, Metadata, ContentType, MD5 string
}

func persistedRows(t *testing.T, s *Service) []savedBlobRow {
	t.Helper()
	rows, err := s.db.Query("SELECT environment,account,container,name,body,etag,modified,metadata,content_type,md5 FROM azure_blob_objects ORDER BY environment,account,container,name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []savedBlobRow{}
	for rows.Next() {
		var row savedBlobRow
		if err = rows.Scan(&row.Environment, &row.Account, &row.Container, &row.Name, &row.Body, &row.ETag, &row.Modified, &row.Metadata, &row.ContentType, &row.MD5); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
func TestAzureBlobReadAndDeleteConditionsDoNotBypassAuthorizationOrChangeState(t *testing.T) {
	denied := false
	auth := func(ctx context.Context, tx *sql.Tx, a Authorization) (bool, error) {
		return !denied && a.Actor.Subject == "learner" && a.Resource.Owner == "owner", nil
	}
	_, s := fixture(t, ":memory:", config("accountone", "owner"), auth)
	create(t, s, "box")
	create(t, s, "peer")
	created := put(t, s, "box", "conditional", "private-body-must-not-leak", map[string]string{"x-ms-meta-label": "retained"}, 201)
	put(t, s, "peer", "conditional", "unrelated-peer", nil, 201)
	tag := created.Header().Get("ETag")
	before := persistedRows(t, s)
	// Wrong DELETE If-Match must preserve all stored fields and all peer rows,
	// rather than merely return412 while deleting/corrupting the target anyway.
	request(t, s, "DELETE", "/accountone/box/conditional", nil, map[string]string{"If-Match": `"wrong"`}, learner(), 412)
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("failed conditional delete changed persisted state")
	}
	get := request(t, s, "GET", "/accountone/box/conditional", nil, nil, learner(), 200)
	if get.Body.String() != "private-body-must-not-leak" || get.Header().Get("ETag") != tag || get.Header().Get("x-ms-meta-label") != "retained" {
		t.Fatal("failed DELETE changed bytes/identity/metadata")
	}
	notModified := request(t, s, "GET", "/accountone/box/conditional", nil, map[string]string{"If-None-Match": tag}, learner(), 304)
	if notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != tag {
		t.Fatal("304 must have no body and retain the validated ETag")
	}
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("conditional read mutated persisted state")
	}
	denied = true
	rejected := request(t, s, "GET", "/accountone/box/conditional", nil, map[string]string{"If-None-Match": tag}, learner(), 403)
	missing := request(t, s, "GET", "/accountone/box/missing", nil, map[string]string{"If-None-Match": tag}, learner(), 403)
	for _, w := range []*httptest.ResponseRecorder{rejected, missing} {
		if w.Header().Get("ETag") != "" || w.Header().Get("Content-MD5") != "" || w.Header().Get("Last-Modified") != "" || strings.Contains(w.Body.String(), "private-body-must-not-leak") {
			t.Fatal("denied conditional request leaked object state", w.Header())
		}
	}
	if rejected.Body.String() != missing.Body.String() {
		t.Fatal("authorization refusal disclosed object existence")
	}
	request(t, s, "DELETE", "/accountone/box/conditional", nil, map[string]string{"If-Match": tag}, learner(), 403)
	if !reflect.DeepEqual(before, persistedRows(t, s)) {
		t.Fatal("denied read/delete changed persisted state")
	}
	denied = false
	request(t, s, "DELETE", "/accountone/box/conditional", nil, map[string]string{"If-Match": tag}, learner(), 202)
	peer := request(t, s, "GET", "/accountone/peer/conditional", nil, nil, learner(), 200)
	if peer.Body.String() != "unrelated-peer" || len(persistedRows(t, s)) != 1 {
		t.Fatal("successful target delete changed peer")
	}
}
