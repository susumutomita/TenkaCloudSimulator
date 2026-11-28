package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
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

func TestRoute53InterruptedStagedReceipt(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx := t.Context()
			file := filepath.Join(t.TempDir(), "zones.db")
			open := func() (*core.Store, *Route53Store) {
				t.Helper()
				host, err := core.New(file)
				r53must(t, err)
				t.Cleanup(func() { host.Close() })
				service, err := NewRoute53Store(host.Database())
				r53must(t, err)
				return host, service
			}
			host, service := open()
			zone := r53create(t, service)
			peer, _, err := service.create(ctx, "other.test.", "peer", "")
			r53must(t, err)
			peerID := strings.TrimPrefix(peer.ID, "/hostedzone/")
			sibling := r53record("sibling.example.test.", "A", "192.0.2.10")
			peerRow := r53record("sibling.other.test.", "A", "192.0.2.20")
			_, err = service.change(ctx, zone, "", []Route53Change{{"CREATE", sibling}})
			r53must(t, err)
			_, err = service.change(ctx, peerID, "", []Route53Change{{"CREATE", peerRow}})
			r53must(t, err)

			frame := func(db *sql.DB) []string {
				t.Helper()
				var result []string
				for _, query := range []string{
					"SELECT json_array(id, name, reference, comment) FROM aws_route53_zones ORDER BY id",
					"SELECT json_array(zone, name, type, document) FROM aws_route53_records ORDER BY zone, name, type",
					"SELECT json_array(id, document) FROM aws_route53_changes ORDER BY id",
				} {
					rows, err := db.QueryContext(ctx, query)
					r53must(t, err)
					for rows.Next() {
						var row string
						r53must(t, rows.Scan(&row))
						result = append(result, row)
					}
					r53must(t, rows.Err())
					r53must(t, rows.Close())
				}
				return result
			}
			before := frame(host.Database())
			if len(before) != 12 {
				t.Fatalf("before = %d rows, want two zones, six RRsets, four receipts", len(before))
			}

			var requestContext context.Context
			var cancel context.CancelFunc
			var observed [][2]string
			connection, err := host.Database().Conn(ctx)
			r53must(t, err)
			r53must(t, connection.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().CreateFunction("inspect_route53_receipt", 2, 0,
					func(result sqlite3.Context, args ...sqlite3.Value) {
						observed = append(observed, [2]string{args[0].Text(), args[1].Text()})
						if cause == context.Canceled {
							cancel()
						}
						<-requestContext.Done()
						result.ResultNull()
					})
			}))
			_, err = connection.ExecContext(ctx, `CREATE TEMP TRIGGER interrupt_route53_receipt
				AFTER INSERT ON aws_route53_changes BEGIN
				SELECT inspect_route53_receipt(
					(SELECT document FROM aws_route53_records WHERE name='changed.example.test.' AND type='A'),
					(SELECT document FROM aws_route53_changes WHERE id=NEW.id));
				END`)
			r53must(t, err)
			r53must(t, connection.Close())

			post := func(s *Route53Store, requestContext context.Context) *httptest.ResponseRecorder {
				t.Helper()
				body := `<ChangeResourceRecordSetsRequest xmlns="` + r53namespace + `"><ChangeBatch><Changes><Change><Action>CREATE</Action><ResourceRecordSet><Name>changed.example.test.</Name><Type>A</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>192.0.2.35</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>`
				req := httptest.NewRequestWithContext(requestContext, "POST", "/2013-04-01/hostedzone/"+zone+"/rrset", strings.NewReader(body))
				response := httptest.NewRecorder()
				s.ServeHTTP(response, req, r53allow)
				return response
			}
			if cause == context.DeadlineExceeded {
				requestContext, cancel = context.WithTimeout(ctx, 2*time.Second)
			} else {
				requestContext, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			response := post(service, requestContext)
			if !errors.Is(requestContext.Err(), cause) || response.Code != http.StatusInternalServerError || len(observed) != 1 {
				t.Fatalf("interruption = %v, HTTP = %d, staged callbacks = %d", requestContext.Err(), response.Code, len(observed))
			}
			var failure struct {
				Code string `xml:"Error>Code"`
			}
			r53must(t, xml.Unmarshal(response.Body.Bytes(), &failure))
			if failure.Code != "InternalFailure" {
				t.Fatalf("unexpected failure %q", failure.Code)
			}
			var staged Route53Record
			var receipt Route53Receipt
			r53must(t, json.Unmarshal([]byte(observed[0][0]), &staged))
			r53must(t, json.Unmarshal([]byte(observed[0][1]), &receipt))
			want := r53record("changed.example.test.", "A", "192.0.2.35")
			if !r53same(staged, want) || !strings.HasPrefix(receipt.ID, "/change/C") || receipt.Status != "INSYNC" {
				t.Fatal("expected a staged native RRset and corresponding receipt")
			}
			host.Close()
			freshHost, fresh := open()
			if !reflect.DeepEqual(frame(freshHost.Database()), before) {
				t.Fatal("reopening after interruption changed zone, record or receipt rows")
			}
			if res := r53call(fresh, "GET", strings.TrimPrefix(receipt.ID, "/"), "", r53allow); res.Code != http.StatusNotFound {
				t.Fatal("interrupted receipt is readable", res.Code)
			}
			if res := post(fresh, ctx); res.Code != http.StatusOK {
				t.Fatal("retry rejected", res.Code)
			}
			if len(frame(freshHost.Database())) != len(before)+2 {
				t.Fatal("one successful retry should add exactly one RRset and one receipt")
			}
			answer := r53call(fresh, "GET", "testdnsanswer?hostedzoneid="+zone+"&recordname=changed.example.test.&recordtype=A", "", r53allow)
			var actual struct {
				Values []string `xml:"RecordData>RecordDataEntry"`
			}
			r53must(t, xml.Unmarshal(answer.Body.Bytes(), &actual))
			if answer.Code != http.StatusOK || !reflect.DeepEqual(actual.Values, []string{"192.0.2.35"}) {
				t.Fatal("retry answer", answer.Code, actual.Values)
			}
			for _, cleanup := range []struct {
				id      string
				changes []Route53Change
			}{
				{zone, []Route53Change{{"DELETE", want}, {"DELETE", sibling}}},
				{peerID, []Route53Change{{"DELETE", peerRow}}},
			} {
				_, err := fresh.change(ctx, cleanup.id, "", cleanup.changes)
				r53must(t, err)
				_, err = fresh.remove(ctx, cleanup.id)
				r53must(t, err)
			}
			var count int
			r53must(t, freshHost.Database().QueryRowContext(ctx, "SELECT (SELECT count(*) FROM aws_route53_zones)+(SELECT count(*) FROM aws_route53_records)").Scan(&count))
			if count != 0 {
				t.Fatal("cleanup left live resources", count)
			}
		})
	}
}
