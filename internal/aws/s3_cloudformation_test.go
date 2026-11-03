package aws

import (
	_ "embed"
	"encoding/json"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

//go:embed testdata/s3-evidence.yaml
var s3StackTemplate string

func s3StackState(t *testing.T) (*core.Store, *S3Store, *StackStore, *ParameterStore) {
	t.Helper()
	db, s := s3TestState(t)
	parameters, err := NewParameterStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	stacks, err := NewStackStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return db, s, stacks, parameters
}
func TestS3StackSharesActualStateAndRefusesRetainedHistory(t *testing.T) {
	db, s, stacks, parameters := s3StackState(t)
	record, err := compileStack("ap-northeast-1", "evidence", s3StackTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Outputs["Bucket"] != "cfn-evidence" || record.Outputs["ARN"] != "arn:aws:s3:::cfn-evidence" {
		t.Fatal(record.Outputs)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	b, err := db.Bucket("cfn-evidence")
	if err != nil || b.Region != "ap-northeast-1" {
		t.Fatal(b, err)
	}
	tags, err := s.Tags("cfn-evidence")
	if err != nil || len(tags) != 1 || tags[0].Key != "TenkaCloud:NamePrefix" {
		t.Fatal(tags, err)
	}
	first, err := s.Put("cfn-evidence", "incident.json", "application/json", []byte("evidence"), "", "")
	if err != nil || first.ID == "" || first.ID == "null" {
		t.Fatal(first, err)
	}
	marker, err := s.Delete("cfn-evidence", "incident.json", "")
	if err != nil || !marker.Marker {
		t.Fatal(marker, err)
	}
	if err = stacks.Delete(record.Region, record.ID); err == nil || !strings.Contains(err.Error(), "retains objects") {
		t.Fatal("history deletion unexpectedly succeeded", err)
	}
	if _, err = parameters.Get(record.Region, "/cfn-evidence/config"); err != nil {
		t.Fatal("failed stack deletion removed an earlier resource", err)
	}
	records, err := stacks.Records(record.Region, record.ID)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	var claims int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", record.ID).Scan(&claims); err != nil || claims != 2 {
		t.Fatal("failed delete lost ownership", claims, err)
	}
	// Cleanup is explicit; DeleteStack never erases evidence versions to pass.
	for _, id := range []string{marker.ID, first.ID} {
		if _, err = s.Delete("cfn-evidence", "incident.json", id); err != nil {
			t.Fatal(err)
		}
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Bucket("cfn-evidence"); core.Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
	if err = db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}
func TestS3StackGlobalReservationAndCreateRollback(t *testing.T) {
	db, s, stacks, _ := s3StackState(t)
	record, err := compileStack("us-east-1", "owned", s3StackTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	// A manual deletion does not release the old stack's global reservation.
	if err = s.DeleteBucket("cfn-evidence"); err != nil {
		t.Fatal(err)
	}
	other, err := compileStack("eu-west-1", "other", s3StackTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(other); err == nil {
		t.Fatal("other region stole drifted S3 name")
	}
	if err = stacks.Delete(record.Region, record.ID); err != nil {
		t.Fatal("drifted absent bucket prevented stack cleanup", err)
	}
	if err = stacks.Create(other); err != nil {
		t.Fatal(err)
	}
	// A later resource failure must roll back a newly created bucket/config/tags.
	failBody := strings.ReplaceAll(s3StackTemplate, "AConfig:", "ZConfig:")
	failed, err := compileStack("us-east-1", "failed", failBody, map[string]string{"Prefix": "rollback-evidence"}, "")
	if err != nil {
		t.Fatal(err)
	}
	s3SQL(t, db, `CREATE TRIGGER deny_cfn_parameter BEFORE INSERT ON aws_ssm_parameters BEGIN SELECT RAISE(ABORT,'late failure'); END;`)
	if err = stacks.Create(failed); err == nil {
		t.Fatal("late stack write failure succeeded")
	}
	if _, err = db.Bucket("rollback-evidence"); core.Code(err) != "NoSuchBucket" {
		t.Fatal("partial bucket retained", err)
	}
	if _, err = s.Tags("rollback-evidence"); core.Code(err) != "NoSuchBucket" {
		t.Fatal("partial tags retained", err)
	}
	records, err := stacks.Records(failed.Region, failed.ID)
	if err == nil || len(records) != 0 {
		t.Fatal(records, err)
	}
	if err = db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}
func TestS3GeneratedBucketNamesAreGlobalAndStableInRecord(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	body := `{"Resources":{"EvidenceBucket":{"Type":"AWS::S3::Bucket"}},"Outputs":{"Name":{"Value":{"Ref":"EvidenceBucket"}}}}`
	records := []StackRecord{}
	for _, region := range []string{"us-east-1", "eu-west-1"} {
		r, err := compileStack(region, "same-stack", body, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = stacks.Create(r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	if records[0].Outputs["Name"] == records[1].Outputs["Name"] {
		t.Fatal("generated global bucket collision")
	}
	if err := stacks.Delete(records[0].Region, records[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Bucket(records[1].Outputs["Name"]); err != nil {
		t.Fatal("other region bucket deleted", err)
	}
}
func TestS3StackRejectsUnsupportedProperties(t *testing.T) {
	cases := []map[string]any{
		{"BucketName": "Bad_Name"}, {"BucketName": true}, {"VersioningConfiguration": true}, {"VersioningConfiguration": map[string]any{"Status": "Disabled"}}, {"VersioningConfiguration": map[string]any{"Status": "Enabled", "Extra": true}},
		{"PublicAccessBlockConfiguration": true}, {"PublicAccessBlockConfiguration": map[string]any{"BlockPublicAcls": "true"}}, {"PublicAccessBlockConfiguration": map[string]any{"Unknown": true}},
		{"Tags": true}, {"Tags": []any{true}}, {"Tags": []any{map[string]any{"Key": "missing-value"}}}, {"Tags": []any{map[string]any{"Key": true, "Value": "x"}}}, {"Tags": []any{map[string]any{"Key": "aws:reserved", "Value": "x"}}}, {"BucketEncryption": map[string]any{}},
	}
	for _, props := range cases {
		body, _ := json.Marshal(map[string]any{"Resources": map[string]any{"B": map[string]any{"Type": "AWS::S3::Bucket", "Properties": props}}})
		if _, err := compileStack("us-east-1", "invalid", string(body), nil, ""); err == nil {
			t.Fatal("unsupported S3 properties accepted", string(body))
		}
	}
	if _, err := compileStack("us-east-1", "invalid", `{"Resources":{"B":{"Type":"AWS::S3::Bucket","Properties":[]}}}`, nil, ""); err == nil {
		t.Fatal("nonobject properties accepted")
	}
}
