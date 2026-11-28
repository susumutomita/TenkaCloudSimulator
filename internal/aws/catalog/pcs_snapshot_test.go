package catalog

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"reflect"
	"testing"
)

func newPCSHarness(t *testing.T) *harness {
	h := newHarness(t, pcs)
	h.engine.cfg.AuthorizeSnapshot = pcsPolicySnapshot(h.policy)
	return h
}

func pcsPolicySnapshot(p *policy) func(context.Context, string) (func(string, string) string, error) {
	return func(ctx context.Context, principal string) (func(string, string) string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.err != nil {
			return nil, p.err
		}
		denied := map[string]bool{}
		for key, value := range p.deny {
			denied[key] = value
		}
		return func(action, resource string) string {
			p.mu.Lock()
			p.calls = append(p.calls, principal+" "+action+" "+resource)
			p.mu.Unlock()
			if denied[action] || denied[action+" "+resource] {
				return "explicit-deny"
			}
			return "allowed"
		}, nil
	}
}

func TestPCSCompoundAuthorizationRequiresSnapshot(t *testing.T) {
	for _, mode := range []string{"missing", "empty", "error"} {
		t.Run(mode, func(t *testing.T) {
			h := newPCSHarness(t)
			cluster, _, queue := pcsIAMSeed(t, h)
			before := pcsRawRows(t, h.db)
			switch mode {
			case "missing":
				h.engine.cfg.AuthorizeSnapshot = nil
			case "empty":
				h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) { return nil, nil }
			case "error":
				h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) {
					return nil, errors.New("snapshot unavailable")
				}
			}
			for _, operation := range []string{"GetQueue", "UpdateQueue", "DeleteQueue"} {
				input := map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue}
				if operation == "UpdateQueue" {
					input["computeNodeGroupConfigurations"] = []any{}
				}
				if got := h.json1("pcs", operation, input); got.status != 500 {
					t.Fatalf("%s without stable evaluator: %d", operation, got.status)
				}
			}
			if !reflect.DeepEqual(before, pcsRawRows(t, h.db)) {
				t.Fatal("missing snapshot changed rows")
			}
		})
	}
}

func TestPCSCompoundDecisionsUseOneActualPolicyRevision(t *testing.T) {
	for _, operation := range []string{"GetQueue", "UpdateQueue"} {
		t.Run(operation, func(t *testing.T) {
			h := newPCSHarness(t)
			cluster, _, queue := pcsIAMSeed(t, h)
			parent := "arn:aws:pcs:us-east-1:000000000000:cluster/" + cluster
			child := parent + "/queue/" + queue
			var seq int
			var name, path string
			if err := h.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			identityStore, err := core.New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = identityStore.Close() })
			identities := core.NewIdentities(identityStore)
			principal := "pcs-replaced"
			h.engine.cfg.DefaultPrincipal = principal
			set := func(resources ...string) {
				t.Helper()
				if err := identities.Set(principal, core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"pcs:" + operation}, Resource: core.StringList(resources)}}}); err != nil {
					t.Fatal(err)
				}
			}
			set(parent)
			snapshots, swaps := 0, 0
			replaceAfterParent := func(resource string) {
				if resource == parent && swaps == 0 {
					swaps++
					set(child)
				}
			}
			h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
				decision, err := identities.DecisionContext(ctx, principal, action, resource)
				replaceAfterParent(resource)
				return decision, err
			}
			h.engine.cfg.AuthorizeSnapshot = func(ctx context.Context, principal string) (func(string, string) string, error) {
				snapshots++
				decide, err := identities.DecisionSnapshot(ctx, principal)
				if err != nil {
					return nil, err
				}
				return func(action, resource string) string {
					decision := decide(action, resource)
					replaceAfterParent(resource)
					return decision
				}, nil
			}
			input := map[string]any{"clusterIdentifier": cluster, "queueIdentifier": queue}
			if operation == "UpdateQueue" {
				input["computeNodeGroupConfigurations"] = []any{}
			}
			before := pcsRawRows(t, h.db)
			expect(t, h.json1("pcs", operation, input), 403, "AccessDeniedException")
			if snapshots != 1 || swaps != 1 {
				t.Fatalf("not one snapshot across actual replacement: snapshots=%d swaps=%d", snapshots, swaps)
			}
			if !reflect.DeepEqual(before, pcsRawRows(t, h.db)) {
				t.Fatal("mixed policy revisions changed catalog rows")
			}
			set(parent, child)
			snapshots = 0
			expect(t, h.json1("pcs", operation, input), 200, "")
			if snapshots != 1 {
				t.Fatal("fresh request did not take one fresh snapshot")
			}
			h.engine.cfg.DefaultPrincipal = "pcs-missing-policy"
			snapshots = 0
			expect(t, h.json1("pcs", operation, input), 403, "AccessDeniedException")
			if snapshots != 1 {
				t.Fatal("missing policy bypassed snapshot")
			}
			h.engine.cfg.DefaultPrincipal = principal
			if err := identities.Set(principal, core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"pcs:" + operation}, Resource: core.StringList{parent, child}}, {Effect: "Deny", Action: core.StringList{"pcs:" + operation}, Resource: core.StringList{child}}}}); err != nil {
				t.Fatal(err)
			}
			denied := h.json1("pcs", operation, input)
			expect(t, denied, 403, "AccessDeniedException")
			if denied.header.Get("X-Simulator-IAM-Decision") != "explicitDeny" {
				t.Fatal("explicit deny decision lost")
			}
		})
	}
}
