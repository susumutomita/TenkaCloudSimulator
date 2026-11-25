package queue

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestQueueWireCountPresenceByResponse(t *testing.T) {
	text := "payload"
	for _, tc := range []struct {
		name    string
		message wireMessage
		count   *int64
	}{
		{"peek zero", wireMessage{ID: "id", Text: &text}, new(int64)},
		{"receive positive", wireMessage{ID: "id", Text: &text, Count: 3, Receipt: "receipt"}, func() *int64 { n := int64(3); return &n }()},
		{"send no count", wireMessage{ID: "id", Receipt: "receipt"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := xml.Marshal(messageList{Messages: []wireMessage{tc.message}})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Messages []struct {
					Count *int64 `xml:"DequeueCount"`
					ID    string `xml:"MessageId"`
				} `xml:"QueueMessage"`
			}
			if err := xml.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Messages) != 1 || got.Messages[0].ID != "id" {
				t.Fatalf("message changed: %s", body)
			}
			count := got.Messages[0].Count
			if tc.count == nil {
				if count != nil {
					t.Fatalf("unexpected send count: %s", body)
				}
			} else if count == nil || *count != *tc.count || strings.Count(string(body), "<DequeueCount>") != 1 {
				t.Fatalf("count presence/value: %s", body)
			}
		})
	}
}
