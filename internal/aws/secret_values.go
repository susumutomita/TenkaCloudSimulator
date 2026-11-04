package aws

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

type SecretIdentity struct {
	ARN  string `json:"ARN"`
	Name string `json:"Name"`
}

type SecretValueResult struct {
	ARN           string   `json:"ARN"`
	Name          string   `json:"Name"`
	VersionId     string   `json:"VersionId"`
	VersionStages []string `json:"VersionStages,omitempty"`
	CreatedDate   float64  `json:"CreatedDate"`
	material      secretMaterial
}

// A full physical ARN never resolves through its former name. Partial ARNs and
// cross-region/account requests are explicitly outside this bounded adapter.
func secretFullARN(region, id string) bool {
	prefix := "arn:aws:secretsmanager:" + region + ":000000000000:secret:"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	name := strings.TrimPrefix(id, prefix)
	if len(name) < 8 || name[len(name)-7] != '-' || !secretNamePattern.MatchString(name[:len(name)-7]) {
		return false
	}
	for _, ch := range name[len(name)-6:] {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func secretResolve(ctx context.Context, tx *sql.Tx, region, id string) (SecretMetadata, error) {
	var meta SecretMetadata
	if !secretRegion(region) {
		return meta, secretInvalid()
	}
	column := "name"
	if !secretNamePattern.MatchString(id) {
		if !secretFullARN(region, id) {
			return meta, secretInvalid()
		}
		column = "arn"
	}
	var created, changed int64
	err := tx.QueryRowContext(ctx, "SELECT arn,name,description,created,changed FROM aws_secrets WHERE region=? AND "+column+"=?", region, id).Scan(&meta.ARN, &meta.Name, &meta.Description, &created, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return meta, secretMissing()
	}
	if err != nil {
		return meta, err
	}
	if !secretFullARN(region, meta.ARN) || meta.ARN[:len(meta.ARN)-7] != "arn:aws:secretsmanager:"+region+":000000000000:secret:"+meta.Name {
		return SecretMetadata{}, errors.New("invalid persisted secret identity")
	}
	meta.CreatedDate = float64(created) / 1000
	meta.LastChangedDate = float64(changed) / 1000
	return meta, nil
}

func secretVersionStages(ctx context.Context, tx *sql.Tx, region, arn, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT stage FROM aws_secret_stages WHERE region=? AND arn=? AND version_id=? ORDER BY stage", region, arn, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var stage string
		if err = rows.Scan(&stage); err != nil {
			return nil, err
		}
		result = append(result, stage)
	}
	return result, rows.Err()
}

func (s *SecretStore) Describe(ctx context.Context, region, id string, authorize SecretAuthorizer) (SecretDescription, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretDescription{}, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return SecretDescription{}, err
	}
	if err = secretAuthorize(authorize, "DescribeSecret", meta.ARN); err != nil {
		return SecretDescription{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT version_id,stage FROM aws_secret_stages WHERE region=? AND arn=? ORDER BY version_id,stage", region, meta.ARN)
	if err != nil {
		return SecretDescription{}, err
	}
	stages := map[string][]string{}
	for rows.Next() {
		var version, stage string
		if err = rows.Scan(&version, &stage); err != nil {
			rows.Close()
			return SecretDescription{}, err
		}
		stages[version] = append(stages[version], stage)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return SecretDescription{}, err
	}
	if err = tx.Commit(); err != nil {
		return SecretDescription{}, err
	}
	return SecretDescription{SecretMetadata: meta, VersionIdsToStages: stages}, nil
}

func (s *SecretStore) Get(ctx context.Context, region, id, version, stage string, authorize SecretAuthorizer) (SecretValueResult, error) {
	if version != "" && !secretToken(version) || stage != "" && stage != "AWSCURRENT" && stage != "AWSPREVIOUS" {
		return SecretValueResult{}, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretValueResult{}, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return SecretValueResult{}, err
	}
	if err = secretAuthorize(authorize, "GetSecretValue", meta.ARN); err != nil {
		return SecretValueResult{}, err
	}
	if version == "" && stage == "" {
		stage = "AWSCURRENT"
	}
	if stage != "" {
		var selected string
		err = tx.QueryRowContext(ctx, "SELECT version_id FROM aws_secret_stages WHERE region=? AND arn=? AND stage=?", region, meta.ARN, stage).Scan(&selected)
		if errors.Is(err, sql.ErrNoRows) {
			return SecretValueResult{}, secretMissing()
		}
		if err != nil {
			return SecretValueResult{}, err
		}
		if version != "" && version != selected {
			return SecretValueResult{}, secretFailure{"InvalidRequestException", "VersionId and VersionStage do not select the same version."}
		}
		version = selected
	}
	var raw []byte
	var created int64
	err = tx.QueryRowContext(ctx, "SELECT value,created FROM aws_secret_versions WHERE region=? AND arn=? AND version_id=?", region, meta.ARN, version).Scan(&raw, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return SecretValueResult{}, secretMissing()
	}
	if err != nil {
		return SecretValueResult{}, err
	}
	stages, err := secretVersionStages(ctx, tx, region, meta.ARN, version)
	if err != nil {
		return SecretValueResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SecretValueResult{}, err
	}
	return SecretValueResult{ARN: meta.ARN, Name: meta.Name, VersionId: version, VersionStages: stages, CreatedDate: float64(created) / 1000, material: newSecretMaterial(string(raw))}, nil
}

func secretCurrent(ctx context.Context, tx *sql.Tx, region, arn string) (string, error) {
	var current string
	err := tx.QueryRowContext(ctx, "SELECT version_id FROM aws_secret_stages WHERE region=? AND arn=? AND stage='AWSCURRENT'", region, arn).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("secret current label is missing")
	}
	return current, err
}

func secretMoveCurrent(ctx context.Context, tx *sql.Tx, region, arn, old, target string) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM aws_secret_stages WHERE region=? AND arn=?", region, arn); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO aws_secret_stages VALUES(?,?,'AWSPREVIOUS',?)", region, arn, old); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO aws_secret_stages VALUES(?,?,'AWSCURRENT',?)", region, arn, target)
	return err
}

func (s *SecretStore) Put(ctx context.Context, region, id, token string, material secretMaterial, authorize SecretAuthorizer) (SecretMutationResult, error) {
	if !secretToken(token) || !utf8.ValidString(material.plaintext()) || len(material.plaintext()) < 1 || len(material.plaintext()) > 65536 {
		return SecretMutationResult{}, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretMutationResult{}, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return SecretMutationResult{}, err
	}
	if err = secretAuthorize(authorize, "PutSecretValue", meta.ARN); err != nil {
		return SecretMutationResult{}, err
	}
	var prior []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM aws_secret_versions WHERE region=? AND arn=? AND version_id=?", region, meta.ARN, token).Scan(&prior)
	if err == nil {
		if subtle.ConstantTimeCompare(prior, []byte(material.plaintext())) != 1 {
			return SecretMutationResult{}, secretFailure{"ResourceExistsException", "A version ID cannot replace an existing value."}
		}
		stages, err := secretVersionStages(ctx, tx, region, meta.ARN, token)
		if err != nil {
			return SecretMutationResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return SecretMutationResult{}, err
		}
		return SecretMutationResult{ARN: meta.ARN, Name: meta.Name, VersionId: token, VersionStages: stages}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SecretMutationResult{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_secret_versions WHERE region=? AND arn=?", region, meta.ARN).Scan(&count); err != nil {
		return SecretMutationResult{}, err
	}
	if count >= 100 {
		return SecretMutationResult{}, secretFailure{"LimitExceededException", "Simulator per-secret version limit reached."}
	}
	if err = secretValueQuota(ctx, tx, len(material.plaintext())); err != nil {
		return SecretMutationResult{}, err
	}
	current, err := secretCurrent(ctx, tx, region, meta.ARN)
	if err != nil {
		return SecretMutationResult{}, err
	}
	now := s.now().UnixMilli()
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_secret_versions VALUES(?,?,?,?,?)", region, meta.ARN, token, []byte(material.plaintext()), now); err != nil {
		return SecretMutationResult{}, err
	}
	if err = secretMoveCurrent(ctx, tx, region, meta.ARN, current, token); err != nil {
		return SecretMutationResult{}, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aws_secrets SET changed=? WHERE region=? AND arn=?", now, region, meta.ARN); err != nil {
		return SecretMutationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return SecretMutationResult{}, err
	}
	return SecretMutationResult{ARN: meta.ARN, Name: meta.Name, VersionId: token, VersionStages: []string{"AWSCURRENT"}}, nil
}

func (s *SecretStore) Promote(ctx context.Context, region, id, target, remove string, authorize SecretAuthorizer) (SecretIdentity, error) {
	if !secretToken(target) || remove != "" && !secretToken(remove) {
		return SecretIdentity{}, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretIdentity{}, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return SecretIdentity{}, err
	}
	if err = secretAuthorize(authorize, "UpdateSecretVersionStage", meta.ARN); err != nil {
		return SecretIdentity{}, err
	}
	current, err := secretCurrent(ctx, tx, region, meta.ARN)
	if err != nil {
		return SecretIdentity{}, err
	}
	if remove != "" && remove != current {
		return SecretIdentity{}, secretFailure{"InvalidRequestException", "RemoveFromVersionId does not own AWSCURRENT."}
	}
	if target != current {
		if remove == "" {
			return SecretIdentity{}, secretFailure{"InvalidRequestException", "Moving AWSCURRENT requires its current owner."}
		}
		var found int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM aws_secret_versions WHERE region=? AND arn=? AND version_id=?", region, meta.ARN, target).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return SecretIdentity{}, secretMissing()
		}
		if err != nil {
			return SecretIdentity{}, err
		}
		if err = secretMoveCurrent(ctx, tx, region, meta.ARN, current, target); err != nil {
			return SecretIdentity{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE aws_secrets SET changed=? WHERE region=? AND arn=?", s.now().UnixMilli(), region, meta.ARN); err != nil {
			return SecretIdentity{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return SecretIdentity{}, err
	}
	return SecretIdentity{ARN: meta.ARN, Name: meta.Name}, nil
}
