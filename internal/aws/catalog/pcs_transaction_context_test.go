package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func pcsContextTeam(t *testing.T, path string) (*core.Store, *harness) {
	t.Helper()
	host, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	p := &policy{deny: map[string]bool{}}
	identities := core.NewIdentities(host)
	engine, err := New(host.Database(), Config{Regions: []string{"us-east-1", "us-west-2"}, Account: "000000000000", DefaultPrincipal: "fixture", Authorize: identities.DecisionContext, AuthorizeSnapshot: identities.DecisionSnapshot}, []*Service{pcs})
	if err != nil {
		t.Fatal(err)
	}
	return host, &harness{t: t, db: host.Database(), engine: engine, policy: p}
}

func pcsContextGrant(t *testing.T, host *core.Store) {
	t.Helper()
	if err := core.NewIdentities(host).Set("fixture", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"pcs:*"}, Resource: core.StringList{"*"}}}}); err != nil {
		t.Fatal(err)
	}
}

func pcsContextFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	frame := groupCatalogFrame(t, db)
	rows, err := db.Query(`SELECT json_array('policy',name,document) FROM identity_policies ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			t.Fatal(err)
		}
		frame = append(frame, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return frame
}

type pcsContextFixture struct {
	input                                             map[string]any
	cluster, group, queue, siblingQueue, siblingGroup string
	sequence, generation                              int64
}

func pcsContextSeed(t *testing.T, h *harness, operation string) pcsContextFixture {
	t.Helper()
	cluster, group := pcsAssociationSeed(t, h, "owner")
	siblingCluster, siblingGroup := pcsAssociationSeed(t, h, "sibling")
	sibling := h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": siblingCluster, "queueName": "sibling", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": siblingGroup}}})
	expect(t, sibling, 200, "")
	f := pcsContextFixture{cluster: cluster, group: group, siblingGroup: siblingGroup, siblingQueue: str(sibling.body["queue"].(map[string]any)["id"])}
	f.input = map[string]any{"clusterIdentifier": cluster, "queueName": "interrupted", "computeNodeGroupConfigurations": []any{map[string]any{"computeNodeGroupId": group}}}
	if operation != "CreateQueue" {
		refs := []any{map[string]any{"computeNodeGroupId": group}}
		made := h.json1("pcs", "CreateQueue", map[string]any{"clusterIdentifier": cluster, "queueName": "interrupted", "computeNodeGroupConfigurations": refs})
		expect(t, made, 200, "")
		f.queue = str(made.body["queue"].(map[string]any)["id"])
		if operation == "DeleteComputeNodeGroup" {
			expect(t, h.json1("pcs", "UpdateQueue", map[string]any{"clusterIdentifier": cluster, "queueIdentifier": f.queue, "computeNodeGroupConfigurations": []any{}}), 200, "")
		}
		f.input = map[string]any{"clusterIdentifier": cluster, "queueIdentifier": f.queue, "computeNodeGroupConfigurations": []any{}}
		if operation == "DeleteComputeNodeGroup" {
			f.input = map[string]any{"clusterIdentifier": cluster, "computeNodeGroupIdentifier": group}
		}
	}
	if operation == "CreateQueue" {
		if err := h.db.QueryRow("SELECT next FROM aws_catalog_sequence").Scan(&f.sequence); err != nil {
			t.Fatal(err)
		}
		f.generation = 1
	} else {
		target := f.queue
		if operation == "DeleteComputeNodeGroup" {
			target = f.group
		}
		if err := h.db.QueryRow("SELECT seq,generation FROM aws_catalog_resources WHERE service='pcs' AND id=?", target).Scan(&f.sequence, &f.generation); err != nil {
			t.Fatal(err)
		}
		if operation == "UpdateQueue" {
			f.generation++
		}
	}
	return f
}

func pcsContextRetry(t *testing.T, h *harness, operation string, f pcsContextFixture) {
	t.Helper()
	result := h.json1("pcs", operation, f.input)
	expect(t, result, 200, "")
	if operation == "DeleteComputeNodeGroup" {
		expect(t, h.json1("pcs", "GetComputeNodeGroup", map[string]any{"clusterIdentifier": f.cluster, "computeNodeGroupIdentifier": f.group}), 400, "ResourceNotFoundException")
	} else {
		queue := f.queue
		if operation == "CreateQueue" {
			queue = str(result.body["queue"].(map[string]any)["id"])
		}
		get := h.json1("pcs", "GetQueue", map[string]any{"clusterIdentifier": f.cluster, "queueIdentifier": queue})
		expect(t, get, 200, "")
		refs := refValues(get.body["queue"].(map[string]any), "computeNodeGroupConfigurations[].computeNodeGroupId")
		if operation == "CreateQueue" && !reflect.DeepEqual(refs, []string{f.group}) || operation == "UpdateQueue" && len(refs) != 0 {
			t.Fatal("fresh retry did not have its intended association effect")
		}
	}
}

func TestPCSTransactionInterruptionAfterStagedWrite(t *testing.T) {
	for _, operation := range []string{"CreateQueue", "UpdateQueue", "DeleteComputeNodeGroup"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(operation+"/"+cause.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.sqlite")
				host, owner := pcsContextTeam(t, path)
				peerHost, peer := pcsContextTeam(t, filepath.Join(t.TempDir(), "peer.sqlite"))
				pcsContextGrant(t, host)
				pcsContextGrant(t, peerHost)
				f := pcsContextSeed(t, owner, operation)
				pcsContextSeed(t, peer, operation)
				before, peerBefore := pcsContextFrame(t, owner.db), pcsContextFrame(t, peer.db)
				var ctx context.Context
				var stop context.CancelFunc
				staged, active, hits := false, false, 0
				conn, err := owner.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_pcs_write", 7, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
						hits++
						active = ctx.Err() == nil
						staged = args[0].Text() != "" && args[1].Text() == f.cluster && args[2].Int64() == f.sequence && args[3].Int64() == f.generation && args[6].Text() == f.siblingGroup
						presence := int64(1)
						if operation == "DeleteComputeNodeGroup" {
							presence = 0
							staged = staged && args[0].Text() == f.group
						} else {
							var entries []map[string]any
							e := json.Unmarshal([]byte(args[4].Text()), &entries)
							staged = staged && e == nil
							if operation == "CreateQueue" {
								staged = staged && len(entries) == 1 && entries[0]["computeNodeGroupId"] == f.group
							} else {
								staged = staged && len(entries) == 0 && args[0].Text() == f.queue
							}
						}
						staged = staged && args[5].Int64() == presence
						if cause == context.Canceled {
							stop()
						}
						<-ctx.Done()
						result.ResultNull()
					})
				})
				quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
				event, prefix, kind, where := "INSERT", "NEW", "queue", "NEW.name='interrupted' AND NEW.parent="+quote(f.cluster)
				refs := "json_extract(NEW.document,'$.computeNodeGroupConfigurations')"
				if operation == "UpdateQueue" {
					event = "UPDATE"
					where = "NEW.id=" + quote(f.queue)
				}
				if operation == "DeleteComputeNodeGroup" {
					event, prefix, kind = "DELETE", "OLD", "computeNodeGroup"
					where = "OLD.id=" + quote(f.group)
					refs = "'[]'"
				}
				if err == nil {
					_, err = conn.ExecContext(t.Context(), fmt.Sprintf(`CREATE TEMP TRIGGER observe_pcs AFTER %s ON aws_catalog_resources WHEN %s.service='pcs' AND %s.kind=%s AND %s BEGIN
     SELECT observe_pcs_write(%s.id,%s.parent,%s.seq,%s.generation,%s,
       (SELECT count(*) FROM aws_catalog_resources WHERE service='pcs' AND id=%s),
       (SELECT json_extract(document,'$.computeNodeGroupConfigurations[0].computeNodeGroupId') FROM aws_catalog_resources WHERE service='pcs' AND id=%s)); END`, event, prefix, prefix, quote(kind), where, prefix, prefix, prefix, prefix, refs, quote(f.group), quote(f.siblingQueue)))
				}
				closeErr := conn.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
				if cause == context.Canceled {
					ctx, stop = context.WithCancel(t.Context())
				} else {
					ctx, stop = context.WithTimeout(t.Context(), time.Second)
				}
				defer stop()
				data, err := json.Marshal(f.input)
				if err != nil {
					t.Fatal(err)
				}
				b := owner.binding("pcs")
				request := httptest.NewRequestWithContext(ctx, "POST", "/", bytes.NewReader(data))
				request.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+"."+operation)
				request.Header.Set("Content-Type", "application/x-amz-json-"+b.model.Metadata.JSONVersion)
				sign(request, "pcs", "us-east-1")
				output := owner.do(request)
				if !staged || !active || hits != 1 || !errors.Is(ctx.Err(), cause) {
					t.Fatalf("expected one actual active staged write: staged=%v active=%v hits=%d cause=%v", staged, active, hits, ctx.Err())
				}
				if output.status < 400 || output.body["queue"] != nil || output.body["computeNodeGroup"] != nil {
					t.Error("interrupted operation acknowledged native success")
				}
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				_, resumed := pcsContextTeam(t, path)
				if !reflect.DeepEqual(before, pcsContextFrame(t, resumed.db)) || !reflect.DeepEqual(peerBefore, pcsContextFrame(t, peer.db)) {
					t.Fatal("interrupted write changed durable row/sequence/token/tag/policy or private peer frame")
				}
				pcsContextRetry(t, resumed, operation, f)
				if !reflect.DeepEqual(peerBefore, pcsContextFrame(t, peer.db)) {
					t.Fatal("fresh retry changed private peer")
				}
			})
		}
	}
}

// This deliberately observes transaction admission, not the generic HTTP
// preflight lookups, which retain their existing cancellation behavior.
func TestPCSTransactionAdmissionContext(t *testing.T) {
	for _, operation := range []string{"CreateQueue", "UpdateQueue", "DeleteComputeNodeGroup"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(operation+"/"+cause.Error(), func(t *testing.T) {
				host, owner := pcsContextTeam(t, filepath.Join(t.TempDir(), "owner.sqlite"))
				pcsContextGrant(t, host)
				f := pcsContextSeed(t, owner, operation)
				before := pcsContextFrame(t, owner.db)
				blocker, err := owner.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Close()
				if owner.db.Stats().MaxOpenConnections != 1 {
					t.Fatal("fixture must hold the only pool connection")
				}
				var ctx context.Context
				var stop context.CancelFunc
				if cause == context.Canceled {
					ctx, stop = context.WithCancel(t.Context())
				} else {
					ctx, stop = context.WithTimeout(t.Context(), time.Second)
				}
				defer stop()
				b := owner.binding("pcs")
				q := request{e: owner.engine, b: b, op: b.ops[operation], ctx: ctx}
				type result struct {
					tx  *sql.Tx
					err error
				}
				done := make(chan result, 1)
				waits := owner.db.Stats().WaitCount
				go func() { tx, err := q.beginMutation(); done <- result{tx, err} }()
				observation := time.NewTimer(3 * time.Second)
				defer observation.Stop()
				poll := time.NewTicker(time.Millisecond)
				defer poll.Stop()
				for owner.db.Stats().WaitCount == waits {
					select {
					case <-poll.C:
					case <-observation.C:
						t.Fatal("no actual pool wait observed")
					}
				}
				if ctx.Err() != nil {
					t.Fatal("context ended before the pool wait was observed")
				}
				if cause == context.Canceled {
					stop()
				}
				<-ctx.Done()
				var got result
				returnedWhileHeld := false
				select {
				case got = <-done:
					returnedWhileHeld = true
				case <-time.After(time.Second):
					t.Error("context did not stop transaction admission while blocker remained held")
				}
				if err := blocker.Close(); err != nil {
					t.Fatal(err)
				}
				if !returnedWhileHeld {
					select {
					case got = <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("transaction did not unwind after releasing fixture connection")
					}
				}
				if got.tx != nil {
					_ = got.tx.Rollback()
				}
				if !returnedWhileHeld || got.tx != nil || !errors.Is(got.err, cause) {
					t.Errorf("expected original typed context failure and no transaction while pool blocked: returned=%v transaction=%v error=%v", returnedWhileHeld, got.tx != nil, got.err)
				}
				if !reflect.DeepEqual(before, pcsContextFrame(t, owner.db)) {
					t.Fatal("queued interruption changed persistent frame")
				}
				pcsContextRetry(t, owner, operation, f)
			})
		}
	}
}
