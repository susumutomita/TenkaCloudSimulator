//go:build linux

package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestvm"
)

// Signs actual SSM wire bytes; Authenticate verifies them before Gateway sees
// any authority. No direct context injection or skipped provider auth is used.
func positiveCommandSigned(c STSCredentials, operation, body string) *http.Request {
	at := time.Now().UTC()
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Date", at.Format("20060102T150405Z"))
	r.Header.Set("X-Amz-Security-Token", c.SessionToken)
	r.Header.Set("X-Amz-Target", "AmazonSSM."+operation)
	headers := "content-type;host;x-amz-date;x-amz-security-token;x-amz-target"
	canonical := "content-type:application/x-amz-json-1.1\nhost:127.0.0.1:4566\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\nx-amz-security-token:" + c.SessionToken + "\nx-amz-target:AmazonSSM." + operation + "\n"
	digest := sha256.Sum256([]byte(body))
	request := "POST\n/\n\n" + canonical + "\n" + headers + "\n" + hex.EncodeToString(digest[:])
	hash := sha256.Sum256([]byte(request))
	date := at.Format("20060102")
	scope := date + "/us-east-1/ssm/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	key := sdkHMAC(sdkHMAC(sdkHMAC([]byte("AWS4"+c.SecretAccessKey), date), "us-east-1"), "ssm")
	signature := sdkHMAC(sdkHMAC(key, "aws4_request"), toSign)
	r.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%x", c.AccessKeyID, scope, headers, signature))
	return r
}

func TestSignedNativeCommandCanonicalMemoryRoundTrip(t *testing.T) {
	var owner EC2GuestOwner
	var props map[string]any
	b, leases, coordinator, _, _, _, ctx := positiveCommandVMFixture(t, func(db *sql.DB) EC2GuestOwner {
		iam, err := NewIAMRoleStore(db)
		if err != nil {
			t.Fatal(err)
		}
		profile := profileCreate(t, iam, "RoundTripProfile")
		if _, err = NewStackStore(db); err != nil {
			t.Fatal(err)
		}
		owner = leaseOwner()
		owner.ProfileName = profile.InstanceProfileName
		owner.ProfileID = profile.InstanceProfileID
		props = map[string]any{"ImageId": "ami-local", "InstanceType": "t3.micro", "IamInstanceProfile": owner.ProfileName, "SubnetId": "subnet-test", "SecurityGroupIds": []any{"sg-test"}, "UserData": "dHJ1ZQ==", "Tags": []any{map[string]any{"Key": "Team", "Value": "one"}}}
		owner.Instance, err = compileStackInstance(owner.Deployment, "VM", props, map[string]string{})
		if err != nil {
			t.Fatal(err)
		}
		return owner
	})
	peer := &canonicalCommandPeer{}
	b.ops.agent = func(call context.Context, _ *guestvm.Prepared) (io.Closer, error) { return peer.client(t, call), nil }
	lease, err := leases.Acquire(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = leases.Reconcile(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	passiveCtx, cancel := context.WithDeadline(ctx, time.Now().Add(4*time.Second))
	t.Cleanup(cancel)
	receipt, err := coordinator.LaunchVMAndAttach(ctx, ctx, lease, b.Prepared(), b.descriptor, &attachmentPassive{ctx: passiveCtx, cancel: cancel}, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipt.Seal(ctx); _ = receipt.WaitCleanup(ctx) })
	plan := EC2InstancePlan{Region: owner.Region, StackID: owner.Deployment, InstanceID: owner.Instance, canonical: "signed-round-trip-fixture"}
	runtime := &EC2InstanceRuntime{db: b.db, retained: map[string]*ec2InstanceRuntimeEntry{owner.Instance: {plan: plan, backend: b, receipt: receipt}}}
	if _, err = b.db.Exec(`CREATE TABLE aws_ec2_instance_runtime(region TEXT,stack_id TEXT,instance TEXT,plan TEXT,state TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Exec(`INSERT INTO aws_ec2_instance_runtime VALUES(?,?,?,?,'ready')`, owner.Region, owner.Deployment, owner.Instance, plan.canonical); err != nil {
		t.Fatal(err)
	}
	record := StackRecord{Name: "fixture", ID: owner.Deployment, Region: owner.Region, Status: "CREATE_COMPLETE", Resources: []StackResource{{LogicalID: "VM", Type: "AWS::EC2::Instance", PhysicalID: owner.Instance, Properties: props}}}
	if _, err = b.db.Exec(`INSERT INTO aws_cfn_stacks(region,name,id,document) VALUES(?,?,?,?)`, owner.Region, record.Name, record.ID, ssmEncode(record)); err != nil {
		t.Fatal(err)
	}
	if _, err = b.db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(owner.Region, "AWS::EC2::Instance"), "AWS::EC2::Instance", owner.Instance, record.ID); err != nil {
		t.Fatal(err)
	}
	store, backend, err := NewCanonicalSSMCommandStore(runtime)
	if err != nil {
		t.Fatal(err)
	}
	api, err := NewSSMCommandAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{Commands: api}
	authority := NewSTSAuthority("")
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:SendCommand"}, Resource: core.StringList{"arn:aws:ssm:us-east-1::document/AWS-RunShellScript", networkARN(owner.Region, "instance", owner.Instance)}}, {Effect: "Allow", Action: core.StringList{"ssm:GetCommandInvocation", "ssm:ListCommands"}, Resource: core.StringList{"*"}}}}
	issue := func(session string) STSCredentials {
		c, e := authority.Issue(STSSessionRequest{Principal: "operator", RoleARN: "arn:aws:iam::000000000000:role/round-trip", RoleID: "AROAABCDEFGHIJKLMNOP", SessionName: session, Duration: 15 * time.Minute, Validate: func(context.Context) error { return nil }, Snapshot: func(context.Context) (core.Policy, error) { return policy, nil }})
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	credentials := issue("first")
	call := func(c STSCredentials, operation, body string) map[string]json.RawMessage {
		r, _, e := authority.Authenticate(positiveCommandSigned(c, operation, body))
		if e != nil {
			t.Fatal(e)
		}
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s HTTP %d: %s", operation, w.Code, w.Body.String())
		}
		var out map[string]json.RawMessage
		if e = json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	sendBody := ssmEncode(SSMCommandRequest{DocumentName: "AWS-RunShellScript", InstanceIds: []string{owner.Instance}, Parameters: map[string][]string{"commands": {"printf 'ok\\n'"}}, Comment: "round-trip-operation", TimeoutSeconds: 30})
	sent := call(credentials, "SendCommand", sendBody)
	var summary nativeCommandSummary
	if err = json.Unmarshal(sent["Command"], &summary); err != nil {
		t.Fatal(err)
	}
	grant := SSMCommandAuthority{Account: owner.Account, Region: owner.Region, Principal: "AROAABCDEFGHIJKLMNOP", Deployment: owner.Deployment}
	retained, err := store.Get(ctx, grant, summary.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	waitCanonicalCommand(t, backend, retained.Targets[0].Intent, ctx)
	invoked := call(credentials, "GetCommandInvocation", ssmEncode(map[string]string{"CommandId": summary.CommandID, "InstanceId": owner.Instance}))
	var state string
	_ = json.Unmarshal(invoked["Status"], &state)
	var code int
	_ = json.Unmarshal(invoked["ResponseCode"], &code)
	var output string
	_ = json.Unmarshal(invoked["StandardOutputContent"], &output)
	if state != "Success" || code != 0 || output != "ok\n" || peer.starts.Load() != 1 {
		t.Fatalf("not known real memory exit: %s %d %q starts=%d", state, code, output, peer.starts.Load())
	}
	// Fresh temporary credentials of the same role generation recover the exact
	// original request, without resubmitting after a possibly lost Send response.
	recovered := call(issue("recovery"), "ListCommands", `{"Filters":[{"key":"InvokedAfter","value":"2000-01-01T00:00:00Z"}],"MaxResults":50}`)
	var commands []nativeCommandSummary
	if err = json.Unmarshal(recovered["Commands"], &commands); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].CommandID != summary.CommandID || commands[0].Comment != "round-trip-operation" || commands[0].RequestedDateTime != float64(retained.RequestedAt)/1000 || peer.starts.Load() != 1 {
		t.Fatalf("recovery mismatch: %+v starts=%d", commands, peer.starts.Load())
	}
	// Signed bytes cannot be relabeled as a different operation.
	tampered := positiveCommandSigned(credentials, "GetCommandInvocation", `{}`)
	tampered.Header.Set("X-Amz-Target", "AmazonSSM.SendCommand")
	if _, _, err = authority.Authenticate(tampered); err == nil {
		t.Fatal("tampered request authenticated")
	}
	if peer.starts.Load() != 1 {
		t.Fatal("recovery or failed auth redispatched")
	}
}
