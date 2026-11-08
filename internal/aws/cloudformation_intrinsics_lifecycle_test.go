package aws

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Real native state, not EC2 stand-ins: the intrinsic outputs are stored only in
// supported SSM parameters, using the unchanged admission/ownership/event path.
const intrinsicLifecycleTemplate = `Parameters:
  Position: {Type: Number, Default: 0}
Conditions:
  Off: !Equals [a, b]
Resources:
  AEncoded:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /intrinsic/encoded
      Type: String
      Value:
        Fn::Base64: !Sub |
          雪 ${ZSource.Value}
          literal ${!AEncoded.Value} ${!aws:userid}
  Selected:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /intrinsic/selected
      Type: String
      Value: !Select [!Ref Position, [!GetAtt ZSource.Value, unused]]
  Zone:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /intrinsic/zone
      Type: String
      Value: !Select [0, !GetAZs {Ref: 'AWS::Region'}]
  ZSource:
    Type: AWS::SSM::Parameter
    Properties: {Name: /intrinsic/source, Type: String, Value: source}
Outputs:
  Encoded: {Value: !GetAtt AEncoded.Value}
  Escaped: {Value: !Sub '${!Missing.Arn}-${!aws:userid}'}
`

func TestStackIntrinsicNativeLifecycleAndRefusalFrame(t *testing.T) {
	db, s3, stacks, parameters := s3StackState(t)
	peer, _, _, _ := s3StackState(t)
	g := &Gateway{Store: db, S3: s3, Stacks: stacks, Parameters: parameters, Identities: core.NewIdentities(db)}
	if _, err := parameters.Put("ap-northeast-1", "/intrinsic/neighbor", "String", "unchanged", nil, false); err != nil {
		t.Fatal(err)
	}
	before, peerBefore := stackConditionalDBFrame(t, db.Database()), stackConditionalDBFrame(t, peer.Database())
	for _, body := range []string{
		battleIntrinsicTemplate(t),
		strings.Replace(intrinsicLifecycleTemplate, "!Select [0, !GetAZs {Ref: 'AWS::Region'}]", `!Select [1, !GetAZs ""]`, 1),
		strings.Replace(intrinsicLifecycleTemplate, "!Select [0, !GetAZs {Ref: 'AWS::Region'}]", `!Select [0, !GetAZs unknown-region]`, 1),
		strings.Replace(intrinsicLifecycleTemplate, "!Select [0, !GetAZs {Ref: 'AWS::Region'}]", `!Select [0, [good, null]]`, 1),
		strings.Replace(intrinsicLifecycleTemplate, "${!AEncoded.Value}", "${AEncoded.Value}", 1),
		strings.Replace(intrinsicLifecycleTemplate, "${!AEncoded.Value}", "${Missing}", 1),
		strings.Replace(intrinsicLifecycleTemplate, "[!GetAtt ZSource.Value, unused]", "[!GetAtt ZSource.Value, !Ref Missing]", 1),
		strings.Replace(intrinsicLifecycleTemplate, "Fn::Base64: !Sub |", "Fn::Base64: !Sub |", 1) + "  Malformed: {Value: !If [Off, !Select [0, null], safe]}\n",
	} {
		w := conditionBoundarySend(g, "CreateStack", body, "", nil)
		conditionBoundaryError(t, w, 400, "ValidationError", "")
		if stackConditionalDBFrame(t, db.Database()) != before || stackConditionalDBFrame(t, peer.Database()) != peerBefore {
			t.Fatal("intrinsic refusal mutated state/peer/history")
		}
	}
	if err := core.NewIdentities(db).Set("intrinsic-reader", core.Policy{Version: "2012-10-17", Statement: []core.Statement{{Effect: "Allow", Action: []string{"cloudformation:CreateStack"}, Resource: []string{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	// The new compiler path must not bypass underlying SSM action preflight.
	authorizedBefore := stackConditionalDBFrame(t, db.Database())
	conditionBoundaryError(t, conditionBoundarySend(g, "CreateStack", intrinsicLifecycleTemplate, "intrinsic-reader", nil), 403, "AccessDenied", "")
	if stackConditionalDBFrame(t, db.Database()) != authorizedBefore {
		t.Fatal("denied native action changed state")
	}
	before = authorizedBefore
	w := conditionBoundarySend(g, "CreateStack", intrinsicLifecycleTemplate, "", nil)
	if w.Code != 200 {
		t.Fatal("native intrinsic create", w.Code, w.Body.String())
	}
	id := conditionBoundaryGeneration(t, stacks)
	wantDecoded := "雪 source\nliteral ${AEncoded.Value} ${aws:userid}\n"
	for name, want := range map[string]string{"/intrinsic/encoded": base64.StdEncoding.EncodeToString([]byte(wantDecoded)), "/intrinsic/zone": "ap-northeast-1a", "/intrinsic/source": "source", "/intrinsic/selected": "source"} {
		actual, err := parameters.Get("ap-northeast-1", name)
		if err != nil || actual.Value != want {
			t.Fatal("native intrinsic value", name, err)
		}
	}
	records, err := stacks.Records("ap-northeast-1", id)
	if err != nil || len(records) != 1 || records[0].Outputs["Escaped"] != "${Missing.Arn}-${aws:userid}" {
		t.Fatal("escaped output not literal", err)
	}
	// An ignored physical DELETE must still roll back all resources/history.
	if _, err = db.Database().Exec(`CREATE TRIGGER intrinsic_ignore_delete BEFORE DELETE ON aws_ssm_parameters WHEN OLD.name='/intrinsic/encoded' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	failedDeleteBefore := stackConditionalDBFrame(t, db.Database())
	if w = conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code == 200 {
		t.Fatal("unacknowledged physical deletion accepted")
	}
	if stackConditionalDBFrame(t, db.Database()) != failedDeleteBefore {
		t.Fatal("failed deletion changed native frame")
	}
	if _, err = db.Database().Exec(`DROP TRIGGER intrinsic_ignore_delete`); err != nil {
		t.Fatal(err)
	}

	if w = conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code != 200 {
		t.Fatal("cleanup failed", w.Code, w.Body.String())
	}
	if intrinsicCompletedFrame(t, stacks, id) != before || stackConditionalDBFrame(t, peer.Database()) != peerBefore {
		t.Fatal("cleanup failed to restore frame or modified peer")
	}
	// Reuse the name only after real cleanup; a supplied numeric index resolves
	// through Number/Ref rather than float conversion or implicit truncation.
	params := url.Values{"Parameters.member.1.ParameterKey": {"Position"}, "Parameters.member.1.ParameterValue": {"1"}}
	if w = conditionBoundarySend(g, "CreateStack", intrinsicLifecycleTemplate, "", params); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	item, err := parameters.Get("ap-northeast-1", "/intrinsic/selected")
	if err != nil || item.Value != "unused" {
		t.Fatal("numeric parameter selection", err)
	}
	if w = conditionBoundarySend(g, "DeleteStack", "", "", nil); w.Code != 200 {
		t.Fatal("second cleanup", w.Code)
	}
}

func intrinsicCompletedFrame(t *testing.T, stacks *StackStore, id string) string {
	t.Helper()
	events, next, err := stacks.Events("ap-northeast-1", id, "", nil)
	if err != nil || next != "" || len(events) != 20 {
		t.Fatalf("completed intrinsic history: count=%d next=%q error=%v", len(events), next, err)
	}
	expected := map[string]string{"boundary": id, "AEncoded": "/intrinsic/encoded", "Selected": "/intrinsic/selected", "Zone": "/intrinsic/zone", "ZSource": "/intrinsic/source"}
	seenIDs, transitions := map[string]bool{}, map[string]int{}
	for _, event := range events {
		if event.StackID != id || event.StackName != "boundary" || expected[event.LogicalResourceID] != event.PhysicalResourceID || event.EventID == "" || seenIDs[event.EventID] {
			t.Fatal("unexpected event identity")
		}
		seenIDs[event.EventID] = true
		transitions[event.LogicalResourceID+":"+event.ResourceStatus]++
	}
	for logical := range expected {
		for _, status := range []string{"CREATE_IN_PROGRESS", "CREATE_COMPLETE", "DELETE_IN_PROGRESS", "DELETE_COMPLETE"} {
			if transitions[logical+":"+status] != 1 {
				t.Fatal("missing/duplicate native transition", logical, status)
			}
		}
	}
	var deleted int
	if err = stacks.db.QueryRow("SELECT deleted FROM aws_cfn_event_history WHERE stack_id=?", id).Scan(&deleted); err != nil || deleted != 1 {
		t.Fatal("missing deleted history", err)
	}
	return stackConditionalFrameExceptHistory(t, stacks.db, id)
}
