package queue

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// Queue visibility is local, but both storage limits apply across an account.
// Fill to the declared limits without issuing 10,000 individual HTTP requests;
// create the expiring message and both quota-bearing operations over native HTTP.
func TestAccountQuotaReclaimsExpiredMessagesFromIdleQueue(t *testing.T) {
	const idle = "/localaccount/idle"
	for _, kind := range []string{"send-at-count-limit", "grow-at-byte-limit"} {
		t.Run(kind, func(t *testing.T) {
			h, db, now := fixture(t)
			request(t, h, "PUT", qp, "", 201)
			request(t, h, "PUT", idle, "", 201)
			wrap := func(s string) string { return "<QueueMessage><MessageText>" + s + "</MessageText></QueueMessage>" }
			// A real receipt is needed for the content update. The source is a
			// one-byte payload, so a multibyte replacement needs new capacity.
			original := messages(t, request(t, h, "POST", qp+"/messages", wrap("a"), 201))[0]
			n, filler, retiring := 9998, "f", "retire"
			if kind == "grow-at-byte-limit" {
				n, filler, retiring = 511, strings.Repeat("f", 64<<10), strings.Repeat("r", (64<<10)-1)
			}
			gone := messages(t, request(t, h, "POST", idle+"/messages?messagettl=10", wrap(retiring), 201))[0]
			_, err := db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<?)
				INSERT INTO azure_queue_messages(queue,id,text,inserted,expires,visible,receipt)
				SELECT 'idle','fixture-'||x,?,?,?,?, 'seed' FROM n`, n, filler, now.Unix(), now.Add(time.Hour).Unix(), now.Add(time.Hour).Unix())
			if err != nil {
				t.Fatal(err)
			}
			mutate := func(blocked bool) string {
				t.Helper()
				method, path, want := "POST", qp+"/messages", 201
				if kind == "grow-at-byte-limit" {
					method, path, want = "PUT", qp+"/messages/"+original.ID+"?visibilitytimeout=0&popreceipt="+url.QueryEscape(original.Receipt), 204
				}
				if blocked {
					want = 409
				}
				w := request(t, h, method, path, wrap("月"), want)
				if blocked && !strings.Contains(w.Body.String(), "QuotaExceeded") {
					t.Fatalf("expected quota error, got %s", w.Body.String())
				}
				return w.Header().Get("x-ms-popreceipt")
			}
			*now = now.Add(9 * time.Second)
			mutate(true)
			*now = now.Add(time.Second)
			// No reads or writes to idle in between: a touch of the old queue must not be required.
			receipt := mutate(false)
			visible := messages(t, request(t, h, "GET", qp+"/messages?peekonly=true&numofmessages=2", "", 200))
			expected := 2
			if kind == "grow-at-byte-limit" {
				expected = 1
				if receipt == "" || receipt == original.Receipt {
					t.Fatal("successful update did not rotate the receipt")
				}
			}
			if len(visible) != expected || visible[expected-1].Text == nil || *visible[expected-1].Text != "月" {
				t.Fatal("visible messages do not reflect successful quota-bearing operation")
			}
			var retained, expired int
			if err := db.Database().QueryRow(`SELECT count(*),count(CASE WHEN id=? THEN 1 END) FROM azure_queue_messages WHERE queue='idle'`, gone.ID).Scan(&retained, &expired); err != nil {
				t.Fatal(err)
			}
			if retained != n || expired != 0 {
				t.Fatalf("expected %d live idle messages and no expired row, got %d and %d", n, retained, expired)
			}
		})
	}
}
