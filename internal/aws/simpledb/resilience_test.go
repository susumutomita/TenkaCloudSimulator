package simpledb

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestStorageValidationPreservesCommittedValues(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	const a, r, d, i = "account", "region", "domain", "item"
	if err := s.CreateDomain(ctx, a, r, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, a, r, d, i, map[string][]string{"color": {"red"}}, nil); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() error{
		"domain create scope": func() error { return s.CreateDomain(ctx, "", r, d) },
		"domain delete name":  func() error { return s.DeleteDomain(ctx, a, r, "x") },
		"item scope":          func() error { return s.Put(ctx, a, r, d, "", map[string][]string{"color": {"blue"}}, nil) },
		"empty mutation":      func() error { return s.Put(ctx, a, r, d, i, nil, nil) },
		"empty values":        func() error { return s.Put(ctx, a, r, d, i, map[string][]string{"color": nil}, nil) },
		"invalid value": func() error {
			return s.Put(ctx, a, r, d, i, map[string][]string{"color": {strings.Repeat("x", 1025)}}, nil)
		},
		"replace absent attribute": func() error {
			return s.Put(ctx, a, r, d, i, map[string][]string{"color": {"blue"}}, map[string]bool{"other": true})
		},
		"read item scope": func() error { _, err := s.Get(ctx, a, r, d, "", nil); return err },
		"empty read name": func() error { _, err := s.Get(ctx, a, r, d, i, []string{""}); return err },
		"list scope":      func() error { _, _, err := s.ListDomains(ctx, a, "", 1, ""); return err },
		"list maximum":    func() error { _, _, err := s.ListDomains(ctx, a, r, 101, ""); return err },
	}
	for label, call := range cases {
		t.Run(label, func(t *testing.T) {
			if err := call(); err != storeError("InvalidParameterValue") {
				t.Fatalf("got %v, want InvalidParameterValue", err)
			}
		})
	}
	got, err := s.Get(ctx, a, r, d, i, nil)
	if err != nil || len(got) != 1 || strings.Join(got["color"], ",") != "red" {
		t.Fatalf("value changed: %v, %v", got, err)
	}
}

func TestStorageCanceledOperationsDoNotChangeState(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.CreateDomain(ctx, "a", "r", "original"); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() error{
		"create": func() error { return s.CreateDomain(cancelled, "a", "r", "other") },
		"put": func() error {
			return s.Put(cancelled, "a", "r", "original", "item", map[string][]string{"k": {"v"}}, nil)
		},
		"get":  func() error { _, err := s.Get(cancelled, "a", "r", "original", "item", nil); return err },
		"list": func() error { _, _, err := s.ListDomains(cancelled, "a", "r", 1, ""); return err },
	}
	for label, call := range cases {
		t.Run(label, func(t *testing.T) {
			if err := call(); err != storeError("Cancelled") {
				t.Fatalf("got %v, want Cancelled", err)
			}
		})
	}
	got, _, err := s.ListDomains(ctx, "a", "r", 0, "")
	if err != nil || strings.Join(got, ",") != "original" {
		t.Fatalf("unexpected committed domains %v, %v", got, err)
	}
	attrs, err := s.Get(ctx, "a", "r", "original", "item", nil)
	if err != nil || len(attrs) != 0 {
		t.Fatalf("canceled write produced attributes %v, %v", attrs, err)
	}
}

func TestClosedCallerDBRefusesWork(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := NewStore(db); err == nil || reopened != nil {
		t.Fatalf("closed DB accepted: %v %v", reopened, err)
	}
	if err := s.CreateDomain(ctx, "a", "r", "domain"); err == nil {
		t.Fatal("write accepted after close")
	}
	if got, err := s.Get(ctx, "a", "r", "domain", "item", nil); err == nil || got != nil {
		t.Fatalf("read accepted after close: %v %v", got, err)
	}
	if got, next, err := s.ListDomains(ctx, "a", "r", 0, ""); err == nil || got != nil || next != "" {
		t.Fatalf("list accepted after close: %v %q %v", got, next, err)
	}
}

func TestInvalidStoredAttributesAreNotSilentlyOverwritten(t *testing.T) {
	for _, invalid := range []string{"{", "null"} {
		t.Run(invalid, func(t *testing.T) {
			s, db := fixture(t)
			ctx := context.Background()
			if err := s.CreateDomain(ctx, "a", "r", "domain"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO aws_simpledb_items VALUES (?,?,?,?,?)", "a", "r", "domain", "item", invalid); err != nil {
				t.Fatal(err)
			}
			if got, err := s.Get(ctx, "a", "r", "domain", "item", nil); err == nil || got != nil {
				t.Fatalf("corruption hidden: %v %v", got, err)
			}
			if err := s.Put(ctx, "a", "r", "domain", "item", map[string][]string{"k": {"v"}}, nil); err == nil {
				t.Fatal("overwrote corrupt attributes")
			}
			var after string
			if err := db.QueryRow("SELECT attributes FROM aws_simpledb_items").Scan(&after); err != nil || after != invalid {
				t.Fatalf("prior state changed: %q %v", after, err)
			}
		})
	}
}

func assertQueryError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var result queryResponse
	err := xml.Unmarshal(w.Body.Bytes(), &result)
	if err != nil || w.Code != status || result.XMLName.Local != "Response" ||
		result.Errors == nil || result.Errors.Error.Code != code || result.Get != nil || result.List != nil {
		t.Fatalf("want %d %s error envelope, got %d %q (%v)", status, code, w.Code, w.Body.String(), err)
	}
}

func TestHTTPQueryFailureEnvelopes(t *testing.T) {
	s, db := fixture(t)
	scope := Scope{Account: "account", Region: "region"}
	h := NewHTTPHandler(s, scope, func(context.Context, string, string) bool { return true })
	t.Run("missing domain", func(t *testing.T) {
		assertQueryError(t, callHTTP(h, params("GetAttributes", map[string]string{"DomainName": "missing", "ItemName": "item"})), 400, "NoSuchDomain")
	})
	t.Run("body exceeds limit", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 1<<20+1)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		assertQueryError(t, w, 400, "InvalidParameterValue")
	})
	t.Run("bad store configuration", func(t *testing.T) {
		assertQueryError(t, callHTTP(NewHTTPHandler(s, Scope{}, func(context.Context, string, string) bool { return true }), params("ListDomains", nil)), 403, "AccessDenied")
	})
	t.Run("closed database", func(t *testing.T) {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		assertQueryError(t, callHTTP(h, params("ListDomains", nil)), 500, "InternalError")
	})
}

func TestStorageCancellationClassification(t *testing.T) {
	ctx := context.Background()
	if got := failure(ctx, context.DeadlineExceeded); got != storeError("Cancelled") {
		t.Fatalf("got %v", got)
	}
	original := errors.New("caller SQLite unavailable")
	if got := failure(ctx, original); got != original {
		t.Fatalf("database failure changed to %v", got)
	}
}

func TestHTTPRejectedFormsNeverReachAuthorizer(t *testing.T) {
	s, _ := fixture(t)
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool {
		t.Error("invalid query reached authorization")
		return false
	})
	for _, tc := range []struct{ name, method, path, raw, code string }{
		{"wrong method", http.MethodGet, "/", "", "InvalidParameterValue"},
		{"form encoding", http.MethodPost, "/", "Action=%ZZ", "InvalidParameterValue"},
		{"missing version", http.MethodPost, "/", "Action=ListDomains", "MissingParameter"},
		{"unsupported operation", http.MethodPost, "/", params("Unknown", nil).Encode(), "InvalidAction"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.raw))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assertQueryError(t, w, 400, tc.code)
		})
	}
	for _, tc := range []struct {
		name string
		form url.Values
		code string
	}{
		{"empty item", params("GetAttributes", map[string]string{"DomainName": "domain", "ItemName": ""}), "InvalidParameterValue"},
		{"empty attribute filter", params("GetAttributes", map[string]string{"DomainName": "domain", "ItemName": "item", "AttributeName.1": ""}), "InvalidParameterValue"},
		{"missing attributes", params("PutAttributes", map[string]string{"DomainName": "domain", "ItemName": "item"}), "MissingParameter"},
		{"invalid replace option", params("PutAttributes", map[string]string{"DomainName": "domain", "ItemName": "item", "Attribute.1.Name": "color", "Attribute.1.Value": "red", "Attribute.1.Replace": "maybe"}), "InvalidParameterValue"},
		{"conflicting replace options", params("PutAttributes", map[string]string{"DomainName": "domain", "ItemName": "item", "Attribute.1.Name": "color", "Attribute.1.Value": "red", "Attribute.1.Replace": "true", "Attribute.2.Name": "color", "Attribute.2.Value": "blue", "Attribute.2.Replace": "false"}), "InvalidParameterValue"},
	} {
		t.Run(tc.name, func(t *testing.T) { assertQueryError(t, callHTTP(h, tc.form), 400, tc.code) })
	}
}

func TestCallerDatabaseRejectedWritesRollBack(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	const a, r, d, i = "a", "r", "domain", "item"
	if err := s.Put(ctx, a, r, d, i, map[string][]string{"color": {"red"}}, nil); err != storeError("NoSuchDomain") {
		t.Fatalf("missing domain: %v", err)
	}
	for _, tc := range []struct {
		name, table, operation string
		call                   func() error
	}{
		{"create domain", "domains", "INSERT", func() error { return s.CreateDomain(ctx, a, r, "new") }},
		{"put item", "items", "UPDATE", func() error { return s.Put(ctx, a, r, d, i, map[string][]string{"color": {"blue"}}, nil) }},
		{"delete item", "items", "DELETE", func() error { return s.DeleteDomain(ctx, a, r, d) }},
		{"delete domain", "domains", "DELETE", func() error { return s.DeleteDomain(ctx, a, r, d) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.CreateDomain(ctx, a, r, d); err != nil {
				t.Fatal(err)
			}
			if err := s.Put(ctx, a, r, d, i, map[string][]string{"color": {"red"}}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TRIGGER refuse BEFORE " + tc.operation + " ON aws_simpledb_" + tc.table + " BEGIN SELECT RAISE(ABORT, 'test refusal'); END"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := db.Exec("DROP TRIGGER refuse"); err != nil {
					t.Error(err)
				}
			}()
			if err := tc.call(); err == nil {
				t.Fatal("rejected write reported success")
			}
			got, err := s.Get(ctx, a, r, d, i, nil)
			if err != nil || strings.Join(got["color"], ",") != "red" {
				t.Fatalf("write changed state: %v %v", got, err)
			}
			ds, _, err := s.ListDomains(ctx, a, r, 0, "")
			if err != nil || strings.Join(ds, ",") != d {
				t.Fatalf("unexpected committed domains: %v %v", ds, err)
			}
		})
	}
}

func TestCursorRejectsInvalidEncodingAndPriorInstance(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	for _, name := range []string{"aaa", "bbb"} {
		if err := s.CreateDomain(ctx, "a", "r", name); err != nil {
			t.Fatal(err)
		}
	}
	_, next, err := s.ListDomains(ctx, "a", "r", 1, "")
	if err != nil || next == "" {
		t.Fatalf("no first cursor: %v", err)
	}
	payload, signature, ok := strings.Cut(next, ".")
	if !ok {
		t.Fatal("missing issued cursor envelope")
	}
	for _, token := range []string{strings.Repeat("a", 8193), "!." + signature, payload + ".!", payload + ".YQ"} {
		got, after, err := s.ListDomains(ctx, "a", "r", 1, token)
		if err != storeError("InvalidNextToken") || len(got) != 0 || after != "" {
			t.Fatalf("invalid token produced a page: %v %q %v", got, after, err)
		}
	}
	reopened, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if got, after, err := reopened.ListDomains(ctx, "a", "r", 1, next); err != storeError("InvalidNextToken") || len(got) != 0 || after != "" {
		t.Fatalf("reopened store accepted stale cursor: %v %q %v", got, after, err)
	}
	if got, _, err := reopened.ListDomains(ctx, "a", "r", 0, ""); err != nil || strings.Join(got, ",") != "aaa,bbb" {
		t.Fatalf("reopen lost rows: %v %v", got, err)
	}
}
