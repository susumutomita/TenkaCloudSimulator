package aws

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func r53test(t *testing.T) (*core.Store, *Route53Store) {
	t.Helper()
	db, e := core.New(filepath.Join(t.TempDir(), "route53.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	s, e := NewRoute53Store(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	return db, s
}
func r53must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func r53code(e error) string {
	var f route53Error
	if errors.As(e, &f) {
		return f.code
	}
	return ""
}
func r53call(s *Route53Store, method, path, body string, auth func(string, string) error) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost/2013-04-01/"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, auth)
	return w
}
func r53allow(string, string) error { return nil }
func r53createXML(ref string) string {
	return `<CreateHostedZoneRequest xmlns="` + r53namespace + `"><Name>Example.TEST</Name><CallerReference>` + ref + `</CallerReference></CreateHostedZoneRequest>`
}
func r53create(t *testing.T, s *Route53Store) string {
	t.Helper()
	z, _, e := s.create(context.Background(), "example.test", "one", "")
	r53must(t, e)
	return strings.TrimPrefix(z.ID, "/hostedzone/")
}
func r53record(name, kind, value string) Route53Record {
	return Route53Record{name, kind, r53ttl(60), []string{value}}
}
func TestRoute53NativeLifecycleAndIAM(t *testing.T) {
	_, s := r53test(t)
	calls := []string{}
	auth := func(a, r string) error { calls = append(calls, a+" "+r); return nil }
	w := r53call(s, "POST", "hostedzone", r53createXML("ref"), auth)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		Zone   Route53Zone    `xml:"HostedZone"`
		Change Route53Receipt `xml:"ChangeInfo"`
	}
	r53must(t, xml.Unmarshal(w.Body.Bytes(), &created))
	if created.Zone.Count != 2 || created.Zone.Name != "example.test." || len(calls) != 2 || calls[1] != "ec2:DescribeVpcs *" {
		t.Fatalf("%+v %v", created, calls)
	}
	id := strings.TrimPrefix(created.Zone.ID, "/hostedzone/")
	for _, test := range []struct{ method, path, body, action, resource string }{
		{"GET", "hostedzone/" + id, "", "GetHostedZone", "hostedzone/" + id},
		{"GET", "hostedzone/" + id + "/rrset", "", "ListResourceRecordSets", "hostedzone/" + id},
		{"GET", strings.TrimPrefix(created.Change.ID, "/"), "", "GetChange", strings.TrimPrefix(created.Change.ID, "/")},
		{"GET", "hostedzone", "", "ListHostedZones", "*"},
		{"GET", "testdnsanswer?hostedzoneid=" + id + "&recordname=example.test&recordtype=SOA", "", "TestDNSAnswer", "*"},
	} {
		calls = nil
		w = r53call(s, test.method, test.path, test.body, auth)
		if w.Code != 200 {
			t.Fatal(test.path, w.Code, w.Body.String())
		}
		resource := test.resource
		if resource != "*" {
			resource = "arn:aws:route53:::" + resource
		}
		if len(calls) != 1 || calls[0] != "route53:"+test.action+" "+resource {
			t.Fatal(calls)
		}
	}
	w = r53call(s, "DELETE", "hostedzone/"+id, "", func(a, r string) error { return errors.New("deny") })
	if w.Code != 403 {
		t.Fatal(w.Body.String())
	}
	_, _, e := s.snapshot(context.Background(), id)
	r53must(t, e)
	w = r53call(s, "DELETE", "hostedzone/"+id, "", r53allow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = r53call(s, "GET", "hostedzone/"+id, "", r53allow)
	if w.Code != 404 {
		t.Fatal(w.Body.String())
	}
	w = r53call(s, "GET", strings.TrimPrefix(created.Change.ID, "/"), "", r53allow)
	if w.Code != 200 {
		t.Fatal("receipt lost after zone deletion")
	}
}
func TestRoute53AtomicBatchesAndResolution(t *testing.T) {
	_, s := r53test(t)
	ctx := context.Background()
	id := r53create(t, s)
	a := r53record("a.example.test", "A", "192.0.2.1")
	aaaa := r53record("a.example.test", "AAAA", "2001:db8::1")
	alias := r53record("alias.example.test", "CNAME", "a.example.test")
	_, e := s.change(ctx, id, "", []Route53Change{{"CREATE", a}, {"CREATE", aaaa}, {"CREATE", alias}})
	r53must(t, e)
	for _, tc := range []struct{ name, kind, want string }{{"alias.example.test", "A", "192.0.2.1"}, {"a.example.test", "AAAA", "2001:db8::1"}, {"missing.example.test", "A", "NXDOMAIN"}, {"a.example.test", "MX", "NOERROR"}} {
		w := r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname="+tc.name+"&recordtype="+tc.kind, "", r53allow)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatal(tc, w.Body.String())
		}
	}
	changed := r53record(a.Name, "A", "192.0.2.2")
	bad := r53record("bad.example.test", "A", "not-ip")
	_, e = s.change(ctx, id, "", []Route53Change{{"UPSERT", changed}, {"CREATE", bad}})
	if e == nil {
		t.Fatal("accepted invalid late batch")
	}
	_, rr, e := s.snapshot(ctx, id)
	r53must(t, e)
	for _, r := range rr {
		if r.Name == "a.example.test." && r.Type == "A" && r.Values[0] != "192.0.2.1" {
			t.Fatal("partial batch")
		}
	}
	_, e = s.change(ctx, id, "", []Route53Change{{"DELETE", changed}})
	if r53code(e) != "InvalidChangeBatch" {
		t.Fatal(e)
	}
	_, e = s.change(ctx, id, "", []Route53Change{{"CREATE", r53record(a.Name, "CNAME", "other.example.test")}})
	if r53code(e) != "InvalidChangeBatch" {
		t.Fatal(e)
	}
	if _, e = s.remove(ctx, id); r53code(e) != "HostedZoneNotEmpty" {
		t.Fatal(e)
	}
	_, e = s.change(ctx, id, "", []Route53Change{{"DELETE", a}, {"DELETE", aaaa}, {"DELETE", alias}})
	r53must(t, e)
	_, e = s.remove(ctx, id)
	r53must(t, e)
}
func TestRoute53StrictInputAndAuthentication(t *testing.T) {
	_, s := r53test(t)
	for _, body := range []string{
		`<CreateHostedZoneRequest><Name>example.test</Name><Name>other.test</Name><CallerReference>x</CallerReference></CreateHostedZoneRequest>`,
		`<CreateHostedZoneRequest><Name>example.test</Name><CallerReference>x</CallerReference><VPC><VPCId>vpc-fiction</VPCId></VPC></CreateHostedZoneRequest>`,
		`<CreateHostedZoneRequest><Name>example.test</Name><CallerReference>x</CallerReference><HostedZoneConfig><PrivateZone>true</PrivateZone></HostedZoneConfig></CreateHostedZoneRequest>`,
		`<CreateHostedZoneRequest><Name><Nested>example.test</Nested></Name><CallerReference>x</CallerReference></CreateHostedZoneRequest>`,
		`<CreateHostedZoneRequest other="ignored"><Name>example.test</Name><CallerReference>x</CallerReference></CreateHostedZoneRequest>`,
		r53createXML("x") + r53createXML("y"), `<!DOCTYPE x>` + r53createXML("x"), `<CreateHostedZoneRequest xmlns="wrong"><Name>example.test</Name></CreateHostedZoneRequest>`,
	} {
		w := r53call(s, "POST", "hostedzone", body, r53allow)
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"hostedzone?maxitems=0", "hostedzone?maxitems=1&maxitems=2", "hostedzone?unknown=x", "hostedzone?maxitems=", "hostedzone?bad=%xx"} {
		w := r53call(s, "GET", path, "", r53allow)
		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	for _, header := range []string{"AWS4-HMAC-SHA256 Credential=REAL/20261110/us-east-1/route53/aws4_request", "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261110/ap-northeast-1/route53/aws4_request"} {
		r := httptest.NewRequest("GET", "http://localhost/2013-04-01/hostedzone", nil)
		r.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r, r53allow)
		if w.Code != 403 {
			t.Fatal(w.Code)
		}
	}
	w := r53call(s, "POST", "hostedzone", r53createXML("denied"), func(a, r string) error {
		if a == "ec2:DescribeVpcs" {
			return errors.New("deny")
		}
		return nil
	})
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	var count int
	r53must(t, s.db.QueryRow("SELECT count(*) FROM aws_route53_zones").Scan(&count))
	if count != 0 {
		t.Fatal("invalid or denied request persisted")
	}
}
func TestRoute53RecordValidation(t *testing.T) {
	for _, name := range []string{"", "a..test", "-a.test", "a-.test", "*.test", "a_test", "é.test", strings.Repeat("a", 64) + ".test", "a.test.."} {
		if _, e := r53name(name); e == nil {
			t.Fatal(name)
		}
	}
	for _, rec := range []Route53Record{r53record("outside.test", "A", "192.0.2.1"), r53record("a.example.test", "A", "::1"), r53record("a.example.test", "AAAA", "::ffff:192.0.2.1"), r53record("a.example.test", "AAAA", "fe80::1%eth0"), r53record("example.test", "CNAME", "target.test"), r53record("a.example.test", "NS", "target.test"), r53record("a.example.test", "MX", "70000 target.test"), r53record("a.example.test", "TXT", "unquoted"), r53record("a.example.test", "TXT", `"bad\escape"`), {Name: "a.example.test", Type: "A", Values: []string{"192.0.2.1"}}, {Name: "a.example.test", Type: "A", TTL: r53ttl(-1), Values: []string{"192.0.2.1"}}, {Name: "a.example.test", Type: "A", TTL: r53ttl(1), Values: []string{"192.0.2.1", "192.0.2.1"}}} {
		if _, e := r53validateRecord(rec, "example.test."); e == nil {
			t.Fatalf("accepted %+v", rec)
		}
	}
	for _, rec := range []Route53Record{r53record("a.example.test", "TXT", `"hello"`), r53record("a.example.test", "MX", "10 MAIL.EXAMPLE.TEST"), r53record("a.example.test", "AAAA", "2001:0db8::1")} {
		if _, e := r53validateRecord(rec, "example.test."); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRoute53CNAMECyclesAndEmptyNonterminals(t *testing.T) {
	_, s := r53test(t)
	id := r53create(t, s)
	ctx := context.Background()
	_, e := s.change(ctx, id, "", []Route53Change{{"CREATE", r53record("a.example.test", "CNAME", "b.example.test")}, {"CREATE", r53record("b.example.test", "CNAME", "a.example.test")}, {"CREATE", r53record("external.example.test", "CNAME", "outside.test")}, {"CREATE", r53record("leaf.empty.example.test", "A", "192.0.2.4")}})
	r53must(t, e)
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{{"a.example.test", 200, "SERVFAIL"}, {"external.example.test", 501, "NotImplemented"}, {"empty.example.test", 200, "NOERROR"}, {"outside.test", 400, "InvalidInput"}} {
		w := r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname="+tc.name+"&recordtype=A", "", r53allow)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatal(tc, w.Body.String())
		}
	}
}
func TestRoute53PersistenceIsolationPaginationAndReplay(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	ctx := context.Background()
	_, _, e := s.create(ctx, "different.test", "one", "")
	if r53code(e) != "HostedZoneAlreadyExists" {
		t.Fatal(e)
	}
	_, e = s.change(ctx, id, "", []Route53Change{{"CREATE", r53record("a.example.test", "A", "192.0.2.1")}, {"CREATE", r53record("a.example.test", "AAAA", "2001:db8::1")}})
	r53must(t, e)
	// Reconstruct provider from persisted DB, rather than carrying an in-memory map.
	reopened, e := NewRoute53Store(db.Database())
	r53must(t, e)
	z, rr, e := reopened.snapshot(ctx, id)
	r53must(t, e)
	if z.Count != 4 || len(rr) != 4 {
		t.Fatal(z, rr)
	}
	_, other := r53test(t)
	if _, _, e = other.snapshot(ctx, id); r53code(e) != "NoSuchHostedZone" {
		t.Fatal(e)
	}
	w := r53call(s, "GET", "hostedzone/"+id+"/rrset?maxitems=1", "", r53allow)
	var page struct {
		Records                        []Route53Record `xml:"ResourceRecordSets>ResourceRecordSet"`
		NextRecordName, NextRecordType string
		IsTruncated                    bool
	}
	r53must(t, xml.Unmarshal(w.Body.Bytes(), &page))
	if !page.IsTruncated || len(page.Records) != 1 || page.Records[0].Type != "NS" || page.NextRecordType != "SOA" {
		t.Fatal(w.Body.String())
	}
	w = r53call(s, "GET", "hostedzone/"+id+"/rrset?name=example.test.&type=SOA&maxitems=1", "", r53allow)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<Type>SOA</Type>") {
		t.Fatal(w.Body.String())
	}
	_, _, e = s.create(ctx, "other.test", "two", "")
	r53must(t, e)
	w = r53call(s, "GET", "hostedzone?maxitems=1", "", r53allow)
	var zones struct {
		NextMarker  string
		IsTruncated bool
	}
	r53must(t, xml.Unmarshal(w.Body.Bytes(), &zones))
	if !zones.IsTruncated {
		t.Fatal(w.Body.String())
	}
	w = r53call(other, "GET", "hostedzone?marker="+zones.NextMarker, "", r53allow)
	if w.Code != 400 {
		t.Fatal("cross database cursor accepted")
	}
	w = r53call(s, "GET", "hostedzone?marker="+zones.NextMarker, "", r53allow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestRoute53FaultRollbackAndQuota(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	ctx := context.Background()
	a := r53record("a.example.test", "A", "192.0.2.1")
	r53must(t, func() error {
		_, e := db.Database().Exec("CREATE TRIGGER route53_fail_receipt BEFORE INSERT ON aws_route53_changes BEGIN SELECT RAISE(IGNORE); END")
		return e
	}())
	if _, e := s.change(ctx, id, "", []Route53Change{{"CREATE", a}}); e == nil {
		t.Fatal("ignored receipt committed")
	}
	z, rr, e := s.snapshot(ctx, id)
	r53must(t, e)
	if z.Count != 2 || len(rr) != 2 {
		t.Fatal("fault leaked mutation")
	}
	if _, e = s.remove(ctx, id); e == nil {
		t.Fatal("ignored receipt deleted zone")
	}
	_, _, e = s.snapshot(ctx, id)
	r53must(t, e)
	_, e = db.Database().Exec("DROP TRIGGER route53_fail_receipt")
	r53must(t, e)
	tx, e := db.Database().BeginTx(ctx, nil)
	r53must(t, e)
	for i := 0; i < 99; i++ {
		_, e = tx.Exec("INSERT INTO aws_route53_zones VALUES(?,?,?,?)", fmt.Sprintf("quota%d", i), "quota.test.", fmt.Sprintf("ref%d", i), "")
		r53must(t, e)
	}
	r53must(t, tx.Commit())
	_, _, e = s.create(ctx, "over.test", "over", "")
	if r53code(e) != "TooManyHostedZones" {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.change(canceled, id, "", []Route53Change{{"CREATE", a}}); e == nil {
		t.Fatal("canceled mutation accepted")
	}
}
func TestRoute53HTTPChangeXML(t *testing.T) {
	_, s := r53test(t)
	id := r53create(t, s)
	body := `<ChangeResourceRecordSetsRequest xmlns="` + r53namespace + `"><ChangeBatch><Changes><Change><Action>CREATE</Action><ResourceRecordSet><Name>www.example.test</Name><Type>A</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>192.0.2.1</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`
	w := r53call(s, "POST", "hostedzone/"+id+"/rrset/", body, r53allow)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bad := range []string{strings.Replace(body, "<TTL>60</TTL>", "<TTL>60</TTL><TTL>61</TTL>", 1), strings.Replace(body, "<TTL>60</TTL>", "<TTL>60</TTL><Weight>10</Weight>", 1), strings.Replace(body, "<TTL>60</TTL>", "<TTL></TTL>", 1)} {
		w = r53call(s, "POST", "hostedzone/"+id+"/rrset", bad, r53allow)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestRoute53ActualDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db, e := core.New(path)
	r53must(t, e)
	s, e := NewRoute53Store(db.Database())
	r53must(t, e)
	id := r53create(t, s)
	receipt, e := s.change(context.Background(), id, "restart", []Route53Change{{"CREATE", r53record("www.example.test", "A", "192.0.2.7")}})
	r53must(t, e)
	r53must(t, db.Close())
	db, e = core.New(path)
	r53must(t, e)
	defer db.Close()
	s, e = NewRoute53Store(db.Database())
	r53must(t, e)
	w := r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname=www.example.test&recordtype=A", "", r53allow)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "192.0.2.7") {
		t.Fatal(w.Body.String())
	}
	w = r53call(s, "GET", strings.TrimPrefix(receipt.ID, "/"), "", r53allow)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "restart") {
		t.Fatal(w.Body.String())
	}
}
func TestRoute53DeniedBatchAndReceiptRetention(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	ctx := context.Background()
	w := r53call(s, "POST", "hostedzone/"+id+"/rrset", "malformed XML", func(string, string) error { return errors.New("deny") })
	if w.Code != 403 {
		t.Fatal(w.Body.String())
	}
	var oldest string
	r53must(t, db.Database().QueryRow("SELECT id FROM aws_route53_changes ORDER BY rowid LIMIT 1").Scan(&oldest))
	_, e := db.Database().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO aws_route53_changes SELECT 'quota'||x,'{}' FROM n`)
	r53must(t, e)
	// Receipt insertion failure must restore both the eviction and zone deletion.
	_, e = db.Database().Exec("CREATE TRIGGER fail_retention BEFORE INSERT ON aws_route53_changes BEGIN SELECT RAISE(IGNORE); END")
	r53must(t, e)
	if _, e = s.remove(ctx, id); e == nil {
		t.Fatal("ignored receipt permitted cleanup")
	}
	var n int
	r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_changes WHERE id=?", oldest).Scan(&n))
	if n != 1 {
		t.Fatal("rollback lost evicted receipt")
	}
	_, _, e = s.snapshot(ctx, id)
	r53must(t, e)
	_, e = db.Database().Exec("DROP TRIGGER fail_retention")
	r53must(t, e)
	receipt, e := s.remove(ctx, id)
	r53must(t, e)
	w = r53call(s, "GET", "change/"+oldest, "", r53allow)
	if w.Code != 404 {
		t.Fatal("expired receipt remains")
	}
	w = r53call(s, "GET", strings.TrimPrefix(receipt.ID, "/"), "", r53allow)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_changes").Scan(&n))
	if n != 10000 {
		t.Fatal(n)
	}
}
func TestRoute53DeleteFinalAcknowledgment(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	// With foreign keys disabled to reproduce a broken cascade, deletion must
	// still fail closed rather than report success with orphaned records.
	_, e := db.Database().Exec("PRAGMA foreign_keys=OFF")
	r53must(t, e)
	if _, e = s.remove(context.Background(), id); e == nil {
		t.Fatal("missing cascade accepted")
	}
	_, _, e = s.snapshot(context.Background(), id)
	r53must(t, e)
	_, e = db.Database().Exec("PRAGMA foreign_keys=ON")
	r53must(t, e)
	_, e = s.remove(context.Background(), id)
	r53must(t, e)
}

func TestRoute53UnknownIDsAndNilAuthority(t *testing.T) {
	_, s := r53test(t)
	for _, path := range []string{"hostedzone/ZUNKNOWN", "change/CUNKNOWN"} {
		w := r53call(s, "GET", path, "", r53allow)
		if w.Code != 404 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	w := r53call(s, "GET", "hostedzone", "", nil)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestRoute53DamagedRecordFailsClosed(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	_, e := s.change(context.Background(), id, "", []Route53Change{{"CREATE", r53record("alias.example.test", "CNAME", "example.test")}})
	r53must(t, e)
	_, e = db.Database().Exec(`UPDATE aws_route53_records SET document='{"Name":"alias.example.test.","Type":"CNAME","TTL":60,"Values":[]}' WHERE zone=? AND type='CNAME'`, id)
	r53must(t, e)
	w := r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname=alias.example.test&recordtype=A", "", r53allow)
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRoute53BatchFinalAcknowledgment(t *testing.T) {
	db, s := r53test(t)
	id := r53create(t, s)
	_, e := db.Database().Exec(`CREATE TRIGGER rewrite_route53 AFTER INSERT ON aws_route53_records WHEN NEW.type='A' BEGIN UPDATE aws_route53_records SET document='{}' WHERE zone=NEW.zone AND name=NEW.name AND type=NEW.type; END`)
	r53must(t, e)
	if _, e = s.change(context.Background(), id, "", []Route53Change{{"CREATE", r53record("www.example.test", "A", "192.0.2.1")}}); e == nil {
		t.Fatal("trigger rewrite acknowledged")
	}
	z, _, e := s.snapshot(context.Background(), id)
	r53must(t, e)
	if z.Count != 2 {
		t.Fatal("trigger failure leaked batch")
	}
}
