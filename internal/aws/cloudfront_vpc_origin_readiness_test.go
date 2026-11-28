package aws

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// These are complete logical frames of owned fixture stores, not snapshots of
// host files or user credentials. Close each cursor before another pool query.
func cfVPCContractFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_master ORDER BY type,name")
	if err != nil {
		t.Fatal(err)
	}
	var frame, tables []string
	for rows.Next() {
		var kind, name, parent, statement string
		if err = rows.Scan(&kind, &name, &parent, &statement); err != nil {
			t.Fatal(err)
		}
		value, _ := json.Marshal([]string{kind, name, parent, statement})
		frame = append(frame, string(value))
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, table := range tables {
		query := `SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`
		values, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := values.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var records []string
		for values.Next() {
			row := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range row {
				pointers[i] = &row[i]
			}
			if err = values.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(row)
			records = append(records, string(raw))
		}
		if err = values.Err(); err != nil {
			t.Fatal(err)
		}
		values.Close()
		slices.Sort(records)
		frame = append(frame, table+":"+strings.Join(records, "\n"))
	}
	return frame
}

func cfVPCContractConnection(t *testing.T, db *sql.DB, apply func(*sqlite3.Conn) error) {
	t.Helper()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error { return apply(raw.(driver.Conn).Raw()) })
	closeErr := connection.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestCloudFrontVpcOriginConstructorFailureIsAtomic(t *testing.T) {
	if value, err := NewCloudFrontStore(nil); err == nil || value != nil {
		t.Fatal("nil database produced a store")
	}
	closed, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Close(); err != nil {
		t.Fatal(err)
	}
	if value, err := NewCloudFrontStore(closed.Database()); err == nil || value != nil {
		t.Fatal("closed database produced a store")
	}
	for _, stage := range []string{"origin schema", "distribution schema", "commit"} {
		t.Run(stage, func(t *testing.T) {
			store, err := core.New("")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			peer, err := core.New("")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = peer.Close() })
			for _, owned := range []*core.Store{store, peer} {
				if err = owned.Create("preserved", "us-east-1"); err != nil {
					t.Fatal(err)
				}
				if _, err = owned.Put("preserved", "value", "text/plain", []byte("committed"), "", ""); err != nil {
					t.Fatal(err)
				}
			}
			conflict := "aws_cloudfront_vpc_origins"
			if stage == "distribution schema" {
				conflict = "aws_cloudfront_distributions"
			}
			if stage != "commit" {
				if _, err = store.Database().Exec("CREATE TABLE schema_holder(value INTEGER); CREATE INDEX " + conflict + " ON schema_holder(value)"); err != nil {
					t.Fatal(err)
				}
			}
			before, peerBefore := cfVPCContractFrame(t, store.Database()), cfVPCContractFrame(t, peer.Database())
			commits := 0
			if stage == "commit" {
				cfVPCContractConnection(t, store.Database(), func(connection *sqlite3.Conn) error {
					connection.CommitHook(func() bool { commits++; return false })
					return nil
				})
			}
			value, err := NewCloudFrontStore(store.Database())
			if stage == "commit" {
				cfVPCContractConnection(t, store.Database(), func(connection *sqlite3.Conn) error { connection.CommitHook(nil); return nil })
				if commits != 1 {
					t.Fatalf("constructor commit hook count %d", commits)
				}
			}
			if err == nil || value != nil {
				t.Fatal("unavailable schema/commit acknowledged a ready store")
			}
			if stage != "commit" && !strings.Contains(err.Error(), conflict) {
				t.Fatal("failure did not identify intended schema conflict", err)
			}
			if !reflect.DeepEqual(before, cfVPCContractFrame(t, store.Database())) || !reflect.DeepEqual(peerBefore, cfVPCContractFrame(t, peer.Database())) {
				t.Fatal("failed constructor changed schema, saved data or peer")
			}
			if stage != "commit" {
				if _, err = store.Database().Exec("DROP INDEX " + conflict); err != nil {
					t.Fatal(err)
				}
			}
			if value, err = NewCloudFrontStore(store.Database()); err != nil || value == nil {
				t.Fatal("repaired constructor unavailable", err)
			}
			if !reflect.DeepEqual(peerBefore, cfVPCContractFrame(t, peer.Database())) {
				t.Fatal("constructor retry changed peer")
			}
		})
	}
}

func TestCloudFrontVpcOriginReadOutageRefusesCreateAndRepairs(t *testing.T) {
	for _, column := range []string{"name", ""} {
		t.Run(fmt.Sprintf("column_%q", column), func(t *testing.T) {
			host, peer := cfHarnessFor(t), cfHarnessFor(t)
			peer.origin(peer.request("POST", cloudFrontPath, cfCreateXML(peer.config)), 202)
			before, peerBefore := cfVPCContractFrame(t, host.store.Database()), cfVPCContractFrame(t, peer.store.Database())
			denied := 0
			cfVPCContractConnection(t, host.store.Database(), func(connection *sqlite3.Conn) error {
				return connection.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, table, field, _, _ string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_READ && table == "aws_cloudfront_vpc_origins" && field == column {
						denied++
						return sqlite3.AUTH_DENY
					}
					return sqlite3.AUTH_OK
				})
			})
			failed := host.request("POST", cloudFrontPath, cfCreateXML(host.config))
			cfVPCContractConnection(t, host.store.Database(), func(connection *sqlite3.Conn) error { return connection.SetAuthorizer(nil) })
			cfExpectError(t, failed, 500, "InternalError")
			if denied != 1 || failed.Header().Get("ETag") != "" || failed.Header().Get("Location") != "" {
				t.Fatal("outage did not refuse exact native read without acknowledgment", denied)
			}
			if !reflect.DeepEqual(before, cfVPCContractFrame(t, host.store.Database())) || !reflect.DeepEqual(peerBefore, cfVPCContractFrame(t, peer.store.Database())) {
				t.Fatal("read outage changed owner or peer")
			}
			created := host.origin(host.request("POST", cloudFrontPath, cfCreateXML(host.config)), 202)
			fresh := host.origin(host.request("GET", cloudFrontPath+"/"+created.ID, ""), 200)
			if fresh.ETag != created.ETag || fresh.Config.ARN != host.alb || fresh.Config.Name != "origin-a" || fresh.Status != "Deployed" {
				t.Fatal("repair did not persist real origin configuration")
			}
			host.origin(host.request("DELETE", cloudFrontPath+"/"+created.ID, "", "If-Match", created.ETag), 202)
			cfExpectError(t, host.request("GET", cloudFrontPath+"/"+created.ID, ""), 404, "EntityNotFound")
			if !reflect.DeepEqual(peerBefore, cfVPCContractFrame(t, peer.store.Database())) {
				t.Fatal("repair/cleanup changed peer")
			}
		})
	}
}

func TestCloudFrontVpcOriginUnavailableAuthorityAndStoreGuards(t *testing.T) {
	host, peer := cfHarnessFor(t), cfHarnessFor(t)
	peer.origin(peer.request("POST", cloudFrontPath, cfCreateXML(peer.config)), 202)
	created := host.origin(host.request("POST", cloudFrontPath, cfCreateXML(host.config)), 202)
	before, peerBefore := cfVPCContractFrame(t, host.store.Database()), cfVPCContractFrame(t, peer.store.Database())
	authority := host.gateway.Identities
	host.gateway.Identities = nil
	failed := host.request("GET", cloudFrontPath+"/"+created.ID, "")
	host.gateway.Identities = authority
	cfExpectError(t, failed, 500, "InternalError")
	if failed.Header().Get("ETag") != "" || failed.Header().Get("Location") != "" {
		t.Fatal("missing authority exposed successful metadata")
	}
	// Exported store guard: an empty ID cannot silently become a creation.
	config, _, err := parseCFOriginRequest([]byte(cfCreateXML(host.config)), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.gateway.CloudFront.Update(t.Context(), "", created.ETag, config); err == nil {
		t.Fatal("empty update ID was accepted")
	} else {
		var failure cfFailure
		if !errors.As(err, &failure) || failure.status != 404 || failure.code != "EntityNotFound" {
			t.Fatal("wrong empty-ID refusal", err)
		}
	}
	// CloudFormation's preallocated-ID component contract, not an HTTP path.
	transaction, err := host.store.Database().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = saveCFOriginTx(t.Context(), transaction, "not-a-vpc-origin", "", true, config, nil)
	rollbackErr := transaction.Rollback()
	var invalid cfFailure
	if !errors.As(err, &invalid) || invalid.status != 400 || invalid.code != "InvalidArgument" || rollbackErr != nil {
		t.Fatal("invalid preallocated ID not refused", err, rollbackErr)
	}
	if !reflect.DeepEqual(before, cfVPCContractFrame(t, host.store.Database())) || !reflect.DeepEqual(peerBefore, cfVPCContractFrame(t, peer.store.Database())) {
		t.Fatal("guard refusal changed owner or peer")
	}
	fresh := host.origin(host.request("GET", cloudFrontPath+"/"+created.ID, ""), 200)
	if fresh.ETag != created.ETag || fresh.Config.ARN != host.alb {
		t.Fatal("restored authority lost existing origin")
	}
	host.origin(host.request("DELETE", cloudFrontPath+"/"+created.ID, "", "If-Match", created.ETag), 202)
}
