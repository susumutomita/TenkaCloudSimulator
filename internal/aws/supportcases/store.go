// Package supportcases stores participant-authored AWS Support cases in the
// local team's database. It has no transport to an external support service.
package supportcases

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	MaxFileBytes       = 64 << 10
	MaxFilesPerSet     = 3
	maxStoredFileBytes = 32 << 20
	maxCases           = 256
	maxSets            = 128
	maxCommunications  = 4096
)

// Fault identifies a public AWS JSON error. Database error details stay local.
type Fault struct {
	Code    string
	Message string
}

func (e Fault) Error() string      { return e.Message }
func invalid(message string) error { return Fault{"ValidationException", message} }

type Store struct {
	db       *sql.DB
	now      func() time.Time
	tokenKey [32]byte
}

// New uses the same connection, transaction locking and foreign keys as the
// rest of this team's simulator. It never opens a separate database.
func New(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("AWS Support requires the team database")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(schema)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s := &Store{db: db, now: time.Now}
	rand.Read(s.tokenKey[:])
	return s, nil
}

const schema = `
CREATE TABLE IF NOT EXISTS aws_support_cases (
  display_id INTEGER PRIMARY KEY AUTOINCREMENT,
  case_id TEXT NOT NULL UNIQUE, subject TEXT NOT NULL, created_us INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('opened','resolved')),
  language TEXT NOT NULL, issue_type TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS aws_support_communications (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  case_id TEXT NOT NULL REFERENCES aws_support_cases(case_id) ON DELETE CASCADE,
  created_us INTEGER NOT NULL, author TEXT NOT NULL, body TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS aws_support_case_communications
  ON aws_support_communications(case_id, sequence DESC);
CREATE TABLE IF NOT EXISTS aws_support_attachment_sets (
  set_id TEXT PRIMARY KEY, expires_us INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS aws_support_staged_files (
  set_id TEXT NOT NULL REFERENCES aws_support_attachment_sets(set_id) ON DELETE CASCADE,
  position INTEGER NOT NULL, name TEXT NOT NULL, data BLOB NOT NULL,
  PRIMARY KEY(set_id, position)
);
CREATE TABLE IF NOT EXISTS aws_support_attachments (
  attachment_id TEXT PRIMARY KEY, sequence INTEGER NOT NULL
    REFERENCES aws_support_communications(sequence) ON DELETE CASCADE,
  position INTEGER NOT NULL, name TEXT NOT NULL, data BLOB NOT NULL,
  UNIQUE(sequence, position)
);`

type Attachment struct {
	Name string `json:"fileName"`
	Data []byte `json:"data"`
}

type Input struct {
	Subject               string       `json:"subject"`
	Body                  string       `json:"communicationBody"`
	CaseID                string       `json:"caseId"`
	CaseIDs               []string     `json:"caseIdList"`
	DisplayID             string       `json:"displayId"`
	SetID                 string       `json:"attachmentSetId"`
	AttachmentID          string       `json:"attachmentId"`
	Attachments           []Attachment `json:"attachments"`
	Language              string       `json:"language"`
	IssueType             string       `json:"issueType"`
	IncludeResolved       bool         `json:"includeResolvedCases"`
	IncludeCommunications *bool        `json:"includeCommunications"`
	AfterTime             string       `json:"afterTime"`
	BeforeTime            string       `json:"beforeTime"`
	MaxResults            int          `json:"maxResults"`
	NextToken             string       `json:"nextToken"`
}

func timestamp(us int64) string { return time.UnixMicro(us).UTC().Format(time.RFC3339Nano) }

// execOne catches ignored writes before a successful response is constructed.
func execOne(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	before, err := integer(ctx, tx, "SELECT total_changes()")
	if err != nil {
		return nil, err
	}
	r, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, fmt.Errorf("AWS Support expected one changed row, got %d", n)
	}
	after, err := integer(ctx, tx, "SELECT total_changes()")
	if err != nil {
		return nil, err
	}
	if after-before != 1 {
		return nil, errors.New("AWS Support write changed additional rows")
	}
	return r, nil
}

func integer(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&n)
	return n, err
}

func caseStatus(ctx context.Context, tx *sql.Tx, id string, oldest int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM aws_support_cases
		WHERE case_id=? AND created_us>=?`, id, oldest).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", Fault{"CaseIdNotFound", "The requested support case does not exist."}
	}
	return status, err
}

// Case data is retained for twelve calendar months. Expiring a temporary
// attachment set must never delete a participant's submitted communication.
func prune(ctx context.Context, tx *sql.Tx, now time.Time) error {
	for _, step := range []struct {
		sql  string
		when int64
	}{
		{`DELETE FROM aws_support_cases WHERE created_us < ?`, now.AddDate(-1, 0, 0).UnixMicro()},
		{`DELETE FROM aws_support_staged_files WHERE set_id IN
			(SELECT set_id FROM aws_support_attachment_sets WHERE expires_us <= ?)`, now.UnixMicro()},
		{`DELETE FROM aws_support_attachment_sets WHERE expires_us < ?`, now.Add(-24 * time.Hour).UnixMicro()},
	} {
		if _, err := tx.ExecContext(ctx, step.sql, step.when); err != nil {
			return err
		}
	}
	return nil
}
