package server

// This oracle precedes routing implementation. The existing Azure Blob route
// stays separate; Queue must be mounted behind the same local access boundary.
import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAzureQueueRoutingLifecycleAndIsolation(t *testing.T) {
	cfg := DefaultCloudStorageConfig()
	type host struct {
		s         *httptest.Server
		close     func() error
		db, token string
	}
	start := func(db, token string) *host {
		t.Helper()
		h, c, err := NewWithCloudStorage(db, cfg)
		if err != nil {
			t.Fatal(err)
		}
		a, err := NewAccess(h, token, false)
		if err != nil {
			c()
			t.Fatal(err)
		}
		return &host{httptest.NewServer(a), c, db, token}
	}
	stop := func(h *host) {
		h.s.Close()
		if err := h.close(); err != nil {
			t.Error(err)
		}
	}
	a := start(filepath.Join(t.TempDir(), "a.db"), strings.Repeat("a", 64))
	defer func() { stop(a) }()
	b := start(filepath.Join(t.TempDir(), "b.db"), strings.Repeat("b", 64))
	defer stop(b)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	call := func(h *host, method, path, token, principal, body string, want int) ([]byte, http.Header) {
		t.Helper()
		r, err := http.NewRequest(method, h.s.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("x-ms-version", "2023-11-03")
		if token != "" {
			r.Header.Set("X-Simulator-Token", token)
		}
		if principal != "" {
			r.Header.Set("X-Simulator-Principal", principal)
		}
		res, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, want, data)
		}
		return data, res.Header
	}
	decode := func(body []byte) []queueOracleMessage {
		t.Helper()
		rows, err := validateQueueOracleXML(body, "read")
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	base := "/_simulator/providers/azure-queue/" + cfg.AzureAccount + "/routing-queue"
	// Genuine registered owner path, not a provider stub or metadata endpoint.
	call(a, "PUT", base, a.token, "", "", 201)
	call(a, "GET", base+"/messages", "", "", "", 401)
	call(a, "GET", base+"/messages", b.token, "", "", 401)
	call(a, "GET", base+"/messages", a.token, "reader", "", 403)
	call(b, "GET", base+"/messages", b.token, "", "", 404)
	sentXML, _ := call(a, "POST", base+"/messages", a.token, "", "<QueueMessage><MessageText>native &amp; persistent</MessageText></QueueMessage>", 201)
	sent, err := validateQueueOracleXML(sentXML, "send")
	if err != nil || len(sent) != 1 {
		t.Fatalf("send: %v %#v", err, sent)
	}
	// Same queue name in B must hold independent data before A is restarted.
	call(b, "PUT", base, b.token, "", "", 201)
	call(b, "POST", base+"/messages", b.token, "", "<QueueMessage><MessageText>team-b-only</MessageText></QueueMessage>", 201)
	assertB := func() {
		t.Helper()
		body, _ := call(b, "GET", base+"/messages?peekonly=true", b.token, "", "", 200)
		rows := decode(body)
		if len(rows) != 1 || *rows[0].Text != "team-b-only" || *rows[0].Count != 0 {
			t.Fatalf("team B contaminated: %#v", rows)
		}
	}
	assertB()
	data, _ := call(a, "GET", base+"/messages?peekonly=true", a.token, "", "", 200)
	peek := decode(data)
	if len(peek) != 1 || peek[0].ID != sent[0].ID || *peek[0].Text != "native & persistent" || peek[0].Receipt != "" || *peek[0].Count != 0 {
		t.Fatalf("peek: %#v", peek)
	}
	db, token := a.db, a.token
	stop(a)
	// Reopen drift must fail without adopting the persisted owner namespace.
	for _, mutate := range []func(*CloudStorageConfig){
		func(c *CloudStorageConfig) { c.AzureAccount = "otheraccount" },
		func(c *CloudStorageConfig) { c.AzureOwner = "otherowner" },
		func(c *CloudStorageConfig) { c.Environment = "otherenvironment" },
	} {
		wrong := cfg
		mutate(&wrong)
		_, closeWrong, err := NewWithCloudStorage(db, wrong)
		if err == nil {
			closeWrong()
			t.Error("restart config drift accepted")
		}
	}
	a = start(db, token)
	assertB()
	data, _ = call(a, "GET", base+"/messages?visibilitytimeout=30", a.token, "", "", 200)
	got, err := validateQueueOracleXML(data, "receive")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != peek[0].ID || got[0].Receipt == "" || *got[0].Count != 1 {
		t.Fatalf("receive: %#v", got)
	}
	id := url.PathEscape(got[0].ID)
	old := url.QueryEscape(got[0].Receipt)
	_, headers := call(a, "PUT", base+"/messages/"+id+"?popreceipt="+old+"&visibilitytimeout=0", a.token, "", "", 204)
	fresh := headers.Get("x-ms-popreceipt")
	if fresh == "" || fresh == got[0].Receipt {
		t.Fatal("receipt not rotated")
	}
	call(a, "DELETE", base+"/messages/"+id+"?popreceipt="+old, a.token, "", "", 400)
	call(a, "DELETE", base+"/messages/"+id+"?popreceipt="+url.QueryEscape(fresh), a.token, "", "", 204)
	data, _ = call(a, "GET", base+"/messages", a.token, "", "", 200)
	if len(decode(data)) != 0 {
		t.Fatal("delete no-op before clear")
	}
	call(a, "POST", base+"/messages", a.token, "", "<QueueMessage><MessageText>clear separately</MessageText></QueueMessage>", 201)
	call(a, "DELETE", base+"/messages", a.token, "", "", 204)
	data, _ = call(a, "GET", base+"/messages", a.token, "", "", 200)
	if len(decode(data)) != 0 {
		t.Fatal("clear no-op")
	}
	call(a, "DELETE", base, a.token, "", "", 204)
	call(a, "GET", base+"/messages", a.token, "", "", 404)
	// A's clear/delete did not clear or delete the same-name queue in B.
	assertB()
	call(b, "DELETE", base, b.token, "", "", 204)
	// Adding Queue must preserve Blob's native route and container semantics.
	blob := "/_simulator/providers/azure/" + cfg.AzureAccount + "/unaffected-blob?restype=container"
	call(a, "PUT", blob, a.token, "", "", 201)
	call(a, "DELETE", blob, a.token, "", "", 202)
}

// Pointer fields distinguish absent XML fields from legitimate zero/empty values.
type queueOracleMessage struct {
	ID       string  `xml:"MessageId"`
	Inserted string  `xml:"InsertionTime"`
	Expires  string  `xml:"ExpirationTime"`
	Receipt  string  `xml:"PopReceipt"`
	Visible  string  `xml:"TimeNextVisible"`
	Text     *string `xml:"MessageText"`
	Count    *int    `xml:"DequeueCount"`
}

func validateQueueOracleXML(body []byte, mode string) ([]queueOracleMessage, error) {
	var list struct {
		XMLName  xml.Name
		Messages []queueOracleMessage `xml:"QueueMessage"`
	}
	if err := xml.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	if list.XMLName.Local != "QueueMessagesList" {
		return nil, fmt.Errorf("wrong XML root")
	}
	for _, m := range list.Messages {
		if strings.TrimSpace(m.ID) == "" {
			return nil, fmt.Errorf("MessageId missing/empty")
		}
		for _, value := range []string{m.Inserted, m.Expires} {
			if _, err := http.ParseTime(value); err != nil {
				return nil, fmt.Errorf("required message timestamp absent/invalid")
			}
		}
		if mode != "send" && (m.Text == nil || m.Count == nil || *m.Count < 0) {
			return nil, fmt.Errorf("MessageText/DequeueCount absent or invalid")
		}
		if mode == "send" || mode == "receive" {
			if m.Receipt == "" {
				return nil, fmt.Errorf("PopReceipt absent")
			}
			if _, err := http.ParseTime(m.Visible); err != nil {
				return nil, fmt.Errorf("TimeNextVisible absent/invalid")
			}
		}
	}
	return list.Messages, nil
}
func TestAzureQueueXMLOracleNegativeControls(t *testing.T) {
	stamp := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).Format(http.TimeFormat)
	good := "<QueueMessagesList><QueueMessage><MessageId>id</MessageId><InsertionTime>" + stamp + "</InsertionTime><ExpirationTime>" + stamp + "</ExpirationTime><MessageText></MessageText><DequeueCount>0</DequeueCount></QueueMessage></QueueMessagesList>"
	if _, err := validateQueueOracleXML([]byte(good), "read"); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"<MessageId>id</MessageId>", "<InsertionTime>" + stamp + "</InsertionTime>", "<ExpirationTime>" + stamp + "</ExpirationTime>", "<MessageText></MessageText>", "<DequeueCount>0</DequeueCount>"} {
		if _, err := validateQueueOracleXML([]byte(strings.Replace(good, fragment, "", 1)), "read"); err == nil {
			t.Fatalf("missing %s accepted", fragment)
		}
	}
	if _, err := validateQueueOracleXML([]byte(strings.Replace(good, "<MessageId>id</MessageId>", "<MessageId></MessageId>", 1)), "read"); err == nil {
		t.Fatal("empty ID accepted")
	}
	if _, err := validateQueueOracleXML([]byte(good), "receive"); err == nil {
		t.Fatal("missing receive receipt/time accepted")
	}
}
