package aws

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// SecretVersionListQuery selects metadata only. Pagination is a live keyset,
// not a snapshot: subsequent stage changes may change filtered membership.
type SecretVersionListQuery struct {
	IncludeDeprecated bool
	MaxResults        int
	NextToken         string
}
type SecretVersionListEntry struct {
	VersionId     string   `json:"VersionId"`
	VersionStages []string `json:"VersionStages,omitempty"`
	CreatedDate   float64  `json:"CreatedDate"`
}
type SecretVersionListResult struct {
	ARN       string                   `json:"ARN"`
	Name      string                   `json:"Name"`
	Versions  []SecretVersionListEntry `json:"Versions"`
	NextToken string                   `json:"NextToken,omitempty"`
}
type secretVersionCursor struct {
	Domain            string `json:"d"`
	Region            string `json:"r"`
	ARN               string `json:"a"`
	IncludeDeprecated bool   `json:"i"`
	Created           int64  `json:"c"`
	Version           string `json:"v"`
	Expires           int64  `json:"e"`
}

func secretVersionsInvalidToken() error {
	return secretFailure{"InvalidNextTokenException", "The version-list continuation token is invalid."}
}
func (s *SecretStore) encodeVersionCursor(c secretVersionCursor) string {
	b, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *SecretStore) decodeVersionCursor(raw, region, arn string, include bool) (secretVersionCursor, error) {
	var c secretVersionCursor
	if len(raw) > 4096 {
		return c, secretVersionsInvalidToken()
	}
	a, b, ok := strings.Cut(raw, ".")
	if !ok {
		return c, secretVersionsInvalidToken()
	}
	data, err := base64.RawURLEncoding.DecodeString(a)
	sig, e := base64.RawURLEncoding.DecodeString(b)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(data)
	if err != nil || e != nil || !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(data, &c) != nil || c.Domain != "secret-versions-v1" || c.Region != region || c.ARN != arn || c.IncludeDeprecated != include || c.Created < 0 || !secretToken(c.Version) || c.Expires <= s.now().UnixMilli() {
		return secretVersionCursor{}, secretVersionsInvalidToken()
	}
	return c, nil
}

// ListSecretVersionIds authorizes the saved physical ARN inside the same short
// read transaction. It never selects value bytes or synthesizes access/KMS data.
func (s *SecretStore) ListSecretVersionIds(ctx context.Context, region, id string, q SecretVersionListQuery, authorize SecretAuthorizer) (SecretVersionListResult, error) {
	var zero SecretVersionListResult
	if s == nil || s.db == nil || s.now == nil || len(s.cursorKey) != 32 {
		return zero, errors.New("secret version store unavailable")
	}
	if !secretRegion(region) || q.MaxResults < 1 || q.MaxResults > 100 {
		return zero, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return zero, err
	}
	if err = secretAuthorize(authorize, "ListSecretVersionIds", meta.ARN); err != nil {
		return zero, err
	}
	var cursor secretVersionCursor
	if q.NextToken != "" {
		cursor, err = s.decodeVersionCursor(q.NextToken, region, meta.ARN, q.IncludeDeprecated)
		if err != nil {
			return zero, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT version_id,created FROM aws_secret_versions WHERE region=? AND arn=? ORDER BY created,version_id LIMIT 101", region, meta.ARN)
	if err != nil {
		return zero, err
	}
	type version struct {
		id      string
		created int64
		stages  []string
	}
	all := []version{}
	positions := map[string]int{}
	for rows.Next() {
		var v version
		if err = rows.Scan(&v.id, &v.created); err != nil {
			rows.Close()
			return zero, err
		}
		if !secretToken(v.id) || v.created < 0 {
			rows.Close()
			return zero, errors.New("invalid persisted secret version metadata")
		}
		positions[v.id] = len(all)
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	if len(all) == 0 || len(all) > 100 {
		return zero, errors.New("invalid persisted secret version count")
	}
	stages, err := tx.QueryContext(ctx, "SELECT version_id,stage FROM aws_secret_stages WHERE region=? AND arn=? ORDER BY stage", region, meta.ARN)
	if err != nil {
		return zero, err
	}
	current := false
	seen := map[string]bool{}
	for stages.Next() {
		var versionID, stage string
		if err = stages.Scan(&versionID, &stage); err != nil {
			stages.Close()
			return zero, err
		}
		p, ok := positions[versionID]
		if !ok || (stage != "AWSCURRENT" && stage != "AWSPREVIOUS") || seen[stage] {
			stages.Close()
			return zero, errors.New("invalid persisted secret stage metadata")
		}
		seen[stage] = true
		current = current || stage == "AWSCURRENT"
		all[p].stages = append(all[p].stages, stage)
	}
	err = stages.Err()
	stages.Close()
	if err != nil {
		return zero, err
	}
	if !current {
		return zero, errors.New("missing persisted current secret stage")
	}
	selected := []version{}
	for _, v := range all {
		if !q.IncludeDeprecated && len(v.stages) == 0 {
			continue
		}
		if q.NextToken != "" && (v.created < cursor.Created || v.created == cursor.Created && v.id <= cursor.Version) {
			continue
		}
		selected = append(selected, v)
	}
	result := SecretVersionListResult{ARN: meta.ARN, Name: meta.Name, Versions: []SecretVersionListEntry{}}
	if len(selected) > q.MaxResults {
		selected = selected[:q.MaxResults]
		last := selected[len(selected)-1]
		result.NextToken = s.encodeVersionCursor(secretVersionCursor{Domain: "secret-versions-v1", Region: region, ARN: meta.ARN, IncludeDeprecated: q.IncludeDeprecated, Created: last.created, Version: last.id, Expires: s.now().Add(24 * time.Hour).UnixMilli()})
	}
	for _, v := range selected {
		sort.Strings(v.stages)
		result.Versions = append(result.Versions, SecretVersionListEntry{VersionId: v.id, CreatedDate: float64(v.created) / 1000, VersionStages: v.stages})
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

// decodeSecretVersionListRequest is for the existing native JSON1.1 adapter,
// after its unchanged transport/auth/selector/body-limit checks. Null, aliases,
// duplicate and unsupported fields are never silently normalized to defaults.
func decodeSecretVersionListRequest(data []byte) (string, SecretVersionListQuery, error) {
	q := SecretVersionListQuery{MaxResults: 100}
	if !utf8.Valid(data) || !secretUnicodeScalars(data) {
		return "", q, secretInvalid()
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if secretUniqueJSON(dec, 0) != nil {
		return "", q, secretInvalid()
	}
	if _, e := dec.Token(); e != io.EOF {
		return "", q, secretInvalid()
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return "", q, secretInvalid()
	}
	var id string
	for key, value := range raw {
		switch key {
		case "SecretId":
			if json.Unmarshal(value, &id) != nil {
				return "", q, secretInvalid()
			}
		case "IncludeDeprecated":
			if json.Unmarshal(value, &q.IncludeDeprecated) != nil {
				return "", q, secretInvalid()
			}
		case "MaxResults":
			if json.Unmarshal(value, &q.MaxResults) != nil {
				return "", q, secretInvalid()
			}
		case "NextToken":
			if json.Unmarshal(value, &q.NextToken) != nil || q.NextToken == "" {
				return "", q, secretInvalid()
			}
		default:
			return "", q, secretInvalid()
		}
	}
	if id == "" || len(id) > 2048 || q.MaxResults < 1 || q.MaxResults > 100 {
		return "", q, secretInvalid()
	}
	return id, q, nil
}
