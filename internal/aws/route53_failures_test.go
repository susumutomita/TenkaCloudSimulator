package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Compare all persisted rows, including receipts, so refusal cannot hide writes.
func r53PersistentState(t *testing.T, db *sql.DB) string {
	t.Helper()
	state := [][][]any{}
	for _, query := range []string{
		"SELECT id,name,reference,comment FROM aws_route53_zones ORDER BY id",
		"SELECT zone,name,type,document FROM aws_route53_records ORDER BY zone,name,type",
		"SELECT id,document FROM aws_route53_changes ORDER BY id",
	} {
		rows, err := db.Query(query)
		r53must(t, err)
		columns, err := rows.Columns()
		r53must(t, err)
		values := [][]any{}
		for rows.Next() {
			row := make([]any, len(columns))
			ptrs := make([]any, len(columns))
			for i := range row {
				ptrs[i] = &row[i]
			}
			r53must(t, rows.Scan(ptrs...))
			values = append(values, row)
		}
		r53must(t, rows.Err())
		r53must(t, rows.Close())
		state = append(state, values)
	}
	raw, err := json.Marshal(state)
	r53must(t, err)
	return string(raw)
}

type r53FailedBody struct{}

func (r53FailedBody) Read([]byte) (int, error) { return 0, errors.New("private transport detail") }
func (r53FailedBody) Close() error             { return nil }

func TestRoute53HTTPRefusalsPreserveAllRows(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	before := r53PersistentState(t, db.Database())
	prefix := "/2013-04-01/"
	for _, tc := range []struct {
		name, method, path, body, header, value, rawPath string
		status                                           int
	}{
		{name: "malformed XML", body: "<CreateHostedZoneRequest>", status: 400},
		{name: "deep XML", body: "<CreateHostedZoneRequest>" + strings.Repeat("<x>", 12) + strings.Repeat("</x>", 12) + "</CreateHostedZoneRequest>", status: 400},
		{name: "outer text", body: "unexpected" + r53createXML("new"), status: 400},
		{name: "empty XML", body: " ", status: 400},
		{name: "processing instruction", body: "<?custom data?>" + r53createXML("new"), status: 400},
		{name: "late declaration", body: r53createXML("new") + "<?xml version=\"1.0\"?>", status: 400},
		{name: "container text", body: strings.Replace(r53createXML("new"), "<Name>", "unexpected<Name>", 1), status: 400},
		{name: "typed boolean", body: `<CreateHostedZoneRequest><Name>example.test</Name><CallerReference>new</CallerReference><HostedZoneConfig><PrivateZone>maybe</PrivateZone></HostedZoneConfig></CreateHostedZoneRequest>`, status: 400},
		{name: "session token", body: r53createXML("new"), header: "X-Amz-Security-Token", value: "temporary", status: 400},
		{name: "wrong prefix", path: "/wrong/hostedzone", body: r53createXML("new"), status: 400},
		{name: "encoded path", body: r53createXML("new"), rawPath: "/2013-04-01/%68ostedzone", status: 400},
		{name: "unknown operation", path: prefix + "unsupported", method: "GET", status: 501},
		{name: "short id", path: prefix + "hostedzone/Z", method: "GET", status: 501},
		{name: "lowercase id", path: prefix + "hostedzone/Zlower", method: "GET", status: 501},
		{name: "wrong MIME", body: r53createXML("new"), header: "Content-Type", value: "application/json", status: 400},
		{name: "malformed MIME", body: r53createXML("new"), header: "Content-Type", value: "text/xml; broken", status: 400},
		{name: "oversize body", body: strings.Repeat("x", (1<<20)+1), status: 400},
		{name: "GET body", method: "GET", body: "unexpected", status: 400},
		{name: "reference replay", body: r53createXML("one"), status: 409},
		{name: "unknown zone", path: prefix + "hostedzone/ZUNKNOWN", method: "GET", status: 404},
		{name: "record page limit", path: prefix + "hostedzone/" + id + "/rrset?maxitems=0", method: "GET", status: 400},
		{name: "record type without name", path: prefix + "hostedzone/" + id + "/rrset?type=A", method: "GET", status: 400},
		{name: "record unsupported type", path: prefix + "hostedzone/" + id + "/rrset?name=example.test&type=CAA", method: "GET", status: 400},
		{name: "record invalid start", path: prefix + "hostedzone/" + id + "/rrset?name=bad_name.test", method: "GET", status: 400},
		{name: "missing record zone", path: prefix + "hostedzone/ZUNKNOWN/rrset", method: "GET", status: 404},
		{name: "invalid zone marker", path: prefix + "hostedzone?marker=bad", method: "GET", status: 400},
		{name: "answer missing zone", path: prefix + "testdnsanswer?recordname=example.test&recordtype=A", method: "GET", status: 400},
		{name: "answer unsupported type", path: prefix + "testdnsanswer?hostedzoneid=" + id + "&recordname=example.test&recordtype=CAA", method: "GET", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.method == "" {
				tc.method = "POST"
			}
			if tc.path == "" {
				tc.path = prefix + "hostedzone"
			}
			r := httptest.NewRequest(tc.method, "http://localhost"+tc.path, strings.NewReader(tc.body))
			r.URL.RawPath = tc.rawPath
			if tc.header != "" {
				r.Header.Set(tc.header, tc.value)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r, r53allow)
			expectedCode := map[int]string{400: "InvalidInput", 404: "NoSuchHostedZone", 409: "HostedZoneAlreadyExists", 501: "NotImplemented"}[tc.status]
			if w.Code != tc.status || !strings.Contains(w.Body.String(), "<ErrorResponse") || !strings.Contains(w.Body.String(), "<Code>"+expectedCode+"</Code>") {
				t.Fatalf("%d: %s", w.Code, w.Body.String())
			}
			if got := r53PersistentState(t, db.Database()); got != before {
				t.Fatal("refused request changed persisted rows")
			}
		})
	}
	r := httptest.NewRequest("POST", prefix+"hostedzone", nil)
	r.Body = r53FailedBody{}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, r53allow)
	if w.Code != 400 || strings.Contains(w.Body.String(), "private transport detail") {
		t.Fatal(w.Code, w.Body.String())
	}
	if r53PersistentState(t, db.Database()) != before {
		t.Fatal("failed body read mutated state")
	}
}

func TestRoute53ClosedStoreAndUnavailableIdentityFailClosed(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	g := &Gateway{Store: db, Route53: s, Identities: core.NewIdentities(db)}
	r := httptest.NewRequest("POST", "/2013-04-01/hostedzone", strings.NewReader(r53createXML("new")))
	r.Header.Set("X-Simulator-Principal", "missing")
	before := r53PersistentState(t, db.Database())
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 || r53PersistentState(t, db.Database()) != before {
		t.Fatal(w.Code, w.Body.String())
	}
	r53must(t, db.Close())
	if store, err := NewRoute53Store(db.Database()); err == nil || store != nil {
		t.Fatal("closed DB initialization accepted")
	}
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r.Clone(context.Background()))
	if w.Code != 500 || strings.Contains(w.Body.String(), "database is closed") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "hostedzone", r53createXML("new")}, {"GET", "hostedzone/" + id, ""},
		{"GET", "hostedzone", ""}, {"GET", "hostedzone/" + id + "/rrset", ""},
		{"DELETE", "hostedzone/" + id, ""},
	} {
		w := r53call(s, tc.method, tc.path, tc.body, r53allow)
		if w.Code != 500 || !strings.Contains(w.Body.String(), "InternalFailure") || strings.Contains(w.Body.String(), "database is closed") {
			t.Fatal(tc.path, w.Code, w.Body.String())
		}
	}
	if _, err := s.change(context.Background(), id, "", []Route53Change{{"CREATE", r53record("x.example.test", "A", "192.0.2.1")}}); err == nil {
		t.Fatal("closed DB mutation accepted")
	}
}

func TestRoute53StorageWriteFaultsRollBackAllRows(t *testing.T) {
	for _, tc := range []struct{ name, trigger, operation string }{
		{"zone insertion", `CREATE TRIGGER fault BEFORE INSERT ON aws_route53_zones BEGIN SELECT RAISE(IGNORE); END`, "create"},
		{"default insertion", `CREATE TRIGGER fault BEFORE INSERT ON aws_route53_records BEGIN SELECT RAISE(ABORT,'write fault'); END`, "create"},
		{"record deletion", `CREATE TRIGGER fault BEFORE DELETE ON aws_route53_records BEGIN SELECT RAISE(ABORT,'write fault'); END`, "change"},
		{"ignored deletion", `CREATE TRIGGER fault BEFORE DELETE ON aws_route53_records BEGIN SELECT RAISE(IGNORE); END`, "change"},
		{"record rewrite", `CREATE TRIGGER fault BEFORE INSERT ON aws_route53_records BEGIN SELECT RAISE(IGNORE); END`, "change"},
		{"zone deletion", `CREATE TRIGGER fault BEFORE DELETE ON aws_route53_zones BEGIN SELECT RAISE(IGNORE); END`, "delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, s := r53test(t)
			id := r53create(t, s)
			before := r53PersistentState(t, db.Database())
			_, err := db.Database().Exec(tc.trigger)
			r53must(t, err)
			switch tc.operation {
			case "create":
				_, _, err = s.create(context.Background(), "other.test", "new", "")
			case "change":
				_, err = s.change(context.Background(), id, "", []Route53Change{{"CREATE", r53record("x.example.test", "A", "192.0.2.1")}})
			case "delete":
				_, err = s.remove(context.Background(), id)
			}
			if err == nil {
				t.Fatal("storage fault acknowledged as success")
			}
			if got := r53PersistentState(t, db.Database()); got != before {
				t.Fatal("storage fault changed persisted rows")
			}
		})
	}
}

var _ io.ReadCloser = r53FailedBody{}
