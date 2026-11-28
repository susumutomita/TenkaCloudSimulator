package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestServiceQuotaIncreaseInterruptionAfterInsert(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			open := func(path string) (*core.Store, *harness) {
				t.Helper()
				host, err := core.New(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = host.Close() })
				h := &harness{t: t, db: host.Database(), policy: &policy{deny: map[string]bool{}}}
				h.engine, err = New(h.db, Config{Regions: []string{"us-east-1"}, Account: "000000000000", DefaultPrincipal: "fixture", Authorize: h.policy.authorize}, []*Service{b2bi, serviceQuotas})
				if err != nil {
					t.Fatal(err)
				}
				return host, h
			}
			ownerPath := filepath.Join(t.TempDir(), "owner.db")
			host, owner := open(ownerPath)
			_, peer := open(filepath.Join(t.TempDir(), "peer.db"))
			history := func(h *harness) map[string]any {
				t.Helper()
				r := h.json1("service-quotas", "ListRequestedServiceQuotaChangeHistory", map[string]any{})
				expect(t, r, 200, "")
				return r.body
			}
			for _, h := range []*harness{owner, peer} {
				expect(t, h.json1("service-quotas", "RequestServiceQuotaIncrease", quotaRequestArgs(6, false)), 200, "")
			}
			ownerBefore, peerBefore := history(owner), history(peer)
			var ctx context.Context
			var cancel context.CancelFunc
			var stagedID string
			conn, err := owner.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_quota_request", 2, 0, func(out sqlite3.Context, args ...sqlite3.Value) {
					if args[1].Text() == "PENDING" {
						stagedID = args[0].Text()
					}
					if cause == context.Canceled {
						cancel()
					}
					<-ctx.Done()
					out.ResultInt(1)
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER observe_quota AFTER INSERT ON aws_b2bi_profile_quota_requests BEGIN
				SELECT observe_quota_request(id,status) FROM aws_b2bi_profile_quota_requests WHERE seq=NEW.seq; END`)
			closeErr := conn.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			if cause == context.Canceled {
				ctx, cancel = context.WithCancel(t.Context())
			} else {
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			arg := quotaRequestArgs(9, true)
			body, err := json.Marshal(arg)
			if err != nil {
				t.Fatal(err)
			}
			r := owner.do(newJSONRequest(owner, "service-quotas", "RequestServiceQuotaIncrease", body, "us-east-1").WithContext(ctx))
			if stagedID == "" || !errors.Is(ctx.Err(), cause) {
				t.Fatalf("original context did not interrupt a stored pending request: %v", ctx.Err())
			}
			if r.status != 500 || r.body["RequestedQuota"] != nil {
				t.Errorf("interrupted request returned %d %v", r.status, r.body)
			}
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			_, reopened := open(ownerPath)
			if !reflect.DeepEqual(history(reopened), ownerBefore) || !reflect.DeepEqual(history(peer), peerBefore) {
				t.Fatal("interruption changed saved owner or peer history")
			}
			expect(t, reopened.json1("service-quotas", "GetRequestedServiceQuotaChange", map[string]any{"RequestId": stagedID}), 400, "NoSuchResourceException")
			retry := reopened.json1("service-quotas", "RequestServiceQuotaIncrease", arg)
			expect(t, retry, 200, "")
			if retry.body["RequestedQuota"].(map[string]any)["Id"] == stagedID {
				t.Fatal("retry returned the rolled-back request ID")
			}
			if len(history(reopened)["RequestedQuotas"].([]any)) != 2 || !reflect.DeepEqual(history(peer), peerBefore) {
				t.Fatal("retry changed more than the owned request")
			}
		})
	}
}
