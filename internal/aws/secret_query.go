package aws

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SecretDeletionResult struct {
	ARN          string  `json:"ARN"`
	Name         string  `json:"Name"`
	DeletionDate float64 `json:"DeletionDate"`
}

// DeleteForce requires the full physical ARN. Neither unknown/deleted ARNs nor
// their former names are adopted, created, or added to the reservation ledger.
func (s *SecretStore) DeleteForce(ctx context.Context, region, arn string, authorize SecretAuthorizer) (SecretDeletionResult, error) {
	if !secretRegion(region) || !secretFullARN(region, arn) {
		return SecretDeletionResult{}, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretDeletionResult{}, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, arn)
	var failure secretFailure
	if err != nil && !(errors.As(err, &failure) && failure.code == "ResourceNotFoundException") {
		return SecretDeletionResult{}, err
	}
	if err = secretAuthorize(authorize, "DeleteSecret", arn); err != nil {
		return SecretDeletionResult{}, err
	}
	name := meta.Name
	if name == "" {
		prefix := "arn:aws:secretsmanager:" + region + ":000000000000:secret:"
		name = strings.TrimPrefix(arn[:len(arn)-7], prefix)
	}
	expected := int64(0)
	if meta.ARN != "" {
		expected = 1
	}
	if err = secretExecRows(ctx, tx, expected, "DELETE FROM aws_secrets WHERE region=? AND arn=?", region, arn); err != nil {
		return SecretDeletionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SecretDeletionResult{}, err
	}
	return SecretDeletionResult{ARN: arn, Name: name, DeletionDate: float64(s.now().UnixMilli()) / 1000}, nil
}

type SecretListQuery struct {
	MaxResults int
	NextToken  string
	SortBy     string
	SortOrder  string
}

type secretCursor struct {
	Region    string `json:"r"`
	SortBy    string `json:"b"`
	SortOrder string `json:"o"`
	Name      string `json:"n"`
	Created   int64  `json:"c"`
	ARN       string `json:"a"`
	Expires   int64  `json:"e"`
}

func (s *SecretStore) encodeSecretCursor(c secretCursor) string {
	body, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *SecretStore) decodeSecretCursor(raw, region, sortBy, sortOrder string) (secretCursor, error) {
	var cursor secretCursor
	if len(raw) > 4096 {
		return cursor, secretInvalid()
	}
	a, b, ok := strings.Cut(raw, ".")
	if !ok {
		return cursor, secretInvalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(a)
	sig, sigErr := base64.RawURLEncoding.DecodeString(b)
	if err != nil || sigErr != nil {
		return cursor, secretInvalid()
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(data)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(data, &cursor) != nil || cursor.Region != region || cursor.SortBy != sortBy || cursor.SortOrder != sortOrder || cursor.Expires <= s.now().UnixMilli() {
		return cursor, secretInvalid()
	}
	return cursor, nil
}

func secretStageMap(ctx context.Context, tx *sql.Tx, region, arn string) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT version_id,stage FROM aws_secret_stages WHERE region=? AND arn=? ORDER BY version_id,stage", region, arn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	stages := map[string][]string{}
	for rows.Next() {
		var id, stage string
		if err = rows.Scan(&id, &stage); err != nil {
			return nil, err
		}
		stages[id] = append(stages[id], stage)
	}
	return stages, rows.Err()
}

func (s *SecretStore) List(ctx context.Context, region string, q SecretListQuery, authorize SecretAuthorizer) ([]SecretListEntry, string, error) {
	if q.SortBy == "" {
		q.SortBy = "created-date"
	}
	if q.SortOrder == "" {
		q.SortOrder = "asc"
	}
	if !secretRegion(region) || q.MaxResults < 1 || q.MaxResults > 100 || q.SortBy != "created-date" && q.SortBy != "name" || q.SortOrder != "asc" && q.SortOrder != "desc" {
		return nil, "", secretInvalid()
	}
	if err := secretAuthorize(authorize, "ListSecrets", "*"); err != nil {
		return nil, "", err
	}
	var cursor secretCursor
	var err error
	if q.NextToken != "" {
		cursor, err = s.decodeSecretCursor(q.NextToken, region, q.SortBy, q.SortOrder)
		if err != nil {
			return nil, "", err
		}
	}
	column, operator, direction := "created", ">", "ASC"
	if q.SortBy == "name" {
		column = "name"
	}
	if q.SortOrder == "desc" {
		operator, direction = "<", "DESC"
	}
	query := "SELECT arn,name,description,created,changed FROM aws_secrets WHERE region=?"
	args := []any{region}
	if q.NextToken != "" {
		var position any = cursor.Created
		if q.SortBy == "name" {
			position = cursor.Name
		}
		query += " AND (" + column + operator + "? OR (" + column + "=? AND arn" + operator + "?))"
		args = append(args, position, position, cursor.ARN)
	}
	query += " ORDER BY " + column + " " + direction + ",arn " + direction + " LIMIT ?"
	args = append(args, q.MaxResults+1)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	type entry struct {
		meta    SecretMetadata
		created int64
	}
	items := []entry{}
	for rows.Next() {
		var item entry
		var changed int64
		if err = rows.Scan(&item.meta.ARN, &item.meta.Name, &item.meta.Description, &item.created, &changed); err != nil {
			rows.Close()
			return nil, "", err
		}
		item.meta.CreatedDate = float64(item.created) / 1000
		item.meta.LastChangedDate = float64(changed) / 1000
		if !secretFullARN(region, item.meta.ARN) || item.meta.ARN[:len(item.meta.ARN)-7] != "arn:aws:secretsmanager:"+region+":000000000000:secret:"+item.meta.Name {
			rows.Close()
			return nil, "", errors.New("invalid persisted secret identity")
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > q.MaxResults {
		items = items[:q.MaxResults]
		last := items[len(items)-1]
		next = s.encodeSecretCursor(secretCursor{Region: region, SortBy: q.SortBy, SortOrder: q.SortOrder, Name: last.meta.Name, Created: last.created, ARN: last.meta.ARN, Expires: s.now().Add(24 * time.Hour).UnixMilli()})
	}
	result := []SecretListEntry{}
	for _, item := range items {
		stages, err := secretStageMap(ctx, tx, region, item.meta.ARN)
		if err != nil {
			return nil, "", err
		}
		result = append(result, SecretListEntry{SecretMetadata: item.meta, SecretVersionsToStages: stages})
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return result, next, nil
}
