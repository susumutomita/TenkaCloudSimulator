package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func openQueryTeam(t *testing.T, path string) (*core.Store, *harness) {
	t.Helper()
	host, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	h := &harness{t: t, db: host.Database(), policy: &policy{deny: map[string]bool{}}}
	h.engine, err = New(h.db, Config{
		Regions: []string{"us-east-1"}, Account: "000000000000",
		DefaultPrincipal: "fixture", Authorize: h.policy.authorize,
		AuthorizeSnapshot: func(context.Context, string) (func(string, string) string, error) {
			return func(string, string) string { return "allowed" }, nil
		},
	}, []*Service{resourceGroups, ecr})
	if err != nil {
		t.Fatal(err)
	}
	return host, h
}

func groupCatalogFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT json_array('resource',service,region,kind,parent,id,name,arn,document,tags,seq,generation) FROM aws_catalog_resources
		UNION ALL SELECT json_array('sequence',id,next) FROM aws_catalog_sequence
		UNION ALL SELECT json_array('token',service,region,operation,token,fingerprint,arn,created) FROM aws_catalog_idempotency ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var frame []string
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

func TestResourceGroupRequestInterruptionBeforeCommit(t *testing.T) {
	for _, operation := range []string{"CreateGroup", "UpdateGroupQuery"} {
		for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(operation+"/"+cause.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.db")
				host, owner := openQueryTeam(t, path)
				_, peer := openQueryTeam(t, filepath.Join(t.TempDir(), "peer.db"))
				blue, green := repositoryTagQuery("Team", "blue"), repositoryTagQuery("Team", "green")
				group := func(h *harness, name string) {
					t.Helper()
					expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": name, "ResourceQuery": blue}), 200, "")
				}
				repo := func(h *harness, name, value string) string {
					t.Helper()
					result := h.json1("ecr", "CreateRepository", map[string]any{"repositoryName": name, "tags": []any{map[string]any{"Key": "Team", "Value": value}}})
					expect(t, result, 200, "")
					return str(result.body["repository"].(map[string]any)["repositoryArn"])
				}
				group(owner, "sibling")
				group(peer, "target")
				blueARN := repo(owner, "blue-repo", "blue")
				greenARN := repo(owner, "green-repo", "green")
				peerARN := repo(peer, "peer-repo", "blue")
				members := func(h *harness, name, want string) {
					t.Helper()
					result := h.rest("resource-groups", "POST", "/list-group-resources", map[string]any{"Group": name})
					expect(t, result, 200, "")
					items := result.body["ResourceIdentifiers"].([]any)
					if len(items) != 1 || items[0].(map[string]any)["ResourceArn"] != want {
						t.Fatal("unexpected real repository membership", items)
					}
				}
				var seq, generation int64
				endpoint, event := "/groups", "INSERT"
				input := map[string]any{"Name": "target", "ResourceQuery": green}
				if operation == "UpdateGroupQuery" {
					group(owner, "target")
					members(owner, "target", blueARN)
					endpoint, event = "/update-group-query", "UPDATE"
					input = map[string]any{"Group": "target", "ResourceQuery": green}
					if err := owner.db.QueryRow("SELECT seq,generation FROM aws_catalog_resources WHERE name='target'").Scan(&seq, &generation); err != nil {
						t.Fatal(err)
					}
				} else if err := owner.db.QueryRow("SELECT next FROM aws_catalog_sequence").Scan(&seq); err != nil {
					t.Fatal(err)
				}
				saved, peerSaved := groupCatalogFrame(t, owner.db), groupCatalogFrame(t, peer.db)
				var ctx context.Context
				var stop context.CancelFunc
				var hits int
				var staged bool
				conn, err := owner.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_group_write", 6, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
						hits++
						staged = args[0].Text() == "arn:aws:resource-groups:us-east-1:000000000000:group/target" &&
							args[1].Int64() == seq && args[2].Int64() == generation+1 && args[3].Text() == green["Query"] &&
							args[4].Text() == greenARN && args[5].Text() == blue["Query"]
						if cause == context.Canceled {
							stop()
						}
						<-ctx.Done()
						result.ResultNull()
					})
				})
				if err == nil {
					// The group row has changed in SQLite. Read its new query and a matching
					// real ECR repository, plus the unmodified sibling, before commit.
					_, err = conn.ExecContext(t.Context(), "CREATE TEMP TRIGGER observe_group AFTER "+event+` ON aws_catalog_resources WHEN NEW.service='resource-groups' AND NEW.name='target' BEGIN
						SELECT observe_group_write(NEW.arn,NEW.seq,NEW.generation,json_extract(NEW.document,'$.ResourceQuery.Query'),
							(SELECT arn FROM aws_catalog_resources WHERE service='ecr' AND json_extract(tags,'$.Team')=json_extract(json_extract(NEW.document,'$.ResourceQuery.Query'),'$.TagFilters[0].Values[0]')),
							(SELECT json_extract(document,'$.ResourceQuery.Query') FROM aws_catalog_resources WHERE service='resource-groups' AND name='sibling'));
						END`)
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
				body, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
				sign(request, "resource-groups", "us-east-1")
				result := owner.do(request)
				if !staged || hits != 1 || !errors.Is(ctx.Err(), cause) {
					t.Fatalf("expected staged native write before interruption: staged=%v hits=%d cause=%v", staged, hits, ctx.Err())
				}
				if result.status < 400 || result.body["Group"] != nil || result.body["GroupQuery"] != nil || result.body["ResourceQuery"] != nil {
					t.Errorf("interrupted %s returned HTTP %d with a success response", operation, result.status)
				}
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				_, resumed := openQueryTeam(t, path)
				if !slices.Equal(saved, groupCatalogFrame(t, resumed.db)) || !slices.Equal(peerSaved, groupCatalogFrame(t, peer.db)) {
					t.Fatal("interrupted operation changed durable catalog state after reopen")
				}
				members(resumed, "sibling", blueARN)
				members(peer, "target", peerARN)
				if operation == "CreateGroup" {
					expect(t, resumed.rest("resource-groups", "POST", "/get-group-query", map[string]any{"Group": "target"}), 404, "NotFoundException")
				} else {
					members(resumed, "target", blueARN)
				}
				expect(t, resumed.rest("resource-groups", "POST", endpoint, input), 200, "")
				members(resumed, "target", greenARN)
				var retriedSeq, retriedGeneration int64
				err = resumed.db.QueryRow("SELECT seq,generation FROM aws_catalog_resources WHERE name='target'").Scan(&retriedSeq, &retriedGeneration)
				if err != nil || retriedSeq != seq || retriedGeneration != generation+1 {
					t.Fatalf("retry did not produce exactly one durable revision: seq=%d generation=%d err=%v", retriedSeq, retriedGeneration, err)
				}
				members(resumed, "sibling", blueARN)
				members(peer, "target", peerARN)
			})
		}
	}
}
