package aws

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRoute53OriginalASCIIValidation(t *testing.T) {
	for _, name := range []string{"K.example.test", "www.K.example.test", "K.EXAMPLE.TEST."} {
		if _, e := r53name(name); r53code(e) != "InvalidInput" {
			t.Fatalf("Unicode spelling accepted: %q %v", name, e)
		}
	}
	if got, e := r53name("K.EXAMPLE.TEST."); e != nil || got != "k.example.test." {
		t.Fatal(got, e)
	}
	for _, record := range []Route53Record{r53record("www.example.test", "CNAME", "K.example.test"), r53record("www.example.test", "MX", "10 K.example.test")} {
		if _, e := r53validateRecord(record, "example.test."); r53code(e) != "InvalidInput" {
			t.Fatal(record, e)
		}
	}
}
func TestRoute53KelvinHTTPMutationPreservesASCIIState(t *testing.T) {
	_, s := r53test(t)
	id := r53create(t, s)
	ctx := context.Background()
	_, e := s.change(ctx, id, "", []Route53Change{{"CREATE", r53record("k.example.test", "A", "192.0.2.1")}})
	r53must(t, e)
	for _, action := range []string{"UPSERT", "DELETE"} {
		body := fmt.Sprintf(`<ChangeResourceRecordSetsRequest><ChangeBatch><Changes><Change><Action>%s</Action><ResourceRecordSet><Name>K.example.test</Name><Type>A</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>192.0.2.1</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`, action)
		if action == "UPSERT" {
			body = strings.Replace(body, "192.0.2.1", "192.0.2.9", 1)
		}
		w := r53call(s, "POST", "hostedzone/"+id+"/rrset", body, r53allow)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "InvalidInput") {
			t.Fatal(action, w.Code, w.Body.String())
		}
		w = r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname=k.example.test&recordtype=A", "", r53allow)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "192.0.2.1") {
			t.Fatal("ASCII record changed", w.Code, w.Body.String())
		}
	}
	w := r53call(s, "POST", "hostedzone", strings.Replace(r53createXML("kelvin"), "Example.TEST", "K.example.test", 1), r53allow)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname=%E2%84%AA.example.test&recordtype=A", "", r53allow)
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRoute53CreateFinalDefaultsAndMetadataAcknowledgment(t *testing.T) {
	triggers := map[string]string{
		"deleted default":   `CREATE TRIGGER r53_create_fault AFTER INSERT ON aws_route53_changes BEGIN DELETE FROM aws_route53_records WHERE type='NS'; END`,
		"rewritten default": `CREATE TRIGGER r53_create_fault AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_records SET document='{}' WHERE type='SOA'; END`,
		"changed name":      `CREATE TRIGGER r53_create_fault AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_zones SET name='different.test.'; END`,
		"changed reference": `CREATE TRIGGER r53_create_fault AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_zones SET reference='different-reference'; END`,
		"changed comment":   `CREATE TRIGGER r53_create_fault AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_zones SET comment='different-comment'; END`,
	}
	for name, trigger := range triggers {
		t.Run(name, func(t *testing.T) {
			db, s := r53test(t)
			_, e := db.Database().Exec(trigger)
			r53must(t, e)
			if _, _, e = s.create(context.Background(), "example.test", "reference", "comment"); e == nil {
				t.Fatal("damaged creation acknowledged")
			}
			for _, table := range []string{"aws_route53_zones", "aws_route53_records", "aws_route53_changes"} {
				var n int
				r53must(t, db.Database().QueryRow("SELECT count(*) FROM "+table).Scan(&n))
				if n != 0 {
					t.Fatal("creation rollback leaked", table, n)
				}
			}
		})
	}
}
func TestRoute53ReceiptAfterTriggerRollbackAcrossMutations(t *testing.T) {
	for _, operation := range []string{"create", "change", "delete"} {
		for _, fault := range []string{"delete", "rewrite", "retention"} {
			t.Run(operation+"/"+fault, func(t *testing.T) {
				db, s := r53test(t)
				ctx := context.Background()
				id := r53create(t, s)
				var oldest string
				r53must(t, db.Database().QueryRow("SELECT id FROM aws_route53_changes ORDER BY rowid LIMIT 1").Scan(&oldest))
				_, e := db.Database().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<9999) INSERT INTO aws_route53_changes SELECT 'quota'||x,'{}' FROM n`)
				r53must(t, e)
				sql := `CREATE TRIGGER r53_receipt_after AFTER INSERT ON aws_route53_changes BEGIN DELETE FROM aws_route53_changes WHERE id=NEW.id; END`
				if fault == "rewrite" {
					sql = `CREATE TRIGGER r53_receipt_after AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_changes SET document='{}' WHERE id=NEW.id; END`
				}
				if fault == "retention" {
					sql = `CREATE TRIGGER r53_receipt_after AFTER INSERT ON aws_route53_changes BEGIN DELETE FROM aws_route53_changes WHERE id='quota2'; END`
				}
				_, e = db.Database().Exec(sql)
				r53must(t, e)
				switch operation {
				case "create":
					_, _, e = s.create(ctx, "other.test", "other", "")
				case "change":
					_, e = s.change(ctx, id, "", []Route53Change{{"CREATE", r53record("www.example.test", "A", "192.0.2.1")}})
				case "delete":
					_, e = s.remove(ctx, id)
				}
				if e == nil {
					t.Fatal("receipt fault acknowledged")
				}
				z, rr, e := s.snapshot(ctx, id)
				r53must(t, e)
				if z.Count != 2 || len(rr) != 2 {
					t.Fatal("resource mutation escaped rollback")
				}
				var zones, receipts, retained int
				r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_zones").Scan(&zones))
				r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_changes").Scan(&receipts))
				r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_changes WHERE id=?", oldest).Scan(&retained))
				if zones != 1 || receipts != 10000 || retained != 1 {
					t.Fatal("retention/resource frame lost", zones, receipts, retained)
				}
			})
		}
	}
}

func TestRoute53CopiedDefaultDocumentCannotMasqueradeAsSOA(t *testing.T) {
	for _, operation := range []string{"create", "change"} {
		t.Run(operation, func(t *testing.T) {
			db, s := r53test(t)
			ctx := context.Background()
			id := r53create(t, s)
			_, e := db.Database().Exec(`CREATE TRIGGER r53_copy_default AFTER INSERT ON aws_route53_changes BEGIN UPDATE aws_route53_records SET document=(SELECT source.document FROM aws_route53_records AS source WHERE source.zone=aws_route53_records.zone AND source.type='NS') WHERE type='SOA'; END`)
			r53must(t, e)
			if operation == "create" {
				_, _, e = s.create(ctx, "other.test", "other", "")
			} else {
				_, e = s.change(ctx, id, "", []Route53Change{{"CREATE", r53record("www.example.test", "A", "192.0.2.1")}})
			}
			if e == nil || !strings.Contains(e.Error(), "persisted record key mismatch") {
				t.Fatal("SQL-key/document mismatch was not rejected", e)
			}
			z, rr, e := s.snapshot(ctx, id)
			r53must(t, e)
			if z.Count != 2 || len(rr) != 2 {
				t.Fatal("existing defaults changed")
			}
			var zones, receipts int
			r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_zones").Scan(&zones))
			r53must(t, db.Database().QueryRow("SELECT count(*) FROM aws_route53_changes").Scan(&receipts))
			if zones != 1 || receipts != 1 {
				t.Fatal("copied document escaped rollback", zones, receipts)
			}
		})
	}
}
