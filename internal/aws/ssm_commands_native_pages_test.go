package aws

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandPageBindingsAndStableSnapshot(t *testing.T) {
	store, _, db, authority, leases := ssmFixture(t, 1)
	if err := initNativeCommandPages(db); err != nil {
		t.Fatal(err)
	}
	api := &SSMCommandAPI{db: db, store: store}
	ctx := context.Background()
	first := ssmSend(t, store, authority, leases)
	second := ssmSend(t, store, authority, leases)
	input := nativeCommandInput{InvokedAfter: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), MaxResults: 1}
	page, token, err := api.list(ctx, authority.Principal, authority.Region, input)
	if err != nil || len(page) != 1 || token == "" {
		t.Fatalf("first page %v %q %v", page, token, err)
	}
	third := ssmSend(t, store, authority, leases)
	next := input
	next.NextToken = token
	page, rest, err := api.list(ctx, authority.Principal, authority.Region, next)
	if err != nil || len(page) != 1 || rest != "" || page[0].CommandID == third.CommandId {
		t.Fatalf("snapshot changed: %v %q %v", page, rest, err)
	}
	for _, change := range []string{"role", "region", "filter", "limit", "token"} {
		t.Run(change, func(t *testing.T) {
			p, r := authority.Principal, authority.Region
			bad := next
			switch change {
			case "role":
				p += "replacement"
			case "region":
				r = "us-west-2"
			case "filter":
				bad.InvokedAfter = time.Now().UTC().Format(time.RFC3339Nano)
			case "limit":
				bad.MaxResults = 2
			case "token":
				bad.NextToken = "corrupt"
			}
			if _, _, err := api.list(ctx, p, r, bad); err == nil {
				t.Fatal("cross-scope token accepted")
			}
		})
	}
	root := strings.Split(token, ".")[0]
	var raw string
	if err = db.QueryRow(`SELECT record FROM aws_ssm_native_command_pages WHERE token=?`, root).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE aws_ssm_native_command_pages SET record=json_set(record,'$.Expires',1) WHERE token=?`, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err = api.list(ctx, authority.Principal, authority.Region, next); err == nil {
		t.Fatal("expired accepted")
	}
	if _, err = db.Exec(`UPDATE aws_ssm_native_command_pages SET record=? WHERE token=?`, raw, root); err != nil {
		t.Fatal(err)
	}
	// Removing retained immutable commands invalidates discovery rather than
	// presenting snapshots as new evidence of successful execution.
	if _, err = db.Exec(`DELETE FROM aws_ssm_command_tokens WHERE command_id IN (?,?)`, first.CommandId, second.CommandId); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM aws_ssm_commands WHERE id IN (?,?)`, first.CommandId, second.CommandId); err != nil {
		t.Fatal(err)
	}
	if _, _, err = api.list(ctx, authority.Principal, authority.Region, next); err == nil {
		t.Fatal("deleted command silently returned")
	}
}

func TestNativeCommandPageFullRetainedLimit(t *testing.T) {
	store, _, db, authority, leases := ssmFixture(t, 1)
	if err := initNativeCommandPages(db); err != nil {
		t.Fatal(err)
	}
	api := &SSMCommandAPI{db: db, store: store}
	ctx := context.Background()
	for n := 0; n < ssmCommandLimit; n++ {
		ssmSend(t, store, authority, leases)
	}
	if _, err := store.Send(ctx, authority, ssmCommandRequestFixture(leases), leases); err == nil {
		t.Fatal("retained command limit exceeded")
	}
	input := nativeCommandInput{InvokedAfter: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), MaxResults: 50}
	seen := map[string]bool{}
	for n := 0; n < 3; n++ {
		items, next, err := api.list(ctx, authority.Principal, authority.Region, input)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if seen[item.CommandID] {
				t.Fatal("duplicate page item")
			}
			seen[item.CommandID] = true
		}
		input.NextToken = next
		if n < 2 && next == "" {
			t.Fatal("premature end")
		}
		if n == 2 && next != "" {
			t.Fatal("unbounded continuation")
		}
	}
	if len(seen) != ssmCommandLimit {
		t.Fatalf("got %d", len(seen))
	}
}
