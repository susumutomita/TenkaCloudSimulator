package aws

import (
	"context"
	"encoding/json"
	"fmt"
	"go.yaml.in/yaml/v3"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureStackInstanceRuntime struct{ attempt *fixtureStackInstanceAttempt }

func (r fixtureStackInstanceRuntime) PrepareStackInstance(context.Context, StackRecord, StackResource) (StackInstanceAttempt, error) {
	return r.attempt, nil
}

// fixtureStackInstance is placed in the stack's own public subnet and security
// group, as a compiled template instance is.
func fixtureStackInstance(record StackRecord) StackResource {
	return StackResource{LogicalID: "FixtureInstance", PhysicalID: "i-fixture", Type: "AWS::EC2::Instance",
		Properties: map[string]any{"SubnetId": networkID(record, "PublicSubnet"), "SecurityGroupIds": []any{networkID(record, "Sg")}}}
}

type fixtureStackInstanceAttempt struct {
	started chan struct{}
	release chan struct{}
	sealed  atomic.Bool
	failure error
}

func (a *fixtureStackInstanceAttempt) Launch(ctx context.Context) error {
	close(a.started)
	select {
	case <-a.release:
		return a.failure
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (a *fixtureStackInstanceAttempt) Seal(context.Context) error        { a.sealed.Store(true); return nil }
func (a *fixtureStackInstanceAttempt) WaitCleanup(context.Context) error { return nil }

// Explicit fake receipt source tests only the CFN lifecycle. It is not a VM test.
func TestStackInstancePendingOutsideSQLAndActualCompletion(t *testing.T) {
	stack, _, record := networkFixture(t)
	if err := stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	fresh, compileErr := compileStack(record.Region, record.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	record = fresh
	record.Resources = append(record.Resources, fixtureStackInstance(record))
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	if err := stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stack.Create(record) }()
	select {
	case <-attempt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not begin")
	}
	// With a single DB connection this would block if Launch held a SQL tx.
	read := make(chan []StackRecord, 1)
	go func() { r, _ := stack.Records(record.Region, record.Name); read <- r }()
	select {
	case records := <-read:
		if len(records) != 1 || stackStatus(records[0]) != "CREATE_IN_PROGRESS" {
			t.Fatalf("premature completion: %v", records)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("launch held SQL transaction")
	}
	close(attempt.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	records, err := stack.Records(record.Region, record.Name)
	if err != nil || len(records) != 1 || stackStatus(records[0]) != "CREATE_COMPLETE" {
		t.Fatalf("completion: %v %v", records, err)
	}
	if err = stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	if !attempt.sealed.Load() {
		t.Fatal("delete did not seal runtime")
	}
}

func TestStackInstanceFailureNeverCompletes(t *testing.T) {
	stack, _, record := networkFixture(t)
	if err := stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	fresh, compileErr := compileStack(record.Region, record.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if compileErr != nil {
		t.Fatal(compileErr)
	}
	record = fresh
	record.Resources = append(record.Resources, fixtureStackInstance(record))
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{}), failure: fmt.Errorf("fixture launch rejected")}
	close(attempt.release)
	if err := stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	if err := stack.Create(record); err == nil {
		t.Fatal("failed launch reported success")
	}
	records, err := stack.Records(record.Region, record.Name)
	if err != nil || len(records) != 1 || stackStatus(records[0]) != "CREATE_FAILED" {
		t.Fatalf("failure: %v %v", records, err)
	}
	if !attempt.sealed.Load() {
		t.Fatal("failure did not seal retained runtime")
	}
}

func TestStackInstanceDeleteRejectsLateReadiness(t *testing.T) {
	stack, _, old := networkFixture(t)
	if err := stack.Delete(old.Region, old.Name); err != nil {
		t.Fatal(err)
	}
	record, err := compileStack(old.Region, old.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	record.Resources = append(record.Resources, fixtureStackInstance(record))
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stack.Create(record) }()
	select {
	case <-attempt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("launch did not begin")
	}
	if err = stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	close(attempt.release)
	if err = <-done; err == nil {
		t.Fatal("late receipt completed deleted generation")
	}
	var stackRows int
	if err = stack.db.QueryRow("SELECT count(*) FROM aws_cfn_stacks WHERE region=? AND name=?", record.Region, record.Name).Scan(&stackRows); err != nil || stackRows != 0 {
		t.Fatalf("deleted stack resurrected: %d %v", stackRows, err)
	}
}

func TestStackInstanceUnconfiguredHasNoSideEffects(t *testing.T) {
	stack, _, old := networkFixture(t)
	if err := stack.Delete(old.Region, old.Name); err != nil {
		t.Fatal(err)
	}
	record, err := compileStack(old.Region, old.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	record.Resources = append(record.Resources, fixtureStackInstance(record))
	if err = stack.Create(record); err == nil {
		t.Fatal("unconfigured runtime accepted")
	}
	var stackRows int
	if err = stack.db.QueryRow("SELECT count(*) FROM aws_cfn_stacks WHERE region=? AND name=?", record.Region, record.Name).Scan(&stackRows); err != nil || stackRows != 0 {
		t.Fatalf("unconfigured runtime committed stack: %d %v", stackRows, err)
	}
	var rows int
	if err = stack.db.QueryRow("SELECT count(*) FROM aws_ec2_network_resources").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("unconfigured runtime created network: %d %v", rows, err)
	}
}

func TestStackInstanceFinalizeReadbackRejectsTriggerDrift(t *testing.T) {
	stack, _, old := networkFixture(t)
	if err := stack.Delete(old.Region, old.Name); err != nil {
		t.Fatal(err)
	}
	record, err := compileStack(old.Region, old.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	record.Resources = append(record.Resources, fixtureStackInstance(record))
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	close(attempt.release)
	if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	_, err = stack.db.Exec(`CREATE TRIGGER corrupt_instance_completion AFTER INSERT ON aws_cfn_events WHEN NEW.status='CREATE_COMPLETE' AND NEW.resource_type='AWS::EC2::Instance' BEGIN UPDATE aws_cfn_stacks SET document=json_set(document,'$.Status','CREATE_IN_PROGRESS') WHERE id=NEW.stack_id; END`)
	if err != nil {
		t.Fatal(err)
	}
	if err = stack.Create(record); err == nil {
		t.Fatal("unacknowledged finalization reported success")
	}
	if !attempt.sealed.Load() {
		t.Fatal("failed finalization did not seal runtime")
	}
	records, err := stack.Records(record.Region, record.Name)
	if err != nil || len(records) != 1 || stackStatus(records[0]) == "CREATE_COMPLETE" {
		t.Fatalf("corrupted completion persisted: %v %v", records, err)
	}
}

func TestStackInstancePermissionsUseConcreteDependencies(t *testing.T) {
	instance := StackResource{Type: "AWS::EC2::Instance", PhysicalID: "i-0123456789abcdef0", Properties: map[string]any{"ImageId": "ami-pinned", "SubnetId": "subnet-local", "IamInstanceProfile": "local-profile", "SecurityGroupIds": []any{"sg-local"}}}
	record := StackRecord{Region: "us-east-1", Resources: []StackResource{{Type: "AWS::IAM::InstanceProfile", PhysicalID: "local-profile", NativeIAM: &stackNativeIAM{RoleName: "local-role"}}, instance}}
	got := map[string]bool{}
	if err := stackInstancePermissions(record, instance, false, func(action, resource string) error {
		if resource == "*" {
			t.Fatal("wildcard grant inferred")
		}
		got[action+" "+resource] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"iam:PassRole arn:aws:iam::000000000000:role/local-role",
		"ec2:RunInstances arn:aws:ec2:us-east-1::image/ami-pinned",
		"ec2:RunInstances arn:aws:ec2:us-east-1:000000000000:instance/i-0123456789abcdef0",
		"ec2:RunInstances arn:aws:ec2:us-east-1:000000000000:subnet/subnet-local",
		"ec2:RunInstances arn:aws:ec2:us-east-1:000000000000:security-group/sg-local",
		"ec2:RunInstances arn:aws:ec2:us-east-1:000000000000:network-interface/eni-0123456789abcdef0",
		"ec2:RunInstances arn:aws:ec2:us-east-1:000000000000:volume/vol-0123456789abcdef0",
	}
	if len(got) != len(expected) {
		t.Fatal(got)
	}
	for _, key := range expected {
		if !got[key] {
			t.Fatalf("missing %s", key)
		}
	}
	instance.Properties["IamInstanceProfile"] = "external-profile"
	if err := stackInstancePermissions(record, instance, false, func(string, string) error { return nil }); err == nil {
		t.Fatal("unbound external profile accepted")
	}
}

// This deliberately isolates the original network+instance/profile declarations
// from the still-failing participant policy. Full-template acceptance remains a
// separate, non-skipped test; this fixture cannot establish Battle completion.
func TestStackInstanceOriginalNativeResourcesLifecycle(t *testing.T) {
	stack, _, old := networkFixture(t)
	if err := stack.Delete(old.Region, old.Name); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var node yaml.Node
	if err = yaml.Unmarshal(raw, &node); err != nil {
		t.Fatal(err)
	}
	decoded, err := taggedYAML(&node)
	if err != nil {
		t.Fatal(err)
	}
	template := object(decoded)
	delete(object(template["Resources"]), "ParticipantViewerRole")
	delete(object(template["Outputs"]), "ParticipantViewerRoleArn")
	body, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	record, err := compileStackWithParameters(old.Region, "native-instance", string(body), map[string]string{"NamePrefix": "tc-hello-team", "TenkaCloudAccountId": "000000000000", "ExternalId": "fixture-external-id-only"}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
		v := map[string]string{}
		for key := range keys {
			v[key] = "ami-local-pinned"
		}
		return v, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Resources) != 11 {
		t.Fatal(len(record.Resources))
	}
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	close(attempt.release)
	if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	calls := map[string]bool{}
	if err = stack.createChecked(record, func(action, resource string) error { calls[action+" "+resource] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !calls["ec2:RunInstances "+networkARN(record.Region, "instance", networkID(record, "Ec2"))] {
		t.Fatal("RunInstances admission absent")
	}
	records, err := stack.Records(record.Region, record.Name)
	if err != nil || len(records) != 1 || stackStatus(records[0]) != "CREATE_COMPLETE" {
		t.Fatalf("native metadata lifecycle: %v %v", records, err)
	}
	if err = stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	if !attempt.sealed.Load() {
		t.Fatal("runtime not sealed")
	}
}

func TestStackInstanceOriginalFullTemplatePersistenceLifecycle(t *testing.T) {
	stack, _, old := networkFixture(t)
	if err := stack.Delete(old.Region, old.Name); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	record, err := compileStackWithParameters(old.Region, "full-template-persistence", string(raw), map[string]string{"NamePrefix": "tc-hello-team", "TenkaCloudAccountId": "000000000000", "ExternalId": "fixture-external-id-only"}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
		v := map[string]string{}
		for key := range keys {
			v[key] = "ami-local-pinned"
		}
		return v, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Resources) != 12 {
		t.Fatal("full template declarations omitted")
	}
	// Deliberate in-memory lifecycle fixture. No actual NIC/VM or participant
	// condition context is verified by this provider persistence test.
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	close(attempt.release)
	if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	if err = stack.createChecked(record, func(string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	records, err := stack.Records(record.Region, record.Name)
	if err != nil || len(records) != 1 || stackStatus(records[0]) != "CREATE_COMPLETE" {
		t.Fatalf("full persistence: %v %v", records, err)
	}
	if records[0].Outputs["FrontendUrl"] != "" || records[0].Outputs["ApiUrl"] != "" {
		t.Fatal("registered endpoints fabricated")
	}
	if err = stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	if !attempt.sealed.Load() {
		t.Fatal("runtime cleanup omitted")
	}
	var networkRows, claims int
	if err = stack.db.QueryRow("SELECT (SELECT count(*) FROM aws_ec2_network_resources),(SELECT count(*) FROM aws_cfn_ownership)").Scan(&networkRows, &claims); err != nil || networkRows != 0 || claims != 0 {
		t.Fatalf("cleanup incomplete: %d %d %v", networkRows, claims, err)
	}
}

// A stack instance must be placed in its own stack's subnet and security
// groups. Any other placement refuses the stack inside the create transaction:
// nothing is committed, no guest launch starts and discovery stays readable.
func TestStackInstancePlacementRefusedBeforeCommitAndLaunch(t *testing.T) {
	stack, _, record := networkFixture(t)
	if err := stack.Delete(record.Region, record.Name); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// EC2-owned network rows and another stack's network already exist.
	vpc, err := stack.createNativeVPC(ctx, record.Region, "10.200.0.0/16", nil)
	if err != nil {
		t.Fatal(err)
	}
	nativeSubnet, err := stack.createNativeSubnet(ctx, record.Region, ec2SubnetMutation{vpc: vpc.ID, cidr: "10.200.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	nativeGroup, err := stack.createEC2NativeSecurityGroup(ctx, record.Region, ec2NativeSecurityGroupQuery{name: "detached", description: "detached group", vpc: vpc.ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := compileStack(record.Region, "other-network", networkTemplate(t), map[string]string{"NamePrefix": "tc-other-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stack.Create(other); err != nil {
		t.Fatal(err)
	}
	attempt := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
	if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{attempt}); err != nil {
		t.Fatal(err)
	}
	// Every compile draws a new stack ID, and network IDs derive from it, so a
	// case must name the rows of the stack it is about to create.
	compiled := func() StackRecord {
		t.Helper()
		fresh, err := compileStack(record.Region, record.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return fresh
	}
	rows := func(fresh StackRecord) int {
		t.Helper()
		var n int
		if err := stack.db.QueryRow(`SELECT (SELECT count(*) FROM aws_cfn_stacks WHERE id=?1)+(SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?1)+(SELECT count(*) FROM aws_ec2_network_resources WHERE stack_id=?1)+(SELECT count(*) FROM aws_cfn_event_history WHERE stack_id=?1)+(SELECT count(*) FROM aws_cfn_events WHERE stack_id=?1)`, fresh.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	own := func(fresh StackRecord, logical string) string {
		t.Helper()
		id := networkID(fresh, logical)
		if id == "" || id == networkID(record, logical) || id == networkID(other, logical) {
			t.Fatalf("%s is not a row of the stack being created: %q", logical, id)
		}
		return id
	}
	for _, tc := range []struct {
		name  string
		place func(fresh StackRecord) (subnet string, groups any)
	}{
		{"EC2-owned subnet", func(f StackRecord) (string, any) { return nativeSubnet.ID, []any{own(f, "Sg")} }},
		{"another stack's subnet", func(f StackRecord) (string, any) { return networkID(other, "PublicSubnet"), []any{own(f, "Sg")} }},
		{"missing subnet", func(f StackRecord) (string, any) { return "subnet-0000000000000000f", []any{own(f, "Sg")} }},
		{"own VPC as subnet", func(f StackRecord) (string, any) { return own(f, "Vpc"), []any{own(f, "Sg")} }},
		{"own security group as subnet", func(f StackRecord) (string, any) { return own(f, "Sg"), []any{own(f, "Sg")} }},
		{"EC2-owned security group", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), []any{nativeGroup.ID} }},
		{"another stack's security group", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), []any{networkID(other, "Sg")} }},
		{"missing security group", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), []any{"sg-0000000000000000f"} }},
		{"own subnet as security group", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), []any{own(f, "PublicSubnet")} }},
		// Both rows foreign and in one VPC: only the ownership check can refuse these.
		{"another stack's subnet and group", func(f StackRecord) (string, any) {
			return networkID(other, "PublicSubnet"), []any{networkID(other, "Sg")}
		}},
		{"EC2-owned subnet and group", func(f StackRecord) (string, any) { return nativeSubnet.ID, []any{nativeGroup.ID} }},
		{"own group followed by a foreign one", func(f StackRecord) (string, any) {
			return own(f, "PublicSubnet"), []any{own(f, "Sg"), networkID(other, "Sg")}
		}},
		{"no security group", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), []any{} }},
		{"malformed security groups", func(f StackRecord) (string, any) { return own(f, "PublicSubnet"), "sg" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := compiled()
			instance := fixtureStackInstance(fresh)
			subnet, groups := tc.place(fresh)
			instance.Properties = map[string]any{"SubnetId": subnet, "SecurityGroupIds": groups}
			fresh.Resources = append(fresh.Resources, instance)
			// Without the refusal the create would commit and block in Launch.
			done := make(chan error, 1)
			go func() { done <- stack.Create(fresh) }()
			var err error
			select {
			case err = <-done:
			case <-attempt.started:
				t.Fatal("a refused placement started a guest launch")
			case <-time.After(5 * time.Second):
				t.Fatal("the placement refusal did not return")
			}
			if failure, ok := err.(ssmFailure); !ok || failure.code != "ValidationError" {
				t.Fatalf("placement was not refused as a validation error: %v", err)
			}
			// The stack, its claims, its own network rows and its events are all rolled back.
			if n := rows(fresh); n != 0 {
				t.Fatalf("a refused stack left %d rows", n)
			}
			if _, retained := stack.instanceAttempts.Load(fresh.ID); retained {
				t.Fatal("a refused stack retained its launch attempt")
			}
			if _, err = stack.ec2Read(ctx, record.Region, "DescribeInstances"); err != nil {
				t.Fatalf("discovery must stay readable after a refusal: %v", err)
			}
		})
	}
	// Two placements that no supported action can produce are still refused, as
	// DescribeInstances would reject them: a group of the same stack in another
	// VPC than the subnet, and a foreign group inside the stack's own VPC. Their
	// rows are seeded directly, under IDs derived from the new stack.
	for _, tc := range []struct {
		name string
		seed func(fresh StackRecord) []EC2NetworkResource
	}{
		{"own group in another VPC", func(f StackRecord) []EC2NetworkResource {
			return []EC2NetworkResource{
				{ID: "vpc-0000000000000000e", Kind: "AWS::EC2::VPC", Region: f.Region, StackID: f.ID, CIDR: "10.201.0.0/16", Revision: 1},
				{ID: "sg-0000000000000000e", Kind: "AWS::EC2::SecurityGroup", Region: f.Region, StackID: f.ID, VPCID: "vpc-0000000000000000e", Revision: 1},
			}
		}},
		{"foreign group in the own VPC", func(f StackRecord) []EC2NetworkResource {
			return []EC2NetworkResource{{ID: "sg-0000000000000000e", Kind: "AWS::EC2::SecurityGroup", Region: f.Region, StackID: other.ID, VPCID: own(f, "Vpc"), Revision: 1}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh := compiled()
			seed, err := stack.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			// The store has one connection: never leave this transaction or its rows behind.
			defer seed.Rollback()
			defer stack.db.Exec(`DELETE FROM aws_ec2_network_resources WHERE id IN ('vpc-0000000000000000e','sg-0000000000000000e')`)
			for _, row := range tc.seed(fresh) {
				if err = networkWrite(ctx, seed, row, true); err != nil {
					t.Fatal(err)
				}
			}
			if err = seed.Commit(); err != nil {
				t.Fatal(err)
			}
			instance := fixtureStackInstance(fresh)
			instance.Properties["SecurityGroupIds"] = []any{own(fresh, "Sg"), "sg-0000000000000000e"}
			fresh.Resources = append(fresh.Resources, instance)
			done := make(chan error, 1)
			go func() { done <- stack.Create(fresh) }()
			select {
			case err = <-done:
			case <-attempt.started:
				t.Fatal("a refused placement started a guest launch")
			case <-time.After(5 * time.Second):
				t.Fatal("the placement refusal did not return")
			}
			if failure, ok := err.(ssmFailure); !ok || failure.code != "ValidationError" {
				t.Fatalf("the seeded placement was not refused: %v", err)
			}
			// Only the seeded rows may remain under this stack ID.
			if _, err = stack.db.Exec(`DELETE FROM aws_ec2_network_resources WHERE id IN ('vpc-0000000000000000e','sg-0000000000000000e')`); err != nil {
				t.Fatal(err)
			}
			if n := rows(fresh); n != 0 {
				t.Fatalf("a refused stack left %d rows", n)
			}
		})
	}
	// A read that fails is an error, not a placement verdict, and stores nothing.
	t.Run("cancelled read", func(t *testing.T) {
		fresh := compiled()
		fresh.Resources = append(fresh.Resources, fixtureStackInstance(fresh))
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err := stack.createCheckedContext(cancelled, fresh, nil)
		if _, verdict := err.(ssmFailure); err == nil || verdict {
			t.Fatalf("a failed read was reported as a placement verdict or success: %v", err)
		}
		if n := rows(fresh); n != 0 {
			t.Fatalf("a failed create left %d rows", n)
		}
	})
	// A failed case may have started the one launch this double can run; the
	// remaining checks would then block instead of reporting.
	if t.Failed() {
		return
	}
	select {
	case <-attempt.started:
		t.Fatal("a refused placement started a guest launch")
	default:
	}
	// The refusals changed neither the EC2-owned rows nor the other stack.
	var kept int
	if err = stack.db.QueryRow(`SELECT count(*) FROM aws_ec2_network_resources WHERE region=? AND id IN (?,?,?,?)`, record.Region, nativeSubnet.ID, nativeGroup.ID, networkID(other, "PublicSubnet"), networkID(other, "Sg")).Scan(&kept); err != nil || kept != 4 {
		t.Fatalf("a refused placement changed foreign rows: %d %v", kept, err)
	}
	// The same stack with its own subnet and group is admitted and launches.
	fresh := compiled()
	fresh.Resources = append(fresh.Resources, fixtureStackInstance(fresh))
	done := make(chan error, 1)
	go func() { done <- stack.Create(fresh) }()
	select {
	case <-attempt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("a valid placement did not start its launch")
	}
	close(attempt.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if read, err := stack.ec2Read(ctx, record.Region, "DescribeInstances"); err != nil || len(read.Reservations) != 1 {
		t.Fatalf("the admitted instance is not readable: %v %v", read.Reservations, err)
	}
}
