package aws

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

func TestSSMCommandRequestValidationBoundsEveryField(t *testing.T) {
	base := func() SSMCommandRequest {
		return SSMCommandRequest{DocumentName: "AWS-RunShellScript", Parameters: map[string][]string{"commands": {"printf hello"}}, Comment: "c", TimeoutSeconds: 30, InstanceIds: []string{"i-01", "i-02"}}
	}
	if !ssmRequestValid(base()) {
		t.Fatal("canonical request refused")
	}
	many := make([]string, 33)
	for i := range many {
		many[i] = "x"
	}
	for name, edit := range map[string]func(*SSMCommandRequest){
		"document":         func(r *SSMCommandRequest) { r.DocumentName = "AWS-RunPowerShellScript" },
		"short timeout":    func(r *SSMCommandRequest) { r.TimeoutSeconds = 29 },
		"long timeout":     func(r *SSMCommandRequest) { r.TimeoutSeconds = 3601 },
		"long comment":     func(r *SSMCommandRequest) { r.Comment = strings.Repeat("c", 101) },
		"NUL comment":      func(r *SSMCommandRequest) { r.Comment = "a\x00" },
		"invalid UTF-8":    func(r *SSMCommandRequest) { r.Comment = "\xff" },
		"no instances":     func(r *SSMCommandRequest) { r.InstanceIds = nil },
		"nine instances":   func(r *SSMCommandRequest) { r.InstanceIds = strings.Fields("i-1 i-2 i-3 i-4 i-5 i-6 i-7 i-8 i-9") },
		"unsorted":         func(r *SSMCommandRequest) { r.InstanceIds = []string{"i-02", "i-01"} },
		"duplicate":        func(r *SSMCommandRequest) { r.InstanceIds = []string{"i-01", "i-01"} },
		"bad instance":     func(r *SSMCommandRequest) { r.InstanceIds = []string{"bad id"} },
		"extra parameter":  func(r *SSMCommandRequest) { r.Parameters["workingDirectory"] = []string{"/"} },
		"no commands key":  func(r *SSMCommandRequest) { r.Parameters = map[string][]string{"other": {"x"}} },
		"empty commands":   func(r *SSMCommandRequest) { r.Parameters["commands"] = nil },
		"33 commands":      func(r *SSMCommandRequest) { r.Parameters["commands"] = many },
		"NUL command":      func(r *SSMCommandRequest) { r.Parameters["commands"] = []string{"a\x00"} },
		"invalid command":  func(r *SSMCommandRequest) { r.Parameters["commands"] = []string{"\xff"} },
		"empty script":     func(r *SSMCommandRequest) { r.Parameters["commands"] = []string{""} },
		"oversized script": func(r *SSMCommandRequest) { r.Parameters["commands"] = []string{strings.Repeat("a", 8193)} },
	} {
		r := base()
		edit(&r)
		if ssmRequestValid(r) {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSSMCommandObservationValidationMatchesEachState(t *testing.T) {
	i := guestcommand.DurableIntent{Token: "t", Command: "c", RequestedAt: 1000}
	ok := map[string]guestcommand.DurableObservation{
		"unknown":          {Intent: i, State: "Unknown"},
		"unknown cleanup":  {Intent: i, State: "Unknown", StartedAt: 1001, CleanupRequired: true},
		"in progress":      {Intent: i, State: "InProgress", StartedAt: 1001},
		"rejected":         {Intent: i, State: "Rejected", FinishedAt: 1000},
		"success":          {Intent: i, State: "Success", StartedAt: 1000, FinishedAt: 1001, Stdout: "ok"},
		"failed truncated": {Intent: i, State: "Failed", StartedAt: 1000, FinishedAt: 1001, ExitCode: 2, OutputTruncated: true},
		"timed out":        {Intent: i, State: "TimedOut", StartedAt: 1000, FinishedAt: 2000, ExitCode: 124},
	}
	for name, o := range ok {
		if !ssmObservationValid(o, i) {
			t.Errorf("%s refused", name)
		}
	}
	other := i
	other.Token = "u"
	for name, o := range map[string]guestcommand.DurableObservation{
		"foreign intent":        {Intent: other, State: "Unknown"},
		"invalid stdout":        {Intent: i, State: "Failed", StartedAt: 1000, FinishedAt: 1000, Stdout: "\xff"},
		"oversized stderr":      {Intent: i, State: "Failed", StartedAt: 1000, FinishedAt: 1000, Stderr: strings.Repeat("e", ssmCommandOutput+1)},
		"negative start":        {Intent: i, State: "Unknown", StartedAt: -1},
		"start before request":  {Intent: i, State: "InProgress", StartedAt: 999},
		"unknown with output":   {Intent: i, State: "Unknown", Stdout: "x"},
		"unknown finished":      {Intent: i, State: "Unknown", FinishedAt: 1001},
		"in progress cleanup":   {Intent: i, State: "InProgress", StartedAt: 1001, CleanupRequired: true},
		"in progress finished":  {Intent: i, State: "InProgress", StartedAt: 1001, FinishedAt: 1002},
		"rejected after start":  {Intent: i, State: "Rejected", StartedAt: 1001, FinishedAt: 1002},
		"rejected early":        {Intent: i, State: "Rejected", FinishedAt: 999},
		"success nonzero":       {Intent: i, State: "Success", StartedAt: 1000, FinishedAt: 1001, ExitCode: 1},
		"success truncated":     {Intent: i, State: "Success", StartedAt: 1000, FinishedAt: 1001, OutputTruncated: true},
		"finished before start": {Intent: i, State: "Failed", StartedAt: 1002, FinishedAt: 1001},
		"terminal cleanup":      {Intent: i, State: "TimedOut", StartedAt: 1000, FinishedAt: 1001, CleanupRequired: true},
		"unstarted terminal":    {Intent: i, State: "Failed", FinishedAt: 1001},
		"unknown state":         {Intent: i, State: "Cancelled"},
	} {
		if ssmObservationValid(o, i) {
			t.Errorf("%s accepted", name)
		}
	}
}

// A stored command is accepted only when every target obligation still binds
// the exact request, authority, lease and token it was created with.
func TestSSMCommandRecordValidationRejectsTampering(t *testing.T) {
	db := newFaultDB(t)
	s, a, leases := faultCommandStore(t, db.DB, 2)
	r, err := s.Send(context.Background(), a, ssmCommandRequestFixture(leases), leases)
	if err != nil {
		t.Fatal(err)
	}
	if !ssmRecordValid(r) {
		t.Fatal("sent record invalid")
	}
	clone := func() SSMCommand {
		var c SSMCommand
		if err := json.Unmarshal([]byte(ssmEncode(r)), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for name, edit := range map[string]func(*SSMCommand){
		"command ID":   func(c *SSMCommand) { c.CommandId = "nope" },
		"authority":    func(c *SSMCommand) { c.Authority.Account = "111111111111" },
		"request":      func(c *SSMCommand) { c.Request.TimeoutSeconds = 10 },
		"requested at": func(c *SSMCommand) { c.RequestedAt = 0 },
		"target count": func(c *SSMCommand) { c.Targets = c.Targets[:1] },
		"lease state":  func(c *SSMCommand) { c.Targets[0].Lease.State = "released" },
		"lease region": func(c *SSMCommand) { c.Targets[0].Lease.Owner.Region = "eu-west-1" },
		"lease instance": func(c *SSMCommand) {
			c.Targets[0].Lease.Owner.Instance, c.Targets[1].Lease.Owner.Instance = c.Targets[1].Lease.Owner.Instance, c.Targets[0].Lease.Owner.Instance
		},
		"bad token":         func(c *SSMCommand) { c.Targets[0].Intent.Token = "x" },
		"duplicate token":   func(c *SSMCommand) { c.Targets[1].Intent.Token = c.Targets[0].Intent.Token },
		"intent command":    func(c *SSMCommand) { c.Targets[0].Intent.Command = "other" },
		"intent fence":      func(c *SSMCommand) { c.Targets[0].Intent.LeaseFence = "other" },
		"request digest":    func(c *SSMCommand) { c.Targets[0].Intent.Request = strings.Repeat("0", 64) },
		"script":            func(c *SSMCommand) { c.Targets[0].Intent.ScriptJSON = `["rm -rf /"]` },
		"timeout":           func(c *SSMCommand) { c.Targets[0].Intent.TimeoutSeconds = 31 },
		"intent time":       func(c *SSMCommand) { c.Targets[0].Intent.RequestedAt++ },
		"observation state": func(c *SSMCommand) { c.Targets[0].Observation.State = "Bogus" },
	} {
		c := clone()
		edit(&c)
		if ssmRecordValid(c) {
			t.Errorf("%s tampering accepted", name)
		}
	}
	// The same checks guard durable reads.
	if _, err = db.Exec(`UPDATE aws_ssm_commands SET record=replace(record,'operation-1','operation-2') WHERE id=?`, r.CommandId); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), a, r.CommandId); !errors.Is(err, ErrSSMCommandInvalid) {
		t.Fatal("tampered stored command read", err)
	}
	if _, err = db.Exec(`UPDATE aws_ssm_commands SET record=replace(record,'operation-2','operation-1') WHERE id=?`, r.CommandId); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), a, r.CommandId); err != nil {
		t.Fatal("restored command unreadable", err)
	}
	if _, err = db.Exec(`DELETE FROM aws_ssm_command_tokens WHERE token=?`, r.Targets[0].Intent.Token); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(context.Background(), a, r.CommandId); err == nil {
		t.Fatal("command read without its token binding")
	}
	foreign := a
	foreign.Principal = "intruder"
	if _, err = s.Get(context.Background(), foreign, r.CommandId); err == nil {
		t.Fatal("foreign authority read the command")
	}
}
