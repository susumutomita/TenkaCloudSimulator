package supportcases

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStagedFileLimitExpiryAndAppendRollback(t *testing.T) {
	db, s := openTeam(t, filepath.Join(t.TempDir(), "owned.sqlite"))
	stamp := time.Date(2026, 11, 28, 8, 0, 0, 123456000, time.UTC)
	s.now = func() time.Time { return stamp }
	max := bytes.Repeat([]byte{0, 0xff}, MaxFileBytes/2)
	set := call(t, s, "AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "max.bin", Data: max}}})
	id := set["attachmentSetId"].(string)
	if set["expiryTime"] != stamp.Add(time.Hour).Format(time.RFC3339Nano) {
		t.Fatal("wrong original deadline")
	}
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: id, Attachments: []Attachment{{Name: "over", Data: append(bytes.Clone(max), 0)}}}, "AttachmentSetSizeLimitExceeded")
	if _, err := db.Database().Exec("CREATE TEMP TRIGGER refuse_last_review BEFORE INSERT ON aws_support_staged_files WHEN NEW.position=2 BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Run(context.Background(), "AddAttachmentsToSet", "local-author", Input{SetID: id, Attachments: []Attachment{{Name: "first new", Data: []byte{4}}, {Name: "later", Data: []byte{5}}}})
	if err == nil {
		t.Fatal("late failed append returned success")
	}
	var n int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_support_staged_files WHERE set_id=?", id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stored staged count %d: %v", n, err)
	}
	if _, err = db.Database().Exec("DROP TRIGGER refuse_last_review"); err != nil {
		t.Fatal(err)
	}
	stamp = stamp.Add(time.Hour - time.Microsecond)
	caseID := call(t, s, "CreateCase", Input{Subject: "own", Body: "initial", SetID: id})["caseId"].(string)
	m := call(t, s, "DescribeCommunications", Input{CaseID: caseID})["communications"].([]map[string]any)[0]
	fid := m["attachmentSet"].([]map[string]string)[0]["attachmentId"]
	stamp = stamp.Add(time.Microsecond)
	wantFault(t, s, "AddCommunicationToCase", Input{CaseID: caseID, Body: "after expiry", SetID: id}, "AttachmentSetExpired")
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: id, Attachments: []Attachment{{Name: "late", Data: []byte{1}}}}, "AttachmentSetExpired")
	file := call(t, s, "DescribeAttachment", Input{AttachmentID: fid})["attachment"].(Attachment)
	if file.Name != "max.bin" || !bytes.Equal(file.Data, max) {
		t.Fatal("saved bytes changed after expiry")
	}
	call(t, s, "ResolveCase", Input{CaseID: caseID})
	if more := call(t, s, "DescribeCommunications", Input{CaseID: caseID})["communications"].([]map[string]any); len(more) != 1 {
		t.Fatal("unexpected history")
	}
}

func TestQueuedCancellationPreservesBothCases(t *testing.T) {
	store, s := openTeam(t, filepath.Join(t.TempDir(), "team.sqlite"))
	first := call(t, s, "CreateCase", Input{Subject: "first", Body: "first"})["caseId"].(string)
	second := call(t, s, "CreateCase", Input{Subject: "peer", Body: "peer"})["caseId"].(string)
	db := store.Database()
	held, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	baseline := db.Stats().WaitCount
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := s.Run(ctx, "AddCommunicationToCase", "local-author", Input{CaseID: first, Body: "cancelled"})
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for db.Stats().WaitCount == baseline && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if db.Stats().WaitCount == baseline {
		t.Fatal("write did not queue on actual connection")
	}
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("unexpected stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop before release")
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first, second} {
		if n := len(call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)); n != 1 {
			t.Fatalf("stored messages: %d", n)
		}
	}
	call(t, s, "AddCommunicationToCase", Input{CaseID: first, Body: "after cancellation"})
}
