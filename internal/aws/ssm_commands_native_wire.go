package aws

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

var nativeCommandInstance = regexp.MustCompile(`^i-[a-f0-9]{17}$`)

type nativeCommandInput struct {
	Send                                           SSMCommandRequest
	CommandID, InstanceID, InvokedAfter, NextToken string
	MaxResults                                     int
}

func decodeNativeCommand(operation string, raw []byte) (nativeCommandInput, error) {
	var out nativeCommandInput
	var fields map[string]json.RawMessage
	if len(raw) > 48<<10 || strictDocumentJSON(raw, &fields) != nil || fields == nil {
		return out, ErrSSMCommandInvalid
	}
	var allowed map[string]bool
	switch operation {
	case "SendCommand":
		allowed = map[string]bool{"DocumentName": true, "InstanceIds": true, "Parameters": true, "Comment": true, "TimeoutSeconds": true}
	case "GetCommandInvocation":
		allowed = map[string]bool{"CommandId": true, "InstanceId": true}
	case "ListCommands":
		allowed = map[string]bool{"Filters": true, "MaxResults": true, "NextToken": true}
	default:
		return out, ErrSSMCommandInvalid
	}
	for key := range fields {
		if !allowed[key] {
			return out, ErrSSMCommandInvalid
		}
	}
	switch operation {
	case "SendCommand":
		if err := strictDocumentJSON(raw, &out.Send); err != nil {
			return out, err
		}
		sort.Strings(out.Send.InstanceIds)
		if !ssmRequestValid(out.Send) {
			return out, ErrSSMCommandInvalid
		}
		for _, id := range out.Send.InstanceIds {
			if !nativeCommandInstance.MatchString(id) {
				return out, ErrSSMCommandInvalid
			}
		}
	case "GetCommandInvocation":
		if json.Unmarshal(fields["CommandId"], &out.CommandID) != nil || json.Unmarshal(fields["InstanceId"], &out.InstanceID) != nil || !ssmCommandID.MatchString(out.CommandID) || !nativeCommandInstance.MatchString(out.InstanceID) {
			return out, ErrSSMCommandInvalid
		}
	case "ListCommands":
		out.MaxResults = 50
		if value, ok := fields["MaxResults"]; ok {
			if json.Unmarshal(value, &out.MaxResults) != nil || out.MaxResults < 1 || out.MaxResults > 50 {
				return out, ErrSSMCommandInvalid
			}
		}
		if value, ok := fields["NextToken"]; ok {
			if json.Unmarshal(value, &out.NextToken) != nil || len(out.NextToken) > 512 {
				return out, ErrSSMCommandInvalid
			}
		}
		var filters []map[string]json.RawMessage
		if json.Unmarshal(fields["Filters"], &filters) != nil || len(filters) != 1 || len(filters[0]) != 2 {
			return out, ErrSSMCommandInvalid
		}
		var key string
		if json.Unmarshal(filters[0]["key"], &key) != nil || key != "InvokedAfter" || json.Unmarshal(filters[0]["value"], &out.InvokedAfter) != nil {
			return out, ErrSSMCommandInvalid
		}
		after, err := time.Parse(time.RFC3339Nano, out.InvokedAfter)
		if err != nil || after.UnixMilli() < 0 {
			return out, ErrSSMCommandInvalid
		}
	}
	return out, nil
}

type nativeCommandSummary struct {
	CommandID         string              `json:"CommandId"`
	DocumentName      string              `json:"DocumentName"`
	InstanceIDs       []string            `json:"InstanceIds"`
	Parameters        map[string][]string `json:"Parameters"`
	Comment           string              `json:"Comment"`
	RequestedDateTime float64             `json:"RequestedDateTime"`
	TimeoutSeconds    int                 `json:"TimeoutSeconds"`
}

func commandWireSummary(id string, r SSMCommandRequest, at int64) nativeCommandSummary {
	// AWS JSON timestamp numbers are seconds, not the store's milliseconds.
	return nativeCommandSummary{id, r.DocumentName, append([]string(nil), r.InstanceIds...), r.Parameters, r.Comment, float64(at) / 1000, r.TimeoutSeconds}
}

type nativeCommandInvocation struct {
	CommandID              string `json:"CommandId"`
	InstanceID             string `json:"InstanceId"`
	DocumentName           string `json:"DocumentName"`
	Comment                string `json:"Comment"`
	Status                 string `json:"Status"`
	StatusDetails          string `json:"StatusDetails"`
	ResponseCode           int    `json:"ResponseCode"`
	StandardOutputContent  string `json:"StandardOutputContent"`
	StandardErrorContent   string `json:"StandardErrorContent"`
	ExecutionStartDateTime string `json:"ExecutionStartDateTime,omitempty"`
	ExecutionEndDateTime   string `json:"ExecutionEndDateTime,omitempty"`
}

func commandWireInvocation(r SSMCommand, instance string, o guestcommand.DurableObservation) (nativeCommandInvocation, error) {
	out := nativeCommandInvocation{CommandID: r.CommandId, InstanceID: instance, DocumentName: r.Request.DocumentName, Comment: r.Request.Comment, ResponseCode: -1}
	switch o.State {
	case "Unknown":
		return out, ErrSSMCommandUnknown
	case "InProgress":
		out.Status = "InProgress"
	case "Rejected":
		out.Status = "Failed"
		out.StatusDetails = "Delivery rejected without execution"
	case "Success", "Failed", "TimedOut":
		if o.CleanupRequired || o.OutputTruncated && o.State == "Success" {
			return out, ErrSSMCommandUnknown
		}
		out.Status = o.State
		out.ResponseCode = o.ExitCode
		out.StandardOutputContent = o.Stdout
		out.StandardErrorContent = o.Stderr
	default:
		return out, fmt.Errorf("unrecognized command observation")
	}
	if out.StatusDetails == "" {
		out.StatusDetails = out.Status
	}
	if o.StartedAt != 0 {
		out.ExecutionStartDateTime = time.UnixMilli(o.StartedAt).UTC().Format(time.RFC3339Nano)
	}
	if o.FinishedAt != 0 {
		out.ExecutionEndDateTime = time.UnixMilli(o.FinishedAt).UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}
