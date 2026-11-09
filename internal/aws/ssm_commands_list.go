package aws

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SSMCommandSummary retains every field needed for exact Host recovery. Status
// and outputs are intentionally read separately using Get; List is a snapshot
// of immutable command identities, not a promise about present guest execution.
type SSMCommandSummary struct {
	CommandId   string
	Request     SSMCommandRequest
	RequestedAt int64
}
type ssmCommandPage struct {
	Authority    SSMCommandAuthority
	InvokedAfter int64
	MaxResults   int
	ExpiresAt    int64
	Commands     []SSMCommandSummary
}

// List supports the bounded InvokedAfter/MaxResults/NextToken recovery shape.
// InvokedAfter is parsed as RFC3339Nano (including Host ISO milliseconds).
// Membership and all returned fields are immutable across pages and restart.
// Tokens bind authority, exact filter and page size; expire in ten minutes.
// At most 16 live snapshots, each <=8 MiB, are retained globally. Expired tokens
// fail closed, never silently restart a listing. An exhausted quota is an error.
func (s *SSMCommandStore) List(ctx context.Context, a SSMCommandAuthority, invokedAfter string, maxResults int, nextToken string) ([]SSMCommandSummary, string, error) {
	after, err := time.Parse(time.RFC3339Nano, invokedAfter)
	if err != nil || !ssmAuthorityValid(a) || maxResults < 1 || maxResults > 50 || after.UnixMilli() < 0 {
		return nil, "", ErrSSMCommandInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	page := ssmCommandPage{Authority: a, InvokedAfter: after.UnixMilli(), MaxResults: maxResults, ExpiresAt: now + 600000}
	offset := 0
	token := ""
	if nextToken != "" {
		parts := strings.Split(nextToken, ".")
		if len(parts) != 2 || !guestLeaseToken.MatchString(parts[0]) {
			return nil, "", ErrSSMCommandInvalid
		}
		token = parts[0]
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset >= ssmCommandLimit || strconv.Itoa(offset) != parts[1] {
			return nil, "", ErrSSMCommandInvalid
		}
		var raw string
		err = tx.QueryRowContext(ctx, `SELECT record FROM aws_ssm_command_pages WHERE token=?`, token).Scan(&raw)
		if err != nil {
			return nil, "", err
		}
		if len(raw) > 8388608 || json.Unmarshal([]byte(raw), &page) != nil || ssmEncode(page) != raw || page.Authority != a || page.InvokedAfter != after.UnixMilli() || page.MaxResults != maxResults || page.ExpiresAt <= now || page.ExpiresAt > now+600000 || len(page.Commands) > ssmCommandLimit || offset >= len(page.Commands) || offset%maxResults != 0 {
			return nil, "", ErrSSMCommandInvalid
		}
	} else {
		rows, e := tx.QueryContext(ctx, `SELECT id FROM aws_ssm_commands ORDER BY id`)
		if e != nil {
			return nil, "", e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				_ = rows.Close()
				return nil, "", e
			}
			ids = append(ids, id)
			if len(ids) > ssmCommandLimit {
				_ = rows.Close()
				return nil, "", ErrSSMCommandInvalid
			}
		}
		e = rows.Err()
		_ = rows.Close()
		if e != nil {
			return nil, "", e
		}
		for _, id := range ids {
			// Read authority first to avoid cross-authority disclosure, then use full
			// record validation and token-index fences for records within this scope.
			var raw string
			var r SSMCommand
			if e = tx.QueryRowContext(ctx, `SELECT record FROM aws_ssm_commands WHERE id=?`, id).Scan(&raw); e != nil {
				return nil, "", e
			}
			if len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &r) != nil {
				return nil, "", ErrSSMCommandInvalid
			}
			if r.Authority != a {
				continue
			}
			r, _, e = ssmRead(ctx, tx, id, a)
			if e != nil {
				return nil, "", e
			}
			if r.RequestedAt >= page.InvokedAfter {
				page.Commands = append(page.Commands, SSMCommandSummary{r.CommandId, r.Request, r.RequestedAt})
			}
		}
		sort.Slice(page.Commands, func(i, j int) bool {
			l, r := page.Commands[i], page.Commands[j]
			if l.RequestedAt == r.RequestedAt {
				return l.CommandId < r.CommandId
			}
			return l.RequestedAt < r.RequestedAt
		})
		if len(page.Commands) > maxResults {
			// Pruning only expired ephemeral snapshots cannot remove command obligations.
			if _, err = tx.ExecContext(ctx, `DELETE FROM aws_ssm_command_pages WHERE json_extract(record,'$.ExpiresAt')<=?`, now); err != nil {
				return nil, "", err
			}
			token, err = ssmRandom()
			if err != nil {
				return nil, "", err
			}
			raw := ssmEncode(page)
			if len(raw) > 8388608 {
				return nil, "", ErrSSMCommandInvalid
			}
			res, e := tx.ExecContext(ctx, `INSERT INTO aws_ssm_command_pages(token,record) SELECT ?,? WHERE (SELECT count(*) FROM aws_ssm_command_pages)<16`, token, raw)
			if e != nil {
				return nil, "", e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return nil, "", e
			}
			if n != 1 {
				return nil, "", ErrSSMCommandInvalid
			}
			var got string
			if e = tx.QueryRowContext(ctx, `SELECT record FROM aws_ssm_command_pages WHERE token=?`, token).Scan(&got); e != nil {
				return nil, "", e
			}
			if got != raw {
				return nil, "", ErrSSMCommandInvalid
			}
		}
	}
	// Validate frozen summaries on every page, not just the selected slice.
	for n, c := range page.Commands {
		if !ssmCommandID.MatchString(c.CommandId) || !ssmRequestValid(c.Request) || c.RequestedAt < page.InvokedAfter {
			return nil, "", ErrSSMCommandInvalid
		}
		if n > 0 {
			p := page.Commands[n-1]
			if p.RequestedAt > c.RequestedAt || (p.RequestedAt == c.RequestedAt && p.CommandId >= c.CommandId) {
				return nil, "", ErrSSMCommandInvalid
			}
		}
	}
	end := offset + maxResults
	if end > len(page.Commands) {
		end = len(page.Commands)
	}
	next := ""
	if end < len(page.Commands) {
		next = token + "." + strconv.Itoa(end)
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return page.Commands[offset:end], next, nil
}
