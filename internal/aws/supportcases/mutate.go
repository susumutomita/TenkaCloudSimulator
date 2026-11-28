package supportcases

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"
)

// Run commits at most one operation and only returns the corresponding AWS
// response after commit. The gateway supplies the authenticated local author.
func (s *Store) Run(ctx context.Context, operation, author string, in Input) (map[string]any, error) {
	if author == "" {
		return nil, Fault{"AccessDeniedException", "A local identity is required."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	var result map[string]any
	switch operation {
	case "DescribeCases", "DescribeCommunications", "DescribeAttachment":
		result, err = s.read(ctx, tx, now, operation, author, in)
	case "CreateCase", "AddCommunicationToCase", "ResolveCase", "AddAttachmentsToSet":
		if err = prune(ctx, tx, now); err == nil {
			result, err = mutate(ctx, tx, now, operation, author, in)
		}
	default:
		return nil, Fault{"UnknownOperationException", "This AWS Support operation is unsupported."}
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func mutate(ctx context.Context, tx *sql.Tx, now time.Time, op, author string, in Input) (map[string]any, error) {
	switch op {
	case "AddAttachmentsToSet":
		return stageFiles(ctx, tx, now, in)
	case "CreateCase":
		if in.Subject == "" || utf8.RuneCountInString(in.Subject) > 256 {
			return nil, invalid("A subject of 1 to 256 characters is required.")
		}
		if in.ServiceCode != "" && in.ServiceCode != amsIncidentCode {
			return nil, invalid("The only supported local serviceCode is sentinel-report-incident.")
		}
		if in.Language == "" {
			in.Language = "en"
		}
		if in.Language != "en" && in.Language != "ja" {
			return nil, invalid("Supported local case languages are en and ja.")
		}
		if in.IssueType == "" {
			in.IssueType = "technical"
		}
		if in.IssueType != "technical" && in.IssueType != "customer-service" {
			return nil, invalid("Unsupported case issueType.")
		}
		count, err := integer(ctx, tx, "SELECT COUNT(*) FROM aws_support_cases")
		if err != nil {
			return nil, err
		}
		if count >= maxCases {
			return nil, Fault{"CaseCreationLimitExceeded", "The local case limit has been reached."}
		}
		in.CaseID = fmt.Sprintf("case-%d-%d-%s", now.Unix(), now.Year(), rand.Text())
		if _, err := execOne(ctx, tx, `INSERT INTO aws_support_cases
			(case_id, subject, created_us, status, language, issue_type)
			VALUES(?,?,?,'opened',?,?)`, in.CaseID, in.Subject, now.UnixMicro(), in.Language, in.IssueType); err != nil {
			return nil, err
		}
		if err := addCommunication(ctx, tx, now, author, in); err != nil {
			return nil, err
		}
		if in.ServiceCode != "" {
			if _, err := execOne(ctx, tx, "INSERT INTO aws_support_case_services(case_id,service_code) VALUES(?,?)", in.CaseID, in.ServiceCode); err != nil {
				return nil, err
			}
		}
		return map[string]any{"caseId": in.CaseID}, nil
	}
	if in.CaseID == "" {
		return nil, invalid("An explicit caseId is required.")
	}
	status, err := caseStatus(ctx, tx, in.CaseID, now.AddDate(-1, 0, 0).UnixMicro())
	if err != nil {
		return nil, err
	}
	if op == "ResolveCase" {
		if status != "resolved" {
			if _, err = execOne(ctx, tx, "UPDATE aws_support_cases SET status='resolved' WHERE case_id=? AND status=?", in.CaseID, status); err != nil {
				return nil, err
			}
			current, err := caseStatus(ctx, tx, in.CaseID, 0)
			if err != nil {
				return nil, err
			}
			if current != "resolved" {
				return nil, errors.New("AWS Support case resolution was not stored")
			}
		}
		return map[string]any{"initialCaseStatus": status, "finalCaseStatus": "resolved"}, nil
	}
	if status == "resolved" {
		return nil, invalid("Adding a communication to a resolved case is unsupported.")
	}
	if err := addCommunication(ctx, tx, now, author, in); err != nil {
		return nil, err
	}
	return map[string]any{"result": true}, nil
}

func setExpiry(ctx context.Context, tx *sql.Tx, id string, now time.Time) (int64, error) {
	expiry, err := integer(ctx, tx, "SELECT expires_us FROM aws_support_attachment_sets WHERE set_id=?", id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Fault{"AttachmentSetIdNotFound", "The requested attachment set does not exist."}
	}
	if err != nil {
		return 0, err
	}
	if expiry <= now.UnixMicro() {
		return 0, Fault{"AttachmentSetExpired", "The attachment set has expired."}
	}
	return expiry, nil
}

func reserveFileBytes(ctx context.Context, tx *sql.Tx, extra int64) error {
	total, err := integer(ctx, tx, `SELECT
		(SELECT COALESCE(SUM(length(data)),0) FROM aws_support_staged_files) +
		(SELECT COALESCE(SUM(length(data)),0) FROM aws_support_attachments)`)
	if err != nil {
		return err
	}
	if total+extra > maxStoredFileBytes {
		return invalid("The local attachment storage limit has been reached.")
	}
	return nil
}

func stageFiles(ctx context.Context, tx *sql.Tx, now time.Time, in Input) (map[string]any, error) {
	if len(in.Attachments) == 0 {
		return nil, invalid("Provide at least one attachment.")
	}
	var extra int64
	for _, f := range in.Attachments {
		if f.Name == "" || utf8.RuneCountInString(f.Name) > 255 || f.Data == nil {
			return nil, invalid("An attachment requires a fileName and data.")
		}
		if len(f.Data) > MaxFileBytes {
			return nil, Fault{"AttachmentSetSizeLimitExceeded", "The local file size limit is 64 KiB."}
		}
		extra += int64(len(f.Data))
	}
	var expiry int64
	if in.SetID == "" {
		n, err := integer(ctx, tx, "SELECT COUNT(*) FROM aws_support_attachment_sets WHERE expires_us>?", now.UnixMicro())
		if err != nil {
			return nil, err
		}
		if n >= maxSets {
			return nil, Fault{"AttachmentLimitExceeded", "The local attachment set limit has been reached."}
		}
		in.SetID = "set-" + rand.Text()
		expiry = now.Add(time.Hour).UnixMicro()
		if _, err = execOne(ctx, tx, "INSERT INTO aws_support_attachment_sets VALUES(?,?)", in.SetID, expiry); err != nil {
			return nil, err
		}
	} else {
		var err error
		expiry, err = setExpiry(ctx, tx, in.SetID, now)
		if err != nil {
			return nil, err
		}
	}
	count, err := integer(ctx, tx, "SELECT COUNT(*) FROM aws_support_staged_files WHERE set_id=?", in.SetID)
	if err != nil {
		return nil, err
	}
	if count+int64(len(in.Attachments)) > MaxFilesPerSet {
		return nil, Fault{"AttachmentSetSizeLimitExceeded", "An attachment set permits three files."}
	}
	if err = reserveFileBytes(ctx, tx, extra); err != nil {
		return nil, err
	}
	for i, f := range in.Attachments {
		if _, err = execOne(ctx, tx, "INSERT INTO aws_support_staged_files VALUES(?,?,?,?)", in.SetID, count+int64(i), f.Name, f.Data); err != nil {
			return nil, err
		}
	}
	return map[string]any{"attachmentSetId": in.SetID, "expiryTime": timestamp(expiry)}, nil
}

func addCommunication(ctx context.Context, tx *sql.Tx, now time.Time, author string, in Input) error {
	if in.Body == "" || utf8.RuneCountInString(in.Body) > 8000 {
		return invalid("A communicationBody of 1 to 8000 characters is required.")
	}
	count, err := integer(ctx, tx, "SELECT COUNT(*) FROM aws_support_communications")
	if err != nil {
		return err
	}
	if count >= maxCommunications {
		return invalid("The local case communication limit has been reached.")
	}
	r, err := execOne(ctx, tx, `INSERT INTO aws_support_communications(case_id,created_us,author,body)
		VALUES(?,?,?,?)`, in.CaseID, now.UnixMicro(), author, in.Body)
	if err != nil {
		return err
	}
	seq, err := r.LastInsertId()
	if err != nil || in.SetID == "" {
		return err
	}
	if _, err := setExpiry(ctx, tx, in.SetID, now); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT name, data FROM aws_support_staged_files WHERE set_id=? ORDER BY position", in.SetID)
	if err != nil {
		return err
	}
	files := []Attachment{}
	var total int64
	for rows.Next() {
		var f Attachment
		if err = rows.Scan(&f.Name, &f.Data); err != nil {
			rows.Close()
			return err
		}
		total += int64(len(f.Data))
		files = append(files, f)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(files) == 0 || len(files) > MaxFilesPerSet {
		return errors.New("invalid stored AWS Support attachment set")
	}
	if err = reserveFileBytes(ctx, tx, total); err != nil {
		return err
	}
	for i, f := range files {
		id := "attachment-" + rand.Text()
		if _, err = execOne(ctx, tx, "INSERT INTO aws_support_attachments VALUES(?,?,?,?,?)", id, seq, i, f.Name, f.Data); err != nil {
			return err
		}
		var name string
		var data []byte
		if err = tx.QueryRowContext(ctx, "SELECT name,data FROM aws_support_attachments WHERE attachment_id=? AND sequence=? AND position=?", id, seq, i).Scan(&name, &data); err != nil {
			return err
		}
		if name != f.Name || !bytes.Equal(data, f.Data) {
			return errors.New("AWS Support attachment was changed during submission")
		}
	}
	return nil
}
