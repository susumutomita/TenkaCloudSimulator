package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (a *SSMCommandAPI) retainedAuthority(ctx context.Context, principal, region, id string) (SSMCommandAuthority, error) {
	var raw string
	var record SSMCommand
	if err := a.db.QueryRowContext(ctx, `SELECT record FROM aws_ssm_commands WHERE id=?`, id).Scan(&raw); err != nil {
		return SSMCommandAuthority{}, err
	}
	if len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &record) != nil || record.CommandId != id || record.Authority.Account != "000000000000" || record.Authority.Region != region || record.Authority.Principal != principal {
		return SSMCommandAuthority{}, ErrSSMCommandInvalid
	}
	if _, err := a.store.Get(ctx, record.Authority, id); err != nil {
		return SSMCommandAuthority{}, err
	}
	return record.Authority, nil
}
func (a *SSMCommandAPI) instanceTags(ctx context.Context, lease EC2GuestLease) (map[string]string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, _, err := guestLeaseRead(ctx, tx, lease.Owner)
	if err != nil || current != lease {
		return nil, ErrSSMCommandInvalid
	}
	var raw string
	if err = tx.QueryRowContext(ctx, `SELECT document FROM aws_cfn_stacks WHERE region=? AND id=?`, lease.Owner.Region, lease.Owner.Deployment).Scan(&raw); err != nil {
		return nil, err
	}
	var record StackRecord
	if len(raw) > 2<<20 || json.Unmarshal([]byte(raw), &record) != nil || ssmEncode(record) != raw || record.ID != lease.Owner.Deployment || record.Region != lease.Owner.Region || record.Status != "CREATE_COMPLETE" {
		return nil, ErrSSMCommandInvalid
	}
	var found *StackResource
	for n := range record.Resources {
		r := &record.Resources[n]
		if r.Type == "AWS::EC2::Instance" && r.PhysicalID == lease.Owner.Instance {
			if found != nil {
				return nil, ErrSSMCommandInvalid
			}
			found = r
		}
	}
	if found == nil || stringValue(found.Properties["IamInstanceProfile"]) != lease.Owner.ProfileName {
		return nil, ErrSSMCommandInvalid
	}
	id, err := compileStackInstance(record.ID, found.LogicalID, found.Properties, map[string]string{})
	if err != nil || id != lease.Owner.Instance {
		return nil, ErrSSMCommandInvalid
	}
	var claims int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=? AND stack_id=?`, stackResourceNamespace(record.Region, found.Type), found.Type, found.PhysicalID, record.ID).Scan(&claims); err != nil || claims != 1 {
		return nil, ErrSSMCommandInvalid
	}
	tags, err := networkTags(found.Properties["Tags"])
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return tags, nil
}

type nativeCommandPage struct {
	Principal, Region, InvokedAfter string
	MaxResults                      int
	Expires                         int64
	Commands                        []SSMCommandSummary
}

func initNativeCommandPages(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ssm_native_command_pages(token TEXT PRIMARY KEY,record TEXT NOT NULL CHECK(length(CAST(record AS BLOB))<=8388608))`)
	return err
}
func (a *SSMCommandAPI) list(ctx context.Context, principal, region string, input nativeCommandInput) ([]nativeCommandSummary, string, error) {
	// IAM action is '*' (AWS), while this documented bounded view is additionally
	// restricted to this retained role incarnation across its actual deployments.
	now := time.Now().UnixMilli()
	page := nativeCommandPage{Principal: principal, Region: region, InvokedAfter: input.InvokedAfter, MaxResults: input.MaxResults, Expires: now + 600000}
	offset := 0
	token := ""
	if input.NextToken != "" {
		parts := strings.Split(input.NextToken, ".")
		if len(parts) != 2 || !guestLeaseToken.MatchString(parts[0]) {
			return nil, "", ErrSSMCommandInvalid
		}
		token = parts[0]
		var err error
		offset, err = strconv.Atoi(parts[1])
		if err != nil || offset < 1 || offset >= ssmCommandLimit || strconv.Itoa(offset) != parts[1] {
			return nil, "", ErrSSMCommandInvalid
		}
		var raw string
		if err = a.db.QueryRowContext(ctx, `SELECT record FROM aws_ssm_native_command_pages WHERE token=?`, token).Scan(&raw); err != nil {
			return nil, "", err
		}
		if len(raw) > 8388608 || json.Unmarshal([]byte(raw), &page) != nil || ssmEncode(page) != raw || page.Principal != principal || page.Region != region || page.InvokedAfter != input.InvokedAfter || page.MaxResults != input.MaxResults || page.Expires <= now || page.Expires > now+600000 || len(page.Commands) > ssmCommandLimit || offset >= len(page.Commands) || offset%input.MaxResults != 0 {
			return nil, "", ErrSSMCommandInvalid
		}
	} else {
		after, err := time.Parse(time.RFC3339Nano, input.InvokedAfter)
		if err != nil {
			return nil, "", err
		}
		// Read only bounded retained IDs, then validate through the existing store.
		rows, err := a.db.QueryContext(ctx, `SELECT id,record FROM aws_ssm_commands ORDER BY id`)
		if err != nil {
			return nil, "", err
		}
		type scoped struct {
			id        string
			authority SSMCommandAuthority
		}
		ids := []scoped{}
		count := 0
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return nil, "", err
			}
			count++
			var record SSMCommand
			if count > ssmCommandLimit || len(raw) > ssmCommandBytes || json.Unmarshal([]byte(raw), &record) != nil {
				rows.Close()
				return nil, "", ErrSSMCommandInvalid
			}
			if record.Authority.Account == "000000000000" && record.Authority.Region == region && record.Authority.Principal == principal {
				ids = append(ids, scoped{id, record.Authority})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, "", err
		}
		for _, entry := range ids {
			record, err := a.store.Get(ctx, entry.authority, entry.id)
			if err != nil {
				return nil, "", err
			}
			if record.RequestedAt >= after.UnixMilli() {
				page.Commands = append(page.Commands, SSMCommandSummary{record.CommandId, record.Request, record.RequestedAt})
			}
		}
		sort.Slice(page.Commands, func(i, j int) bool {
			if page.Commands[i].RequestedAt == page.Commands[j].RequestedAt {
				return page.Commands[i].CommandId < page.Commands[j].CommandId
			}
			return page.Commands[i].RequestedAt < page.Commands[j].RequestedAt
		})
		if len(page.Commands) > input.MaxResults {
			token, err = ssmRandom()
			if err != nil {
				return nil, "", err
			}
			raw := ssmEncode(page)
			if len(raw) > 8388608 {
				return nil, "", ErrSSMCommandInvalid
			}
			tx, err := a.db.BeginTx(ctx, nil)
			if err != nil {
				return nil, "", err
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, `DELETE FROM aws_ssm_native_command_pages WHERE json_extract(record,'$.Expires')<=?`, now); err != nil {
				return nil, "", err
			}
			result, err := tx.ExecContext(ctx, `INSERT INTO aws_ssm_native_command_pages SELECT ?,? WHERE (SELECT count(*) FROM aws_ssm_native_command_pages)<16`, token, raw)
			if err != nil {
				return nil, "", err
			}
			n, err := result.RowsAffected()
			if err != nil || n != 1 {
				return nil, "", ErrSSMCommandInvalid
			}
			var stored string
			if err = tx.QueryRowContext(ctx, `SELECT record FROM aws_ssm_native_command_pages WHERE token=?`, token).Scan(&stored); err != nil || stored != raw {
				return nil, "", ErrSSMCommandInvalid
			}
			if err = tx.Commit(); err != nil {
				return nil, "", err
			}
		}
	}
	// A stored cursor is an immutable ordered snapshot, not trusted opaque data.
	after, err := time.Parse(time.RFC3339Nano, page.InvokedAfter)
	if err != nil {
		return nil, "", ErrSSMCommandInvalid
	}
	seen := map[string]bool{}
	for n, item := range page.Commands {
		if seen[item.CommandId] || !ssmCommandID.MatchString(item.CommandId) || !ssmRequestValid(item.Request) || item.RequestedAt < after.UnixMilli() {
			return nil, "", ErrSSMCommandInvalid
		}
		seen[item.CommandId] = true
		if n > 0 {
			previous := page.Commands[n-1]
			if previous.RequestedAt > item.RequestedAt || (previous.RequestedAt == item.RequestedAt && previous.CommandId >= item.CommandId) {
				return nil, "", ErrSSMCommandInvalid
			}
		}
	}
	end := offset + input.MaxResults
	if end > len(page.Commands) {
		end = len(page.Commands)
	}
	out := make([]nativeCommandSummary, 0, end-offset)
	for _, item := range page.Commands[offset:end] {
		authority, err := a.retainedAuthority(ctx, principal, region, item.CommandId)
		if err != nil {
			return nil, "", err
		}
		record, err := a.store.Get(ctx, authority, item.CommandId)
		if err != nil || record.RequestedAt != item.RequestedAt || ssmEncode(record.Request) != ssmEncode(item.Request) {
			return nil, "", ErrSSMCommandInvalid
		}
		out = append(out, commandWireSummary(item.CommandId, item.Request, item.RequestedAt))
	}
	next := ""
	if end < len(page.Commands) {
		next = token + "." + strconv.Itoa(end)
	}
	return out, next, nil
}
