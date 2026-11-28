package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func openMSKTeam(t *testing.T, path string) (*core.Store, *harness) {
	t.Helper()
	host, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.Close() })
	ids := core.NewIdentities(host)
	engine, err := New(host.Database(), Config{Regions: []string{"us-east-1", "ap-northeast-1"}, Account: "000000000000", DefaultPrincipal: core.Administrator, Authorize: ids.DecisionContext, AuthorizeSnapshot: ids.DecisionSnapshot}, []*Service{msk})
	if err != nil {
		t.Fatal(err)
	}
	return host, &harness{t: t, db: host.Database(), engine: engine}
}

func TestMSKCreateUsesOnePolicyRevision(t *testing.T) {
	host, h := openMSKTeam(t, filepath.Join(t.TempDir(), "msk.db"))
	ids := core.NewIdentities(host)
	arn, _ := createMSKTestConfiguration(t, h, "shared", nil)
	const target = "arn:aws:kafka:us-east-1:000000000000:cluster/candidate/*"
	grant := func(action, resource string) core.Statement {
		return core.Statement{Effect: "Allow", Action: core.StringList{action}, Resource: core.StringList{resource}}
	}
	cluster, config, tags := grant("kafka:CreateCluster", target), grant("kafka:CreateCluster", arn), grant("kafka:TagResource", target)
	set := func(statements ...core.Statement) {
		t.Helper()
		if err := ids.Set("team", core.Policy{Version: "2012-10-17", Statement: statements}); err != nil {
			t.Fatal(err)
		}
	}
	h.engine.cfg.DefaultPrincipal = "team"
	input := mskTestCluster("candidate", map[string]any{"arn": arn, "revision": 1})
	before := groupCatalogFrame(t, h.db)
	// Update the real policy after its first evaluation. Neither persisted revision grants all three actions.
	set(cluster)
	captures := 0
	h.engine.cfg.AuthorizeSnapshot = func(ctx context.Context, p string) (func(string, string) string, error) {
		captures++
		decide, err := ids.DecisionSnapshot(ctx, p)
		if err != nil {
			return nil, err
		}
		first := true
		return func(action, resource string) string {
			result := decide(action, resource)
			if first {
				first = false
				set(config, tags)
			}
			return result
		}, nil
	}
	expect(t, h.rest("kafka", "POST", mskClusters, input), 403, "ForbiddenException")
	if captures != 1 {
		t.Fatalf("policy captured %d times", captures)
	}
	h.engine.cfg.AuthorizeSnapshot = ids.DecisionSnapshot
	for _, statements := range [][]core.Statement{{config, tags}, {cluster, tags}, {cluster, config}} {
		set(statements...)
		expect(t, h.rest("kafka", "POST", mskClusters, input), 403, "ForbiddenException")
	}
	set(cluster, tags)
	missing := mskTestCluster("candidate", map[string]any{"arn": arn + "-missing", "revision": 1})
	expect(t, h.rest("kafka", "POST", mskClusters, missing), 403, "ForbiddenException")
	set(cluster, config, tags)
	snapshot := h.engine.cfg.AuthorizeSnapshot
	h.engine.cfg.AuthorizeSnapshot = nil
	expect(t, h.rest("kafka", "POST", mskClusters, input), 500, "InternalServerErrorException")
	h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) { return nil, nil }
	expect(t, h.rest("kafka", "POST", mskClusters, input), 500, "InternalServerErrorException")
	h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) {
		return nil, errors.New("test policy failure")
	}
	expect(t, h.rest("kafka", "POST", mskClusters, input), 500, "InternalServerErrorException")
	h.engine.cfg.AuthorizeSnapshot = snapshot
	if !slices.Equal(before, groupCatalogFrame(t, h.db)) {
		t.Fatal("refused creates changed catalog")
	}
	created := h.rest("kafka", "POST", mskClusters, input)
	expect(t, created, 200, "")
	h.engine.cfg.DefaultPrincipal = core.Administrator
	path := mskClusters + "/" + url.PathEscape(str(created.body["clusterArn"]))
	desc := h.rest("kafka", "GET", path, nil).body["clusterInfo"].(map[string]any)
	if desc["tags"].(map[string]any)["purpose"] != "practice" || desc["currentBrokerSoftwareInfo"].(map[string]any)["configurationArn"] != arn {
		t.Fatal("missing approved tags/attachment")
	}
	expect(t, h.rest("kafka", "DELETE", path, nil), 200, "")
	h.engine.cfg.DefaultPrincipal = "team"
	delete(input, "tags")
	set(cluster, config)
	expect(t, h.rest("kafka", "POST", mskClusters, input), 200, "")
}

func TestMSKRegionalListTargetsPrefixAndFields(t *testing.T) {
	h := mskAttachmentHarness(t, msk)
	var arns []string
	for _, name := range []string{"match-one", "other", "match-two"} {
		in := mskTestCluster(name, nil)
		if name == "other" {
			in["encryptionInfo"] = map[string]any{"encryptionInTransit": map[string]any{"clientBroker": "TLS_PLAINTEXT"}}
		}
		if name == "match-two" {
			in["encryptionInfo"] = map[string]any{"encryptionInTransit": map[string]any{"inCluster": false}}
		}
		result := h.rest("kafka", "POST", mskClusters, in)
		expect(t, result, 200, "")
		arn := str(result.body["clusterArn"])
		arns = append(arns, arn)
		detail := h.rest("kafka", "GET", mskClusters+"/"+url.PathEscape(arn), nil)
		transit := detail.body["clusterInfo"].(map[string]any)["encryptionInfo"].(map[string]any)["encryptionInTransit"].(map[string]any)
		client := "TLS"
		if name == "other" {
			client = "TLS_PLAINTEXT"
		}
		if transit["clientBroker"] != client || transit["inCluster"] != (name != "match-two") {
			t.Fatalf("wrong persisted encryption metadata %v", transit)
		}
	}
	before := groupCatalogFrame(t, h.db)
	for _, subnets := range [][]any{{"a"}, {"a", "a"}, {"a", "b", "c", "d"}, {"a", ""}} {
		in := mskTestCluster("invalid", nil)
		in["numberOfBrokerNodes"] = len(subnets)
		in["brokerNodeGroupInfo"].(map[string]any)["clientSubnets"] = subnets
		expect(t, h.rest("kafka", "POST", mskClusters, in), 400, "BadRequestException")
	}
	for _, method := range []string{"GET", "DELETE"} {
		expect(t, h.rest("kafka", method, mskClusters+"/other", nil), 400, "BadRequestException")
	}
	const filter = mskClusters + "?clusterNameFilter=match&maxResults=1"
	list := func(h *harness, path, region string) reply {
		req := httptest.NewRequest("GET", path, nil)
		sign(req, "kafka", region)
		return h.do(req)
	}
	first := list(h, filter, "us-east-1")
	expect(t, first, 200, "")
	results := first.body["clusterInfoList"].([]any)
	token := str(first.body["nextToken"])
	if token == "" || len(results) != 1 || results[0].(map[string]any)["clusterArn"] != arns[0] {
		t.Fatalf("bad first page %v", first.body)
	}
	suffix := "&nextToken=" + url.QueryEscape(token)
	next := list(h, filter+suffix, "us-east-1")
	expect(t, next, 200, "")
	results = next.body["clusterInfoList"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["clusterArn"] != arns[2] || next.body["nextToken"] != nil {
		t.Fatal("bad final page", next.body)
	}
	expect(t, list(h, strings.Replace(filter, "=match", "=other", 1)+suffix, "us-east-1"), 400, "BadRequestException")
	expect(t, list(h, filter+suffix, "ap-northeast-1"), 400, "BadRequestException")
	if got := list(h, filter, "ap-northeast-1"); len(got.body["clusterInfoList"].([]any)) != 0 {
		t.Fatal("cross-region list")
	}
	for _, tc := range []struct{ action, path string }{{"ListClusters", mskClusters}, {"ListConfigurations", mskConfigurations}} {
		h.engine.cfg.Authorize = func(_ context.Context, _ string, action, resource string) (string, error) {
			if resource != "*" || action != "kafka:"+tc.action {
				t.Fatalf("unexpected list target %s %s", action, resource)
			}
			return "implicit-deny", nil
		}
		denied := list(h, tc.path, "us-east-1")
		expect(t, denied, 403, "ForbiddenException")
		if len(denied.body) != 1 || str(denied.body["message"]) == "" {
			t.Fatal("wire refusal", denied.body)
		}
	}
	if !slices.Equal(before, groupCatalogFrame(t, h.db)) {
		t.Fatal("invalid and read-only requests changed stored resources")
	}
}

func TestMSKInterruptionAfterPhysicalWrite(t *testing.T) {
	for _, operation := range []string{"CreateCluster", "DeleteCluster", "CreateConfiguration", "DeleteConfiguration"} {
		for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(operation+"/"+reason.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.db")
				host, h := openMSKTeam(t, path)
				_, peer := openMSKTeam(t, filepath.Join(t.TempDir(), "peer.db"))
				cfg, _ := createMSKTestConfiguration(t, h, "attached", nil)
				createMSKTestConfiguration(t, peer, "target", nil)
				endpoint := mskConfigurations
				method, event, alias := "POST", "INSERT", "NEW"
				input := map[string]any{"name": "target", "serverProperties": "YQD/Cg=="}
				if strings.HasSuffix(operation, "Cluster") {
					endpoint = mskClusters
					input = mskTestCluster("target", map[string]any{"arn": cfg, "revision": 1})
				}
				if strings.HasPrefix(operation, "Delete") {
					made := h.rest("kafka", "POST", endpoint, input)
					expect(t, made, 200, "")
					key := "arn"
					if endpoint == mskClusters {
						key = "clusterArn"
					}
					endpoint += "/" + url.PathEscape(str(made.body[key]))
					method, event, alias = "DELETE", "DELETE", "OLD"
					input = nil
				}
				before, foreign := groupCatalogFrame(t, h.db), groupCatalogFrame(t, peer.db)
				var ctx context.Context
				var stop context.CancelFunc
				seen := 0
				conn, err := h.db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_msk", 3, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
						valid := args[0].Text() == "target" && args[1].Int64() > 0
						if strings.HasSuffix(operation, "Cluster") {
							valid = valid && args[2].Text() == cfg
						} else {
							valid = valid && args[2].Text() == "YQD/Cg=="
						}
						if valid {
							seen++
						}
						if reason == context.Canceled {
							stop()
						}
						<-ctx.Done()
						result.ResultNull()
					})
				})
				if err == nil {
					field := "$.ServerProperties"
					if strings.HasSuffix(operation, "Cluster") {
						field = "$.CurrentBrokerSoftwareInfo.ConfigurationArn"
					}
					_, err = conn.ExecContext(t.Context(), "CREATE TEMP TRIGGER msk_witness AFTER "+event+" ON aws_catalog_resources WHEN "+alias+".service='kafka' AND "+alias+".name='target' BEGIN SELECT observe_msk("+alias+".name,"+alias+".seq,json_extract("+alias+".document,'"+field+"')); END")
				}
				closeErr := conn.Close()
				if err != nil || closeErr != nil {
					t.Fatal(err, closeErr)
				}
				if reason == context.Canceled {
					ctx, stop = context.WithCancel(t.Context())
				} else {
					ctx, stop = context.WithTimeout(t.Context(), 400*time.Millisecond)
				}
				defer stop()
				var raw []byte
				if input != nil {
					raw, err = json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
				}
				req := httptest.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
				sign(req, "kafka", "us-east-1")
				result := h.do(req)
				if seen != 1 || !errors.Is(ctx.Err(), reason) {
					t.Fatal("expected the physical SQL write before interruption", seen, ctx.Err())
				}
				if result.status < 400 || result.body["state"] != nil || result.body["arn"] != nil || result.body["clusterArn"] != nil {
					t.Errorf("interrupted operation returned success HTTP %d", result.status)
				}
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				_, reopened := openMSKTeam(t, path)
				if !reflect.DeepEqual(before, groupCatalogFrame(t, reopened.db)) || !reflect.DeepEqual(foreign, groupCatalogFrame(t, peer.db)) {
					t.Fatal("interrupted operation changed durable state")
				}
				var retry reply
				if input == nil {
					retry = reopened.rest("kafka", method, endpoint, nil)
				} else {
					retry = reopened.rest("kafka", method, endpoint, input)
				}
				expect(t, retry, 200, "")
				if slices.Equal(before, groupCatalogFrame(t, reopened.db)) || !slices.Equal(foreign, groupCatalogFrame(t, peer.db)) {
					t.Fatal("retry did not preserve peer and change owner")
				}

			})
		}
	}
}

func TestMSKNineModeledIAMRefusals(t *testing.T) {
	_, h := openMSKTeam(t, filepath.Join(t.TempDir(), "owner.db"))
	arn, configuration := createMSKTestConfiguration(t, h, "saved", nil)
	input := mskTestCluster("saved", map[string]any{"arn": arn, "revision": 1})
	created := h.rest("kafka", "POST", mskClusters, input)
	expect(t, created, 200, "")
	cluster := mskClusters + "/" + url.PathEscape(str(created.body["clusterArn"]))
	h.engine.cfg.DefaultPrincipal = "no-grants"
	before := groupCatalogFrame(t, h.db)
	for _, tc := range []struct {
		method, path string
		input        any
	}{
		{"POST", mskClusters, input}, {"GET", cluster, nil}, {"DELETE", cluster, nil}, {"GET", mskClusters, nil},
		{"POST", mskConfigurations, map[string]any{"name": "private", "serverProperties": "YQ=="}}, {"GET", configuration, nil},
		{"GET", configuration + "/revisions/1", nil}, {"DELETE", configuration, nil}, {"GET", mskConfigurations, nil},
		{"GET", cluster + "-missing", nil}, {"DELETE", configuration + "-missing", nil},
	} {
		denied := h.rest("kafka", tc.method, tc.path, tc.input)
		expect(t, denied, 403, "ForbiddenException")
		if len(denied.body) != 1 || str(denied.body["message"]) != "IAM evaluation: implicitDeny" {
			t.Fatalf("unexpected refusal %v", denied.body)
		}
	}
	if !slices.Equal(before, groupCatalogFrame(t, h.db)) {
		t.Fatal("denials changed saved state")
	}
}

func TestMSKBoundedReadWhileDatabaseConnectionIsHeld(t *testing.T) {
	_, h := openMSKTeam(t, filepath.Join(t.TempDir(), "owner.db"))
	_, path := createMSKTestConfiguration(t, h, "saved", nil)
	_, peer := openMSKTeam(t, filepath.Join(t.TempDir(), "peer.db"))
	createMSKTestConfiguration(t, peer, "save", nil)
	for _, tc := range []struct{ name, method, path string }{{"list", "GET", mskConfigurations}, {"lookup", "GET", path}, {"delete lookup", "DELETE", path}} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := h.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			start := h.db.Stats().WaitCount
			ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
			defer cancel()
			req := httptest.NewRequestWithContext(ctx, tc.method, tc.path, nil)
			sign(req, "kafka", "us-east-1")
			result := make(chan reply, 1)
			go func() { result <- h.do(req) }()
			for h.db.Stats().WaitCount <= start && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			if h.db.Stats().WaitCount <= start {
				t.Fatal("request never entered the owned connection wait")
			}
			peerList := peer.rest("kafka", "GET", mskConfigurations, nil)
			expect(t, peerList, 200, "")
			if len(peerList.body["configurations"].([]any)) != 1 {
				t.Fatal("peer changed during owner wait")
			}
			select {
			case r := <-result:
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) || r.status < 400 {
					t.Fatal("unexpected wait outcome", ctx.Err(), r.status)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("operation did not release the queued request before the connection was released")
			}
		})
	}
	expect(t, h.rest("kafka", "GET", path, nil), 200, "")
}
