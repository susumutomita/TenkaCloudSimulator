package simpledb

import (
	"context"
	"encoding/xml"
	"net/http"
	"testing"
	"time"
)

func TestDomainMetadataWire(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	const account, region, domain = "local", "us-east-1", "metadata"
	action := "DomainMetadata"
	var permitted bool
	var checks int
	h := NewHTTPHandler(s, Scope{account, region}, func(_ context.Context, gotAction, gotResource string) bool {
		checks++
		if gotAction != "sdb:DomainMetadata" || gotResource != "arn:aws:sdb:us-east-1:local:domain/metadata" {
			t.Fatalf("unexpected authorization inputs %q %q", gotAction, gotResource)
		}
		return permitted
	})
	call := func(args map[string]string, status int, code string) DomainStats {
		t.Helper()
		before := time.Now().Unix()
		w := callHTTP(h, params(action, args))
		if w.Code != status {
			t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		var wire struct {
			XMLName xml.Name
			Stats   *DomainStats `xml:"DomainMetadataResult"`
			ID      string       `xml:"ResponseMetadata>RequestId"`
			Code    string       `xml:"Errors>Error>Code"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &wire); err != nil || wire.ID == "" || wire.Code != code ||
			wire.XMLName.Space != "http://sdb.amazonaws.com/doc/2009-04-15/" {
			t.Fatalf("unexpected wire response: %v: %s", err, w.Body.String())
		}
		if code != "" {
			if wire.Stats != nil || wire.XMLName.Local != "Response" {
				t.Fatalf("invalid error envelope: %s", w.Body.String())
			}
			return DomainStats{}
		}
		if wire.XMLName.Local != "DomainMetadataResponse" || wire.Stats == nil ||
			wire.Stats.Timestamp < before || wire.Stats.Timestamp > time.Now().Unix() {
			t.Fatalf("invalid metadata XML: %s", w.Body.String())
		}
		// Unmarshal explicitly to prove zero-valued result fields were present.
		var fields struct {
			Result struct {
				ItemCount                *int64
				ItemNamesSizeBytes       *int64
				AttributeNameCount       *int64
				AttributeNamesSizeBytes  *int64
				AttributeValueCount      *int64
				AttributeValuesSizeBytes *int64
				Timestamp                *int64
			} `xml:"DomainMetadataResult"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		resultFields := fields.Result
		if resultFields.ItemCount == nil || resultFields.ItemNamesSizeBytes == nil ||
			resultFields.AttributeNameCount == nil || resultFields.AttributeNamesSizeBytes == nil ||
			resultFields.AttributeValueCount == nil || resultFields.AttributeValuesSizeBytes == nil || resultFields.Timestamp == nil {
			t.Fatalf("missing statistic: %s", w.Body.String())
		}
		stats := *wire.Stats
		stats.Timestamp = 0
		return stats
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]string{"DomainName": domain}
	call(nil, http.StatusBadRequest, "MissingParameter")
	call(map[string]string{"DomainName": domain, "ItemName": "other"}, http.StatusBadRequest, "InvalidParameterValue")
	call(map[string]string{"DomainName": ""}, http.StatusBadRequest, "InvalidParameterValue")
	if checks != 0 {
		t.Fatalf("invalid inputs reached authorization")
	}
	call(args, http.StatusForbidden, "AccessDenied")
	permitted = true
	call(args, http.StatusBadRequest, "NoSuchDomain")
	must(s.CreateDomain(ctx, account, region, domain))
	if got := call(args, http.StatusOK, ""); got != (DomainStats{}) {
		t.Fatalf("empty domain returned %+v", got)
	}

	must(s.Put(ctx, account, region, domain, "雪", map[string][]string{"色": {"茶", "茶", ""}, "tag": {"a\x00b"}}, nil))
	must(s.Put(ctx, account, region, domain, "second", map[string][]string{"色": {"茶", "blue"}, "Tag": {"🧸"}}, nil))
	for _, peer := range []Scope{{"peer", region}, {account, "us-west-2"}} {
		must(s.CreateDomain(ctx, peer.Account, peer.Region, domain))
		must(s.Put(ctx, peer.Account, peer.Region, domain, "excluded", map[string][]string{"excluded": {"excluded"}}, nil))
	}
	want := DomainStats{ItemCount: 2, ItemNamesSizeBytes: 9, AttributeNameCount: 3, AttributeNamesSizeBytes: 9, AttributeValueCount: 6, AttributeValuesSizeBytes: 17}
	if got := call(args, http.StatusOK, ""); got != want {
		t.Fatalf("populated domain got %+v, want %+v", got, want)
	}
	// A write through another Store must be visible, and replace must subtract
	// old values rather than relying on process-local counters.
	other, err := NewStore(db)
	must(err)
	must(other.Put(ctx, account, region, domain, "second", map[string][]string{"色": {""}}, map[string]bool{"色": true}))
	want.AttributeValueCount--
	want.AttributeValuesSizeBytes -= 7
	if got := call(args, http.StatusOK, ""); got != want {
		t.Fatalf("replace got %+v, want %+v", got, want)
	}
	must(other.DeleteDomain(ctx, account, region, domain))
	call(args, http.StatusBadRequest, "NoSuchDomain")
	must(s.CreateDomain(ctx, account, region, domain))
	if got := call(args, http.StatusOK, ""); got != (DomainStats{}) {
		t.Fatalf("recreated domain retained metadata: %+v", got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got, err := s.DomainMetadata(cancelled, account, region, domain); err != storeError("Cancelled") || got != (DomainStats{}) {
		t.Fatalf("cancelled read got %+v, %v", got, err)
	}
}
