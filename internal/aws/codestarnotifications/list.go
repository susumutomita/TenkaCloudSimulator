package codestarnotifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"slices"
	"strings"
)

func (s *Store) list(ctx context.Context, tx *sql.Tx, region, actor string, in Input) (map[string]any, error) {
	limit := 100
	if in.MaxResults != nil {
		limit = *in.MaxResults
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("MaxResults must be from 1 to 100.")
	}
	// Same-name filters are alternatives; distinct names constrain one another.
	if len(in.Filters) > 10 {
		return nil, invalid("Use at most ten notification rule filters per local query.")
	}
	grouped := make(map[string][]string)
	for _, filter := range in.Filters {
		if filter.Value == "" {
			return nil, invalid("A filter requires a value.")
		}
		switch filter.Name {
		case "RESOURCE", "CREATED_BY", "EVENT_TYPE_ID", "TARGET_ADDRESS":
			grouped[filter.Name] = append(grouped[filter.Name], filter.Value)
		default:
			return nil, invalid("Unsupported notification rule filter.")
		}
	}
	query := `SELECT seq,arn FROM aws_notification_rules WHERE region=? AND seq<?`
	args := []any{region, int64(math.MaxInt64)}
	for _, group := range []struct{ name, condition string }{
		{"RESOURCE", "source=?"}, {"CREATED_BY", "owner=?"},
		{"EVENT_TYPE_ID", "instr(','||events||',', ','||?||',')>0"},
		{"TARGET_ADDRESS", "EXISTS(SELECT 1 FROM aws_notification_targets t WHERE rule_arn=aws_notification_rules.arn AND topic_arn=?)"},
	} {
		values := grouped[group.name]
		if len(values) == 0 {
			continue
		}
		slices.Sort(values)
		values = slices.Compact(values)
		grouped[group.name] = values
		clauses := make([]string, 0, len(values))
		for _, value := range values {
			if group.name == "EVENT_TYPE_ID" && value != BranchCreated && value != BranchUpdated {
				clauses = append(clauses, "0")
				continue
			}
			clauses = append(clauses, group.condition)
			args = append(args, value)
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	scope, _ := json.Marshal([]any{region, actor, grouped})
	if in.NextToken != "" {
		token, err := base64.RawURLEncoding.Strict().DecodeString(in.NextToken)
		if err != nil || len(token) != 8+sha256.Size || strings.ContainsAny(in.NextToken, "\r\n") || !hmac.Equal(token[8:], s.tokenMAC(scope, token[:8])) {
			return nil, Fault{"InvalidNextTokenException", "The listing continuation is invalid for this query."}
		}
		before := int64(binary.BigEndian.Uint64(token[:8]))
		if before <= 0 {
			return nil, Fault{"InvalidNextTokenException", "Invalid sequence boundary."}
		}
		args[1] = before
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY seq DESC LIMIT ?", append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]any{}
	items := make([]map[string]string, 0, limit)
	var last int64
	for rows.Next() {
		var arn string
		var seq int64
		if err := rows.Scan(&seq, &arn); err != nil {
			return nil, err
		}
		if len(items) == limit {
			var bound [8]byte
			binary.BigEndian.PutUint64(bound[:], uint64(last))
			result["NextToken"] = base64.RawURLEncoding.EncodeToString(append(bound[:], s.tokenMAC(scope, bound[:])...))
			break
		}
		items = append(items, map[string]string{"Arn": arn, "Id": strings.TrimPrefix(arn, prefix("codestar-notifications", region)+"notificationrule/")})
		last = seq
	}
	result["NotificationRules"] = items
	return result, rows.Err()
}

func (s *Store) tokenMAC(scope, bound []byte) []byte {
	h := hmac.New(sha256.New, s.tokenKey[:])
	h.Write(scope)
	h.Write(bound)
	return h.Sum(nil)
}
