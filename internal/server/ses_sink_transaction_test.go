package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

func openSinkDB(t *testing.T, path, seed string) (sesDomainClient, *sql.DB, func() error) {
	t.Helper()
	var db *sql.DB
	h, closeDB, err := NewWithProviders(path, ProviderConfig{CloudStorage: DefaultCloudStorageConfig(), configureInstances: func(d *sql.DB, _ *aws.StackStore) error { db = d; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat(seed, 64)
	access, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	return sesDomainClient{t: t, h: access, token: token, region: "us-east-1"}, db, closeDB
}

func seedMailDomain(c sesDomainClient) {
	c.t.Helper()
	const domain = "owned.test"
	z := c.zone(domain, "sink")
	for _, token := range c.identity("POST", domain, 200)[1:] {
		c.record(z, "CREATE", domain, token, token+".dkim.amazonses.com", 200)
	}
}

func sinkFrame(t *testing.T, db *sql.DB) string {
	t.Helper()
	var state string
	err := db.QueryRow(`SELECT json_array(
	 (SELECT json_group_array(json_array(seq,region,id,sender,recipient,template,accepted_at,outcome,reason) ORDER BY seq) FROM aws_ses_sink_messages),
	 (SELECT json_group_array(json_array(id,hex(mime),hex(sha256)) ORDER BY id) FROM aws_ses_local_inbox),
	 (SELECT json_group_array(json_array(zone,name,type,document) ORDER BY zone,name,type) FROM aws_route53_records),
	 (SELECT json_group_array(json_array(seq,region,domain,zone_id,marker,first_token,second_token,third_token) ORDER BY seq) FROM aws_ses_domain_identities),
	 (SELECT seq FROM sqlite_sequence WHERE name='aws_ses_sink_messages'))`).Scan(&state)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestSESMailboxStagedInterruption(t *testing.T) {
	for _, op := range []string{"receive", "refuse", "cleanup"} {
		for _, why := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(op+"/"+why.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.db")
				owner, db, closeOwner := openSinkDB(t, path, "a")
				peer, peerDB, closePeer := openSinkDB(t, filepath.Join(t.TempDir(), "peer.db"), "b")
				defer closePeer()
				for _, c := range []sesDomainClient{owner, peer} {
					seedMailDomain(c)
					localSend(c, "inbox@inbox.ses.test", true, 200)
				}
				dest := "inbox@inbox.ses.test"
				if op == "refuse" {
					dest = "absent@inbox.ses.test"
				}
				var id string
				method, target, body := "POST", sesSendPath, localSendBody(dest, true)
				trigger := `CREATE TEMP TRIGGER watch_mail AFTER INSERT ON aws_ses_local_inbox BEGIN SELECT witness(NEW.id,length(NEW.mime),length(NEW.sha256)); END`
				if op == "refuse" {
					trigger = `CREATE TEMP TRIGGER watch_mail AFTER INSERT ON aws_ses_sink_messages WHEN NEW.recipient='absent@inbox.ses.test' AND NEW.outcome='rejected' AND NEW.reason='mailbox-not-found' BEGIN SELECT witness(NEW.id,NEW.seq,32); END`
				}
				if op == "cleanup" {
					id = localSend(owner, dest, true, 200)
					method, target, body = "DELETE", sesSinkEast+"/"+id, nil
					trigger = `CREATE TEMP TRIGGER watch_mail AFTER DELETE ON aws_ses_sink_messages WHEN OLD.outcome='delivered' BEGIN SELECT witness(OLD.id,OLD.seq,32); END`
				}
				before, foreign := sinkFrame(t, db), sinkFrame(t, peerDB)
				var ctx context.Context
				var stop context.CancelFunc
				var witnessed string
				conn, err := db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("witness", 3, 0, func(out sqlite3.Context, args ...sqlite3.Value) {
						if n := args[0].Text(); len(n) == 32 && args[1].Int64() > 0 && args[2].Int64() == 32 && (op != "cleanup" || n == id) {
							witnessed = n
						}
						if why == context.Canceled {
							stop()
						}
						<-ctx.Done()
						out.ResultNull()
					})
				})
				if err == nil {
					_, err = conn.ExecContext(t.Context(), trigger)
				}
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				if why == context.Canceled {
					ctx, stop = context.WithCancel(t.Context())
				} else {
					ctx, stop = context.WithTimeout(t.Context(), 500*time.Millisecond)
				}
				defer stop()
				req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+target, bytes.NewReader(body))
				req.Header.Set("X-Simulator-Token", owner.token)
				req.Header.Set("X-Simulator-Service", "ses")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Simulator-Region", "us-east-1")
				rec := httptest.NewRecorder()
				owner.h.ServeHTTP(rec, req)
				if witnessed == "" || !errors.Is(ctx.Err(), why) || rec.Code < 400 || strings.Contains(rec.Body.String(), "MessageId") {
					t.Fatalf("staged interrupt failed: observed %t, status %d, reason %v", witnessed != "", rec.Code, ctx.Err())
				}
				if err = closeOwner(); err != nil {
					t.Fatal(err)
				}
				owner, db, closeOwner = openSinkDB(t, path, "a")
				defer closeOwner()
				if before != sinkFrame(t, db) || foreign != sinkFrame(t, peerDB) {
					t.Fatal("staged interruption changed durable owner/peer data")
				}
				if op == "cleanup" {
					owner.call("", "GET", target, "", 200)
					owner.call("", "DELETE", target, "", 204)
					owner.call("", "GET", target, "", 404)
				} else {
					owner.call("", "GET", sesSinkEast+"/"+witnessed, "", 404)
					saved := localSend(owner, dest, true, 200)
					status := 200
					if op == "refuse" {
						status = 404
					}
					owner.call("", "GET", sesSinkEast+"/"+saved+"/content", "", status)
					owner.call("", "DELETE", sesSinkEast+"/"+saved, "", 204)
				}
				if foreign != sinkFrame(t, peerDB) {
					t.Fatal("retry changed peer")
				}
			})
		}
	}
}

func TestSESLocalSinkBoundsAndStorageFailures(t *testing.T) {
	c, db, closeDB := openSinkDB(t, filepath.Join(t.TempDir(), "db"), "a")
	defer closeDB()
	seedMailDomain(c)
	const inbox = "inbox@inbox.ses.test"
	safe := string(localSendBody(inbox, true))
	save := localSend(c, inbox, true, 200)
	before := sinkFrame(t, db)
	for _, body := range []string{
		strings.Replace(safe, "ようこそ", "line\\r\\nBcc: second@example.org", 1),
		strings.Replace(safe, "ようこそ", string([]byte{0xff}), 1), strings.Replace(safe, "ようこそ", `\ud800`, 1),
		strings.Replace(safe, `"Content":`, `"Content":null,"Content":`, 1),
		strings.Replace(safe, "{{untouched}}", strings.Repeat("x", 200000), 1),
		strings.Replace(safe, `"UTF-8"`, `"ASCII"`, 1),
		strings.Replace(safe, `"ToAddresses":`, `"CcAddresses":["other@inbox.ses.test"],"ToAddresses":`, 1),
		strings.Replace(safe, `"FromEmailAddress":`, `"ConfigurationSetName":"outside","FromEmailAddress":`, 1),
		strings.Replace(safe, `"Simple":`, `"Raw":`, 1),
	} {
		c.call("ses", "POST", sesSendPath, body, 400)
	}
	if before != sinkFrame(t, db) {
		t.Fatal("invalid inputs changed the mailbox")
	}
	for _, fault := range []string{
		`CREATE TRIGGER block_mail AFTER INSERT ON aws_ses_local_inbox BEGIN SELECT RAISE(ABORT,'write stopped'); END`,
		`CREATE TRIGGER block_mail AFTER INSERT ON aws_ses_local_inbox BEGIN UPDATE aws_ses_local_inbox SET mime=X'00' WHERE id=NEW.id; END`,
		`CREATE TRIGGER block_mail AFTER INSERT ON aws_ses_sink_messages BEGIN UPDATE aws_ses_sink_messages SET sender='other@owned.test' WHERE id=NEW.id; END`,
	} {
		if _, err := db.Exec(fault); err != nil {
			t.Fatal(err)
		}
		localSend(c, inbox, true, 500)
		if before != sinkFrame(t, db) {
			t.Fatal("failed receipt or content write left partial state")
		}
		if _, err := db.Exec("DROP TRIGGER block_mail"); err != nil {
			t.Fatal(err)
		}
	}
	for range 99 {
		localSend(c, "absent@inbox.ses.test", true, 200)
	}
	if len(localSinkSummary(c)) != 100 {
		t.Fatal("missing real accepted rows at capacity")
	}
	capacity := sinkFrame(t, db)
	localSend(c, inbox, true, 400)
	if capacity != sinkFrame(t, db) {
		t.Fatal("quota overflow changed stored material")
	}
	c.call("", "DELETE", sesSinkEast+"/"+save, "", 204)
	saved := localSend(c, inbox, true, 200)
	// The explicit download detects a same-length stored-byte alteration.
	if _, err := db.Exec("UPDATE aws_ses_local_inbox SET mime=zeroblob(length(mime)) WHERE id=?", saved); err != nil {
		t.Fatal(err)
	}
	c.call("", "GET", sesSinkEast+"/"+saved+"/content", "", 500)
	for id := range localSinkSummary(c) {
		c.call("", "DELETE", sesSinkEast+"/"+id, "", 204)
	}
	if data, _ := json.Marshal(localSinkSummary(c)); string(data) != "{}" {
		t.Fatal("cleanup retained sink messages")
	}
}
