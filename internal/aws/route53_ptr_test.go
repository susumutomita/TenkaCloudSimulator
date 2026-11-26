package aws

import (
	"context"
	"encoding/xml"
	"slices"
	"strings"
	"testing"
)

func TestRoute53PTRAnswersAndPagination(t *testing.T) {
	_, s := r53test(t)
	ctx := context.Background()
	zone, _, err := s.create(ctx, "2.0.192.in-addr.arpa.", "reverse", "")
	r53must(t, err)
	id := strings.TrimPrefix(zone.ID, "/hostedzone/")
	const owner = "7.2.0.192.in-addr.arpa."
	ptr := r53record(owner, "PTR", "WEB.EXAMPLE.test")
	ptr.Values = append(ptr.Values, "alternate.example.test.")
	mx := r53record(owner, "MX", "10 mail.example.test")
	a := r53record(owner, "A", "192.0.2.7")
	_, err = s.change(ctx, id, "", []Route53Change{{"CREATE", a}, {"CREATE", ptr}, {"CREATE", mx}})
	r53must(t, err)
	for _, start := range []struct{ kind, next string }{{"A", "PTR"}, {"PTR", "MX"}} {
		w := r53call(s, "GET", "hostedzone/"+id+"/rrset?maxitems=1&name="+owner+"&type="+start.kind, "", r53allow)
		var page struct {
			Records                        []Route53Record `xml:"ResourceRecordSets>ResourceRecordSet"`
			NextRecordName, NextRecordType string
		}
		r53must(t, xml.Unmarshal(w.Body.Bytes(), &page))
		if w.Code != 200 || len(page.Records) != 1 || page.Records[0].Type != start.kind || page.NextRecordName != owner || page.NextRecordType != start.next {
			t.Fatalf("start %s: %d %s", start.kind, w.Code, w.Body.String())
		}
	}
	// A malformed or canonically duplicate target in a late change must roll
	// back the preceding UPSERT and leave the original PTR targets untouched.
	update := r53record(owner, "PTR", "new.example.test.")
	for _, values := range [][]string{{"-bad.example.test"}, {"Web.example.test", "web.example.test."}} {
		bad := r53record("8.2.0.192.in-addr.arpa.", "PTR", "")
		bad.Values = values
		if _, err = s.change(ctx, id, "", []Route53Change{{"UPSERT", update}, {"CREATE", bad}}); r53code(err) != "InvalidInput" {
			t.Fatalf("accepted malformed PTR batch %v: %v", values, err)
		}
	}
	w := r53call(s, "GET", "testdnsanswer?hostedzoneid="+id+"&recordname="+owner+"&recordtype=PTR", "", r53allow)
	var answer struct {
		ResponseCode string
		Data         []string `xml:"RecordData>RecordDataEntry"`
	}
	r53must(t, xml.Unmarshal(w.Body.Bytes(), &answer))
	if w.Code != 200 || answer.ResponseCode != "NOERROR" || !slices.Equal(answer.Data, []string{"alternate.example.test.", "web.example.test."}) {
		t.Fatalf("expected stored PTR data, not recursive lookup: %d %s", w.Code, w.Body.String())
	}
	_, err = s.change(ctx, id, "", []Route53Change{{"DELETE", ptr}, {"DELETE", mx}, {"DELETE", a}})
	r53must(t, err)
	_, err = s.remove(ctx, id)
	r53must(t, err)
}
