package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

func TestKMSNativeDeadlineAfterLifecycleWrite(t *testing.T) {
	for _, test := range []struct {
		op, table string
		change    sqlite3.AuthorizerActionCode
	}{
		{"CreateGrant", "aws_kms_grants", sqlite3.AUTH_INSERT},
		{"RevokeGrant", "aws_kms_grants", sqlite3.AUTH_DELETE},
		{"ScheduleKeyDeletion", "aws_kms_pending_deletions", sqlite3.AUTH_INSERT},
		{"CancelKeyDeletion", "aws_kms_pending_deletions", sqlite3.AUTH_DELETE},
	} {
		t.Run(test.op, func(t *testing.T) {
			f := newKMSDeletionTeam(t)
			g := &Gateway{KMS: f.store}
			fields := map[string]any{"KeyId": f.target.Arn}
			switch test.op {
			case "CreateGrant":
				fields["GranteePrincipal"] = kmsGrantRolePrefix + "deletion-reader"
				fields["Operations"] = []string{"DescribeKey"}
			case "RevokeGrant":
				fields["GrantId"] = f.targetGrant
			case "ScheduleKeyDeletion":
				fields["PendingWindowInDays"] = 7
			case "CancelKeyDeletion":
				kmsGrantCall(t, g, "ScheduleKeyDeletion", fields, 200, "")
			}
			body, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			before := kmsAliasDatabaseFrame(t, f.store.db)
			var requestContext context.Context
			observed := 0
			liveAtWrite := false
			kmsDeletionConnection(t, f.store.db, func(conn *sqlite3.Conn) error {
				conn.UpdateHook(func(action sqlite3.AuthorizerActionCode, _, table string, _ int64) {
					if table == test.table && action == test.change {
						observed++
						liveAtWrite = requestContext.Err() == nil
						<-requestContext.Done()
					}
				})
				return nil
			})
			// Let the real request timer expire inside SQLite, after its target row
			// has changed. The handler and database/sql still own commit/rollback.
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			requestContext = ctx
			request := httptest.NewRequestWithContext(ctx, "POST", "/", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/x-amz-json-1.1")
			request.Header.Set("X-Amz-Target", "TrentService."+test.op)
			out := httptest.NewRecorder()
			g.ServeHTTP(out, request)
			kmsDeletionConnection(t, f.store.db, func(conn *sqlite3.Conn) error { conn.UpdateHook(nil); return nil })
			if observed != 1 || !liveAtWrite || ctx.Err() != context.DeadlineExceeded {
				t.Fatal("the real deadline did not expire after the addressed SQLite change")
			}
			var failure map[string]string
			if out.Code != 500 || json.Unmarshal(out.Body.Bytes(), &failure) != nil ||
				len(failure) != 2 || failure["__type"] != "KMSInternalException" || failure["message"] == "" {
				t.Fatal("timed-out operation returned an unexpected acknowledgment")
			}
			if kmsAliasDatabaseFrame(t, f.store.db) != before {
				t.Fatal("timed-out transaction changed committed key, grant, alias or deadline state")
			}
			retry := kmsGrantCall(t, g, test.op, fields, 200, "")
			f.checkDependencies(t, f.peer, "alias/deletion-peer", f.peerGrant, true)
			var grants []kmsGrantEntry
			response := kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": f.target.Arn}, 200, "")
			if err := json.Unmarshal(response["Grants"], &grants); err != nil {
				t.Fatal(err)
			}
			switch test.op {
			case "CreateGrant":
				var id, token string
				if json.Unmarshal(retry["GrantId"], &id) != nil || json.Unmarshal(retry["GrantToken"], &token) != nil ||
					id == f.targetGrant || id == "" || token == "" || len(grants) != 2 ||
					(grants[0].GrantId != id && grants[1].GrantId != id) {
					t.Fatal("retry did not create one fresh grant")
				}
			case "RevokeGrant":
				if len(grants) != 0 {
					t.Fatal("revocation retry did not remove the addressed grant")
				}
			default:
				var key KMSKeyMetadata
				describe := kmsGrantCall(t, g, "DescribeKey", map[string]any{"KeyId": f.target.Arn}, 200, "")
				if json.Unmarshal(describe["KeyMetadata"], &key) != nil || key.Arn != f.target.Arn {
					t.Fatal("could not read back the addressed key")
				}
				want := "PendingDeletion"
				if test.op == "CancelKeyDeletion" {
					want = "Disabled"
				}
				if key.KeyState != want || len(grants) != 1 || grants[0].GrantId != f.targetGrant {
					t.Fatal("retry did not commit the expected key state while retaining its grant")
				}
			}
		})
	}
}
