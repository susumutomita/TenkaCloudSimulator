package supportcases

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Tokens authorize no reads. They name an exclusive sequence boundary,
// authenticated by this store and bound to the query and local identity.
type page struct {
	Filter [sha256.Size]byte
	Before int64
}

func filterKey(op, principal string, in Input) [sha256.Size]byte {
	in.NextToken = ""
	in.MaxResults = 0
	in.CaseIDs = slices.Sorted(slices.Values(in.CaseIDs))
	b, _ := json.Marshal(in)
	return sha256.Sum256(append([]byte(op+"\x00"+principal+"\x00"), b...))
}

func (s *Store) token(filter [sha256.Size]byte, before int64) string {
	b, _ := json.Marshal(page{filter, before})
	mac := hmac.New(sha256.New, s.tokenKey[:])
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(append(b, mac.Sum(nil)...))
}

func (s *Store) boundary(token string, filter [sha256.Size]byte) (int64, error) {
	if token == "" {
		return 1<<63 - 1, nil
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(token) > 2048 || len(b) <= sha256.Size || strings.ContainsAny(token, "\r\n") {
		return 0, invalid("Invalid pagination token.")
	}
	body, signature := b[:len(b)-sha256.Size], b[len(b)-sha256.Size:]
	mac := hmac.New(sha256.New, s.tokenKey[:])
	mac.Write(body)
	var p page
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(body, &p) != nil || p.Filter != filter || p.Before <= 0 {
		return 0, invalid("Invalid pagination token.")
	}
	return p.Before, nil
}

func (s *Store) read(ctx context.Context, tx *sql.Tx, now time.Time, op, principal string, in Input) (map[string]any, error) {
	oldest := now.AddDate(-1, 0, 0).UnixMicro()
	if op == "DescribeAttachment" {
		var f Attachment
		err := tx.QueryRowContext(ctx, `SELECT a.name,a.data FROM aws_support_attachments a
			JOIN aws_support_communications m ON m.sequence=a.sequence
			JOIN aws_support_cases c ON c.case_id=m.case_id
			WHERE a.attachment_id=? AND c.created_us>=?`, in.AttachmentID, oldest).Scan(&f.Name, &f.Data)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Fault{"AttachmentIdNotFound", "The requested attachment does not exist."}
		}
		return map[string]any{"attachment": f}, err
	}
	limit := in.MaxResults
	if limit == 0 {
		limit = 100
	}
	if limit < 10 || limit > 100 {
		return nil, invalid("maxResults must be from 10 through 100.")
	}
	filter := filterKey(op, principal, in)
	before, err := s.boundary(in.NextToken, filter)
	if err != nil {
		return nil, err
	}
	if op == "DescribeCommunications" {
		if _, err := caseStatus(ctx, tx, in.CaseID, oldest); err != nil {
			return nil, err
		}
		return s.communications(ctx, tx, in, limit, before, filter)
	}
	return s.cases(ctx, tx, principal, in, limit, before, oldest, filter)
}

type caseRow struct {
	display, created              int64
	id, subject, status, language string
}

func (s *Store) cases(ctx context.Context, tx *sql.Tx, principal string, in Input, limit int, before, oldest int64, filter [sha256.Size]byte) (map[string]any, error) {
	if len(in.CaseIDs) > 100 {
		return nil, invalid("Provide at most 100 case IDs.")
	}
	query := "SELECT display_id,case_id,subject,status,language,created_us FROM aws_support_cases WHERE display_id<? AND created_us>=?"
	args := []any{before, oldest}
	if !in.IncludeResolved {
		query += " AND status<>'resolved'"
	}
	if in.DisplayID != "" {
		id, err := strconv.ParseInt(in.DisplayID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != in.DisplayID {
			return nil, invalid("displayId must be a positive decimal identifier.")
		}
		query += " AND display_id=?"
		args = append(args, id)
	}
	if len(in.CaseIDs) > 0 {
		query += " AND case_id IN (" + strings.Repeat("?,", len(in.CaseIDs)-1) + "?)"
		for _, id := range in.CaseIDs {
			if _, err := caseStatus(ctx, tx, id, oldest); err != nil {
				return nil, err
			}
			args = append(args, id)
		}
	}
	query += " ORDER BY display_id DESC LIMIT ?"
	rows, err := tx.QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	cases := []caseRow{}
	for rows.Next() {
		var c caseRow
		if err := rows.Scan(&c.display, &c.id, &c.subject, &c.status, &c.language, &c.created); err != nil {
			rows.Close()
			return nil, err
		}
		cases = append(cases, c)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]any{}
	if len(cases) > limit {
		result["nextToken"] = s.token(filter, cases[limit-1].display)
		cases = cases[:limit]
	}
	items := make([]map[string]any, 0, len(cases))
	for _, c := range cases {
		item := map[string]any{"caseId": c.id, "displayId": strconv.FormatInt(c.display, 10), "subject": c.subject, "status": c.status, "language": c.language, "timeCreated": timestamp(c.created)}
		if in.IncludeCommunications == nil || *in.IncludeCommunications {
			communicationInput := Input{CaseID: c.id}
			recent, err := s.communications(ctx, tx, communicationInput, 5, 1<<63-1, filterKey("DescribeCommunications", principal, communicationInput))
			if err != nil {
				return nil, err
			}
			item["recentCommunications"] = recent
		}
		items = append(items, item)
	}
	result["cases"] = items
	return result, nil
}

type messageRow struct {
	sequence, created int64
	body, author      string
}

func (s *Store) communications(ctx context.Context, tx *sql.Tx, in Input, limit int, before int64, filter [sha256.Size]byte) (map[string]any, error) {
	query := "SELECT sequence,created_us,body,author FROM aws_support_communications WHERE case_id=? AND sequence<?"
	args := []any{in.CaseID, before}
	var afterTime, beforeTime time.Time
	for _, bound := range []struct {
		value, predicate string
		save             *time.Time
	}{{in.AfterTime, " AND created_us>=?", &afterTime}, {in.BeforeTime, " AND created_us<=?", &beforeTime}} {
		if bound.value == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, bound.value)
		if err != nil {
			return nil, invalid("Timestamps must be in RFC 3339 format.")
		}
		*bound.save = t
		query += bound.predicate
		args = append(args, t.UnixMicro())
	}
	if !afterTime.IsZero() && !beforeTime.IsZero() && afterTime.After(beforeTime) {
		return nil, invalid("afterTime must not be later than beforeTime.")
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	rows, err := tx.QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	var messages []messageRow
	for rows.Next() {
		var row messageRow
		if err := rows.Scan(&row.sequence, &row.created, &row.body, &row.author); err != nil {
			rows.Close()
			return nil, err
		}
		messages = append(messages, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]any{}
	if len(messages) > limit {
		result["nextToken"] = s.token(filter, messages[limit-1].sequence)
		messages = messages[:limit]
	}
	items := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		item := map[string]any{"caseId": in.CaseID, "body": m.body, "timeCreated": timestamp(m.created), "submittedBy": m.author}
		files, err := attachmentDetails(ctx, tx, m.sequence)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			item["attachmentSet"] = files
		}
		items = append(items, item)
	}
	result["communications"] = items
	return result, nil
}

func attachmentDetails(ctx context.Context, tx *sql.Tx, sequence int64) ([]map[string]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT attachment_id,name FROM aws_support_attachments WHERE sequence=? ORDER BY position", sequence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []map[string]string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		files = append(files, map[string]string{"attachmentId": id, "fileName": name})
	}
	return files, rows.Err()
}
