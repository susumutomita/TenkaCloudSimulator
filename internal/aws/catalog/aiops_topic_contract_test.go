package catalog

import "testing"

func TestAIOpsMalformedTopicARNDoesNotStoreGroup(t *testing.T) {
	channel := func(arn string) map[string]any {
		return map[string]any{arn: []any{"arn:aws:chatbot::000000000000:chat-configuration/slack-channel/x"}}
	}
	for _, arn := range []string{
		"arn:aws:sns:us-east-1:badaccount:topic",
		"arn:aws:sqs:us-east-1:000000000000:queue:sns:",
		"arn:aws:sns::000000000000:topic",
		"arn:aws:sns:us-east-1:000000000000:topic/path",
		"arn:aws:sns:us-east-1:000000000000:topic:subscription",
	} {
		t.Run(arn, func(t *testing.T) {
			h := newHarness(t, aiopsGroup)
			created := h.rest("aiops", "POST", "/investigationGroups", map[string]any{
				"name": "group", "roleArn": "arn:aws:iam::000000000000:role/Investigations",
				"chatbotNotificationChannel": channel(arn),
			})
			listed := h.rest("aiops", "GET", "/investigationGroups", nil)
			expect(t, listed, 200, "")
			rows := listed.body["investigationGroups"].([]any)
			t.Logf("create status=%d, stored groups=%d", created.status, len(rows))
			expect(t, created, 400, "ValidationException")
			if len(rows) != 0 {
				t.Fatalf("malformed topic persisted %d groups", len(rows))
			}
		})
	}
	t.Run("valid-fifo-and-update-atomicity", func(t *testing.T) {
		h := newHarness(t, aiopsGroup)
		const good = "arn:aws:sns:us-east-1:000000000000:alerts_Example.fifo"
		const bad = "arn:aws:sns:us-east-1:badaccount:topic"
		created := h.rest("aiops", "POST", "/investigationGroups", map[string]any{
			"name": "group", "roleArn": "arn:aws:iam::000000000000:role/Investigations",
			"chatbotNotificationChannel": channel(good),
		})
		expect(t, created, 201, "")
		arn := str(created.body["arn"])
		path := "/investigationGroups/" + arn[len(arn)-16:]
		updated := h.rest("aiops", "PATCH", path, map[string]any{
			"chatbotNotificationChannel": channel(bad), "isCloudTrailEventHistoryEnabled": false,
		})
		got := h.rest("aiops", "GET", path, nil)
		expect(t, got, 200, "")
		m := got.body["chatbotNotificationChannel"].(map[string]any)
		t.Logf("update status=%d, mixed field=%v old topic present=%t", updated.status, got.body["isCloudTrailEventHistoryEnabled"], m[good] != nil)
		expect(t, updated, 400, "ValidationException")
		if got.body["isCloudTrailEventHistoryEnabled"] != true || m[good] == nil || m[bad] != nil {
			t.Fatal("rejected topic update modified stored metadata")
		}
	})
}
