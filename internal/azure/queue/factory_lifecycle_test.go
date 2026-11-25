package queue

// This test is fixed before the implementation. Its oracle is Microsoft's
// Queue Storage REST specification, not another emulator's observed behavior.
import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestAzureQueueFactoryLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	cfg := Config{Account: "localaccount", Owner: "owner", Environment: "queue-factory"}
	open := func() (*core.Store, http.Handler) {
		t.Helper()
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewOwnedHandler(db.Database(), cfg, func(context.Context) (AuthenticatedActor, bool) {
			return AuthenticatedActor{ExerciseIdentity: "owner"}, true
		})
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		return db, h
	}
	db, h := open()
	t.Cleanup(func() { db.Close() })
	call := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("x-ms-version", "2023-11-03")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w
	}
	type message struct {
		ID      string `xml:"MessageId"`
		Receipt string `xml:"PopReceipt"`
		Text    string `xml:"MessageText"`
		Count   int    `xml:"DequeueCount"`
	}
	decode := func(w *httptest.ResponseRecorder) []message {
		t.Helper()
		var result struct {
			Messages []message `xml:"QueueMessage"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Messages
	}
	root := "/localaccount/tasks"
	call("PUT", root, "", 201)
	sent := decode(call("POST", root+"/messages", "<QueueMessage><MessageText>work &amp; verify</MessageText></QueueMessage>", 201))
	if len(sent) != 1 || sent[0].ID == "" || sent[0].Receipt == "" {
		t.Fatal("enqueue lacks identity/receipt")
	}
	peek := decode(call("GET", root+"/messages?peekonly=true", "", 200))
	if len(peek) != 1 || peek[0].Text != "work & verify" || peek[0].Count != 0 || peek[0].Receipt != "" {
		t.Fatalf("peek mutated/exposed dequeue state: %#v", peek)
	}
	got := decode(call("GET", root+"/messages?visibilitytimeout=30", "", 200))
	if len(got) != 1 || got[0].ID != sent[0].ID || got[0].Count != 1 || got[0].Receipt == "" {
		t.Fatalf("receive: %#v", got)
	}
	if len(decode(call("GET", root+"/messages?peekonly=true", "", 200))) != 0 {
		t.Fatal("invisible message visible to peek")
	}
	if len(decode(call("GET", root+"/messages", "", 200))) != 0 {
		t.Fatal("second consumer received reserved message")
	}
	id := url.PathEscape(got[0].ID)
	update := call("PUT", root+"/messages/"+id+"?popreceipt="+url.QueryEscape(got[0].Receipt)+"&visibilitytimeout=0", "<QueueMessage><MessageText>updated</MessageText></QueueMessage>", 204)
	nextReceipt := update.Header().Get("x-ms-popreceipt")
	if nextReceipt == "" || nextReceipt == got[0].Receipt {
		t.Fatal("update did not rotate receipt")
	}
	stale := call("DELETE", root+"/messages/"+id+"?popreceipt="+url.QueryEscape(got[0].Receipt), "", 400)
	if !strings.Contains(stale.Body.String(), "PopReceiptMismatch") {
		t.Fatal("wrong stale-receipt error")
	}
	// Reopening the real SQLite store must preserve both payload and receipt state.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, h = open()
	got = decode(call("GET", root+"/messages?visibilitytimeout=30", "", 200))
	if len(got) != 1 || got[0].Text != "updated" || got[0].Count != 2 || got[0].Receipt == nextReceipt {
		t.Fatalf("restart/dequeue state: %#v", got)
	}
	// Make the message visible before deleting it: a successful-looking DELETE
	// no-op must be observable immediately, not hidden by its receive timeout.
	visible := call("PUT", root+"/messages/"+id+"?popreceipt="+url.QueryEscape(got[0].Receipt)+"&visibilitytimeout=0", "", 204)
	deleteReceipt := visible.Header().Get("x-ms-popreceipt")
	if deleteReceipt == "" || deleteReceipt == got[0].Receipt {
		t.Fatal("visibility update did not rotate receipt")
	}
	beforeDelete := decode(call("GET", root+"/messages?peekonly=true", "", 200))
	if len(beforeDelete) != 1 || beforeDelete[0].ID != got[0].ID || beforeDelete[0].Text != "updated" {
		t.Fatalf("delete precondition: message must be visible: %#v", beforeDelete)
	}
	call("DELETE", root+"/messages/"+id+"?popreceipt="+url.QueryEscape(deleteReceipt), "", 204)
	for _, query := range []string{"?peekonly=true", "?visibilitytimeout=30"} {
		remaining := decode(call("GET", root+"/messages"+query, "", 200))
		if err := requireNoMessagesAfterDelete(len(remaining)); err != nil {
			t.Fatal(err)
		}
	}
	// ClearMessages is tested separately only after DeleteMessage's absence proof.
	call("PUT", root, "", 204)
	call("POST", root+"/messages", "<QueueMessage><MessageText>cleanup</MessageText></QueueMessage>", 201)
	call("DELETE", root+"/messages", "", 204)
	if len(decode(call("GET", root+"/messages", "", 200))) != 0 {
		t.Fatal("clear left a message")
	}
	call("DELETE", root, "", 204)
	call("GET", root+"/messages", "", 404)
	call("PUT", root, "", 201)
	if len(decode(call("GET", root+"/messages", "", 200))) != 0 {
		t.Fatal("recreate resurrected messages")
	}
	call("DELETE", root, "", 204)
}
