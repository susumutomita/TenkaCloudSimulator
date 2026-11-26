//go:build linux && amd64

package aws

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

func TestRDSOwnedPrivatePostgresQuery(t *testing.T) {
	dir, pin := os.Getenv("TCSIM_PG_TEST_BUNDLE"), os.Getenv("TCSIM_PG_TEST_PIN")
	if dir == "" && pin == "" {
		t.Skip("explicit approved PostgreSQL bundle and independent pin not supplied")
	}
	bundle, err := rdsruntime.OpenBundle(dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"tag", "delete", "modify", "poststart-drift", "acquisition-debt"} {
		t.Run(mode, func(t *testing.T) { runOwnedPrivatePostgres(t, bundle, mode) })
	}
}

func runOwnedPrivatePostgres(t *testing.T, bundle *rdsruntime.Bundle, mode string) {
	t.Helper()
	h := newRDSPrivateTopologyHarness(t, false)
	h.seedNetwork()
	root, err := os.MkdirTemp("", "rds-query-pg-owned-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Never sweep a disposable PostgreSQL directory while its cleanup is unconfirmed.
		if err := os.Remove(root); err != nil {
			t.Error("owned Query work root not confirmed empty")
		}
	})
	config := catalog.Config{Regions: []string{rdsFixtureRegion}, Account: "000000000000",
		DefaultPrincipal: "fixture-operator",
		Authorize: func(_ context.Context, who, action, _ string) (string, error) {
			if who != "fixture-operator" || action == h.denyAction {
				return "denied", nil
			}
			return "allowed", nil
		}}
	var reads int
	var drift bool
	var mutated chan error
	var occupiedErr error
	occupiedPath := filepath.Join(root, "managed-postgres-15")
	if mode == "poststart-drift" {
		mutated = make(chan error, 1)
	}
	config.ValidateRDSPrivatePostgres = func(ctx context.Context, tx *sql.Tx, refs catalog.RDSPrivatePostgresRefs) error {
		reads++
		err := ValidateRDSPrivatePostgresTopology(ctx, tx, refs)
		if mode == "acquisition-debt" && drift && reads == 2 {
			// Simulate an unexpected occupied slot without creating a process.
			occupiedErr = os.Mkdir(occupiedPath, 0700)
		}
		if mutated != nil && drift && reads == 2 {
			go func() {
				// The second check holds the only SQLite connection. Once it
				// closes, remove a native subnet before post-start validation.
				_, writeErr := h.db.ExecContext(t.Context(),
					"DELETE FROM aws_ec2_network_resources WHERE id=? AND region=?", rdsFixtureB, rdsFixtureRegion)
				mutated <- writeErr
			}()
		}
		return err
	}
	options := catalog.OwnedPrivatePostgresConfig{Region: rdsFixtureRegion, Identifier: "owned-pg", Bundle: bundle, WorkRoot: root}
	engine, owner, err := catalog.NewWithOwnedPrivatePostgres(h.db, config, options)
	if err != nil {
		t.Fatal(err)
	}
	h.engine = engine
	t.Cleanup(func() {
		if err := owner.Close(); mode == "acquisition-debt" {
			if err == nil {
				t.Error("unknown acquisition was treated as clean close")
			}
			// Only this test created the empty marker; no process owns it.
			if err := os.Remove(occupiedPath); err != nil {
				t.Error("test-only occupied marker was unexpectedly absent")
			}
		} else if err != nil {
			t.Error(err)
		}
	})
	h.subnetGroup()
	h.denyAction = "rds:CreateDBInstance"
	h.create(403, "AccessDenied", "owned-pg", "postgres", "false", "private-group")
	h.denyAction = ""
	if reads != 0 || ownedQuerySlotPresent(t, root) {
		t.Fatal("unauthorized create reached native reads or process")
	}
	h.query(400, rdsQueryFault, "CreateDBInstance", "DBInstanceIdentifier", "owned-pg", "DBInstanceClass", "db.t3.micro", "Engine", "postgres",
		"PubliclyAccessible", "false", "DBSubnetGroupName", "private-group",
		"VpcSecurityGroupIds.VpcSecurityGroupId.1", foreignFixtureSG)
	if ownedQuerySlotPresent(t, root) {
		t.Fatal("wrong native ownership opened a process")
	}
	reads, drift = 0, true
	// This actual catalog Query invocation waits for the bounded local host
	// operation; no test-only handler substitutes for CreateDBInstance.
	ok := ownedQuery(t.Context(), h.engine, "CreateDBInstance", "DBInstanceIdentifier", "owned-pg", "DBInstanceClass", "db.t3.micro",
		"Engine", "postgres", "PubliclyAccessible", "false", "DBSubnetGroupName", "private-group",
		"VpcSecurityGroupIds.VpcSecurityGroupId.1", rdsFixtureSG)
	if ok.Code != 200 || !strings.Contains(ok.Body.String(), "<DBInstanceStatus>creating</DBInstanceStatus>") ||
		strings.Contains(ok.Body.String(), "<Endpoint") {
		t.Fatal("Create did not retain its metadata-only result", ok.Code, ok.Body.String())
	}
	if mode == "acquisition-debt" {
		if reads != 2 || occupiedErr != nil || owner.Probe(t.Context()) == nil || !ownedQuerySlotPresent(t, root) {
			t.Fatal("unknown acquisition did not retain its exact fence", reads, occupiedErr)
		}
		if response := ownedQuery(t.Context(), h.engine, "DeleteDBInstance", "DBInstanceIdentifier", "owned-pg",
			"SkipFinalSnapshot", "true"); response.Code < 400 {
			t.Fatal("unknown cleanup permitted deletion of its generation")
		}
		if err := owner.Close(); err == nil {
			t.Fatal("unknown cleanup was not sticky")
		}
		reopened := t.TempDir() // separate empty root must still see durable debt
		if err := os.Chmod(reopened, 0700); err != nil {
			t.Fatal(err)
		}
		options.WorkRoot = reopened
		if _, second, err := catalog.NewWithOwnedPrivatePostgres(h.db, config, options); err == nil || second != nil {
			t.Fatal("reopen ignored durable acquisition debt")
		}
		h.query(200, "<DBInstanceStatus>creating</DBInstanceStatus>", "DescribeDBInstances", "DBInstanceIdentifier", "owned-pg")
		t.Log("synthetic occupied slot retained the SQLite generation fence without launching")
		return
	}
	if mode == "poststart-drift" {
		select {
		case err := <-mutated:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("native topology drift did not finish")
		}
		if reads != 3 || owner.Probe(t.Context()) == nil || ownedQuerySlotPresent(t, root) {
			t.Fatal("post-start native mismatch did not physically close its owned process")
		}
		h.query(200, "<DBInstanceStatus>creating</DBInstanceStatus>", "DescribeDBInstances", "DBInstanceIdentifier", "owned-pg")
		t.Log("post-handshake native drift confirmed physical teardown; metadata stayed creating")
		return
	}
	if err := owner.Probe(t.Context()); err != nil || !ownedQuerySlotPresent(t, root) {
		t.Fatal("current authorized generation did not start and authenticate", err)
	}
	h.denyAction = "rds:DeleteDBInstance"
	h.query(403, "AccessDenied", "DeleteDBInstance", "DBInstanceIdentifier", "owned-pg", "SkipFinalSnapshot", "true")
	h.denyAction = ""
	if err := owner.Probe(t.Context()); err != nil {
		t.Fatal("denied delete retired the process", err)
	}
	h.query(400, rdsQueryFault, "ModifyDBInstance", "DBInstanceIdentifier", "owned-pg", "BackupRetentionPeriod", "9",
		"VpcSecurityGroupIds.VpcSecurityGroupId.1", foreignFixtureSG)
	if err := owner.Probe(t.Context()); err != nil || !ownedQuerySlotPresent(t, root) {
		t.Fatal("invalid Modify retired its healthy PostgreSQL workload", err)
	}
	unchanged := h.query(200, "<DBInstanceStatus>creating</DBInstanceStatus>", "DescribeDBInstances", "DBInstanceIdentifier", "owned-pg")
	if strings.Contains(unchanged, "<BackupRetentionPeriod>9</BackupRetentionPeriod>") ||
		strings.Contains(unchanged, foreignFixtureSG) || strings.Contains(unchanged, "<Endpoint") {
		t.Fatal("invalid Modify changed its original metadata")
	}
	mirrorRoot, err := os.MkdirTemp("", "rds-query-pg-reopen-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(mirrorRoot) })
	options.WorkRoot = mirrorRoot
	if _, reopened, err := catalog.NewWithOwnedPrivatePostgres(h.db, config, options); err == nil || reopened != nil {
		t.Fatal("foreign/reopened owner adopted the existing durable generation")
	}

	// A separate metadata-only engine has no in-memory controller but still
	// cannot mutate the same exact row while it is leased. Issue its two
	// writes concurrently through the real Query dispatcher.
	arn := "arn:aws:rds:" + rdsFixtureRegion + ":000000000000:db:owned-pg"
	other, err := catalog.New(h.db, config, catalog.Services())
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan int, 2)
	go func() {
		updates <- ownedQuery(t.Context(), other, "DeleteDBInstance", "DBInstanceIdentifier", "owned-pg", "SkipFinalSnapshot", "true").Code
	}()
	go func() {
		updates <- ownedQuery(t.Context(), other, "AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "Stage", "Tags.Tag.1.Value", "raced").Code
	}()
	for range 2 {
		if status := <-updates; status < 400 {
			t.Fatalf("durable RDS fence accepted a racing mutation: %d", status)
		}
	}
	if err := owner.Probe(t.Context()); err != nil {
		t.Fatal("racing metadata-only lifecycle mutation broke the owned generation", err)
	}

	if mode == "modify" {
		ok := h.query(200, "<BackupRetentionPeriod>7</BackupRetentionPeriod>", "ModifyDBInstance", "DBInstanceIdentifier", "owned-pg",
			"BackupRetentionPeriod", "7")
		if owner.Probe(t.Context()) == nil || ownedQuerySlotPresent(t, root) ||
			!strings.Contains(ok, "<DBInstanceStatus>creating</DBInstanceStatus>") || strings.Contains(ok, "<Endpoint") {
			t.Fatal("valid Modify did not retire the original generation as configuration only")
		}
		t.Log("foreign-SG Modify left exact metadata and workload intact; valid Modify retired and committed")
	}
	if mode == "tag" {
		h.query(200, "AddTagsToResourceResponse", "AddTagsToResource", "ResourceName", arn, "Tags.Tag.1.Key", "Stage", "Tags.Tag.1.Value", "safe")
		if owner.Probe(t.Context()) == nil || ownedQuerySlotPresent(t, root) {
			t.Fatal("generation-advancing tag did not retire its owned process first")
		}
	}
	h.query(200, "<DBInstanceStatus>deleting</DBInstanceStatus>", "DeleteDBInstance", "DBInstanceIdentifier", "owned-pg", "SkipFinalSnapshot", "true")
	if owner.Probe(t.Context()) == nil || ownedQuerySlotPresent(t, root) {
		t.Fatal("authorized deletion did not remove the private workload")
	}
	h.create(200, "CreateDBInstanceResponse", "owned-pg", "postgres", "false", "private-group")
	if owner.Probe(t.Context()) == nil || ownedQuerySlotPresent(t, root) {
		t.Fatal("same-name recreation silently adopted a new generation")
	}
	t.Log("native PostgreSQL probe passed; IAM and racing writer safeguards held; authorized lifecycle teardown removed the slot")
}

func ownedQuerySlotPresent(t *testing.T, root string) bool {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries) != 0
}

func ownedQuery(ctx context.Context, e *catalog.Engine, action string, members ...string) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(ctx, 65*time.Second)
	defer cancel()
	form := url.Values{"Action": {action}, "Version": {"2014-10-31"}}
	for i := 0; i < len(members); i += 2 {
		form.Set(members[i], members[i+1])
	}
	params := form.Encode()
	request := httptest.NewRequest("POST", "/", strings.NewReader(params)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIM/20261125/"+rdsFixtureRegion+"/rds/aws4_request, SignedHeaders=host, Signature=0")
	response := httptest.NewRecorder()
	e.ServeHTTP(response, request)
	return response
}
