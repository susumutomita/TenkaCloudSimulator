package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	driver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func interruptFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		"SELECT json_array('plan',region,name,source_key,source,created,status_start,generation) FROM aws_autoscaling_plans ORDER BY region,name",
		"SELECT json_array('resource',region,plan,ordinal,namespace,resource_id,dimension,instruction,table_id) FROM aws_autoscaling_plan_resources ORDER BY region,plan,ordinal",
		"SELECT json_array('table',region,name,table_id,schema,billing,read_units,write_units,created) FROM aws_dynamodb_tables ORDER BY region,name",
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func interruptTeam(t *testing.T, name string) (*aspHarness, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".sqlite")
	return reopenInterruptTeam(t, path), path
}

func reopenInterruptTeam(t *testing.T, path string) *aspHarness {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dyn, err := NewDynamoDBStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	plans, err := NewAutoScalingPlansStore(db.Database(), dyn)
	if err != nil {
		t.Fatal(err)
	}
	ids := core.NewIdentities(db)
	return &aspHarness{db: db, dynamo: dyn, plans: plans, ids: ids, g: &Gateway{AutoScalingPlans: plans, DynamoDB: dyn, Identities: ids}}
}

func TestMergedScalingPlannerNativeInterruption(t *testing.T) {
	for _, op := range []string{"CreateScalingPlan", "UpdateScalingPlan"} {
		for _, cause := range []string{"client-cancel", "handler-deadline"} {
			t.Run(op+"/"+cause, func(t *testing.T) {
				h, path := interruptTeam(t, "owner")
				peer, _ := interruptTeam(t, "peer")
				for _, team := range []*aspHarness{h, peer} {
					team.table(t, "us-east-1", "orders", "PROVISIONED", 2, 9)
					team.table(t, "us-east-1", "sibling", "PROVISIONED", 11, 13)
				}
				original := aspCreateJSON("study", aspTags("saved"), aspInstructionJSON("orders", autoScalingReadDimension, 5, 8, ""))
				peer.must(t, "CreateScalingPlan", original)
				oldRead, stagedRead := 2, 5
				body := aspCreateJSON("study", aspTags("attempt"), aspInstructionJSON("orders", autoScalingReadDimension, 5, 8, ""))
				if op == "UpdateScalingPlan" {
					h.must(t, "CreateScalingPlan", original)
					oldRead, stagedRead = 5, 3
					body = fmt.Sprintf(`{"ScalingPlanName":"study","ScalingPlanVersion":1,"ApplicationSource":%s,"ScalingInstructions":[%s]}`, aspTags("attempt"), aspInstructionJSON("orders", autoScalingReadDimension, 3, 3, ""))
				}
				before := interruptFrame(t, h.db.Database())
				peerBefore := interruptFrame(t, peer.db.Database())
				var savedID string
				if err := h.db.Database().QueryRow("SELECT table_id FROM aws_dynamodb_tables WHERE region='us-east-1' AND name='orders'").Scan(&savedID); err != nil {
					t.Fatal(err)
				}
				connection, err := h.db.Database().Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				staged := make(chan []string, 1)
				requestStarted := make(chan context.Context, 1)
				err = connection.Raw(func(raw any) error {
					return raw.(driver.Conn).Raw().CreateFunction("observe_scaling_writes", 6, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
						values := make([]string, len(args))
						for i, v := range args {
							values[i] = v.Text()
						}
						ctx := <-requestStarted
						staged <- values
						select {
						case <-ctx.Done():
						case <-time.After(8 * time.Second):
							result.ResultError(errors.New("test request remained active"))
							return
						}
						result.ResultNull()
					})
				})
				if err != nil {
					t.Fatal(err)
				}
				_, err = connection.ExecContext(t.Context(), `CREATE TEMP TRIGGER observe_scaling_stage AFTER INSERT ON aws_autoscaling_plan_resources
     WHEN NEW.plan='study' BEGIN
     SELECT observe_scaling_writes(NEW.plan,NEW.resource_id,NEW.table_id,
        (SELECT json_extract(source,'$.TagFilters[0].Values[0]') FROM aws_autoscaling_plans WHERE name=NEW.plan AND region=NEW.region),
        (SELECT read_units FROM aws_dynamodb_tables WHERE name='orders' AND table_id=NEW.table_id),
        (SELECT write_units FROM aws_dynamodb_tables WHERE name='orders' AND table_id=NEW.table_id));
     END`)
				if err != nil {
					t.Fatal(err)
				}
				if err = connection.Close(); err != nil {
					t.Fatal(err)
				}
				type result struct {
					status int
					body   string
					cause  error
				}
				done := make(chan result, 1)
				clientDone := make(chan result, 1)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx := r.Context()
					if cause == "handler-deadline" {
						var stop context.CancelFunc
						ctx, stop = context.WithTimeout(ctx, 2*time.Second)
						defer stop()
					}
					requestStarted <- ctx
					recorder := httptest.NewRecorder()
					h.g.ServeHTTP(recorder, r.WithContext(ctx))
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					done <- result{recorder.Code, recorder.Body.String(), ctx.Err()}
				}))
				defer srv.Close()
				clientCtx, stop := context.WithCancel(t.Context())
				defer stop()
				req, err := http.NewRequestWithContext(clientCtx, "POST", srv.URL+"/", strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header = aspRequest(op, body).Header
				go func() {
					res, err := srv.Client().Do(req)
					if err != nil {
						clientDone <- result{cause: err}
						return
					}
					defer res.Body.Close()
					b, err := io.ReadAll(res.Body)
					clientDone <- result{res.StatusCode, string(b), err}
				}()
				var witness []string
				select {
				case witness = <-staged:
				case early := <-done:
					t.Fatalf("handler returned before real writes: %#v", early)
				case <-time.After(10 * time.Second):
					t.Fatal("staging timeout")
				}
				if !slices.Equal(witness, []string{"study", "table/orders", savedID, "attempt", fmt.Sprint(stagedRead), "9"}) {
					stop()
					t.Fatalf("wrong transaction witness %v", witness)
				}
				want := error(context.DeadlineExceeded)
				if cause == "client-cancel" {
					want = context.Canceled
					stop()
				}
				receipt := <-done
				received := <-clientDone
				var errorBody map[string]any
				if !errors.Is(receipt.cause, want) || receipt.status < 400 || json.Unmarshal([]byte(receipt.body), &errorBody) != nil || errorBody["__type"] == nil || errorBody["ScalingPlanVersion"] != nil {
					t.Fatalf("unexpected handler result %#v", receipt)
				}
				if cause == "client-cancel" && !errors.Is(received.cause, context.Canceled) {
					t.Fatalf("client cancellation result %#v", received)
				}
				if cause == "handler-deadline" && (received.status != receipt.status || received.body != receipt.body || received.cause != nil) {
					t.Fatalf("deadline response %#v", received)
				}
				srv.Close()
				if err := h.db.Close(); err != nil {
					t.Fatal(err)
				}
				reopened := reopenInterruptTeam(t, path)
				if !slices.Equal(before, interruptFrame(t, reopened.db.Database())) || !slices.Equal(peerBefore, interruptFrame(t, peer.db.Database())) {
					t.Fatal("failure changed durable plan/resource/target or peer frame")
				}
				read, write := reopened.units(t, "us-east-1", "orders")
				if read != int64(oldRead) || write != 9 {
					t.Fatal(read, write)
				}
				reopened.must(t, op, body)
				read, write = reopened.units(t, "us-east-1", "orders")
				if read != int64(stagedRead) || write != 9 || len(aspResources(t, reopened, "study")) != 1 || len(aspPlans(t, reopened, "{}")) != 1 {
					t.Fatal("identical retry did not commit")
				}
				t.Logf("staged plan+target+capacity %d/9; cause=%v gateway=%d %v; reopened %d/9; identical retry %d/9; peer unchanged", stagedRead, receipt.cause, receipt.status, errorBody["__type"], oldRead, read)
			})
		}
	}
}
