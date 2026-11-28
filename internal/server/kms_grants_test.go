package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestKMSSignedDescribeThroughNativeAccess(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	keys, err := aws.NewKMSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	roles, err := aws.NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	const trust = `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sts:AssumeRole","Principal":{"AWS":"arn:aws:iam::999999999999:root"}}}`
	role, err := roles.Create(t.Context(), "grant-reader", trust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.CreateKey(t.Context(), "us-east-1", "signed read")
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccess(&aws.Gateway{KMS: keys, Identities: core.NewIdentities(db)}, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := access.sdk.Issue(aws.STSSessionRequest{Principal: role.RoleName, RoleARN: role.Arn, RoleID: role.RoleID, SessionName: "test", Duration: 15 * time.Minute,
		Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) { return core.Policy{}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"KeyId":%q}`, key.Arn)
	inspect := func(wantStatus int, wantDecision string) {
		t.Helper()
		r := signedJSON11Session(credentials, "kms", "TrentService.DescribeKey", body)
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		if w.Code != wantStatus || w.Header().Get("X-Simulator-IAM-Decision") != wantDecision {
			t.Fatalf("expected %d/%s, got %d/%s", wantStatus, wantDecision, w.Code, w.Header().Get("X-Simulator-IAM-Decision"))
		}
		var out struct {
			Error       string `json:"__type"`
			KeyMetadata struct{ Arn string }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if wantStatus == 403 && (out.Error != "AccessDeniedException" || out.KeyMetadata.Arn != "") ||
			wantStatus == 200 && (out.Error != "" || out.KeyMetadata.Arn != key.Arn) {
			t.Fatal("incorrect signed result")
		}
	}
	inspect(403, "implicitDeny")
	input := fmt.Sprintf(`{"KeyId":%q,"GranteePrincipal":%q,"Operations":["DescribeKey"]}`, key.Arn, role.Arn)
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(input))
	r.Header.Set("X-Simulator-Token", strings.Repeat("a", 64))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "TrentService.CreateGrant")
	w := httptest.NewRecorder()
	access.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("create grant: %d", w.Code)
	}
	inspect(200, "allowed")
}
