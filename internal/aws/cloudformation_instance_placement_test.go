package aws

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// This is a control-plane test. The trusted lifecycle fixture records whether
// pending state was published to Launch; it never starts or emulates a guest.
func TestStackInstancePlacementAdmission(t *testing.T) {
	ref := func(id string) any { return map[string]any{"Ref": id} }
	for _, tc := range []struct {
		name, subnet, second string
		valid                bool
	}{
		{"owned-pair", "PublicSubnet", "ExtraSg", true},
		{"missing-claim", "PublicSubnet", "ExtraSg", false},
		{"missing-subnet", "missing", "ExtraSg", false},
		{"foreign-subnet", "foreign-subnet", "ExtraSg", false},
		{"missing-second-group", "PublicSubnet", "missing", false},
		{"foreign-second-group", "PublicSubnet", "foreign-group", false},
		{"wrong-second-type", "PublicSubnet", "PublicSubnet", false},
		{"other-vpc-in-this-stack", "PublicSubnet", "OtherSg", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stack, network, peer := networkFixture(t)
			peerSubnet, peerSG := networkID(peer, "PublicSubnet"), networkID(peer, "Sg")
			before, err := network.Snapshot(t.Context(), peer.Region, peer.ID, peerSubnet, []string{peerSG})
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var node yaml.Node
			if err = yaml.Unmarshal(body, &node); err != nil {
				t.Fatal(err)
			}
			parsed, err := taggedYAML(&node)
			if err != nil {
				t.Fatal(err)
			}
			template := object(parsed)
			res := object(template["Resources"])
			props := object(object(res["Ec2"])["Properties"])
			res["ExtraSg"] = map[string]any{"Type": "AWS::EC2::SecurityGroup", "Properties": map[string]any{"VpcId": ref("Vpc"), "GroupDescription": "second owned group"}}
			res["OtherVpc"] = map[string]any{"Type": "AWS::EC2::VPC", "Properties": map[string]any{"CidrBlock": "10.98.0.0/16"}}
			res["OtherSg"] = map[string]any{"Type": "AWS::EC2::SecurityGroup", "Properties": map[string]any{"VpcId": ref("OtherVpc"), "GroupDescription": "other VPC"}}
			value := func(v string) any {
				switch v {
				case "missing":
					return "absent-local-id"
				case "foreign-subnet":
					return peerSubnet
				case "foreign-group":
					return peerSG
				default:
					return ref(v)
				}
			}
			props["SubnetId"] = value(tc.subnet)
			props["SecurityGroupIds"] = []any{ref("Sg"), value(tc.second)}
			body, err = json.Marshal(template)
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStackWithParameters(peer.Region, "placement-test", string(body),
				map[string]string{"NamePrefix": "tc-hello-team", "TenkaCloudAccountId": "000000000000", "ExternalId": "fixture-external-id-only"},
				"CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
					out := map[string]string{}
					for k := range keys {
						out[k] = "ami-local-pinned"
					}
					return out, nil
				})
			if err != nil {
				t.Fatal(err)
			}
			a := &fixtureStackInstanceAttempt{started: make(chan struct{}), release: make(chan struct{})}
			close(a.release)
			if err = stack.ConfigureInstanceRuntime(fixtureStackInstanceRuntime{a}); err != nil {
				t.Fatal(err)
			}
			if tc.name == "missing-claim" {
				// A lost ownership claim must not be masked by a matching physical
				// network row. Fault is confined to the new stack in this subtest.
				_, err = stack.db.Exec(`CREATE TRIGGER drop_group_claim AFTER INSERT ON aws_ec2_network_resources
					WHEN NEW.kind='AWS::EC2::SecurityGroup' BEGIN
					DELETE FROM aws_cfn_ownership WHERE kind=NEW.kind AND physical_id=NEW.id AND stack_id=NEW.stack_id; END`)
				if err != nil {
					t.Fatal(err)
				}
			}
			err = stack.createChecked(record, func(string, string) error { return nil })
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-a.started:
				default:
					t.Fatal("valid placement never reached runtime")
				}
				if err = stack.Delete(record.Region, record.Name); err != nil {
					t.Fatal(err)
				}
			} else {
				var f ssmFailure
				if !errors.As(err, &f) || f.code != "ValidationError" {
					t.Fatalf("want ValidationError, got %v", err)
				}
				select {
				case <-a.started:
					t.Fatal("invalid placement reached runtime")
				default:
				}
				if _, ok := stack.instanceAttempts.Load(record.ID); ok {
					t.Fatal("invalid attempt retained")
				}
				for _, q := range []string{
					"SELECT count(*) FROM aws_cfn_stacks WHERE id=?",
					"SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?",
					"SELECT count(*) FROM aws_cfn_events WHERE stack_id=?",
					"SELECT count(*) FROM aws_cfn_event_history WHERE stack_id=?",
					"SELECT count(*) FROM aws_ec2_network_resources WHERE stack_id=?",
				} {
					var n int
					if err := stack.db.QueryRow(q, record.ID).Scan(&n); err != nil || n != 0 {
						t.Fatalf("residual state in %s: n=%d, err=%v", q, n, err)
					}
				}
			}
			after, err := network.Snapshot(t.Context(), peer.Region, peer.ID, peerSubnet, []string{peerSG})
			if err != nil || after.Seal != before.Seal {
				t.Fatalf("peer changed: %v", err)
			}
		})
	}
}

// Compiler, runtime and store can have different callers. Check the final
// transaction guard independently on invalid inputs and lost/damaged rows.
// Every simulated storage fault is rolled back before the next case.
func TestStackInstancePlacementInvalidRecordAndStorageFailures(t *testing.T) {
	stack, _, valid := networkFixture(t)
	subnet := networkID(valid, "PublicSubnet")
	group := networkID(valid, "Sg")
	vpc := networkID(valid, "Vpc")
	for _, tc := range []struct {
		name         string
		storageError bool
	}{
		{"duplicate declaration", false}, {"no groups", false}, {"duplicate group", false},
		{"missing network row", false}, {"changed VPC link", false}, {"missing VPC claim", false},
		{"unreadable network document", true}, {"unavailable claim store", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := stack.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			record := valid
			instance := fixturePlacedStackInstance(record)
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := tx.ExecContext(t.Context(), q, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.name {
			case "duplicate declaration":
				record.Resources = append(append([]StackResource(nil), record.Resources...), StackResource{Type: "AWS::EC2::Subnet", PhysicalID: subnet})
			case "no groups":
				delete(instance.Properties, "SecurityGroupIds")
			case "duplicate group":
				instance.Properties["SecurityGroupIds"] = []any{group, group}
			case "missing network row":
				exec("DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", record.Region, subnet)
			case "changed VPC link":
				exec("UPDATE aws_ec2_network_resources SET document=json_set(document,'$.VPCID','unbound') WHERE region=? AND id=?", record.Region, subnet)
			case "missing VPC claim":
				exec("DELETE FROM aws_cfn_ownership WHERE stack_id=? AND physical_id=?", record.ID, vpc)
			case "unreadable network document":
				exec("UPDATE aws_ec2_network_resources SET document='{}' WHERE region=? AND id=?", record.Region, subnet)
			case "unavailable claim store":
				exec("DROP TABLE aws_cfn_ownership")
			}
			err = validateStackInstancePlacement(t.Context(), tx, record, instance)
			if err == nil || errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("expected bounded failure, got %v", err)
			}
			var failure ssmFailure
			invalid := errors.As(err, &failure) && failure.code == "ValidationError"
			if invalid == tc.storageError {
				t.Fatalf("incorrect error classification: %v", err)
			}
		})
	}
}
