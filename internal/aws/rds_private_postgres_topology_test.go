package aws

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const (
	rdsFixtureRegion = "us-east-1"
	rdsFixtureVpc    = "vpc-12345678"
	rdsFixtureA      = "subnet-12345678"
	rdsFixtureB      = "subnet-87654321"
	rdsFixtureSG     = "sg-12345678"
	foreignFixtureSG = "sg-87654321"
	rdsFixtureOwner  = "retained-native-network-stack"
	rdsQueryFault    = "InvalidVPCNetworkStateFault"
)

type rdsTopologyHarness struct {
	t          *testing.T
	db         *sql.DB
	engine     *catalog.Engine
	reads      int
	denyAction string
}

func newRDSPrivateTopologyHarness(t *testing.T, optIn bool) *rdsTopologyHarness {
	t.Helper()
	store, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.Database()
	// A successful topology read cannot silently open a second connection while
	// the catalog write owns this database's only active transaction.
	db.SetMaxOpenConns(1)
	if _, err := NewEC2NetworkStore(db); err != nil {
		t.Fatal(err)
	}
	h := &rdsTopologyHarness{t: t, db: db}
	cfg := catalog.Config{Regions: []string{rdsFixtureRegion}, Account: "000000000000", DefaultPrincipal: "fixture-operator",
		Authorize: func(_ context.Context, who, action, _ string) (string, error) {
			if who != "fixture-operator" || action == h.denyAction {
				return "denied", nil
			}
			return "allowed", nil
		}}
	if optIn {
		cfg.ValidateRDSPrivatePostgres = func(ctx context.Context, tx *sql.Tx, refs catalog.RDSPrivatePostgresRefs) error {
			h.reads++
			return ValidateRDSPrivatePostgresTopology(ctx, tx, refs)
		}
	}
	var services []*catalog.Service
	for _, spec := range catalog.Services() {
		if spec.Model == "rds" {
			services = append(services, spec)
		}
	}
	h.engine, err = catalog.New(db, cfg, services)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *rdsTopologyHarness) seedNetwork(options ...string) {
	h.t.Helper()
	secondZone := rdsFixtureRegion + "b"
	var autoPublic bool
	var external bool
	for _, option := range options {
		switch option {
		case "single-zone":
			secondZone = rdsFixtureRegion + "a"
		case "auto-public":
			autoPublic = true
		case "gateway":
			external = true
		default:
			h.t.Fatal("unknown topology option")
		}
	}
	rows := []EC2NetworkResource{
		{ID: rdsFixtureVpc, Kind: "AWS::EC2::VPC", CIDR: "10.80.0.0/16"},
		{ID: rdsFixtureA, Kind: "AWS::EC2::Subnet", VPCID: rdsFixtureVpc, CIDR: "10.80.1.0/24", AvailabilityZone: rdsFixtureRegion + "a", PublicIP: autoPublic},
		{ID: rdsFixtureB, Kind: "AWS::EC2::Subnet", VPCID: rdsFixtureVpc, CIDR: "10.80.2.0/24", AvailabilityZone: secondZone},
		{ID: rdsFixtureSG, Kind: "AWS::EC2::SecurityGroup", VPCID: rdsFixtureVpc},
	}
	if external {
		rows = append(rows, EC2NetworkResource{ID: "igw-12345678", Kind: "AWS::EC2::InternetGateway"},
			EC2NetworkResource{ID: "vpc-attach-1", Kind: "AWS::EC2::VPCGatewayAttachment", VPCID: rdsFixtureVpc, GatewayID: "igw-12345678"})
	}
	ctx := h.t.Context()
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	defer tx.Rollback()
	for _, row := range rows {
		row.Region, row.StackID = rdsFixtureRegion, rdsFixtureOwner
		if err := createEC2Network(ctx, tx, row); err != nil {
			h.t.Fatal(err)
		}
	}
	// A peer's authoritative security group really exists, so refusal cannot
	// accidentally be just a missing-row case.
	for _, row := range []EC2NetworkResource{
		{ID: "vpc-87654321", Kind: "AWS::EC2::VPC", CIDR: "10.90.0.0/16"},
		{ID: foreignFixtureSG, Kind: "AWS::EC2::SecurityGroup", VPCID: "vpc-87654321"},
	} {
		row.Region, row.StackID = rdsFixtureRegion, "peer-network-stack"
		if err := createEC2Network(ctx, tx, row); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *rdsTopologyHarness) query(status int, contains string, action string, args ...string) string {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.t.Context(), 3*time.Second)
	defer cancel()
	form := url.Values{"Action": {action}, "Version": {"2014-10-31"}}
	for i := 0; i < len(args); i += 2 {
		form.Add(args[i], args[i+1])
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIM/20261125/"+rdsFixtureRegion+"/rds/aws4_request, SignedHeaders=host, Signature=0")
	out := httptest.NewRecorder()
	if !h.engine.Owns(req) {
		h.t.Fatal("request did not reach the native RDS Query catalog")
	}
	h.engine.ServeHTTP(out, req)
	body := out.Body.String()
	if out.Code != status || !strings.Contains(body, contains) {
		h.t.Fatalf("%s: got %d %s, want %d with %q", action, out.Code, body, status, contains)
	}
	return body
}

func (h *rdsTopologyHarness) subnetGroup() {
	h.query(200, "CreateDBSubnetGroupResponse", "CreateDBSubnetGroup", "DBSubnetGroupName", "private-group", "DBSubnetGroupDescription", "fixture network",
		"SubnetIds.SubnetIdentifier.1", rdsFixtureA, "SubnetIds.SubnetIdentifier.2", rdsFixtureB)
}

func (h *rdsTopologyHarness) create(status int, code, name, backend, public, group string) {
	h.t.Helper()
	body := h.query(status, code, "CreateDBInstance", "DBInstanceIdentifier", name, "DBInstanceClass", "db.t3.micro", "Engine", backend,
		"PubliclyAccessible", public, "DBSubnetGroupName", group, "VpcSecurityGroupIds.VpcSecurityGroupId.1", rdsFixtureSG)
	if status == 200 && (!strings.Contains(body, "<DBInstanceStatus>creating</DBInstanceStatus>") || strings.Contains(body, "<Endpoint")) {
		h.t.Fatal("opt-in validation must not grant runtime readiness or expose an endpoint")
	}
}

func TestRDSPrivatePostgresNativeTopologyQuery(t *testing.T) {
	t.Run("off and excluded metadata paths unchanged", func(t *testing.T) {
		legacy := newRDSPrivateTopologyHarness(t, false)
		legacy.create(200, "CreateDBInstanceResponse", "old-pg", "postgres", "false", "not-present")
		h := newRDSPrivateTopologyHarness(t, true)
		h.create(400, rdsQueryFault, "private-pg", "postgres", "false", "not-present")
		h.create(200, "CreateDBInstanceResponse", "public-pg", "postgres", "true", "not-present")
		h.create(200, "CreateDBInstanceResponse", "private-mysql", "mysql", "false", "not-present")
		if h.reads != 0 {
			t.Fatal("excluded metadata reached EC2 topology validation")
		}
	})
	t.Run("authorized current native scope and atomic modify", func(t *testing.T) {
		h := newRDSPrivateTopologyHarness(t, true)
		h.seedNetwork()
		h.subnetGroup()
		h.denyAction = "rds:CreateDBInstance"
		h.create(403, "AccessDenied", "private-pg", "postgres", "false", "private-group")
		if h.reads != 0 {
			t.Fatal("unauthorized create reached EC2 state")
		}
		h.denyAction = ""
		h.create(200, "CreateDBInstanceResponse", "private-pg", "postgres", "false", "private-group")
		h.denyAction = "rds:ModifyDBInstance"
		h.query(403, "AccessDenied", "ModifyDBInstance", "DBInstanceIdentifier", "private-pg", "BackupRetentionPeriod", "7")
		if h.reads != 1 {
			t.Fatal("unexpected native check before authorized create or after denied modify")
		}
		h.denyAction = ""
		h.query(200, "<BackupRetentionPeriod>7</BackupRetentionPeriod>", "ModifyDBInstance", "DBInstanceIdentifier", "private-pg", "BackupRetentionPeriod", "7")
		h.query(400, rdsQueryFault, "ModifyDBInstance", "DBInstanceIdentifier", "private-pg", "BackupRetentionPeriod", "9",
			"VpcSecurityGroupIds.VpcSecurityGroupId.1", foreignFixtureSG)
		if _, err := h.db.Exec("DELETE FROM aws_ec2_network_resources WHERE region=? AND id=?", rdsFixtureRegion, rdsFixtureB); err != nil {
			t.Fatal(err)
		}
		h.query(400, rdsQueryFault, "ModifyDBInstance", "DBInstanceIdentifier", "private-pg", "BackupRetentionPeriod", "9")
		body := h.query(200, "<BackupRetentionPeriod>7</BackupRetentionPeriod>", "DescribeDBInstances", "DBInstanceIdentifier", "private-pg")
		if !strings.Contains(body, rdsFixtureSG) || strings.Contains(body, foreignFixtureSG) || strings.Contains(body, "<Endpoint") {
			t.Fatal("refused replacement changed persisted state or disclosed an endpoint")
		}
	})
	for _, option := range []string{"single-zone", "auto-public", "gateway", "no-native-rows"} {
		t.Run(option, func(t *testing.T) {
			h := newRDSPrivateTopologyHarness(t, true)
			if option != "no-native-rows" {
				h.seedNetwork(option)
			}
			h.subnetGroup()
			h.create(400, rdsQueryFault, "private-pg", "postgres", "false", "private-group")
			h.query(404, "DBInstanceNotFound", "DescribeDBInstances", "DBInstanceIdentifier", "private-pg")
		})
	}
}
