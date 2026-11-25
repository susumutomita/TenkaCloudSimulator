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
	// An EC2-owned VPC and subnet, and another stack's network, already exist.
	vpc, err := stack.createNativeVPC(ctx, record.Region, "10.200.0.0/16", nil)
	if err != nil {
		t.Fatal(err)
	}
	native, err := stack.createNativeSubnet(ctx, record.Region, ec2Request{vpcID: vpc.ID, cidr: "10.200.1.0/24"})
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
	for _, tc := range []struct {
		name   string
		subnet string
		groups any
	}{
		{"EC2-owned subnet", native.ID, []any{networkID(record, "Sg")}},
		{"another stack's subnet", networkID(other, "PublicSubnet"), []any{networkID(record, "Sg")}},
		{"missing subnet", "subnet-0000000000000000f", []any{networkID(record, "Sg")}},
		{"a VPC as subnet", networkID(record, "Vpc"), []any{networkID(record, "Sg")}},
		{"another stack's security group", networkID(record, "PublicSubnet"), []any{networkID(other, "Sg")}},
		{"missing security group", networkID(record, "PublicSubnet"), []any{"sg-0000000000000000f"}},
		{"a subnet as security group", networkID(record, "PublicSubnet"), []any{networkID(record, "PublicSubnet")}},
		{"no security group", networkID(record, "PublicSubnet"), []any{}},
		{"malformed security groups", networkID(record, "PublicSubnet"), "sg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh, err := compileStack(record.Region, record.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
			if err != nil {
				t.Fatal(err)
			}
			instance := fixtureStackInstance(fresh)
			instance.Properties = map[string]any{"SubnetId": tc.subnet, "SecurityGroupIds": tc.groups}
			fresh.Resources = append(fresh.Resources, instance)
			err = stack.Create(fresh)
			if failure, ok := err.(ssmFailure); !ok || failure.code != "ValidationError" {
				t.Fatalf("placement was not refused as a validation error: %v", err)
			}
			// Records reports a missing stack as an error; either way none is stored.
			if records, _ := stack.Records(record.Region, record.Name); len(records) != 0 {
				t.Fatalf("a refused stack was stored: %v", records)
			}
			var claims int
			if err = stack.db.QueryRow(`SELECT count(*) FROM aws_cfn_ownership WHERE physical_id=?`, instance.PhysicalID).Scan(&claims); err != nil || claims != 0 {
				t.Fatalf("a refused stack kept its instance claim: %d %v", claims, err)
			}
			if _, err = stack.ec2Read(ctx, record.Region, "DescribeInstances"); err != nil {
				t.Fatalf("discovery must stay readable after a refusal: %v", err)
			}
		})
	}
	select {
	case <-attempt.started:
		t.Fatal("a refused placement started a guest launch")
	default:
	}
	// The EC2-owned subnet is unchanged and still usable for a launch record.
	if _, err = stack.runNativeInstance(ctx, record.Region, ec2Request{imageID: "ami-12345678", instanceType: "t3.micro", subnetID: native.ID}); err != nil {
		t.Fatal(err)
	}
	// A correctly placed instance whose ID is already a launch record is refused too.
	fresh, err := compileStack(record.Region, record.Name, networkTemplate(t), map[string]string{"NamePrefix": "tc-hello-team"}, "")
	if err != nil {
		t.Fatal(err)
	}
	instance := fixtureStackInstance(fresh)
	taken, _ := json.Marshal(ec2NativeInstance{ID: instance.PhysicalID, Region: record.Region, SubnetID: native.ID, VPCID: vpc.ID, ImageID: "ami-12345678", InstanceType: "t3.micro", Tags: map[string]string{}})
	if _, err = stack.db.Exec(`INSERT INTO aws_ec2_native_instances VALUES(?,?,?,?,?)`, record.Region, instance.PhysicalID, native.ID, nil, string(taken)); err != nil {
		t.Fatal(err)
	}
	fresh.Resources = append(fresh.Resources, instance)
	if failure, ok := stack.Create(fresh).(ssmFailure); !ok || failure.code != "AlreadyExistsException" {
		t.Fatalf("a stack instance took a launch record's ID: %v", failure)
	}
	if records, _ := stack.Records(record.Region, record.Name); len(records) != 0 {
		t.Fatalf("a refused stack was stored: %v", records)
	}
	if _, err = stack.db.Exec(`DELETE FROM aws_ec2_native_instances WHERE id=?`, instance.PhysicalID); err != nil {
		t.Fatal(err)
	}
	// With the ID free and the placement valid, the same stack is admitted.
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
}
