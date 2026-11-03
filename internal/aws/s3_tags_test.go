package aws

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestS3TagStorageFailureIsAtomicAndPrivate(t *testing.T) {
	db, s := s3TestState(t)
	initial := []S3Tag{{"Name", "original"}}
	if err := s.SetTags("versions-test", initial); err != nil {
		t.Fatal(err)
	}
	s3SQL(t, db, `CREATE TRIGGER reject_tags BEFORE INSERT ON aws_s3_tags BEGIN SELECT RAISE(ABORT,'private write error'); END;`)
	if err := s.SetTags("versions-test", []S3Tag{{"Name", "replacement"}}); err == nil {
		t.Fatal("rejected tag replacement succeeded")
	}
	tags, err := s.Tags("versions-test")
	if err != nil || !reflect.DeepEqual(tags, initial) {
		t.Fatal(tags, err)
	}
	g := &Gateway{Store: db, S3: s}
	s3Wire(t, g, "DELETE", "/versions-test?tagging", "", 204)
	s3SQL(t, db, "DROP TRIGGER reject_tags")
	if err = s.SetTags("versions-test", initial); err != nil {
		t.Fatal(err)
	}
	s3SQL(t, db, `CREATE TRIGGER reject_tag_delete BEFORE DELETE ON aws_s3_tags BEGIN SELECT RAISE(ABORT,'private delete error'); END;`)
	s3Wire(t, g, "DELETE", "/versions-test?tagging", "", 500)
	if err = s.DeleteBucket("versions-test"); err == nil {
		t.Fatal("cascade deletion failure accepted")
	}
	tags, err = s.Tags("versions-test")
	if err != nil || !reflect.DeepEqual(tags, initial) {
		t.Fatal(tags, err)
	}
	s3SQL(t, db, "DROP TRIGGER reject_tag_delete;UPDATE aws_s3_tags SET document='bad JSON'")
	s3Wire(t, g, "GET", "/versions-test?tagging", "", 500)
	if err = s.SetTags("versions-test", initial); err != nil {
		t.Fatal(err)
	}
	if err = s.SetTags("absent", initial); core.Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
	if err = s.DeleteBucket("versions-test"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_s3_tags").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if err = db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s3Wire(t, g, "GET", "/versions-test?tagging", "", 500)
	if err = s.SetTags("versions-test", initial); err == nil {
		t.Fatal("closed database accepted tags")
	}
}
func TestS3TagsPersistWithOriginalWhitespaceAndUnicode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tags.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewS3Store(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create("persist-tags", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	tags := []S3Tag{{"日本語", " 東京 "}, {"empty", ""}}
	if err = s.SetTags("persist-tags", tags); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err = NewS3Store(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Tags("persist-tags")
	sort.Slice(tags, func(i, j int) bool { return tags[i].Key < tags[j].Key })
	if err != nil || !reflect.DeepEqual(got, tags) {
		t.Fatal(got, err)
	}
	for _, bad := range [][]S3Tag{{{"AWS:reserved", "x"}}, {{"invalid\nkey", "x"}}, {{string([]byte{255}), "x"}}} {
		if err = s.SetTags("persist-tags", bad); core.Code(err) != "InvalidTag" {
			t.Fatal("invalid tag accepted", bad, err)
		}
	}
	boundary := []S3Tag{{strings.Repeat("界", 128), strings.Repeat("界", 256)}}
	if err = s.SetTags("persist-tags", boundary); err != nil {
		t.Fatal(err)
	}
}
func TestStackPermissionPreflightScopesAllSupportedResources(t *testing.T) {
	record := StackRecord{Region: "eu-west-1", Resources: []StackResource{
		{Type: "AWS::SSM::Parameter", PhysicalID: "/config/value"},
		{Type: "AWS::IAM::Role", PhysicalID: "executor", Properties: map[string]any{"ManagedPolicyArns": []any{"metadata-only"}, "Tags": []any{map[string]any{"Key": "k", "Value": "v"}}}},
		{Type: "AWS::S3::Bucket", PhysicalID: "evidence-bucket", Properties: map[string]any{"VersioningConfiguration": true, "PublicAccessBlockConfiguration": true, "Tags": true}},
	}}
	for _, deleting := range []bool{false, true} {
		calls := map[string]string{}
		if err := stackResourcePermissions(record, deleting, func(action, arn string) error { calls[action] = arn; return nil }); err != nil {
			t.Fatal(err)
		}
		if deleting {
			if calls["ssm:DeleteParameter"] != "arn:aws:ssm:eu-west-1:000000000000:parameter/config/value" || calls["s3:DeleteBucket"] != "arn:aws:s3:::evidence-bucket" || calls["iam:DetachRolePolicy"] != "arn:aws:iam::000000000000:role/executor" {
				t.Fatal(calls)
			}
		} else {
			for _, action := range []string{"s3:CreateBucket", "s3:PutBucketVersioning", "s3:PutBucketPublicAccessBlock", "s3:PutBucketTagging"} {
				if calls[action] != "arn:aws:s3:::evidence-bucket" {
					t.Fatal(calls)
				}
			}
			if calls["iam:TagRole"] == "" || calls["iam:AttachRolePolicy"] == "" {
				t.Fatal(calls)
			}
		}
		denied := errors.New("action denied")
		if err := stackResourcePermissions(record, deleting, func(string, string) error { return denied }); !errors.Is(err, denied) {
			t.Fatal(err)
		}
	}
	if err := stackResourcePermissions(StackRecord{Resources: []StackResource{{Type: "Unsupported"}}}, false, func(string, string) error { return nil }); err == nil {
		t.Fatal("unknown resource bypassed authorization")
	}
}
