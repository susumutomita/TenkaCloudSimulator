package aws

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Attempt the competing reassociation at the authorization boundary. The
// simulator's single connection must stay reserved by the resource operation
// until its authorized read/delete completes; a second writer must not slip in.
func TestWAFAssociationReadAndDeleteKeepAuthorizationTransaction(t *testing.T) {
	for _, operation := range []string{"GetWebACLForResource", "DisassociateWebACL"} {
		t.Run(operation, func(t *testing.T) {
			store, stage := wafFixture(t)
			ctx := context.Background()
			allowed, err := store.Create(ctx, wafTestRegion, wafTestACL())
			if err != nil {
				t.Fatal(err)
			}
			other := wafTestACL()
			other.Name = "other-private-acl"
			denied, err := store.Create(ctx, wafTestRegion, other)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Associate(ctx, wafTestRegion, stage, allowed.ARN); err != nil {
				t.Fatal(err)
			}
			var attempted bool
			var writeErr error
			response := wafCall(store, operation, wafJSON(t, map[string]string{"ResourceArn": stage}), func(action, resource string) bool {
				if !attempted {
					attempted = true
					deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
					defer cancel()
					writeErr = store.Associate(deadline, wafTestRegion, stage, denied.ARN)
				}
				if action == "apigateway:SetWebACL" {
					return resource == stage
				}
				return resource == allowed.ARN
			})
			if response.Code != 200 {
				t.Fatal(response.Code, response.Body.String())
			}
			if !errors.Is(writeErr, context.DeadlineExceeded) {
				t.Fatalf("association changed between authorization and use: %v; response %s", writeErr, response.Body.String())
			}
			saved, err := store.ForResource(ctx, wafTestRegion, stage)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "GetWebACLForResource" {
				if saved == nil || saved.ARN != allowed.ARN {
					t.Fatal("authorized association not preserved", saved)
				}
			} else if saved != nil {
				t.Fatal("authorized association was not removed", saved)
			}
		})
	}
}

func TestWAFAssociationAuthorizationFailuresPreserveState(t *testing.T) {
	store, stage := wafFixture(t)
	ctx := context.Background()
	summary, err := store.Create(ctx, wafTestRegion, wafTestACL())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Associate(ctx, wafTestRegion, stage, summary.ARN); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"GetWebACLForResource", "DisassociateWebACL"} {
		for _, deniedAction := range []string{"wafv2:" + operation, "wafv2:GetWebACL", "apigateway:SetWebACL", "nil"} {
			if operation == "GetWebACLForResource" && deniedAction == "apigateway:SetWebACL" || operation == "DisassociateWebACL" && deniedAction == "wafv2:GetWebACL" {
				continue
			}
			t.Run(operation+"/"+deniedAction, func(t *testing.T) {
				var authorize func(string, string) bool
				if deniedAction != "nil" {
					authorize = func(action, resource string) bool { return action != deniedAction }
				}
				response := wafCall(store, operation, wafJSON(t, map[string]string{"ResourceArn": stage}), authorize)
				if response.Code != 403 {
					t.Fatal(response.Code, response.Body.String())
				}
				acl, err := store.ForResource(ctx, wafTestRegion, stage)
				if err != nil || acl == nil || acl.ARN != summary.ARN {
					t.Fatal("denied request changed state", acl, err)
				}
			})
		}
	}
	for _, remove := range []bool{false, true} {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := store.resourceOperation(canceled, wafTestRegion, stage, remove, wafAllow); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := store.resourceOperation(ctx, wafTestRegion, "invalid", remove, wafAllow); iamCode(err) != "WAFInvalidParameterException" {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_waf_disassociate BEFORE DELETE ON aws_wafv2_associations BEGIN SELECT RAISE(ABORT,'test delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	response := wafCall(store, "DisassociateWebACL", wafJSON(t, map[string]string{"ResourceArn": stage}), wafAllow)
	if response.Code != 500 {
		t.Fatal(response.Code, response.Body.String())
	}
	acl, err := store.ForResource(ctx, wafTestRegion, stage)
	if err != nil || acl == nil || acl.ARN != summary.ARN {
		t.Fatal("failed delete changed state", acl, err)
	}
	if _, err := store.db.Exec("DROP TRIGGER reject_waf_disassociate"); err != nil {
		t.Fatal(err)
	}
	if err := store.Disassociate(ctx, wafTestRegion, stage); err != nil {
		t.Fatal(err)
	}
	response = wafCall(store, "DisassociateWebACL", wafJSON(t, map[string]string{"ResourceArn": stage}), wafAllow)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
}
