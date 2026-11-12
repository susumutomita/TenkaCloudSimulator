package aws

import (
	"context"
	"strings"
	"testing"
)

func TestRoute53RecordValidationRefusesMalformedValues(t *testing.T) {
	zone := "example.test."
	ok := func(kind string, values ...string) Route53Record {
		return Route53Record{Name: "www.example.test", Type: kind, TTL: r53ttl(60), Values: values}
	}
	for name, r := range map[string]Route53Record{
		"empty value":     ok("A", ""),
		"oversized value": ok("TXT", `"`+strings.Repeat("a", 1100)+`"`),
		"MX one field":    ok("MX", "mail.example.test."),
		"MX three fields": ok("MX", "10 mail.example.test. extra"),
		"TXT non-ASCII":   ok("TXT", `"caf`+"\u00e9"+`"`),
		"A mismatch":      ok("A", "2001:db8::1"),
		"CNAME apex":      {Name: zone, Type: "CNAME", TTL: r53ttl(60), Values: []string{"other.test."}},
	} {
		if _, err := r53validateRecord(r, zone); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	got, err := r53validateRecord(ok("AAAA", "2001:DB8::1"), zone)
	if err != nil || got.Values[0] != "2001:db8::1" {
		t.Fatal("AAAA canonicalization", got, err)
	}
	if (route53Error{"InvalidInput", "bad record"}).Error() != "bad record" {
		t.Fatal("error text")
	}
}

func TestRoute53StoreRefusesInvalidZonesAndBatchesAtomically(t *testing.T) {
	db, s := r53test(t)
	ctx := context.Background()
	for name, c := range map[string][3]string{
		"single label":  {"test", "ref-1", ""},
		"no reference":  {"zone.test", "", ""},
		"long comment":  {"zone.test", "ref-2", strings.Repeat("c", 257)},
		"bad reference": {"zone.test", strings.Repeat("r", 129), ""},
	} {
		if _, _, err := s.create(ctx, c[0], c[1], c[2]); r53code(err) == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	id := r53create(t, s)
	before := r53PersistentState(t, db.Database())
	record := Route53Record{Name: "www.example.test", Type: "A", TTL: r53ttl(60), Values: []string{"192.0.2.1"}}
	many := make([]Route53Change, 101)
	for i := range many {
		many[i] = Route53Change{Action: "UPSERT", Record: record}
	}
	for name, c := range map[string]struct {
		comment string
		changes []Route53Change
	}{
		"empty batch":    {"", nil},
		"oversized":      {"", many},
		"long comment":   {strings.Repeat("c", 257), []Route53Change{{Action: "UPSERT", Record: record}}},
		"unknown action": {"", []Route53Change{{Action: "MERGE", Record: record}}},
		"double create":  {"", []Route53Change{{Action: "CREATE", Record: record}, {Action: "CREATE", Record: record}}},
	} {
		if _, err := s.change(ctx, id, c.comment, c.changes); r53code(err) == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if after := r53PersistentState(t, db.Database()); after != before {
		t.Fatal("refused batch wrote state")
	}
	six := Route53Record{Name: "v6.example.test", Type: "AAAA", TTL: r53ttl(60), Values: []string{"2001:db8::1"}}
	if _, err := s.change(ctx, id, "", []Route53Change{{Action: "CREATE", Record: six}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.change(ctx, id, "", []Route53Change{{Action: "CREATE", Record: six}}); r53code(err) != "InvalidChangeBatch" {
		t.Fatal("duplicate create", err)
	}
	// Persisted rows are re-validated: non-canonical or undecodable documents fail closed.
	for name, document := range map[string]string{
		"non-canonical": `{"Name":"v6.example.test.","Type":"AAAA","TTL":60,"Values":["2001:DB8::1"]}`,
		"undecodable":   `{`,
	} {
		if _, err := db.Database().Exec(`UPDATE aws_route53_records SET document=? WHERE type='AAAA'`, document); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.snapshot(ctx, id); err == nil {
			t.Errorf("%s persisted record read", name)
		}
	}
}
