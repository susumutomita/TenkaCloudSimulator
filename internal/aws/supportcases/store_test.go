package supportcases

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func openTeam(t *testing.T, path string) (*core.Store, *Store) {
	t.Helper()
	team, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = team.Close() })
	s, err := New(team.Database())
	if err != nil {
		t.Fatal(err)
	}
	return team, s
}

func call(t *testing.T, s *Store, op string, in Input) map[string]any {
	t.Helper()
	r, err := s.Run(context.Background(), op, "local-author", in)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return r
}

func wantFault(t *testing.T, s *Store, op string, in Input, code string) {
	t.Helper()
	_, err := s.Run(context.Background(), op, "local-author", in)
	var fault Fault
	if !errors.As(err, &fault) || fault.Code != code {
		t.Fatalf("%s: want %s, got %v", op, code, err)
	}
}

func TestCaseCommunicationsResolveAndReopenDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team.sqlite")
	db, s := openTeam(t, path)
	_, peer := openTeam(t, filepath.Join(t.TempDir(), "peer.sqlite"))
	data := []byte{0, 255, 31}
	set := call(t, s, "AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "note.bin", Data: data}}})["attachmentSetId"].(string)
	id := call(t, s, "CreateCase", Input{Subject: "Synthetic request", Body: "first", SetID: set})["caseId"].(string)
	wantFault(t, peer, "DescribeCases", Input{CaseIDs: []string{id}}, "CaseIdNotFound")
	detail := call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)[0]
	file := detail["attachmentSet"].([]map[string]string)[0]
	if len(file) != 2 || file["fileName"] != "note.bin" || detail["body"] != "first" || detail["submittedBy"] != "local-author" {
		t.Fatal(detail)
	}
	got := call(t, s, "DescribeAttachment", Input{AttachmentID: file["attachmentId"]})["attachment"].(Attachment)
	if got.Name != "note.bin" || !bytes.Equal(got.Data, data) {
		t.Fatal("bytes changed")
	}
	for i := 0; i < 11; i++ {
		call(t, s, "AddCommunicationToCase", Input{CaseID: id, Body: fmt.Sprint(i)})
	}
	c := call(t, s, "DescribeCases", Input{CaseIDs: []string{id}})["cases"].([]map[string]any)[0]
	recent := c["recentCommunications"].(map[string]any)
	m := recent["communications"].([]map[string]any)
	if len(m) != 5 || m[0]["body"] != "10" || m[4]["body"] != "6" {
		t.Fatal(m)
	}
	page := call(t, s, "DescribeCommunications", Input{CaseID: id, NextToken: recent["nextToken"].(string)})["communications"].([]map[string]any)
	if len(page) != 7 || page[0]["body"] != "5" || page[6]["body"] != "first" {
		t.Fatal(page)
	}
	first := call(t, s, "DescribeCommunications", Input{CaseID: id, MaxResults: 10})
	if len(first["communications"].([]map[string]any)) != 10 {
		t.Fatal("wrong first page")
	}
	if len(call(t, s, "DescribeCommunications", Input{CaseID: id, NextToken: first["nextToken"].(string)})["communications"].([]map[string]any)) != 2 {
		t.Fatal("wrong final page")
	}
	omit := false
	summary := call(t, s, "DescribeCases", Input{IncludeCommunications: &omit})["cases"].([]map[string]any)[0]
	if _, ok := summary["recentCommunications"]; ok {
		t.Fatal("communications requested omitted")
	}
	call(t, s, "ResolveCase", Input{CaseID: id})
	if a := call(t, s, "DescribeCases", Input{})["cases"].([]map[string]any); len(a) != 0 {
		t.Fatal(a)
	}
	wantFault(t, s, "AddCommunicationToCase", Input{CaseID: id, Body: "closed"}, "ValidationException")
	oldToken := recent["nextToken"].(string)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, s = openTeam(t, path)
	got = call(t, s, "DescribeAttachment", Input{AttachmentID: file["attachmentId"]})["attachment"].(Attachment)
	if !bytes.Equal(got.Data, data) {
		t.Fatal("restarted data differs")
	}
	r := call(t, s, "DescribeCases", Input{CaseIDs: []string{id}, IncludeResolved: true})["cases"].([]map[string]any)[0]
	if r["status"] != "resolved" {
		t.Fatal(r)
	}
	wantFault(t, s, "DescribeCommunications", Input{CaseID: id, NextToken: oldToken}, "ValidationException")
}

func TestAttachmentExpiryAndAtomicCaseSubmission(t *testing.T) {
	db, s := openTeam(t, "")
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	files := []Attachment{{Name: "one.txt", Data: []byte("one")}}
	set := call(t, s, "AddAttachmentsToSet", Input{Attachments: files})
	setID := set["attachmentSetId"].(string)
	now = now.Add(59 * time.Minute)
	appended := call(t, s, "AddAttachmentsToSet", Input{SetID: setID, Attachments: files})
	if appended["expiryTime"] != set["expiryTime"] {
		t.Fatal("append extended the set")
	}
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: setID, Attachments: append(files, files...)}, "AttachmentSetSizeLimitExceeded")
	input := Input{Subject: "Synthetic request", Body: "first", SetID: setID}
	if _, err := db.Database().Exec("CREATE TEMP TRIGGER refuse_second BEFORE INSERT ON aws_support_attachments WHEN NEW.position=1 BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "CreateCase", "local-author", input); err == nil {
		t.Fatal("acknowledged incomplete case")
	}
	for _, table := range []string{"aws_support_cases", "aws_support_communications", "aws_support_attachments"} {
		var n int
		if err := db.Database().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d, %v", table, n, err)
		}
	}
	if _, err := db.Database().Exec("DROP TRIGGER refuse_second"); err != nil {
		t.Fatal(err)
	}
	id := call(t, s, "CreateCase", input)["caseId"].(string)
	now = now.Add(time.Minute)
	wantFault(t, s, "AddCommunicationToCase", Input{CaseID: id, Body: "expired set", SetID: setID}, "AttachmentSetExpired")
	wantFault(t, s, "CreateCase", input, "AttachmentSetExpired")
	wantFault(t, s, "AddAttachmentsToSet", Input{Attachments: files, SetID: setID}, "AttachmentSetExpired")
	if msgs := call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any); len(msgs) != 1 || len(msgs[0]["attachmentSet"].([]map[string]string)) != 2 {
		t.Fatal(msgs)
	}
}
