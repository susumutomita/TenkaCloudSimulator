package aws

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestKMSGrantLookupFailuresPreserveNativeAuthority(t *testing.T) {
	db, store := kmsTestStore(t, "")
	sqlDB := db.Database()
	ctx := t.Context()
	roles, err := NewIAMRoleStore(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(ctx, "durable-reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, "us-east-1", "durable target")
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{KMS: store, Identities: core.NewIdentities(db)}
	body := map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}}
	grant := kmsGrantCall(t, g, "CreateGrant", body, 200, "")
	var id, token string
	if json.Unmarshal(grant["GrantId"], &id) != nil || json.Unmarshal(grant["GrantToken"], &token) != nil {
		t.Fatal("missing original identifiers")
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := sqlDB.Exec(query, args...); e != nil {
			t.Fatal(e)
		}
	}
	identity := SDKIdentity{Principal: role.RoleName, ARN: "arn:aws:sts::" + kmsAccount + ":assumed-role/" + role.RoleName + "/test", Account: kmsAccount, UserID: role.RoleID + ":test", Expires: time.Now().Add(time.Hour)}
	read := func(actor SDKIdentity, policy core.Policy, tokens []string, status int, code string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", nil)
		r = r.WithContext(context.WithValue(context.WithValue(ctx, sdkIdentityContextKey{}, actor), sdkPolicyContextKey{}, policy))
		tx, e := sqlDB.BeginTx(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		out, e := kmsDescribeAsRole(httptest.NewRecorder(), r, tx, "us-east-1", key.KeyID, kmsRequest{GrantTokens: tokens})
		tx.Rollback()
		if status == 200 {
			if e != nil || out.(map[string]any)["KeyMetadata"].(KMSKeyMetadata).Arn != key.Arn {
				t.Fatal("valid role read failed", e)
			}
			return
		}
		if e == nil || out != nil {
			t.Fatal("role metadata disclosed on failure")
		}
		w := httptest.NewRecorder()
		kmsWriteError(w, e)
		if w.Code != status || w.Header().Get("X-Amzn-Errortype") != code {
			t.Fatalf("wanted %d %s, got %d %s", status, code, w.Code, w.Header().Get("X-Amzn-Errortype"))
		}
	}
	list := map[string]any{"KeyId": key.Arn}
	revoke := map[string]any{"KeyId": key.Arn, "GrantId": id}
	named := map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "stable"}
	kmsGrantCall(t, g, "CreateGrant", named, 200, "")
	keep := kmsGrantCall(t, g, "ListGrants", list, 200, "")
	read(identity, core.Policy{}, []string{token}, 200, "")
	const missingKey = "11111111-1111-4111-8111-111111111111"
	for _, op := range []string{"CreateGrant", "ListGrants", "RevokeGrant"} {
		input := map[string]any{"KeyId": missingKey}
		if op == "CreateGrant" {
			input["GranteePrincipal"] = role.Arn
			input["Operations"] = []string{"DescribeKey"}
		}
		if op == "RevokeGrant" {
			input["GrantId"] = id
		}
		kmsGrantCall(t, g, op, input, 400, "NotFoundException")
	}
	kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.Arn, "GrantId": "short"}, 400, "InvalidGrantIdException")
	kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.Arn, "GranteePrincipal": "unsupported"}, 400, "ValidationException")
	kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.Arn, "Marker": token}, 400, "InvalidMarkerException")
	kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": "alias/no-grant-alias"}, 400, "ValidationException")
	kmsGrantCall(t, g, "DescribeKey", map[string]any{"KeyId": key.Arn, "GrantTokens": []string{token}}, 400, "ValidationException")
	read(identity, core.Policy{}, slices.Repeat([]string{token}, 11), 400, "ValidationException")
	read(identity, core.Policy{}, []string{strings.Repeat("r", 8193)}, 400, "InvalidGrantTokenException")
	damaged := token[:strings.LastIndex(token, ".")+1] + "bad-signature"
	read(identity, core.Policy{}, []string{damaged}, 400, "InvalidGrantTokenException")
	invalid := identity
	invalid.Account = "999999999999"
	read(invalid, core.Policy{}, nil, 403, "AccessDeniedException")
	exec("ALTER TABLE aws_iam_roles RENAME TO customer_role_outage")
	kmsGrantCall(t, g, "CreateGrant", body, 500, "KMSInternalException")
	read(identity, core.Policy{}, nil, 500, "KMSInternalException")
	exec("ALTER TABLE customer_role_outage RENAME TO aws_iam_roles")
	exec("ALTER TABLE aws_kms_grants RENAME TO customer_grant_outage")
	for _, args := range []map[string]any{body, named} {
		kmsGrantCall(t, g, "CreateGrant", args, 500, "KMSInternalException")
	}
	kmsGrantCall(t, g, "ListGrants", list, 500, "KMSInternalException")
	kmsGrantCall(t, g, "RevokeGrant", revoke, 500, "KMSInternalException")
	read(identity, core.Policy{}, nil, 500, "KMSInternalException")
	read(identity, core.Policy{}, []string{token}, 500, "KMSInternalException")
	exec("ALTER TABLE customer_grant_outage RENAME TO aws_kms_grants")
	exec("ALTER TABLE aws_kms_grant_signer RENAME TO customer_signer_outage")
	kmsGrantCall(t, g, "CreateGrant", body, 500, "KMSInternalException")
	kmsGrantCall(t, g, "ListGrants", list, 500, "KMSInternalException")
	read(identity, core.Policy{}, []string{token}, 500, "KMSInternalException")
	exec("ALTER TABLE customer_signer_outage RENAME TO aws_kms_grant_signer")
	var secret []byte
	if err = sqlDB.QueryRow("SELECT secret FROM aws_kms_grant_signer").Scan(&secret); err != nil {
		t.Fatal(err)
	}
	exec("PRAGMA ignore_check_constraints=ON")
	exec("UPDATE aws_kms_grant_signer SET secret=zeroblob(4) WHERE singleton=1")
	kmsGrantCall(t, g, "CreateGrant", body, 500, "KMSInternalException")
	read(identity, core.Policy{}, []string{token}, 500, "KMSInternalException")
	exec("UPDATE aws_kms_grant_signer SET secret=? WHERE singleton=1", secret)
	exec("PRAGMA ignore_check_constraints=OFF")
	var savedGrantTime float64
	if err = sqlDB.QueryRow("SELECT created FROM aws_kms_grants WHERE name='stable'").Scan(&savedGrantTime); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE aws_kms_grants SET created=0 WHERE name='stable'")
	corrupt := kmsGrantCall(t, g, "CreateGrant", named, 500, "KMSInternalException")
	if corrupt["GrantToken"] != nil {
		t.Fatal("corrupt named retry returned token")
	}
	exec("UPDATE aws_kms_grants SET created=? WHERE name='stable'", savedGrantTime)
	var created float64
	if err = sqlDB.QueryRow("SELECT created FROM aws_kms_grants WHERE grant_id=?", id).Scan(&created); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE aws_kms_grants SET created='unreadable' WHERE grant_id=?", id)
	out := kmsGrantCall(t, g, "ListGrants", list, 500, "KMSInternalException")
	if out["Grants"] != nil || out["NextMarker"] != nil {
		t.Fatal("partial page on a broken row")
	}
	exec("UPDATE aws_kms_grants SET created=? WHERE grant_id=?", created, id)
	after := kmsGrantCall(t, g, "ListGrants", list, 200, "")
	if string(after["Grants"]) != string(keep["Grants"]) {
		t.Fatal("recoverable outage changed committed grants")
	}
	read(identity, core.Policy{}, []string{token}, 200, "")
	kmsGrantCall(t, g, "RevokeGrant", revoke, 200, "")
	read(identity, core.Policy{}, []string{token}, 403, "AccessDeniedException")
}

func TestKMSGrantAcknowledgmentRequiresStoredReadbackAndCommit(t *testing.T) {
	db, store := kmsTestStore(t, "")
	ctx := t.Context()
	roleStore, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roleStore.Create(ctx, "checked-grant", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{KMS: store}
	create := map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}}
	saved := kmsGrantCall(t, gateway, "CreateGrant", create, 200, "")
	list := map[string]any{"KeyId": key.KeyID}
	revoke := map[string]any{"KeyId": key.Arn, "GrantId": saved["GrantId"]}
	baseline := kmsGrantCall(t, gateway, "ListGrants", list, 200, "")
	for _, op := range []string{"CreateGrant", "RevokeGrant"} {
		for _, stage := range []string{"before write", "after write", "readback", "commit"} {
			t.Run(op+"/"+stage, func(t *testing.T) {
				writePrepared, countVerified, denied := false, false, false
				snsSetAuthorizer(t, db.Database(), func(action sqlite3.AuthorizerActionCode, table, detail, _, _ string) sqlite3.AuthorizerReturnCode {
					if table == "aws_kms_grants" && (action == sqlite3.AUTH_INSERT || action == sqlite3.AUTH_DELETE) {
						writePrepared = true
					}
					count := action == sqlite3.AUTH_FUNCTION && detail == "total_changes"
					// ID predicates used by DELETE itself are also reads. Detect the later
					// confirmation SELECT independently, after its write-count verification.
					if stage == "readback" && writePrepared && count {
						countVerified = true
					}
					fail := stage == "before write" && !writePrepared && count || stage == "after write" && writePrepared && count ||
						stage == "readback" && countVerified && action == sqlite3.AUTH_READ && table == "aws_kms_grants" ||
						stage == "commit" && action == sqlite3.AUTH_TRANSACTION && strings.EqualFold(table, "COMMIT")
					if fail {
						denied = true
						return sqlite3.AUTH_DENY
					}
					return sqlite3.AUTH_OK
				})
				input := create
				if op == "RevokeGrant" {
					input = revoke
				}
				result := kmsGrantCall(t, gateway, op, input, 500, "KMSInternalException")
				snsSetAuthorizer(t, db.Database(), nil)
				if !denied || result["GrantId"] != nil || result["GrantToken"] != nil {
					t.Fatal("failed acknowledgment returned authority")
				}
				current := kmsGrantCall(t, gateway, "ListGrants", list, 200, "")
				if string(current["Grants"]) != string(baseline["Grants"]) {
					t.Fatal("grant rows changed after failed acknowledgment")
				}
			})
		}
	}
	kmsGrantCall(t, gateway, "RevokeGrant", revoke, 200, "")
	if current := kmsGrantCall(t, gateway, "ListGrants", list, 200, ""); string(current["Grants"]) != "[]" {
		t.Fatal("repair failed")
	}
}

func TestKMSGrantBoundaryAtPublishedQuota(t *testing.T) {
	db, store := kmsTestStore(t, "")
	roleStore, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roleStore.Create(t.Context(), "quota-reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(t.Context(), "us-east-1", "quota")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Database().Exec(`WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM seq WHERE i<49999)
	 INSERT INTO aws_kms_grants(region,key_id,grant_id,role_arn,role_id,name,created)
	 SELECT 'us-east-1',?,printf('%064x',i),?,?,'',1000 FROM seq`, key.KeyID, role.Arn, role.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{KMS: store}
	body := map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "last"}
	last := kmsGrantCall(t, g, "CreateGrant", body, 200, "")
	retry := kmsGrantCall(t, g, "CreateGrant", body, 200, "")
	if string(last["GrantId"]) != string(retry["GrantId"]) || string(last["GrantToken"]) == string(retry["GrantToken"]) {
		t.Fatal("incorrect named retry at quota")
	}
	body["Name"] = "next"
	kmsGrantCall(t, g, "CreateGrant", body, 400, "LimitExceededException")
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_kms_grants WHERE key_id=?", key.KeyID).Scan(&count); err != nil || count != 50000 {
		t.Fatal("quota changed committed count", err)
	}
	kmsGrantCall(t, g, "RevokeGrant", map[string]any{"KeyId": key.Arn, "GrantId": last["GrantId"]}, 200, "")
	kmsGrantCall(t, g, "CreateGrant", body, 200, "")
}
