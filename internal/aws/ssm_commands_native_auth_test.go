package aws

import (
	"context"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"testing"
)

type nativeCommandBackendDouble struct {
	*ssmCommandDouble
	resolve func(context.Context, string, string) (EC2GuestLease, error)
}

func (b *nativeCommandBackendDouble) ResolveCommandTarget(ctx context.Context, region, id string) (EC2GuestLease, error) {
	return b.resolve(ctx, region, id)
}

// All backend effects here are memory doubles. No guest, shell, or socket exists.
func TestNativeCommandPolicyChangeDuringResolution(t *testing.T) {
	for _, mode := range []string{"unchanged", "revoke", "replace-document-grant"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			ls, _, db := leaseFixture(t)
			iam, err := NewIAMRoleStore(db)
			if err != nil {
				t.Fatal(err)
			}
			profile := profileCreate(t, iam, "CommandProfile")
			if _, err = NewStackStore(db); err != nil {
				t.Fatal(err)
			}
			owner := leaseOwner()
			owner.ProfileName = profile.InstanceProfileName
			owner.ProfileID = profile.InstanceProfileID
			props := map[string]any{"ImageId": "ami-local", "InstanceType": "t3.micro", "IamInstanceProfile": owner.ProfileName, "SubnetId": "subnet-local", "SecurityGroupIds": []any{"sg-local"}, "UserData": "dHJ1ZQ==", "Tags": []any{map[string]any{"Key": "Team", "Value": "one"}}}
			id, err := compileStackInstance(owner.Deployment, "VM", props, map[string]string{})
			if err != nil {
				t.Fatal(err)
			}
			owner.Instance = id
			lease, err := ls.Acquire(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			lease, err = ls.Reconcile(ctx, lease)
			if err != nil {
				t.Fatal(err)
			}
			record := StackRecord{Name: "fixture", ID: owner.Deployment, Region: owner.Region, Status: "CREATE_COMPLETE", Resources: []StackResource{{LogicalID: "VM", Type: "AWS::EC2::Instance", PhysicalID: id, Properties: props}}}
			if _, err = db.Exec(`INSERT INTO aws_cfn_stacks(region,name,id,document) VALUES(?,?,?,?)`, owner.Region, record.Name, record.ID, ssmEncode(record)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)`, stackResourceNamespace(owner.Region, "AWS::EC2::Instance"), "AWS::EC2::Instance", id, record.ID); err != nil {
				t.Fatal(err)
			}
			r, a, identity, key := sessionAuthFixture(t)
			caller := ssmSessionCaller{authority: a, key: key, identity: identity}
			document := "arn:aws:ssm:" + owner.Region + "::document/AWS-RunShellScript"
			instance := networkARN(owner.Region, "instance", id)
			parse := func(resource string) core.Policy {
				p, e := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:SendCommand","Resource":%s}]}`, resource)))
				if e != nil {
					t.Fatal(e)
				}
				return p
			}
			policy := parse(fmt.Sprintf(`[%q,%q]`, document, instance))
			if mode == "replace-document-grant" {
				policy = parse(fmt.Sprintf(`%q`, document))
			}
			session := a.sessions[key]
			session.snapshot = func(context.Context) (core.Policy, error) { return policy, nil }
			a.sessions[key] = session
			calls := 0
			backend := &nativeCommandBackendDouble{ssmCommandDouble: &ssmCommandDouble{dispatch: func(guestcommand.DurableIntent) error { calls++; return nil }, observe: func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
				return guestcommand.DurableObservation{Intent: i, State: "Unknown"}, nil
			}}}
			backend.resolve = func(context.Context, string, string) (EC2GuestLease, error) {
				if mode == "revoke" {
					policy = core.Policy{}
				}
				if mode == "replace-document-grant" {
					policy = parse(fmt.Sprintf(`%q`, instance))
				}
				return lease, nil
			}
			store, err := NewSSMCommandStore(db, backend)
			if err != nil {
				t.Fatal(err)
			}
			api, err := NewSSMCommandAPI(store)
			if err != nil {
				t.Fatal(err)
			}
			_, err = api.send(r.Context(), caller, "AROA0000000000000000", owner.Region, ssmCommandRequestFixture([]EC2GuestLease{lease}))
			if mode == "unchanged" {
				if err != nil || calls != 1 {
					t.Fatalf("allowed: calls=%d err=%v", calls, err)
				}
			} else {
				if err == nil || calls != 0 {
					t.Fatalf("changed policy dispatched: calls=%d err=%v", calls, err)
				}
				var n int
				if err = db.QueryRow(`SELECT count(*) FROM aws_ssm_commands`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("unauthorized intent retained: %d %v", n, err)
				}
			}
		})
	}
}

func TestNativeCommandRoleIncarnationNotSessionName(t *testing.T) {
	_, authority, id, key := sessionAuthFixture(t)
	id.UserID = "AROA0000000000000000:job-one"
	caller := ssmSessionCaller{authority: authority, key: key, identity: id}
	principal, err := commandPrincipal(caller)
	if err != nil {
		t.Fatal(err)
	}
	caller.identity.UserID = "AROA0000000000000000:job-two"
	renewed, err := commandPrincipal(caller)
	if err != nil || renewed != principal {
		t.Fatal("session renewal changed role identity", err)
	}
	caller.identity.UserID = "AROA0000000000000001:job-one"
	replaced, err := commandPrincipal(caller)
	if err != nil || replaced == principal {
		t.Fatal("recreated role adopted command identity", err)
	}
	for _, bad := range []string{"viewer:job", "TCSIMOPERATOR", "AROA0000000000000000", "AROA0000000000000000:"} {
		caller.identity.UserID = bad
		if _, err = commandPrincipal(caller); err == nil {
			t.Fatalf("accepted malformed userid %q", bad)
		}
	}
}
