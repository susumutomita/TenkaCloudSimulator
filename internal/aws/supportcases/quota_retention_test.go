package supportcases

import (
	"bytes"
	"testing"
	"time"
)

func TestCaseQuotaAndRetentionCleanUpOnlyOlderRecords(t *testing.T) {
	host, s := openTeam(t, "")
	now := time.Date(2025, 3, 1, 12, 0, 0, 123456000, time.UTC)
	s.now = func() time.Time { return now }
	base := now
	setID := call(t, s, "AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "owned.bin", Data: []byte{0, 255}}}})["attachmentSetId"].(string)
	create := func(subject string) string {
		return call(t, s, "CreateCase", Input{Subject: subject, Body: "Initial", SetID: setID})["caseId"].(string)
	}
	fileID := func(id string) string {
		m := call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)[0]
		return m["attachmentSet"].([]map[string]string)[0]["attachmentId"]
	}
	old := create("Oldest")
	oldFile := fileID(old)
	now = now.Add(time.Microsecond)
	peer := create("Peer")
	peerFile := fileID(peer)
	for i := 2; i < maxCases; i++ {
		call(t, s, "CreateCase", Input{Subject: "Other", Body: "Initial"})
	}
	wantFault(t, s, "CreateCase", Input{Subject: "Over quota", Body: "Initial"}, "CaseCreationLimitExceeded")
	now = base.AddDate(1, 0, 0)
	if got := call(t, s, "DescribeCases", Input{CaseIDs: []string{old, peer}})["cases"].([]map[string]any); len(got) != 2 {
		t.Fatal("the inclusive retention boundary excluded a case")
	}
	now = now.Add(time.Microsecond)
	wantFault(t, s, "DescribeCases", Input{CaseIDs: []string{old}}, "CaseIdNotFound")
	wantFault(t, s, "DescribeCommunications", Input{CaseID: old}, "CaseIdNotFound")
	wantFault(t, s, "DescribeAttachment", Input{AttachmentID: oldFile}, "AttachmentIdNotFound")
	var n int
	if err := host.Database().QueryRow("SELECT count(*) FROM aws_support_cases WHERE case_id=?", old).Scan(&n); err != nil || n != 1 {
		t.Fatal("retention reads modified the expired physical row", err)
	}
	// This authorized write reclaims the one expired case and its dependent
	// communication and attachment. All other cases are exactly at the cutoff.
	call(t, s, "CreateCase", Input{Subject: "Recovered slot", Body: "Initial"})
	for _, query := range []struct {
		sql, id string
	}{
		{"SELECT count(*) FROM aws_support_cases WHERE case_id=?", old},
		{"SELECT count(*) FROM aws_support_communications WHERE case_id=?", old},
		{"SELECT count(*) FROM aws_support_attachments WHERE attachment_id=?", oldFile},
	} {
		if err := host.Database().QueryRow(query.sql, query.id).Scan(&n); err != nil || n != 0 {
			t.Fatal("expired rows survived reclamation", err)
		}
	}
	if err := host.Database().QueryRow("SELECT count(*) FROM aws_support_cases").Scan(&n); err != nil || n != maxCases {
		t.Fatal("case quota was not recovered exactly", err)
	}
	retained := call(t, s, "DescribeAttachment", Input{AttachmentID: peerFile})["attachment"].(Attachment)
	if retained.Name != "owned.bin" || !bytes.Equal(retained.Data, []byte{0, 255}) {
		t.Fatal("pruning the oldest case changed the peer's saved file")
	}
}

func TestAttachmentSetQuotaExpiresAtOriginalDeadline(t *testing.T) {
	_, s := openTeam(t, "")
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	file := []Attachment{{Name: "one", Data: []byte{1}}}
	first := call(t, s, "AddAttachmentsToSet", Input{Attachments: file})["attachmentSetId"].(string)
	for i := 1; i < maxSets; i++ {
		call(t, s, "AddAttachmentsToSet", Input{Attachments: file})
	}
	wantFault(t, s, "AddAttachmentsToSet", Input{Attachments: file}, "AttachmentLimitExceeded")
	call(t, s, "AddAttachmentsToSet", Input{SetID: first, Attachments: file})
	now = now.Add(time.Hour)
	call(t, s, "AddAttachmentsToSet", Input{Attachments: file})
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: first, Attachments: file}, "AttachmentSetExpired")
	now = now.Add(24*time.Hour + time.Microsecond)
	call(t, s, "AddAttachmentsToSet", Input{Attachments: file})
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: first, Attachments: file}, "AttachmentSetIdNotFound")
}

func TestCommunicationQuotaRollsBackTheWouldBeCase(t *testing.T) {
	host, s := openTeam(t, "")
	id := call(t, s, "CreateCase", Input{Subject: "Existing", Body: "Initial"})["caseId"].(string)
	// Seed real, case-bound messages at the capacity boundary. The public
	// requests below perform the actual count and late CreateCase rollback.
	_, err := host.Database().Exec(`WITH RECURSIVE n(i) AS (
	    VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?
	) INSERT INTO aws_support_communications(case_id,created_us,author,body)
	  SELECT c.case_id,c.created_us,'local-author','Saved note' FROM n,aws_support_cases c WHERE c.case_id=?`, maxCommunications-1, id)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Subject: "New case", Body: "Unavailable message slot"}
	wantFault(t, s, "CreateCase", input, "ValidationException")
	wantFault(t, s, "AddCommunicationToCase", Input{CaseID: id, Body: input.Body}, "ValidationException")
	var cases, messages int
	if err := host.Database().QueryRow(`SELECT (SELECT COUNT(*) FROM aws_support_cases),
	        (SELECT COUNT(*) FROM aws_support_communications)`).Scan(&cases, &messages); err != nil || cases != 1 || messages != maxCommunications {
		t.Fatal("refused operation left an extra case or a changed communication count", err)
	}
}

func TestAttachmentBudgetCountsStagedAndSubmittedBytes(t *testing.T) {
	host, s := openTeam(t, "")
	binary := bytes.Repeat([]byte{0x26}, MaxFileBytes)
	setID := call(t, s, "AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "original", Data: binary}}})["attachmentSetId"].(string)
	id := call(t, s, "CreateCase", Input{Subject: "Capacity fixture", Body: "Initial", SetID: setID})["caseId"].(string)
	// The first file already occupies both a staged and a submitted copy.
	// Fill the remaining bytes with individually valid files, in groups of at
	// most three per saved communication. SQLite creates zero BLOBs directly.
	remaining := maxStoredFileBytes - 2*MaxFileBytes
	files := (remaining + MaxFileBytes - 1) / MaxFileBytes
	messages := (files + MaxFilesPerSet - 1) / MaxFilesPerSet
	_, err := host.Database().Exec(`WITH RECURSIVE n(i) AS (
	    VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?
	) INSERT INTO aws_support_communications(case_id,created_us,author,body)
	  SELECT case_id,created_us,'local-author','Bounded files' FROM n,aws_support_cases WHERE case_id=?`, messages, id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Database().Exec(`WITH RECURSIVE n(i) AS (
	    VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<?1
	) INSERT INTO aws_support_attachments(attachment_id,sequence,position,name,data)
	  SELECT 'fixture-file-'||i,
	    (SELECT min(sequence) FROM aws_support_communications WHERE case_id=?2)+1+(i-1)/?3,
	    (i-1)%?3,'file-'||i,zeroblob(min(?4,?5-(i-1)*?4)) FROM n`,
		files, id, MaxFilesPerSet, MaxFileBytes, remaining)
	if err != nil {
		t.Fatal(err)
	}
	// At the exact byte limit, a zero-byte file still fits. One extra byte
	// fails both on an existing set and after a newly allocated set row.
	call(t, s, "AddAttachmentsToSet", Input{SetID: setID, Attachments: []Attachment{{Name: "empty", Data: []byte{}}}})
	one := []Attachment{{Name: "one", Data: []byte{1}}}
	wantFault(t, s, "AddAttachmentsToSet", Input{SetID: setID, Attachments: one}, "ValidationException")
	wantFault(t, s, "AddAttachmentsToSet", Input{Attachments: one}, "ValidationException")
	wantFault(t, s, "CreateCase", Input{Subject: "Late refusal", Body: "New", SetID: setID}, "ValidationException")
	var cases, sets, staged, savedMessages, savedFiles, total int
	err = host.Database().QueryRow(`SELECT
	  (SELECT count(*) FROM aws_support_cases), (SELECT count(*) FROM aws_support_attachment_sets),
	  (SELECT count(*) FROM aws_support_staged_files), (SELECT count(*) FROM aws_support_communications),
	  (SELECT count(*) FROM aws_support_attachments),
	  (SELECT sum(length(data)) FROM aws_support_staged_files)+(SELECT sum(length(data)) FROM aws_support_attachments)`).
		Scan(&cases, &sets, &staged, &savedMessages, &savedFiles, &total)
	if err != nil || cases != 1 || sets != 1 || staged != 2 || savedMessages != messages+1 || savedFiles != files+1 || total != maxStoredFileBytes {
		t.Fatalf("unsaved rows or bytes after the quota refusals: %d %d %d %d %d %d, %v", cases, sets, staged, savedMessages, savedFiles, total, err)
	}
}
