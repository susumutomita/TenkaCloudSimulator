package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func kmsGrantCall(t *testing.T, g *Gateway, op string, body any, status int, code string) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	w, result := kmsHTTP(t, g, op, string(data), "")
	if w.Code != status || code != "" && string(result["__type"]) != fmt.Sprintf("%q", code) {
		t.Fatalf("%s expected %d %s, received %d %s", op, status, code, w.Code, result["__type"])
	}
	return result
}

func TestKMSNativeGrantsIdempotencyPagingAndRefusals(t *testing.T) {
	db, store := kmsTestStore(t, "")
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(t.Context(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateKey(t.Context(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{KMS: store, Identities: core.NewIdentities(db)}
	args := map[string]any{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "practice"}
	create := func() map[string]json.RawMessage { return kmsGrantCall(t, g, "CreateGrant", args, 200, "") }
	first := create()
	again := create()
	if string(first["GrantId"]) != string(again["GrantId"]) || string(first["GrantToken"]) == string(again["GrantToken"]) || len(first) != 2 {
		t.Fatal("named retry or result members")
	}
	delete(args, "Name")
	unnamed := create()
	another := create()
	if string(unnamed["GrantId"]) == string(another["GrantId"]) {
		t.Fatal("distinct unnamed grants coalesced")
	}
	for _, bad := range []map[string]any{
		{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"Decrypt"}},
		{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey", "DescribeKey"}},
		{"KeyId": "alias/practice", "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}},
		{"KeyId": key.KeyID, "GranteePrincipal": "arn:aws:iam::111111111111:role/reader", "Operations": []string{"DescribeKey"}},
		{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "DryRun": true},
	} {
		kmsGrantCall(t, g, "CreateGrant", bad, 400, "ValidationException")
	}
	missing := map[string]any{"KeyId": key.KeyID, "GranteePrincipal": kmsGrantRolePrefix + "absent", "Operations": []string{"DescribeKey"}}
	kmsGrantCall(t, g, "CreateGrant", missing, 400, "NotFoundException")
	list := map[string]any{"KeyId": key.Arn, "Limit": 1}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		page := kmsGrantCall(t, g, "ListGrants", list, 200, "")
		var grants []map[string]any
		if err = json.Unmarshal(page["Grants"], &grants); err != nil || len(grants) != 1 {
			t.Fatal("wrong page length", err)
		}
		entry := grants[0]
		id, _ := entry["GrantId"].(string)
		if !kmsGrantID.MatchString(id) || seen[id] || entry["KeyId"] != key.Arn || entry["GranteePrincipal"] != role.Arn || entry["GrantToken"] != nil || entry["CreationDate"] == nil {
			t.Fatal("invalid grant entry")
		}
		seen[id] = true
		if i < 2 {
			var marker string
			if err = json.Unmarshal(page["NextMarker"], &marker); err != nil || string(page["Truncated"]) != "true" {
				t.Fatal("missing continuation", err)
			}
			kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": other.KeyID, "Limit": 1, "Marker": marker}, 400, "InvalidMarkerException")
			kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.KeyID, "Limit": 2, "Marker": marker}, 400, "InvalidMarkerException")
			list["Marker"] = marker
		} else if _, ok := page["NextMarker"]; ok || string(page["Truncated"]) != "false" {
			t.Fatal("unexpected final marker")
		}
	}
	kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.KeyID, "Limit": 101}, 400, "ValidationException")
	kmsGrantCall(t, g, "RevokeGrant", map[string]any{"KeyId": other.KeyID, "GrantId": first["GrantId"]}, 400, "NotFoundException")
	if err = store.SetEnabled(t.Context(), "us-east-1", key.KeyID, false); err != nil {
		t.Fatal(err)
	}
	kmsGrantCall(t, g, "CreateGrant", args, 400, "DisabledException")
	found := kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.KeyID, "GrantId": first["GrantId"], "GranteePrincipal": role.Arn}, 200, "")
	var one []kmsGrantEntry
	if err = json.Unmarshal(found["Grants"], &one); err != nil || len(one) != 1 || one[0].Name != "practice" {
		t.Fatal("filtered grant read", err)
	}
	kmsGrantCall(t, g, "RevokeGrant", map[string]any{"KeyId": key.KeyID, "GrantId": first["GrantId"]}, 200, "")
	after := kmsGrantCall(t, g, "ListGrants", map[string]any{"KeyId": key.KeyID, "GrantId": first["GrantId"]}, 200, "")
	if string(after["Grants"]) != "[]" {
		t.Fatal("revoked grant still present")
	}
	kmsGrantCall(t, g, "RetireGrant", map[string]any{"KeyId": key.KeyID, "GrantId": first["GrantId"]}, 501, "UnknownOperationException")
}

func TestKMSGrantRoleGenerationAndEffectiveDecision(t *testing.T) {
	db, store := kmsTestStore(t, "")
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(t.Context(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{KMS: store, Identities: core.NewIdentities(db)}
	identity := SDKIdentity{Principal: role.RoleName, ARN: "arn:aws:sts::" + kmsAccount + ":assumed-role/reader/run", Account: kmsAccount, UserID: role.RoleID + ":run", Expires: time.Now().Add(time.Hour)}
	read := func(actor SDKIdentity, policy core.Policy, tokens []string, want string) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", nil)
		ctx := context.WithValue(r.Context(), sdkIdentityContextKey{}, actor)
		r = r.WithContext(context.WithValue(ctx, sdkPolicyContextKey{}, policy))
		tx, err := db.Database().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		v, err := kmsDescribeAsRole(httptest.NewRecorder(), r, tx, "us-east-1", key.KeyID, kmsRequest{GrantTokens: tokens})
		if want != "" {
			kmsWantCode(t, err, want)
			if v != nil {
				t.Fatal("metadata on refusal")
			}
			return
		}
		if err != nil || v.(map[string]any)["KeyMetadata"].(KMSKeyMetadata).Arn != key.Arn {
			t.Fatal("expected actual key metadata", err)
		}
	}
	empty := core.Policy{}
	explicit, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"kms:DescribeKey","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	direct, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"kms:DescribeKey","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	read(identity, empty, nil, "AccessDeniedException")
	read(identity, direct, nil, "")
	grant := kmsGrantCall(t, g, "CreateGrant", map[string]any{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "stable"}, 200, "")
	retry := kmsGrantCall(t, g, "CreateGrant", map[string]any{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "stable"}, 200, "")
	var token, token2 string
	if json.Unmarshal(grant["GrantToken"], &token) != nil || json.Unmarshal(retry["GrantToken"], &token2) != nil {
		t.Fatal("missing tokens")
	}
	read(identity, empty, nil, "")
	read(identity, empty, []string{token, token2}, "")
	read(identity, explicit, nil, "AccessDeniedException")
	read(identity, empty, []string{"invalid"}, "InvalidGrantTokenException")
	if err := roles.Delete(t.Context(), role.RoleName, role.RoleID); err != nil {
		t.Fatal(err)
	}
	newRole, err := roles.Create(t.Context(), role.RoleName, stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	read(identity, direct, nil, "AccessDeniedException")
	identity.UserID = newRole.RoleID + ":run"
	read(identity, empty, nil, "AccessDeniedException")
	kmsGrantCall(t, g, "RevokeGrant", map[string]any{"KeyId": key.KeyID, "GrantId": grant["GrantId"]}, 200, "")
	read(identity, empty, []string{token}, "AccessDeniedException")
}

func TestKMSSignedGrantAdmission(t *testing.T) {
	const key = "TCSIMASIAEXAMPLE"
	for _, tc := range []struct {
		path, media, target, key string
		allowed                  bool
	}{
		{"/", "application/x-amz-json-1.1", "TrentService.DescribeKey", key, true}, {"/", "application/x-amz-json-1.1", "TrentService.Encrypt", key, false}, {"/", "application/x-amz-json-1.1", "TrentService.DescribeKey", "TCSIMOPERATOR", false}, {"/?q=1", "application/x-amz-json-1.1", "TrentService.DescribeKey", key, false}, {"/_simulator/providers/aws/", "application/x-amz-json-1.1", "TrentService.DescribeKey", key, false}, {"/", "text/plain", "TrentService.DescribeKey", key, false},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", tc.media)
		r.Header.Set("X-Amz-Target", tc.target)
		if got := kmsSignedDescribeRequest(r, tc.key); got != tc.allowed {
			t.Fatal("signed scope mismatch", tc.path, tc.target)
		}
	}
}

func TestKMSGrantManagementFramesAndKeyPermissions(t *testing.T) {
	db, store := kmsTestStore(t, "")
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "frame", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.CreateKey(t.Context(), "us-east-1", "frame")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}}
	call := func(op string, input any, allow bool, ctx context.Context) (*httptest.ResponseRecorder, map[string]json.RawMessage) {
		t.Helper()
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("POST", "/", strings.NewReader(string(data))).WithContext(ctx)
		request.Header.Set("X-Amz-Target", "TrentService."+op)
		request.Header.Set("Content-Type", "application/x-amz-json-1.1")
		rec := httptest.NewRecorder()
		count := 0
		store.ServeHTTP(rec, request, "us-east-1", func(action, resource string) bool {
			count++
			if action != "kms:"+op || resource != key.Arn {
				t.Fatal("incorrect authorization target", action, resource)
			}
			return allow
		})
		if count != 1 {
			t.Fatalf("expected one authorization: %d", count)
		}
		result := map[string]json.RawMessage{}
		if rec.Body.Len() > 0 {
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return rec, result
	}
	for _, tc := range []struct {
		op   string
		args any
	}{{"CreateGrant", body}, {"ListGrants", map[string]any{"KeyId": key.Arn}}, {"RevokeGrant", map[string]any{"KeyId": key.KeyID, "GrantId": strings.Repeat("a", 64)}}} {
		result, v := call(tc.op, tc.args, false, t.Context())
		if result.Code != 403 || string(v["__type"]) != `"AccessDeniedException"` {
			t.Fatal("management denial", tc.op, result.Code)
		}
	}
	count := func() int {
		var n int
		if err := db.Database().QueryRow("SELECT count(*) FROM aws_kms_grants").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("denied request wrote a grant")
	}
	if _, err = db.Database().Exec("CREATE TEMP TRIGGER fail_grant BEFORE INSERT ON aws_kms_grants BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	r, v := call("CreateGrant", body, true, t.Context())
	if r.Code != 500 || v["GrantToken"] != nil || count() != 0 {
		t.Fatal("failed write acknowledged a grant")
	}
	if _, err = db.Database().Exec("DROP TRIGGER fail_grant"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, v = call("CreateGrant", body, true, ctx)
	if r.Code != 500 || v["GrantToken"] != nil || count() != 0 {
		t.Fatal("canceled request acknowledged a grant")
	}
	r, v = call("CreateGrant", body, true, t.Context())
	if r.Code != 200 || count() != 1 {
		t.Fatal("expected a committed grant")
	}
	if _, err = db.Database().Exec("CREATE TEMP TRIGGER fail_grant_delete BEFORE DELETE ON aws_kms_grants BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	r, _ = call("RevokeGrant", map[string]any{"KeyId": key.Arn, "GrantId": v["GrantId"]}, true, t.Context())
	if r.Code != 500 || count() != 1 {
		t.Fatal("failed revoke changed state")
	}
}

func TestKMSGrantWriteReadbackAndSiblingFrame(t *testing.T) {
	for _, scenario := range []string{"insert-removed", "insert-changed", "sibling-changed", "delete-restored", "named-retry-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			db, store := kmsTestStore(t, "")
			roles, err := NewIAMRoleStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			role, err := roles.Create(t.Context(), "reader", stsRootTrust, "", 3600)
			if err != nil {
				t.Fatal(err)
			}
			key, err := store.CreateKey(t.Context(), "us-east-1", "")
			if err != nil {
				t.Fatal(err)
			}
			g := &Gateway{KMS: store, Identities: core.NewIdentities(db)}
			args := map[string]any{"KeyId": key.KeyID, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}, "Name": "kept"}
			kmsGrantCall(t, g, "CreateGrant", args, 200, "")
			args["Name"] = "tested"
			op := "CreateGrant"
			sql := ""
			switch scenario {
			case "insert-removed":
				sql = "CREATE TEMP TRIGGER change_grant AFTER INSERT ON aws_kms_grants BEGIN DELETE FROM aws_kms_grants WHERE grant_id=NEW.grant_id; END"
			case "insert-changed":
				sql = "CREATE TEMP TRIGGER change_grant AFTER INSERT ON aws_kms_grants BEGIN UPDATE aws_kms_grants SET created=NEW.created+1 WHERE grant_id=NEW.grant_id; END"
			case "sibling-changed":
				sql = "CREATE TEMP TRIGGER change_grant AFTER INSERT ON aws_kms_grants BEGIN UPDATE aws_kms_grants SET name='unexpected' WHERE name='kept'; END"
			case "delete-restored":
				original := kmsGrantCall(t, g, "CreateGrant", args, 200, "")
				op = "RevokeGrant"
				args = map[string]any{"KeyId": key.KeyID, "GrantId": original["GrantId"]}
				sql = "CREATE TEMP TRIGGER change_grant AFTER DELETE ON aws_kms_grants BEGIN INSERT INTO aws_kms_grants VALUES(OLD.region,OLD.key_id,OLD.grant_id,OLD.role_arn,OLD.role_id,OLD.name,OLD.created); END"
			case "named-retry-mismatch":
				kmsGrantCall(t, g, "CreateGrant", args, 200, "")
				if _, err := db.Database().Exec("UPDATE aws_kms_grants SET role_arn=? WHERE name='tested'", kmsGrantRolePrefix+"other"); err != nil {
					t.Fatal(err)
				}
			}
			read := func() string {
				var snapshot string
				if err := db.Database().QueryRow("SELECT json_group_array(json_array(region,key_id,grant_id,role_arn,role_id,name,created) ORDER BY grant_id) FROM aws_kms_grants").Scan(&snapshot); err != nil {
					t.Fatal(err)
				}
				return snapshot
			}
			before := read()
			if sql != "" {
				if _, err := db.Database().Exec(sql); err != nil {
					t.Fatal(err)
				}
			}
			result := kmsGrantCall(t, g, op, args, 500, "KMSInternalException")
			if result["GrantToken"] != nil || result["GrantId"] != nil || read() != before {
				t.Fatal("failed mutation changed state or returned grant authority")
			}
			if sql != "" {
				if _, err := db.Database().Exec("DROP TRIGGER change_grant"); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Database().Exec("UPDATE aws_kms_grants SET role_arn=? WHERE name='tested'", role.Arn); err != nil {
					t.Fatal(err)
				}
			}
			kmsGrantCall(t, g, op, args, 200, "")
		})
	}
}
