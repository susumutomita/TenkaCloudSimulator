package simpledb

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// Walk every page, not just the first: a forward continuation used for a
// descending comparison can silently repeat a key or skip a boundary key.
func TestSelectLexicalRangeAcrossPages(t *testing.T) {
	s, _ := fixture(t)
	ctx := t.Context()
	if err := s.CreateDomain(ctx, "a", "r", "Sample"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"2", "02", "20", "10"} {
		if err := s.Put(ctx, "a", "r", "Sample", name, map[string][]string{"k": {"v"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { return true })
	for _, tc := range []struct {
		op   string
		want []string
	}{
		{"=", []string{"2"}}, {"<", []string{"10", "02"}}, {"<=", []string{"2", "10", "02"}},
		{">", []string{"20"}}, {">=", []string{"20", "2"}},
	} {
		t.Run(tc.op, func(t *testing.T) {
			q := "select itemName() from Sample where itemName() " + tc.op + " '2' order by itemName() desc limit 1"
			next := ""
			for i, want := range tc.want {
				var args []string
				if next != "" {
					args = []string{"NextToken", next}
				}
				status, page := selectCall(t, h, q, args...)
				if status != 200 || len(page.Items) != 1 || page.Items[0].Name.Text != want || len(page.Items[0].Attr) != 0 ||
					(page.Next != "") != (i+1 < len(tc.want)) || (page.Next != "" && page.Next == next) {
					t.Fatalf("page %d: want %q, got %d %+v", i, want, status, page)
				}
				next = page.Next
			}
		})
	}
}

func TestSelectExpressionByteBoundariesAndEmptyToken(t *testing.T) {
	s, _ := fixture(t)
	if err := s.CreateDomain(t.Context(), "a", "r", "Sample"); err != nil {
		t.Fatal(err)
	}
	checks := 0
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { checks++; return true })
	base := "select * from Sample"
	full := base + strings.Repeat(" ", 8192-len(base))
	value := strings.Repeat("界", 341) + "x" // 1,024 UTF-8 bytes, not 1,024 characters
	predicate := func(v string) string { return base + " where k = '" + v + "'" }
	for _, q := range []string{full, predicate(value)} {
		if status, x := selectCall(t, h, q); status != 200 || x.Code != "" {
			t.Fatalf("boundary rejected: %d %s", status, x.Code)
		}
	}
	before := checks
	for _, q := range []string{full + " ", predicate(value + "x"), predicate(string([]byte{0xff}))} {
		if st, x := selectCall(t, h, q); st != 400 || x.Code != "InvalidQueryExpression" || len(x.Items) != 0 || x.Next != "" {
			t.Fatalf("invalid input: %d %+v", st, x)
		}
	}
	if checks != before {
		t.Fatal("malformed expression reached authorizer")
	}
	if st, x := selectCall(t, h, base, "NextToken", ""); st != 400 || x.Code != "InvalidNextToken" || len(x.Items) != 0 || x.Next != "" {
		t.Fatalf("empty token became a first page: %d %+v", st, x)
	}
}

// Reading all attributes must not leak a partially decoded first row, while a
// name-only projection can still identify the bad record for repair.
func TestSelectStoredFailuresDoNotExposePartialResults(t *testing.T) {
	s, db := fixture(t)
	ctx := t.Context()
	if err := s.CreateDomain(ctx, "a", "r", "Sample"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-good", "b-invalid"} {
		if err := s.Put(ctx, "a", "r", "Sample", name, map[string][]string{"k": {"v"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { return true })
	all := "select * from Sample"
	for _, raw := range []string{"null", "{"} {
		if _, err := db.Exec("UPDATE aws_simpledb_items SET attributes=? WHERE name=?", raw, "b-invalid"); err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{all, "select itemName() from Sample where k = 'v'"} {
			if st, x := selectCall(t, h, q); st != 500 || x.Code != "InternalError" || len(x.Items) != 0 || x.Next != "" {
				t.Fatalf("corrupt %q: %d %+v", raw, st, x)
			}
		}
		if st, x := selectCall(t, h, "select itemName() from Sample"); st != 200 || len(x.Items) != 2 || x.Items[1].Name.Text != "b-invalid" || x.Next != "" {
			t.Fatalf("name-only lookup: %d %+v", st, x)
		}
		// A different read of the same snapshot must not report partial totals.
		if stats, err := s.DomainMetadata(ctx, "a", "r", "Sample"); err == nil || stats != (DomainStats{}) {
			t.Fatalf("corrupt %q produced stats %+v, error %v", raw, stats, err)
		}
	}
	if _, err := db.Exec("ALTER TABLE aws_simpledb_items RENAME TO items_under_repair"); err != nil {
		t.Fatal(err)
	}
	if st, x := selectCall(t, h, all); st != 500 || x.Code != "InternalError" || len(x.Items) != 0 || x.Next != "" {
		t.Fatalf("missing item store looked like an empty page: %d %+v", st, x)
	}
	if stats, err := s.DomainMetadata(ctx, "a", "r", "Sample"); err == nil || stats != (DomainStats{}) {
		t.Fatalf("missing item store produced stats %+v, error %v", stats, err)
	}
	// This time SQLite itself detects the invalid JSON while stepping past
	// the first, valid row. A cursor error is not a successful end of page.
	if _, err := db.Exec(`CREATE VIEW aws_simpledb_items AS
		SELECT account, region, domain, name, json(attributes) AS attributes FROM items_under_repair`); err != nil {
		t.Fatal(err)
	}
	if st, x := selectCall(t, h, all+" where itemName() = 'a-good'"); st != 200 || len(x.Items) != 1 || x.Items[0].Name.Text != "a-good" {
		t.Fatalf("first row should still be readable: %d %+v", st, x)
	}
	if st, x := selectCall(t, h, all); st != 500 || x.Code != "InternalError" || len(x.Items) != 0 || x.Next != "" {
		t.Fatalf("failed iterator returned a partial page: %d %+v", st, x)
	}
	if stats, err := s.DomainMetadata(ctx, "a", "r", "Sample"); err == nil || stats != (DomainStats{}) {
		t.Fatalf("failed iterator produced stats %+v, error %v", stats, err)
	}
}

// Hold the only connection rather than sleeping or relying on query speed.
// synctest advances the service's real timer without delaying the suite.
func TestSelectConnectionWaitHonorsQueryBudget(t *testing.T) {
	s, db := fixture(t)
	if err := s.CreateDomain(t.Context(), "a", "r", "Sample"); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	h := NewHTTPHandler(s, Scope{"a", "r"}, func(context.Context, string, string) bool { return true })
	const q = "select * from Sample"
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		status, x := selectCall(t, h, q)
		if status != http.StatusRequestTimeout || x.Code != "QueryTimeout" || len(x.Items) != 0 || x.Next != "" || time.Since(start) != 5*time.Second {
			t.Fatalf("expected five-second query timeout, got %v %d %+v", time.Since(start), status, x)
		}
	})
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if status, x := selectCall(t, h, q); status != 200 || x.Code != "" {
		t.Fatalf("retry after capacity returned: %d %+v", status, x)
	}
}
