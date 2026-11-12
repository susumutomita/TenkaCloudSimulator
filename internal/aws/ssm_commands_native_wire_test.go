package aws

import (
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
	"strings"
	"testing"
)

func TestNativeCommandExactHostWireShapes(t *testing.T) {
	send := `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["sudo systemctl stop nginx"]},"Comment":"operation-1","TimeoutSeconds":30}`
	got, err := decodeNativeCommand("SendCommand", []byte(send))
	if err != nil || got.Send.TimeoutSeconds != 30 || got.Send.Comment != "operation-1" {
		t.Fatalf("send %v %v", got, err)
	}
	list, err := decodeNativeCommand("ListCommands", []byte(`{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00.001Z"}],"MaxResults":50}`))
	if err != nil || list.MaxResults != 50 {
		t.Fatalf("list %v", err)
	}
	get, err := decodeNativeCommand("GetCommandInvocation", []byte(`{"CommandId":"01234567-89ab-4def-8123-456789abcdef","InstanceId":"i-0123456789abcdef0"}`))
	if err != nil || get.InstanceID != "i-0123456789abcdef0" {
		t.Fatalf("get %v", err)
	}
	wire := commandWireSummary("id", got.Send, 1762819200123)
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["RequestedDateTime"] != float64(1762819200.123) {
		t.Fatal("timestamp is not epoch seconds")
	}
}
func TestNativeCommandRejectsUnsupportedAndAmbiguousWire(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"Filters":null}`, `{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}],"MaxResults":null}`, `{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}],"NextToken":null}`, `{"Filters":[{"key":"InvokedAfter","key":"Status","value":"2026-11-11T00:00:00Z"}]}`, `{"Filters":[{"Key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}]}`, `{"Filters":[{"key":"InvokedAfter","value":"bad"}]}`, `{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}],"MaxResults":0}`, `{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}],"MaxResults":51}`, `{"Filters":[{"key":"InvokedAfter","value":"2026-11-11T00:00:00Z"}]} {}`} {
		if _, err := decodeNativeCommand("ListCommands", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, body := range []string{`{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0","i-0123456789abcdef0"],"Parameters":{"commands":["true"]},"TimeoutSeconds":30}`, `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["true"],"executionTimeout":["30"]},"TimeoutSeconds":30}`, `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["true"]},"TimeoutSeconds":30,"ServiceRoleArn":"arn:fake"}`} {
		if _, err := decodeNativeCommand("SendCommand", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestNativeCommandRejectsNestedNull(t *testing.T) {
	base := `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["true"]},"Comment":"job","TimeoutSeconds":30}`
	if _, err := decodeNativeCommand("SendCommand", []byte(base)); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{`"Comment":"job"`, `"Comment":null`}, {`"TimeoutSeconds":30`, `"TimeoutSeconds":null`}, {`"Parameters":{"commands":["true"]}`, `"Parameters":null`}, {`["true"]`, `null`}, {`["true"]`, `[null]`}} {
		body := strings.Replace(base, pair[0], pair[1], 1)
		if _, err := decodeNativeCommand("SendCommand", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}
func TestNativeCommandUnknownNeverBecomesSuccess(t *testing.T) {
	command := SSMCommand{CommandId: "id", Request: SSMCommandRequest{DocumentName: "AWS-RunShellScript"}}
	for _, obs := range []guestcommand.DurableObservation{{State: "Unknown"}, {State: "Success", CleanupRequired: true}, {State: "Success", OutputTruncated: true}} {
		if _, err := commandWireInvocation(command, "i-0123456789abcdef0", obs); !errors.Is(err, ErrSSMCommandUnknown) {
			t.Fatalf("unknown became result: %v", err)
		}
	}
	got, err := commandWireInvocation(command, "i-0123456789abcdef0", guestcommand.DurableObservation{State: "InProgress"})
	if err != nil || got.Status != "InProgress" || got.ResponseCode != -1 {
		t.Fatal("pending exit invented")
	}
}
func TestNativeCommandTimeoutDefaultsToAWSValueOnlyWhenOmitted(t *testing.T) {
	got, err := decodeNativeCommand("SendCommand", []byte(`{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["true"]}}`))
	if err != nil || got.Send.TimeoutSeconds != 3600 {
		t.Fatalf("omitted TimeoutSeconds: %+v %v", got.Send, err)
	}
	for _, timeout := range []string{"null", "29", "3601", "0"} {
		body := `{"DocumentName":"AWS-RunShellScript","InstanceIds":["i-0123456789abcdef0"],"Parameters":{"commands":["true"]},"TimeoutSeconds":` + timeout + `}`
		if _, err := decodeNativeCommand("SendCommand", []byte(body)); err == nil {
			t.Fatalf("accepted TimeoutSeconds %s", timeout)
		}
	}
}
