package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestIAMSSMCorePinReadOnlyAndNamespace(t *testing.T) {
	ctx := context.Background()
	db, roles := managedStore(t)
	p, err := roles.ManagedPolicy(ctx, iamSSMCoreARN)
	if err != nil || p.PolicyName != iamSSMCoreName || p.DefaultVersionID != "v2" || p.PolicyID != iamSSMCoreID {
		t.Fatal(p, err)
	}
	v, err := roles.ManagedPolicyVersion(ctx, iamSSMCoreARN, "v2")
	if err != nil || v.Document != iamSSMCoreDocument || !v.IsDefaultVersion {
		t.Fatal(v, err)
	}
	sum := sha256.Sum256([]byte(v.Document))
	if hex.EncodeToString(sum[:]) != "b49d8e0a366be4eb7ea97da2ef96ad84a9a563ef6efa926c40c581c1e074b993" {
		t.Fatal("official document pin changed")
	}
	if _, err = roles.ManagedPolicyVersion(ctx, iamSSMCoreARN, "v1"); iamCode(err) != "NoSuchEntity" {
		t.Fatal("invented policy history", err)
	}
	_, versions, err := roles.ManagedPolicyVersions(ctx, iamSSMCoreARN)
	if err != nil || len(versions) != 1 {
		t.Fatal(versions, err)
	}
	before := managedFrame(t, db.Database())
	mutations := []func() error{
		func() error {
			_, e := roles.CreateManagedPolicyVersion(ctx, iamSSMCoreARN, managedAllow, true)
			return e
		},
		func() error { return roles.SetDefaultManagedPolicyVersion(ctx, iamSSMCoreARN, "v2") },
		func() error { return roles.DeleteManagedPolicyVersion(ctx, iamSSMCoreARN, "v2") },
		func() error { return roles.DeleteManagedPolicy(ctx, iamSSMCoreARN) },
	}
	for _, f := range mutations {
		if iamCode(f()) != "UnmodifiableEntity" || managedFrame(t, db.Database()) != before {
			t.Fatal("builtin mutation changed state")
		}
	}
	customer := managedPolicy(t, roles, iamSSMCoreName, managedDeny)
	if customer.Arn == p.Arn || customer.PolicyID == p.PolicyID {
		t.Fatal("namespaces merged")
	}
	list, err := roles.ManagedPolicies(ctx, "", 1001)
	if err != nil || len(list) != 1 || list[0].Arn != customer.Arn {
		t.Fatal(list, err)
	}
	for _, arn := range []string{"", strings.ToLower(iamSSMCoreARN), iamSSMCoreARN + "/", "arn:aws:iam::aws:policy/AdministratorAccess", "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"} {
		if _, err = roles.ManagedPolicy(ctx, arn); err == nil {
			t.Fatal("unknown builtin accepted", arn)
		}
	}
	if _, err = NewIAMRoleStore(db.Database()); err != nil {
		t.Fatal("idempotent initialization", err)
	}
	if err = roles.DeleteManagedPolicy(ctx, customer.Arn); err != nil {
		t.Fatal(err)
	}
	if _, err = roles.ManagedPolicyVersion(ctx, iamSSMCoreARN, "v2"); err != nil {
		t.Fatal("customer mutation damaged builtin", err)
	}
}

func TestIAMSSMCoreNativePermissionsRevocationAndGeneration(t *testing.T) {
	ctx := context.Background()
	g := ssmValueGateway(t)
	roles := &IAMRoleStore{g.Store.Database()}
	g.Roles = roles // Bind the native role store for actual STS policy snapshots.
	role := managedRole(t, roles, "SSMReader")
	peer := managedRole(t, roles, "Peer")
	ssmValuePut(t, g, "us-east-1", "/practice/core", "String", "dummy")
	call := func(action, body string, status int) {
		t.Helper()
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-amz-json-1.1")
		req.Header.Set("X-Amz-Target", "AmazonSSM."+action)
		req.Header.Set("X-Simulator-Principal", role.RoleName)
		req.Header.Set("X-Simulator-Region", "us-east-1")
		out := httptest.NewRecorder()
		g.ServeHTTP(out, req)
		if out.Code != status {
			t.Fatalf("%s: %d %s", action, out.Code, out.Body.String())
		}
	}
	call("GetParameter", `{"Name":"/practice/core"}`, 403)
	if err := roles.AttachRolePolicy(ctx, "ssmreader", iamSSMCoreARN, role.RoleID); err != nil {
		t.Fatal(err)
	}
	call("GetParameter", `{"Name":"/practice/core"}`, 200)
	call("GetParameters", `{"Names":["/practice/core"]}`, 200)
	call("PutParameter", `{"Name":"/practice/other","Type":"String","Value":"dummy"}`, 403)
	call("GetParametersByPath", `{"Path":"/practice"}`, 403)
	// Principal headers alone cannot mint the native assumed-role binding.
	call("SendCommand", `{}`, 403)
	call("StartSession", `{}`, 403)
	// This gateway unit test installs the verifier's private context from a real
	// locally issued STS session. Signature verification is covered separately.
	authority := NewSTSAuthority("local-builtin-test")
	credentials, err := authority.Issue(STSSessionRequest{
		Principal: role.RoleName, RoleARN: role.Arn, RoleID: role.RoleID,
		SessionName: "builtin-regression", Duration: time.Hour,
		Snapshot: func(ctx context.Context) (core.Policy, error) { return g.stsSessionPolicy(ctx, role) },
		Validate: func(ctx context.Context) error {
			current, e := roles.Get(ctx, role.RoleName)
			if e != nil {
				return e
			}
			if current.RoleID != role.RoleID {
				return fmt.Errorf("role generation changed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := authority.sessions[credentials.AccessKeyID]
	policy, err := session.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"ssm:SendCommand", "ssm:StartSession"} {
		if decision := core.EvaluateWithContext(policy, action, "*", core.PolicyContext{"aws:userid": session.identity.UserID}); decision != "implicitDeny" {
			t.Fatalf("SSMCore unexpectedly grants %s: %s", action, decision)
		}
	}
	verifiedCall := func(action string, status int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set("X-Amz-Target", "AmazonSSM."+action)
		c := context.WithValue(r.Context(), stsAuthorityContextKey{}, authority)
		c = context.WithValue(c, sdkIdentityContextKey{}, session.identity)
		c = context.WithValue(c, sdkSessionKeyContextKey{}, credentials.AccessKeyID)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r.WithContext(c))
		if w.Code != status {
			t.Fatalf("verified %s: %d %s", action, w.Code, w.Body.String())
		}
	}
	verifiedCall("SendCommand", 501)
	verifiedCall("StartSession", 501)
	call("DescribeAssociation", `{}`, 501)
	if got, e := g.Identities.Decision(peer.RoleName, "ssm:GetParameter", "*"); e != nil || got != "implicitDeny" {
		t.Fatal(got, e)
	}
	deny := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"ssm:GetParameter","Resource":"*"}]}`
	if err := roles.PutPolicy(ctx, role.RoleName, "Deny", deny, role.RoleID); err != nil {
		t.Fatal(err)
	}
	call("GetParameter", `{"Name":"/practice/core"}`, 403)
	if err := roles.DeletePolicy(ctx, role.RoleName, "Deny", role.RoleID); err != nil {
		t.Fatal(err)
	}
	call("GetParameter", `{"Name":"/practice/core"}`, 200)
	if err := roles.DetachRolePolicy(ctx, role.RoleName, iamSSMCoreARN, role.RoleID); err != nil {
		t.Fatal(err)
	}
	call("GetParameter", `{"Name":"/practice/core"}`, 403)
	if err := roles.Delete(ctx, role.RoleName, role.RoleID); err != nil {
		t.Fatal(err)
	}
	verifiedCall("SendCommand", 403)
	verifiedCall("StartSession", 403)
	fresh := managedRole(t, roles, role.RoleName)
	verifiedCall("SendCommand", 403)
	verifiedCall("StartSession", 403)
	if err := roles.AttachRolePolicy(ctx, fresh.RoleName, iamSSMCoreARN, role.RoleID); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
	if err := roles.AttachRolePolicy(ctx, fresh.RoleName, iamSSMCoreARN, fresh.RoleID); err != nil {
		t.Fatal(err)
	}
	if err := roles.DetachRolePolicy(ctx, fresh.RoleName, iamSSMCoreARN, role.RoleID); iamCode(err) != "NoSuchEntity" {
		t.Fatal(err)
	}
}

func TestIAMSSMCoreQueryRoundtripAndSameNamePagination(t *testing.T) {
	p := newManagedQueryProbe(t)
	p.role("Reader")
	local := p.create(iamSSMCoreName)
	body := p.call("GetPolicyVersion", "", url.Values{"PolicyArn": {iamSSMCoreARN}, "VersionId": {"v2"}}, 200, "<VersionId>v2</VersionId>")
	var parsed struct {
		Result struct {
			Version IAMManagedPolicyVersion `xml:"PolicyVersion"`
		} `xml:"GetPolicyVersionResult"`
	}
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}
	doc, err := url.QueryUnescape(parsed.Result.Version.Document)
	if err != nil || doc != iamSSMCoreDocument {
		t.Fatal("document roundtrip", err, doc)
	}
	for _, arn := range []string{iamSSMCoreARN, local.Arn} {
		p.call("AttachRolePolicy", "", url.Values{"RoleName": {"reader"}, "PolicyArn": {arn}}, 200, "AttachRolePolicyResponse")
	}
	body = p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"Reader"}, "MaxItems": {"1"}}, 200, iamSSMCoreARN)
	marker := queryMarker(t, body, "ListAttachedRolePoliciesResult")
	body = p.call("ListAttachedRolePolicies", "", url.Values{"RoleName": {"Reader"}, "MaxItems": {"1"}, "Marker": {marker}}, 200, local.Arn)
	if strings.Contains(body, iamSSMCoreARN) {
		t.Fatal("pagination repeated builtin")
	}
	p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {"1"}}, 200, local.Arn)
	p.call("ListPolicies", "", url.Values{"Scope": {"AWS"}}, 501, "NotImplemented")
	p.call("ListPolicies", "", nil, 501, "NotImplemented")
	for _, action := range []string{"DeletePolicy", "SetDefaultPolicyVersion", "DeletePolicyVersion", "CreatePolicyVersion"} {
		fields := url.Values{"PolicyArn": {iamSSMCoreARN}}
		if action == "SetDefaultPolicyVersion" || action == "DeletePolicyVersion" {
			fields.Set("VersionId", "v2")
		}
		if action == "CreatePolicyVersion" {
			fields.Set("PolicyDocument", managedAllow)
		}
		p.call(action, "", fields, 400, "UnmodifiableEntity")
	}
}

func TestIAMSSMCorePersistedCorruptionIsNotRepaired(t *testing.T) {
	for _, sql := range []string{
		`UPDATE aws_iam_managed_policies SET policy_id='ANPAFFFFFFFFFFFFFFFF' WHERE name='/aws/AmazonSSMManagedInstanceCore'`,
		`UPDATE aws_iam_managed_policies SET description='foreign' WHERE name='/aws/AmazonSSMManagedInstanceCore'`,
		`UPDATE aws_iam_managed_policies SET deleted=1 WHERE name='/aws/AmazonSSMManagedInstanceCore'`,
		`UPDATE aws_iam_managed_policy_versions SET document='{}' WHERE policy_name='/aws/AmazonSSMManagedInstanceCore'`,
		`DELETE FROM aws_iam_managed_policy_versions WHERE policy_name='/aws/AmazonSSMManagedInstanceCore'`,
	} {
		t.Run(sql, func(t *testing.T) {
			db, roles := managedStore(t)
			// Changing a FK-referenced ID is unnecessary; there are no attachments here.
			if strings.Contains(sql, "SET policy_id") {
				profileSQL(t, db.Database(), `DELETE FROM aws_iam_managed_policy_versions WHERE policy_name='/aws/AmazonSSMManagedInstanceCore'`)
			}
			profileSQL(t, db.Database(), sql)
			before := managedFrame(t, db.Database())
			if _, err := roles.ManagedPolicy(context.Background(), iamSSMCoreARN); err == nil {
				t.Fatal("corrupt pin readable")
			}
			if _, err := NewIAMRoleStore(db.Database()); err == nil {
				t.Fatal("corrupt pin repaired")
			}
			if managedFrame(t, db.Database()) != before {
				t.Fatal("initialization overwrote persisted state")
			}
		})
	}
}

func TestIAMSSMCoreDoesNotConsumeCustomerQuota(t *testing.T) {
	db, roles := managedStore(t)
	// Populate retired customer claims through a bounded fixture, as in existing
	// customer quota tests. The builtin must not consume one of the 1000 claims.
	profileSQL(t, db.Database(), `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO aws_iam_managed_policies SELECT 'retired-'||x,'ANPAFFFFFFFFFFFFFFFF','/','','2020-01-01T00:00:00Z','2020-01-01T00:00:00Z',1,2,1 FROM n`)
	managedPolicy(t, roles, iamSSMCoreName, managedAllow)
	if _, err := roles.CreateManagedPolicy(context.Background(), "overflow", "/", "", managedAllow); iamCode(err) != "LimitExceeded" {
		t.Fatal(err)
	}
	list, err := roles.ManagedPolicies(context.Background(), "", 1001)
	if err != nil || len(list) != 1 || list[0].Arn != iamManagedPrefix+iamSSMCoreName {
		t.Fatal(list, err)
	}
}

func TestIAMSSMCoreLocalPaginationUnchanged(t *testing.T) {
	p := newManagedQueryProbe(t)
	a := p.create(iamSSMCoreName)
	b := p.create("Zulu")
	body := p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {"1"}}, 200, a.Arn)
	if strings.Contains(body, iamSSMCoreARN) {
		t.Fatal("builtin leaked into local list")
	}
	marker := queryMarker(t, body, "ListPoliciesResult")
	body = p.call("ListPolicies", "", url.Values{"Scope": {"Local"}, "MaxItems": {"1"}, "Marker": {marker}}, 200, b.Arn)
	if strings.Contains(body, a.Arn) || strings.Contains(body, iamSSMCoreARN) {
		t.Fatal("customer marker altered")
	}
}

func TestIAMSSMCoreNativeAttachmentACK(t *testing.T) {
	for name, statement := range map[string]string{
		"ignored":         `CREATE TRIGGER fault BEFORE INSERT ON aws_iam_role_managed_policies BEGIN SELECT RAISE(IGNORE); END`,
		"undone":          `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_managed_policies BEGIN DELETE FROM aws_iam_role_managed_policies WHERE role_name=NEW.role_name; END`,
		"inline-injected": `CREATE TRIGGER fault AFTER INSERT ON aws_iam_role_managed_policies BEGIN INSERT INTO aws_iam_role_policies VALUES(NEW.role_name,'Injected','{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			db, roles := managedStore(t)
			role := managedRole(t, roles, "Reader")
			profileSQL(t, db.Database(), statement)
			before := managedFrame(t, db.Database())
			if err := roles.AttachRolePolicy(context.Background(), role.RoleName, iamSSMCoreARN, role.RoleID); err == nil || managedFrame(t, db.Database()) != before {
				t.Fatal("unexpected attachment acknowledged", err)
			}
		})
	}
}

// These are injected-write controls, not claims of externally reachable SQL.
func ssmCorePinFaults() map[string]string {
	return map[string]string{
		"delete-version":   `DELETE FROM aws_iam_managed_policy_versions WHERE policy_name='/aws/AmazonSSMManagedInstanceCore';`,
		"corrupt-document": `UPDATE aws_iam_managed_policy_versions SET document='{}' WHERE policy_name='/aws/AmazonSSMManagedInstanceCore';`,
		"corrupt-metadata": `UPDATE aws_iam_managed_policies SET description='fault' WHERE name='/aws/AmazonSSMManagedInstanceCore';`,
		"delete-pin":       `DELETE FROM aws_iam_managed_policy_versions WHERE policy_name='/aws/AmazonSSMManagedInstanceCore'; DELETE FROM aws_iam_managed_policies WHERE name='/aws/AmazonSSMManagedInstanceCore';`,
	}
}

func TestIAMSSMCoreDetachPreservesTargetPin(t *testing.T) {
	for name, fault := range ssmCorePinFaults() {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, roles := managedStore(t)
			role := managedRole(t, roles, "Reader")
			if err := roles.AttachRolePolicy(ctx, role.RoleName, iamSSMCoreARN, role.RoleID); err != nil {
				t.Fatal(err)
			}
			profileSQL(t, db.Database(), `CREATE TRIGGER fault AFTER DELETE ON aws_iam_role_managed_policies WHEN OLD.policy_name='/aws/AmazonSSMManagedInstanceCore' BEGIN `+fault+` END`)
			before := cleanupACKFrame(t, db.Database())
			if err := roles.DetachRolePolicy(ctx, role.RoleName, iamSSMCoreARN, role.RoleID); err == nil {
				t.Fatal("detach acknowledged damaged target pin")
			}
			if cleanupACKFrame(t, db.Database()) != before {
				t.Fatal("detach failure did not roll back all state")
			}
			profileSQL(t, db.Database(), "DROP TRIGGER fault")
			if err := roles.DetachRolePolicy(ctx, role.RoleName, iamSSMCoreARN, role.RoleID); err != nil {
				t.Fatal("clean detach after rollback", err)
			}
			p, err := roles.ManagedPolicy(ctx, iamSSMCoreARN)
			if err != nil || p.AttachmentCount != 0 {
				t.Fatal(p, err)
			}
		})
	}
}
