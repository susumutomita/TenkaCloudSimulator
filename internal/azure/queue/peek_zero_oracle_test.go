package queue_test

// Independent wire oracle: Peek Messages returns the count without incrementing
// it, including zero before the first receive. Unlike decoding into int64, *int
// distinguishes a missing element from a real zero. Specification:
// https://learn.microsoft.com/en-us/rest/api/storageservices/peek-messages
import (
	"context"
	"encoding/xml"
	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/queue"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAzureQueuePeekZeroWireField(t *testing.T) {
	db, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h, err := queue.NewOwnedHandler(db.Database(), queue.Config{Account: "localaccount", Owner: "owner", Environment: "wire-oracle"}, func(context.Context) (queue.AuthenticatedActor, bool) {
		return queue.AuthenticatedActor{ExerciseIdentity: "owner"}, true
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("x-ms-version", "2023-11-03")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d", method, path, w.Code, want)
		}
		return w.Body.Bytes()
	}
	call("PUT", "/localaccount/zero-count", "", 201)
	call("POST", "/localaccount/zero-count/messages", "<QueueMessage><MessageText>never received</MessageText></QueueMessage>", 201)
	body := call("GET", "/localaccount/zero-count/messages?peekonly=true", "", 200)
	var response struct {
		Messages []struct {
			ID    string `xml:"MessageId"`
			Count *int   `xml:"DequeueCount"`
		} `xml:"QueueMessage"`
	}
	if err := xml.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Messages) != 1 || response.Messages[0].ID == "" {
		t.Fatal("missing message identity")
	}
	if response.Messages[0].Count == nil {
		t.Fatalf("DequeueCount XML element missing for never-received message: %s", body)
	}
	if *response.Messages[0].Count != 0 {
		t.Fatalf("peek changed count: %d", *response.Messages[0].Count)
	}
}
