package catalog

import (
	"reflect"
	"testing"
	"time"
)

// The TriggerConfig API reserves schedule settings for Scheduled flows. A
// Scheduled definition needs an actual schedule; neither mode executes a job.
// https://docs.aws.amazon.com/appflow/1.0/APIReference/API_TriggerConfig.html
// https://docs.aws.amazon.com/appflow/1.0/APIReference/API_ScheduledTriggerProperties.html
func TestAppFlowTriggerConfigurationLifecycle(t *testing.T) {
	h := newHarness(t, appFlow)
	const flow = "trigger-contract"
	identity := map[string]any{"flowName": flow}
	call := func(path string, body any) reply { return h.rest("appflow", "POST", path, body) }
	update := func(trigger map[string]any, description string) reply {
		req := s3Flow(flow)
		delete(req, "tags")
		req["triggerConfig"], req["description"] = trigger, description
		return call("/update-flow", req)
	}
	schedule := func(expression string) map[string]any {
		return map[string]any{"Scheduled": map[string]any{"scheduleExpression": expression}}
	}
	expect(t, call("/create-flow", s3Flow(flow)), 200, "")
	before := call("/describe-flow", identity)
	expect(t, before, 200, "")
	h.now = h.now.Add(time.Hour)
	for _, tc := range []struct {
		name    string
		trigger map[string]any
	}{
		{"on-demand-with-schedule", map[string]any{"triggerType": "OnDemand", "triggerProperties": schedule("rate(5 minutes)")}},
		{"scheduled-unspecified", map[string]any{"triggerType": "Scheduled"}},
		{"scheduled-empty-properties", map[string]any{"triggerType": "Scheduled", "triggerProperties": map[string]any{}}},
		{"scheduled-blank-expression", map[string]any{"triggerType": "Scheduled", "triggerProperties": schedule("   ")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create := s3Flow(tc.name)
			create["triggerConfig"] = tc.trigger
			expect(t, call("/create-flow", create), 400, "ValidationException")
			expect(t, call("/describe-flow", map[string]any{"flowName": tc.name}), 404, "ResourceNotFoundException")
			expect(t, update(tc.trigger, "must not persist"), 400, "ValidationException")
			after := call("/describe-flow", identity)
			expect(t, after, 200, "")
			if !reflect.DeepEqual(before.body, after.body) {
				t.Fatalf("rejected update changed the stored flow: before=%v after=%v", before.body, after.body)
			}
		})
	}

	// A valid schedule survives read-back; switching to OnDemand discards
	// that schedule, including when an SDK serializes empty triggerProperties.
	for _, trigger := range []map[string]any{
		{"triggerType": "Scheduled", "triggerProperties": schedule("rate(5 minutes)")},
		{"triggerType": "OnDemand", "triggerProperties": map[string]any{}},
	} {
		expect(t, update(trigger, "changed"), 200, "")
		read := call("/describe-flow", identity)
		expect(t, read, 200, "")
		if !reflect.DeepEqual(trigger, read.body["triggerConfig"]) || read.body["lastRunExecutionDetails"] != nil {
			t.Fatalf("wrong trigger state or synthetic run after update: %v", read.body)
		}
	}

	expect(t, call("/start-flow", identity), 501, "UnknownOperationException")
	expect(t, call("/delete-flow", identity), 200, "")
	expect(t, call("/describe-flow", identity), 404, "ResourceNotFoundException")
}
