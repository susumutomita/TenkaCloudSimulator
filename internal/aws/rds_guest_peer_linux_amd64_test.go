//go:build linux && amd64

package aws

import (
	"context"
	"database/sql"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

// Only the VM process is inert. Build real CFN reservations, EC2 state,
// completion receipt, and current NIC policy with a genuinely private VPC.
func privatePostgresGuestFixture(t *testing.T) (*StackStore, *EC2VMProbeLaunch, StackRecord, map[string]string) {
	t.Helper()
	b, _, _, _, _, _, _ := vmBackendFixture(t)
	// The fixture's own inert backend has a deliberately short deadline. Our
	// separate CFN instance must remain valid across a real host DB bootstrap.
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	t.Cleanup(cancel)
	s, err := NewStackStore(b.db)
	if err != nil {
		t.Fatal(err)
	}
	var receipt *EC2VMProbeLaunch
	rt, err := NewEC2InstanceRuntime(b.db, b.networkLease.store, EC2InstanceRuntimeConfig{
		Lifetime: ctx, Bundle: b.bundle, Layout: b.layout, Directory: b.directory,
		ImageIDs: []string{"ami-local-pinned"}, OnReady: func(r *EC2VMProbeLaunch) error { receipt = r; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	rt.operations = b.ops
	if _, _, err = NewCanonicalSSMCommandStore(rt); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigureInstanceRuntime(rt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/hello-world-battle-original.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(raw), "MapPublicIpOnLaunch: true", "MapPublicIpOnLaunch: false", 1)
	begin, end := strings.Index(body, "  Igw:"), strings.Index(body, "  # ----- Security Group -----")
	if begin < 0 || end <= begin {
		t.Fatal("unexpected network fixture")
	}
	body = body[:begin] + `  DbA:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref Vpc
      CidrBlock: 10.99.2.0/24
      AvailabilityZone: us-east-1a
  DbB:
    Type: AWS::EC2::Subnet
    Properties:
      VpcId: !Ref Vpc
      CidrBlock: 10.99.3.0/24
      AvailabilityZone: us-east-1b
  DbGroup:
    Type: AWS::EC2::SecurityGroup
    Properties:
      VpcId: !Ref Vpc
      GroupDescription: Only the attached guest
      SecurityGroupIngress:
        - IpProtocol: tcp
          FromPort: 5432
          ToPort: 5432
          CidrIp: 10.99.1.4/32

` + body[end:]
	stack, err := compileStackWithParameters(rdsFixtureRegion, "private-pg-peer", body, map[string]string{
		"NamePrefix": "tc-db-team", "TenkaCloudAccountId": "000000000000", "ExternalId": "inert-fixture-id",
	}, "CAPABILITY_NAMED_IAM", func(_ string, keys map[string]string) (map[string]string, error) {
		out := map[string]string{}
		for key := range keys {
			out[key] = "ami-local-pinned"
		}
		return out, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Create(stack); err != nil {
		t.Fatal(err)
	}
	if receipt == nil || s.VerifyCompletedGuest(ctx, receipt) != nil {
		t.Fatal("missing retained private EC2 instance")
	}
	ids := make(map[string]string, len(stack.Resources))
	for _, r := range stack.Resources {
		ids[r.LogicalID] = r.PhysicalID
	}
	return s, receipt, stack, ids
}

func TestPrivatePostgresPeerNativePolicy(t *testing.T) {
	s, guest, stack, ids := privatePostgresGuestFixture(t)
	ctx := t.Context()
	refs := catalog.RDSPrivatePostgresRefs{Region: stack.Region, SubnetIDs: []string{ids["DbA"], ids["DbB"]}, SecurityGroupIDs: []string{ids["DbGroup"]}}
	check := func(mutate func(*EC2NetworkSnapshot, *netip.Addr, *catalog.RDSPrivatePostgresRefs)) error {
		return s.verifyCompletedGuest(ctx, guest, func(_ EC2GuestOwner, net EC2NetworkSnapshot, ip netip.Addr) error {
			// Only exercise policy here. Do not substitute these controls for
			// a positive owned DB generation or for a real guest connection.
			r := refs
			if mutate != nil {
				mutate(&net, &ip, &r)
			}
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return privatePostgresPeerIngress(ctx, tx, net, ip, r)
		})
	}
	if err := check(nil); err != nil {
		t.Fatal("same VPC, separate app/DB subnets, exact destination /32 and port refused", err)
	}
	t.Run("isolated VPC required independently of catalog configuration", func(t *testing.T) {
		err := s.verifyCompletedGuest(ctx, guest, func(_ EC2GuestOwner, net EC2NetworkSnapshot, ip netip.Addr) error {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			// A rolled-back native storage fault: the app's subnet still has
			// no public address, yet this VPC is no longer isolated.
			if err := networkWrite(ctx, tx, EC2NetworkResource{
				ID: "test-transient-attachment", Kind: "AWS::EC2::VPCGatewayAttachment", Region: stack.Region,
				StackID: stack.ID, VPCID: net.vpcID, Revision: 1,
			}, true); err != nil {
				t.Fatal(err)
			}
			return privatePostgresPeerIngress(ctx, tx, net, ip, refs)
		})
		if err == nil {
			t.Fatal("nonisolated RDS VPC admitted despite no public app subnet flag")
		}
	})
	for name, mutate := range map[string]func(*EC2NetworkSnapshot, *netip.Addr, *catalog.RDSPrivatePostgresRefs){
		"wrong source IP": func(_ *EC2NetworkSnapshot, ip *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) {
			*ip = netip.MustParseAddr("10.99.1.5")
		},
		"source outside subnet": func(_ *EC2NetworkSnapshot, ip *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) {
			*ip = netip.MustParseAddr("10.99.3.4")
		},
		"same CIDR foreign VPC": func(n *EC2NetworkSnapshot, _ *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) { n.vpcID = "vpc-other" },
		"foreign stack":         func(n *EC2NetworkSnapshot, _ *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) { n.StackID = "other" },
		"foreign region":        func(n *EC2NetworkSnapshot, _ *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) { n.Region = "us-west-2" },
		"guest SG does not grant PG": func(_ *EC2NetworkSnapshot, _ *netip.Addr, r *catalog.RDSPrivatePostgresRefs) {
			r.SecurityGroupIDs = []string{ids["Sg"]}
		},
		"public topology": func(n *EC2NetworkSnapshot, _ *netip.Addr, _ *catalog.RDSPrivatePostgresRefs) { n.PublicRoute = true },
	} {
		t.Run(name, func(t *testing.T) {
			if check(mutate) == nil {
				t.Fatal("mismatched peer accepted")
			}
		})
	}
	// A resource with its original native DB row but no exact CFN ownership
	// row cannot pass. Restore only the original fixture's own reservation.
	ns := stackResourceNamespace(stack.Region, ec2SecurityGroupKind)
	setNamespace := func(from, to string) {
		t.Helper()
		result, err := s.db.Exec("UPDATE aws_cfn_ownership SET namespace=? WHERE namespace=? AND kind=? AND physical_id=? AND stack_id=?",
			to, from, ec2SecurityGroupKind, ids["DbGroup"], stack.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := result.RowsAffected(); n != 1 || err != nil {
			t.Fatal(n, err)
		}
	}
	setNamespace(ns, "displaced-owner")
	if check(nil) == nil {
		t.Fatal("unreserved destination group accepted")
	}
	setNamespace("displaced-owner", ns)
	if err := check(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(stack.Region, stack.ID); err != nil {
		t.Fatal(err)
	}
	if check(nil) == nil {
		t.Fatal("guest teardown did not invalidate peer admission")
	}
}

func TestPrivatePostgresGuestAndOwnedDatabase(t *testing.T) {
	dir, pin := os.Getenv("TCSIM_PG_TEST_BUNDLE"), os.Getenv("TCSIM_PG_TEST_PIN")
	if dir == "" && pin == "" {
		t.Skip("independently pinned local PG 15.20 bundle not supplied")
	}
	bundle, err := rdsruntime.OpenBundle(dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	s, guest, stack, ids := privatePostgresGuestFixture(t)
	ctx := t.Context()
	// Both sides must use the very same provider's SQLite handle.
	root, err := os.MkdirTemp("", "pg-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(root); err != nil {
			t.Error("native PG root not empty after cleanup", err)
		}
	})
	engine, target, err := catalog.NewWithOwnedPrivatePostgres(s.db, catalog.Config{
		Regions: []string{stack.Region}, Account: "000000000000", DefaultPrincipal: "fixture-operator",
		Authorize:                  func(context.Context, string, string, string) (string, error) { return "allowed", nil },
		ValidateRDSPrivatePostgres: ValidateRDSPrivatePostgresTopology,
	}, catalog.OwnedPrivatePostgresConfig{
		Region: stack.Region, Identifier: "peer-pg", Bundle: bundle, WorkRoot: root,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := target.Close(); err != nil {
			t.Error(err)
		}
	})
	if s.VerifyPrivatePostgresPeer(ctx, guest, target) == nil {
		t.Fatal("DB not provisioned")
	}
	h := &rdsTopologyHarness{t: t, db: s.db, engine: engine}
	h.query(200, "CreateDBSubnetGroupResponse", "CreateDBSubnetGroup", "DBSubnetGroupName", "peer-subnets", "DBSubnetGroupDescription", "real private CFN peers",
		"SubnetIds.SubnetIdentifier.1", ids["DbA"], "SubnetIds.SubnetIdentifier.2", ids["DbB"])
	h.query(200, "CreateDBInstanceResponse", "CreateDBInstance", "DBInstanceIdentifier", "peer-pg", "DBInstanceClass", "db.t3.micro", "Engine", "postgres",
		"PubliclyAccessible", "false", "DBSubnetGroupName", "peer-subnets", "VpcSecurityGroupIds.VpcSecurityGroupId.1", ids["DbGroup"])
	if err := s.VerifyPrivatePostgresPeer(ctx, guest, target); err != nil {
		t.Fatal("retained EC2 peer and local host PostgreSQL did not validate", err)
	}
	mustNotRun := func(context.Context, *sql.Tx, catalog.RDSPrivatePostgresRefs) error {
		t.Fatal("a different team/account/region reached the policy read")
		return nil
	}
	foreign := newRDSPrivateTopologyHarness(t, false)
	if target.ProbeGuestPeer(ctx, foreign.db, "000000000000", stack.Region, mustNotRun) == nil ||
		target.ProbeGuestPeer(ctx, s.db, "111111111111", stack.Region, mustNotRun) == nil ||
		target.ProbeGuestPeer(ctx, s.db, "000000000000", "us-west-2", mustNotRun) == nil {
		t.Fatal("foreign provider scope accepted")
	}
	h.query(200, "DeleteDBInstanceResponse", "DeleteDBInstance", "DBInstanceIdentifier", "peer-pg", "SkipFinalSnapshot", "true")
	if !guest.Ready() || s.VerifyPrivatePostgresPeer(ctx, guest, target) == nil {
		t.Fatal("live guest could still pair after owned DB teardown")
	}
	if err := s.Delete(stack.Region, stack.ID); err != nil {
		t.Fatal(err)
	}
}
