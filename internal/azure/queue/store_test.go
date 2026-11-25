package queue

import (
	"context"
	"net/url"
	"testing"
	"time"
)

func TestDeleteFailureRollsBackAndLeavesMessageVisible(t *testing.T) {
	h, db, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	m := messages(t, request(t, h, "POST", qp+"/messages", payload, 201))[0]
	_, err := db.Database().Exec(`CREATE TRIGGER reject_queue_delete BEFORE DELETE ON azure_queue_messages BEGIN SELECT RAISE(ABORT,'injected storage failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(m.Receipt), "", 500)
	if len(messages(t, request(t, h, "GET", qp+"/messages?peekonly=true", "", 200))) != 1 {
		t.Fatal("failed delete lost data")
	}
	if _, err = db.Database().Exec("DROP TRIGGER reject_queue_delete"); err != nil {
		t.Fatal(err)
	}
	request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(m.Receipt), "", 204)
	if len(messages(t, request(t, h, "GET", qp+"/messages?peekonly=true", "", 200))) != 0 {
		t.Fatal("delete failed")
	}
}
func TestMissingScopeCannotAdoptExistingData(t *testing.T) {
	h, db, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	if _, err := db.Database().Exec("DELETE FROM azure_queue_scope"); err != nil {
		t.Fatal(err)
	}
	request(t, h, "DELETE", qp, "", 500)
	if _, err := NewOwnedHandler(db.Database(), h.cfg, h.actor); err == nil {
		t.Fatal("orphan data adopted")
	}
}
func TestConstructionValidation(t *testing.T) {
	h, db, _ := fixture(t)
	if _, err := NewOwnedHandler(nil, h.cfg, h.actor); err == nil {
		t.Fatal("nil db")
	}
	if _, err := NewOwnedHandler(db.Database(), h.cfg, nil); err == nil {
		t.Fatal("nil actor")
	}
	cfg := h.cfg
	cfg.Account = "Invalid"
	if _, err := NewOwnedHandler(db.Database(), cfg, h.actor); err == nil {
		t.Fatal("invalid account")
	}
	cfg = h.cfg
	cfg.Owner = ""
	if _, err := NewOwnedHandler(db.Database(), cfg, h.actor); err == nil {
		t.Fatal("empty owner")
	}
	cfg = h.cfg
	cfg.Now = nil
	if _, err := NewOwnedHandler(db.Database(), cfg, func(context.Context) (AuthenticatedActor, bool) { return AuthenticatedActor{}, false }); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := NewOwnedHandler(db.Database(), cfg, h.actor); err == nil {
		t.Fatal("closed db")
	}
}

func TestIgnoredWritesCannotReportSuccess(t *testing.T) {
	cases := []struct{ name, trigger, method, path string }{
		{"put", "BEFORE INSERT ON azure_queue_messages", "POST", qp + "/messages"},
		{"receive", "BEFORE UPDATE ON azure_queue_messages", "GET", qp + "/messages"},
		{"update", "BEFORE UPDATE ON azure_queue_messages", "PUT", "message"},
		{"delete", "BEFORE DELETE ON azure_queue_messages", "DELETE", "message"},
		{"clear", "BEFORE DELETE ON azure_queue_messages", "DELETE", qp + "/messages"},
		{"delete queue", "BEFORE DELETE ON azure_queues", "DELETE", qp},
		{"cascade", "BEFORE DELETE ON azure_queue_messages", "DELETE", qp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _ := fixture(t)
			request(t, h, "PUT", qp, "", 201)
			m := messages(t, request(t, h, "POST", qp+"/messages", payload, 201))[0]
			_, err := db.Database().Exec("CREATE TRIGGER ignore_write " + tc.trigger + " BEGIN SELECT RAISE(IGNORE); END")
			if err != nil {
				t.Fatal(err)
			}
			path := tc.path
			if path == "message" {
				path = qp + "/messages/" + m.ID + "?popreceipt=" + url.QueryEscape(m.Receipt)
				if tc.method == "PUT" {
					path += "&visibilitytimeout=30"
				}
			}
			body := ""
			if tc.method == "POST" {
				body = payload
			}
			request(t, h, tc.method, path, body, 500)
			after := messages(t, request(t, h, "GET", qp+"/messages?peekonly=true", "", 200))
			if len(after) != 1 || after[0].ID != m.ID || after[0].Count != 0 {
				t.Fatal("failed operation changed message")
			}
			if _, err = db.Database().Exec("DROP TRIGGER ignore_write"); err != nil {
				t.Fatal(err)
			}
			// Original receipt remains usable, including after suppressed receive/update.
			request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(m.Receipt), "", 204)
		})
	}
}
func TestIgnoredCreateAndExpiry(t *testing.T) {
	h, db, now := fixture(t)
	_, err := db.Database().Exec("CREATE TRIGGER ignore_create BEFORE INSERT ON azure_queues BEGIN SELECT RAISE(IGNORE); END")
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "PUT", qp, "", 500)
	request(t, h, "GET", qp+"/messages", "", 404)
	if _, err = db.Database().Exec("DROP TRIGGER ignore_create"); err != nil {
		t.Fatal(err)
	}
	request(t, h, "PUT", qp, "", 201)
	request(t, h, "POST", qp+"/messages?messagettl=1", payload, 201)
	*now = now.Add(2 * time.Second)
	if _, err = db.Database().Exec("CREATE TRIGGER ignore_expiry BEFORE DELETE ON azure_queue_messages BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	request(t, h, "GET", qp+"/messages", "", 500)
}
func TestAfterTriggerCannotUndoMutationSilently(t *testing.T) {
	h, db, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	m := messages(t, request(t, h, "POST", qp+"/messages", payload, 201))[0]
	_, err := db.Database().Exec(`CREATE TRIGGER undo_update AFTER UPDATE ON azure_queue_messages BEGIN UPDATE azure_queue_messages SET receipt=OLD.receipt,visible=OLD.visible,dequeues=OLD.dequeues,text=OLD.text WHERE id=NEW.id; END`)
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "GET", qp+"/messages", "", 500)
	request(t, h, "PUT", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(m.Receipt)+"&visibilitytimeout=20", payload, 500)
	if _, err = db.Database().Exec("DROP TRIGGER undo_update"); err != nil {
		t.Fatal(err)
	}
	_, err = db.Database().Exec(`CREATE TRIGGER undo_delete AFTER DELETE ON azure_queue_messages BEGIN INSERT INTO azure_queue_messages(seq,queue,id,text,inserted,expires,visible,receipt,dequeues) VALUES(OLD.seq,OLD.queue,OLD.id,OLD.text,OLD.inserted,OLD.expires,OLD.visible,OLD.receipt,OLD.dequeues); END`)
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(m.Receipt), "", 500)
	if len(messages(t, request(t, h, "GET", qp+"/messages?peekonly=true", "", 200))) != 1 {
		t.Fatal("rollback missing")
	}
}
