package queue

import (
	"context"
	"encoding/xml"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Handler, *core.Store, *time.Time) {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	h, err := NewOwnedHandler(db.Database(), Config{Account: "localaccount", Owner: "owner", Environment: "test", Now: func() time.Time { return now }}, func(context.Context) (AuthenticatedActor, bool) { return AuthenticatedActor{"owner"}, true })
	if err != nil {
		t.Fatal(err)
	}
	return h, db, &now
}
func request(t *testing.T, h http.Handler, method, path, body string, status int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("x-ms-version", apiVersion)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d expected %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func messages(t *testing.T, w *httptest.ResponseRecorder) []wireMessage {
	t.Helper()
	var v messageList
	if err := xml.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Messages
}

const qp = "/localaccount/tasks"
const payload = "<QueueMessage><MessageText>original</MessageText></QueueMessage>"

func TestVisibilityExpiryAndStaleReceipt(t *testing.T) {
	h, _, now := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	m := messages(t, request(t, h, "POST", qp+"/messages?messagettl=60", payload, 201))[0]
	got := messages(t, request(t, h, "GET", qp+"/messages?visibilitytimeout=2", "", 200))[0]
	*now = now.Add(3 * time.Second)
	// A receipt stays valid after timeout until another consumer changes it.
	request(t, h, "PUT", qp+"/messages/"+m.ID+"?visibilitytimeout=0&popreceipt="+url.QueryEscape(got.Receipt), "", 204)
	newer := messages(t, request(t, h, "GET", qp+"/messages", "", 200))[0]
	request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(got.Receipt), "", 400)
	*now = now.Add(time.Minute)
	request(t, h, "DELETE", qp+"/messages/"+m.ID+"?popreceipt="+url.QueryEscape(newer.Receipt), "", 404)
	if len(messages(t, request(t, h, "GET", qp+"/messages", "", 200))) != 0 {
		t.Fatal("expired message returned")
	}
}
func TestOwnerDenialAndIsolation(t *testing.T) {
	h, db, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	for _, cfg := range []Config{{"otheraccount", "owner", "test", nil}, {"localaccount", "other", "test", nil}, {"localaccount", "owner", "other", nil}} {
		if _, err := NewOwnedHandler(db.Database(), cfg, h.actor); err == nil {
			t.Fatal("binding drift accepted")
		}
	}
	old := h.actor
	h.actor = func(context.Context) (AuthenticatedActor, bool) { return AuthenticatedActor{}, false }
	request(t, h, "GET", qp+"/messages", "", 401)
	h.actor = func(context.Context) (AuthenticatedActor, bool) { return AuthenticatedActor{"peer"}, true }
	request(t, h, "DELETE", qp, "", 403)
	request(t, h, "GET", "/localaccount/unknown/messages", "", 403)
	h.actor = old
	request(t, h, "PUT", qp, "", 204)
	other, _, _ := fixture(t)
	request(t, other, "GET", qp+"/messages", "", 404)
}
func TestInvalidRequestsDoNotMutate(t *testing.T) {
	h, _, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	cases := []struct {
		method, path, body string
		code               int
	}{
		{"POST", qp + "/messages?numofmessages=1", payload, 501}, {"POST", qp + "/messages?messagettl=0", payload, 400},
		{"POST", qp + "/messages?messagettl=1&visibilitytimeout=1", payload, 400}, {"POST", qp + "/messages?visibilitytimeout=-1", payload, 400},
		{"POST", qp + "/messages?visibilitytimeout=1&visibilitytimeout=2", payload, 400},
		{"GET", qp + "/messages?peekonly=false", "", 400}, {"GET", qp + "/messages?numofmessages=33", "", 400},
		{"GET", qp + "/messages?peekonly=true&visibilitytimeout=1", "", 400}, {"GET", qp + "/messages?visibilitytimeout=0", "", 400},
		{"GET", qp + "/messages?numofmessages=x", "", 400}, {"GET", qp + "/messages?x=%xx", "", 400},
		{"PUT", "/localaccount/Aa", "", 400}, {"PUT", "/localaccount/a--b", "", 400}, {"GET", qp + "/unknown", "", 501},
		{"HEAD", qp, "", 501}, {"GET", "/wrong/tasks/messages", "", 404},
		{"POST", qp + "/messages", "<QueueMessage><MessageText><child/></MessageText></QueueMessage>", 400},
		{"POST", qp + "/messages", "<QueueMessage><MessageText>x</MessageText><Other/></QueueMessage>", 400},
		{"POST", qp + "/messages", "<QueueMessage bad='true'><MessageText>x</MessageText></QueueMessage>", 400},
		{"POST", qp + "/messages", "<!DOCTYPE x><QueueMessage><MessageText>x</MessageText></QueueMessage>", 400},
		{"POST", qp + "/messages", "<QueueMessage><MessageText>" + strings.Repeat("x", maxMessage+1) + "</MessageText></QueueMessage>", 400},
		{"POST", qp + "/messages", "", 400}, {"PUT", qp + "/messages/noid?visibilitytimeout=0", "", 400},
	}
	for _, c := range cases {
		request(t, h, c.method, c.path, c.body, c.code)
	}
	if len(messages(t, request(t, h, "GET", qp+"/messages", "", 200))) != 0 {
		t.Fatal("invalid write mutated state")
	}
}
func TestBatchReceivePeekAndPayloadUpdate(t *testing.T) {
	h, _, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	for range 3 {
		request(t, h, "POST", qp+"/messages?messagettl=-1", payload, 201)
	}
	before := messages(t, request(t, h, "GET", qp+"/messages?peekonly=true&numofmessages=2", "", 200))
	if len(before) != 2 || before[0].Receipt != "" {
		t.Fatal("peek")
	}
	got := messages(t, request(t, h, "GET", qp+"/messages?numofmessages=2", "", 200))
	if len(got) != 2 || got[0].Count != 1 {
		t.Fatal("receive")
	}
	p := qp + "/messages/" + got[0].ID + "?popreceipt=" + url.QueryEscape(got[0].Receipt)
	request(t, h, "PUT", p, "", 400)
	request(t, h, "PUT", p+"&visibilitytimeout=0", "<QueueMessage><MessageText>changed</MessageText></QueueMessage>", 204)
	peek := messages(t, request(t, h, "GET", qp+"/messages?peekonly=true&numofmessages=32", "", 200))
	if len(peek) != 2 || *peek[0].Text != "changed" {
		t.Fatal("update")
	}
}
func TestRequestAndDatabaseFailures(t *testing.T) {
	h, db, _ := fixture(t)
	r := httptest.NewRequest("PUT", qp, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	r.Header.Set("x-ms-version", apiVersion)
	r.Header.Set("x-ms-meta-x", "y")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 501 {
		t.Fatal(w.Code)
	}
	r.Header.Del("x-ms-meta-x")
	r.ContentLength = maxRequest + 1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
	db.Close()
	request(t, h, "PUT", qp, "", 500)
}
