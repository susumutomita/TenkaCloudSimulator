//go:build linux && amd64

package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

const guardRegion = "us-east-1"

func guardDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func guardConfig() Config {
	return Config{Regions: []string{guardRegion}, Account: "000000000000", DefaultPrincipal: "fixture",
		Authorize: func(context.Context, string, string, string) (string, error) { return "allowed", nil },
		ValidateRDSPrivatePostgres: func(_ context.Context, _ *sql.Tx, refs RDSPrivatePostgresRefs) error {
			if refs.Region != guardRegion || !slices.Equal(refs.SubnetIDs, []string{"subnet-01234567", "subnet-12345678"}) || !slices.Equal(refs.SecurityGroupIDs, []string{"sg-01234567"}) {
				return errors.New("unexpected fixture topology")
			}
			return nil
		}}
}

func guardDB(t *testing.T) *sql.DB {
	t.Helper()
	s, err := core.New(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s.Database()
}

func guardOptions(t *testing.T) OwnedPrivatePostgresConfig {
	t.Helper()
	// Intentionally unconfigured: this suite validates SQLite ownership
	// and failure recovery. No native binary can be started from this value.
	return OwnedPrivatePostgresConfig{Region: guardRegion, Identifier: "private-pg", WorkRoot: guardDir(t), Bundle: &rdsruntime.Bundle{}}
}

func guardRow() row {
	return row{Service: "rds", Kind: "dbInstance", Region: guardRegion, Name: "private-pg", ID: "private-pg",
		ARN: "arn:aws:rds:us-east-1:000000000000:db:private-pg", Seq: 2, Generation: 1, Tags: map[string]string{}, Document: map[string]any{
			"Engine": "postgres", "EngineVersion": "15.20", "PubliclyAccessible": false,
			"DBSubnetGroup":     map[string]any{"DBSubnetGroupName": "private-group"},
			"VpcSecurityGroups": []any{map[string]any{"VpcSecurityGroupId": "sg-01234567"}},
		}}
}

func reserveGuardRows(t *testing.T, db *sql.DB, owner *OwnedPrivatePostgres) row {
	t.Helper()
	r := guardRow()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	group := row{Service: "rds", Region: guardRegion, Kind: "dbSubnetGroup", Name: "private-group", ID: "private-group", ARN: "arn:aws:rds:us-east-1:000000000000:subgrp:private-group", Seq: 1, Generation: 1, Tags: map[string]string{}, Document: map[string]any{"Subnets": []any{map[string]any{"SubnetIdentifier": "subnet-01234567"}, map[string]any{"SubnetIdentifier": "subnet-12345678"}}}}
	for _, record := range []row{group, r} {
		if err := insertRow(tx, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range []func(*row){
		func(r *row) { r.Region = "us-west-1" }, func(r *row) { r.ID = "other" }, func(r *row) { r.Name = "renamed" }, func(r *row) { r.Generation = 2 }, func(r *row) { r.Seq = 0 },
	} {
		bad := r
		change(&bad)
		if acquired, err := owner.reserve(tx, bad); err != nil || acquired {
			t.Fatal("reserved nonmatching generation", bad, err)
		}
	}
	if ok, err := owner.reserve(tx, r); err != nil || !ok {
		t.Fatal("canonical reserve", err)
	}
	if ok, err := owner.reserve(tx, r); ok || !errors.Is(err, errOwnedPostgresDebt) {
		t.Fatal("duplicate reserve", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPrivatePostgresOwnerAdmission(t *testing.T) {
	db := guardDB(t)
	cfg := guardConfig()
	o := guardOptions(t)
	for _, change := range []func(*Config, *OwnedPrivatePostgresConfig){
		func(_ *Config, o *OwnedPrivatePostgresConfig) { o.Bundle = nil },
		func(c *Config, _ *OwnedPrivatePostgresConfig) { c.ValidateRDSPrivatePostgres = nil },
		func(_ *Config, o *OwnedPrivatePostgresConfig) { o.Region = "us-west-2" },
		func(_ *Config, o *OwnedPrivatePostgresConfig) { o.Identifier = "Mixed-Case" },
		func(c *Config, _ *OwnedPrivatePostgresConfig) { c.Authorize = nil },
	} {
		c, p := cfg, o
		change(&c, &p)
		e, owner, err := NewWithOwnedPrivatePostgres(db, c, p)
		if err == nil || owner != nil || e != nil {
			t.Fatal("invalid composition admitted")
		}
	}
	o.WorkRoot = filepath.Join(o.WorkRoot, "missing")
	if _, c, err := NewWithOwnedPrivatePostgres(db, cfg, o); c != nil || !errors.Is(err, errOwnedPostgresDebt) {
		t.Fatal("absent work root accepted", err)
	}
	var owner *OwnedPrivatePostgres
	if err := owner.Probe(t.Context()); !errors.Is(err, rdsruntime.ErrNotReady) {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivatePostgresFenceAcquisitionAndRelease(t *testing.T) {
	for _, mode := range []string{"reject-bundle", "occupied", "lost-topology", "foreign-owner", "rolled-back-reservation"} {
		t.Run(mode, func(t *testing.T) {
			db := guardDB(t)
			cfg := guardConfig()
			opt := guardOptions(t)
			e, c, err := NewWithOwnedPrivatePostgres(db, cfg, opt)
			if err != nil {
				t.Fatal(err)
			}
			r := reserveGuardRows(t, db, c)
			if err := installOwnedPostgresFence(db); !errors.Is(err, errOwnedPostgresDebt) {
				t.Fatal("leased row adopted", err)
			}
			c.key = generationOf(r)
			if !c.current(t.Context()) {
				t.Fatal("current owned generation not found")
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if c.current(cancelled) {
				t.Fatal("cancelled lookup accepted")
			}
			for _, stmt := range []string{"UPDATE aws_catalog_resources SET generation=generation+1 WHERE arn=?", "DELETE FROM aws_catalog_resources WHERE arn=?"} {
				if _, err := db.Exec(stmt, r.ARN); err == nil {
					t.Fatal("guarded row mutation accepted", stmt)
				}
			}
			stale := r
			stale.Seq++
			if err := c.retire(stale); err != nil || c.key != generationOf(r) {
				t.Fatal("stale generation retired", err)
			}
			for _, k := range []Kind{Create, Update, Delete, Tag, Untag, Put, Read} {
				unlock := c.lock(guardRegion, k)
				unlock()
				unlock = c.lock("eu-west-1", k)
				unlock()
			}
			if err := c.preflightModify(&request{op: &Op{Name: "ModifyDBInstance"}}, r); err != nil {
				t.Fatal("no-child preflight unexpectedly consulted runtime", err)
			}
			switch mode {
			case "occupied":
				if err := os.Mkdir(filepath.Join(opt.WorkRoot, "retained"), 0700); err != nil {
					t.Fatal(err)
				}
			case "lost-topology":
				if _, err := db.Exec("DELETE FROM aws_catalog_resources WHERE kind='dbSubnetGroup'"); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				if _, err := db.Exec("UPDATE aws_catalog_owned_postgres SET owner='separate-test-owner'"); err != nil {
					t.Fatal(err)
				}
			case "rolled-back-reservation":
				if _, err := db.Exec("DELETE FROM aws_catalog_owned_postgres"); err != nil {
					t.Fatal(err)
				}
			}
			c.startCommitted(t.Context(), r)
			if c.child != nil {
				t.Fatal("bad bundle unexpectedly started a child")
			}
			debt := mode == "occupied" || mode == "foreign-owner" || mode == "rolled-back-reservation"
			if c.debt != debt {
				t.Fatal("wrong ownership decision", mode, c.last, c.debt)
			}
			if err := c.Probe(nil); !errors.Is(err, rdsruntime.ErrNotReady) {
				t.Fatal("nil context accepted", err)
			}
			switch mode {
			case "reject-bundle":
				if err := c.Probe(t.Context()); !errors.Is(err, rdsruntime.ErrBundle) || !errors.Is(err, rdsruntime.ErrNotReady) {
					t.Fatal("rejection reason lost", err)
				}
			case "lost-topology":
				if err := c.Probe(t.Context()); !errors.Is(err, rdsruntime.ErrNotReady) || errors.Is(err, rdsruntime.ErrBundle) {
					t.Fatal("tried to start after topology disappeared", err)
				}
			default:
				if err := c.Probe(t.Context()); !errors.Is(err, errOwnedPostgresDebt) {
					t.Fatal("cleanup uncertainty hidden", err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := c.Close(); debt != errors.Is(err, errOwnedPostgresDebt) || !debt && err != nil {
					t.Fatal("close decision changed", err)
				}
			}
			var n int
			if err := db.QueryRow("SELECT count(*) FROM aws_catalog_owned_postgres").Scan(&n); err != nil {
				t.Fatal(err)
			}
			fenced := mode == "occupied" || mode == "foreign-owner"
			if fenced != (n == 1) {
				t.Fatal("incorrect durable fence", mode, n)
			}
			if _, err := db.Exec("UPDATE aws_catalog_resources SET generation=generation+1 WHERE arn=?", r.ARN); fenced != (err != nil) {
				t.Fatal("write did not respect durable fence", mode, err)
			}
			if e.ownedPrivateRDS != c {
				t.Fatal("wrong lifecycle owner")
			}
		})
	}
}

func TestPrivatePostgresRegionalLifecycleIsolation(t *testing.T) {
	db := guardDB(t)
	cfg := guardConfig()
	const otherRegion = "eu-west-1"
	cfg.Regions = append(cfg.Regions, otherRegion)
	_, owner, err := NewWithOwnedPrivatePostgres(db, cfg, guardOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	home := guardRow()
	child := &rdsruntime.Runtime{} // sentinel only: never probe or close it
	owner.key, owner.child = generationOf(home), child
	modify := &request{ctx: t.Context(), op: &Op{Name: "ModifyDBInstance"}}
	// No durable lease exists. Only the nominated target consults it. Use
	// the same generation as a sentinel so ARN comparison cannot hide an
	// accidental lifecycle lookup outside the immutable target boundary.
	unlock := owner.lock(home.Region, Update)
	err = owner.preflightModify(modify, home)
	unlock()
	if !errors.Is(err, errOwnedPostgresDebt) {
		t.Fatalf("nominated target did not consult the missing lease: %v", err)
	}
	for _, change := range []func(*row){
		func(r *row) { r.Region = otherRegion },
		func(r *row) { r.ID = "other-db" },
	} {
		r := home
		change(&r)
		unlock := owner.lock(r.Region, Update)
		err := owner.preflightModify(modify, r)
		unlock()
		if err != nil {
			t.Fatalf("unrelated target consulted the missing lease: %v", err)
		}
	}

	// Exercise the unlocked foreign-region path against its own canonical
	// row while a lifecycle owner changes those fields under its real lock.
	// Running with -race catches a mutable read even if it returns false.
	foreign := home
	foreign.Region = otherRegion
	foreign.ARN = "arn:aws:rds:eu-west-1:000000000000:db:private-pg"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 128; i++ {
			unlock := owner.lock(home.Region, Update)
			owner.child, owner.key = nil, privateDBGeneration{}
			owner.child, owner.key = child, generationOf(home)
			unlock()
		}
	}()
	for i := 0; i < 128; i++ {
		unlock := owner.lock(foreign.Region, Update)
		err := owner.preflightModify(modify, foreign)
		retireErr := owner.retire(foreign)
		unlock()
		if err != nil || retireErr != nil {
			<-done
			t.Fatalf("unrelated mutation touched the owner: modify %v, retire %v", err, retireErr)
		}
	}
	<-done
	if owner.key != generationOf(home) || owner.child != child {
		t.Fatal("unrelated mutation changed the nominated generation")
	}
}
