package queue

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestProtocolRefusals(t *testing.T) {
	for _, body := range []string{"<QueueMessage>", "garbage", "<?wrong x?><QueueMessage><MessageText>x</MessageText></QueueMessage>", "<QueueMessage><?xml x?><MessageText>x</MessageText></QueueMessage>", "<QueueMessage><MessageText>x</MessageText></QueueMessage><QueueMessage/>", strings.Repeat(" ", maxRequest+1)} {
		if _, err := textBody(strings.NewReader(body), false); err == nil {
			t.Fatalf("accepted bad XML length=%d", len(body))
		}
	}
	if _, err := integer(url.Values{"n": []string{""}}, "n", 1, 0, 3); err == nil {
		t.Fatal("empty integer")
	}
	if problem(400, "code").Error() != "code" {
		t.Fatal("error text")
	}
	w := httptest.NewRecorder()
	respondXML(w, 200, make(chan int))
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}
func TestQuotaAndExpirationBounds(t *testing.T) {
	h, db, _ := fixture(t)
	request(t, h, "PUT", qp, "", 201)
	m := messages(t, request(t, h, "POST", qp+"/messages?messagettl=10", payload, 201))[0]
	p := qp + "/messages/" + m.ID + "?popreceipt=" + url.QueryEscape(m.Receipt)
	request(t, h, "PUT", p+"&visibilitytimeout=11", "", 400)
	request(t, h, "PUT", p+"&visibilitytimeout=x", "", 400)
	request(t, h, "POST", qp+"/messages?messagettl=604801", payload, 400)
	_, err := db.Database().Exec(`WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO azure_queue_messages(queue,id,text,inserted,expires,visible,receipt) SELECT 'tasks','id'||x,'',0,253402300799,0,'r' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "POST", qp+"/messages", payload, 409)
	_, err = db.Database().Exec(`WITH RECURSIVE n(x) AS(VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO azure_queues SELECT 'q'||x FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, "PUT", "/localaccount/newqueue", "", 409)
}
