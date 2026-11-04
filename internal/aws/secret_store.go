package aws

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"
)

// SecretStore is local practice storage, not encrypted storage or an AWS KMS
// client. The metadata/value tables deliberately do not share a JSON document.
type SecretStore struct {
	db *sql.DB
	now func() time.Time
	suffix func() string
	cursorKey []byte
}

type secretFailure struct {
	code string
	message string
}

func (e secretFailure) Error() string { return e.message }

func secretInvalid() error {
	return secretFailure{"InvalidParameterException", "Invalid or unsupported Secrets Manager request."}
}

func secretMissing() error {
	return secretFailure{"ResourceNotFoundException", "The local secret or version was not found."}
}

var secretNamePattern = regexp.MustCompile(`^[A-Za-z0-9/_+=.@-]{1,512}$`)

type SecretMutationResult struct {
	ARN string `json:"ARN"`
	Name string `json:"Name"`
	VersionId string `json:"VersionId"`
	VersionStages []string `json:"VersionStages,omitempty"`
}

func NewSecretStore(db *sql.DB) (*SecretStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_secret_settings(id INTEGER PRIMARY KEY CHECK(id=1),cursor_key BLOB NOT NULL CHECK(length(cursor_key)=32));
CREATE TABLE IF NOT EXISTS aws_secret_arns(arn TEXT PRIMARY KEY,region TEXT NOT NULL,name TEXT NOT NULL,created INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS aws_secrets(region TEXT NOT NULL,arn TEXT NOT NULL,name TEXT NOT NULL,description TEXT NOT NULL,created INTEGER NOT NULL,changed INTEGER NOT NULL,PRIMARY KEY(region,arn),UNIQUE(region,name),FOREIGN KEY(arn) REFERENCES aws_secret_arns(arn));
CREATE TABLE IF NOT EXISTS aws_secret_versions(region TEXT NOT NULL,arn TEXT NOT NULL,version_id TEXT NOT NULL,value BLOB NOT NULL,created INTEGER NOT NULL,PRIMARY KEY(region,arn,version_id),FOREIGN KEY(region,arn) REFERENCES aws_secrets(region,arn) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS aws_secret_stages(region TEXT NOT NULL,arn TEXT NOT NULL,stage TEXT NOT NULL CHECK(stage IN ('AWSCURRENT','AWSPREVIOUS')),version_id TEXT NOT NULL,PRIMARY KEY(region,arn,stage),FOREIGN KEY(region,arn,version_id) REFERENCES aws_secret_versions(region,arn,version_id) ON DELETE CASCADE);
CREATE TRIGGER IF NOT EXISTS aws_secret_versions_immutable BEFORE UPDATE ON aws_secret_versions BEGIN SELECT RAISE(ABORT,'secret versions are immutable'); END;`)
	if err != nil { return nil, err }
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil { return nil, err }
	if _, err = db.Exec("INSERT OR IGNORE INTO aws_secret_settings VALUES(1,?)", key); err != nil { return nil, err }
	if err = db.QueryRow("SELECT cursor_key FROM aws_secret_settings WHERE id=1").Scan(&key); err != nil { return nil, err }
	if len(key) != 32 { return nil, errors.New("invalid secret cursor state") }
	return &SecretStore{db: db, now: time.Now, suffix: func() string { return rand.Text()[:6] }, cursorKey: key}, nil
}

func secretRegion(region string) bool { return slices.Contains(Regions, region) }

func secretToken(token string) bool {
	return utf8.ValidString(token) && utf8.RuneCountInString(token) >= 32 && utf8.RuneCountInString(token) <= 64
}

func secretAuthorize(authorize SecretAuthorizer, action, arn string) error {
	if authorize == nil { return errors.New("secret authorizer is not configured") }
	return authorize("secretsmanager:"+action, arn)
}

func (s *SecretStore) Create(ctx context.Context, region, name, description, token string, material secretMaterial, authorize SecretAuthorizer) (SecretMutationResult, error) {
	if !secretRegion(region) || !secretNamePattern.MatchString(name) || !secretToken(token) || !utf8.ValidString(description) || utf8.RuneCountInString(description)>2048 || !utf8.ValidString(material.plaintext()) || len(material.plaintext())<1 || len(material.plaintext())>65536 {
		return SecretMutationResult{}, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil { return SecretMutationResult{}, err }
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT arn FROM aws_secrets WHERE region=? AND name=?", region, name).Scan(&existing)
	if err == nil {
		if err = secretAuthorize(authorize, "CreateSecret", existing); err != nil { return SecretMutationResult{}, err }
		var prior []byte
		err = tx.QueryRowContext(ctx, "SELECT value FROM aws_secret_versions WHERE region=? AND arn=? AND version_id=?", region, existing, token).Scan(&prior)
		if errors.Is(err, sql.ErrNoRows) { return SecretMutationResult{}, secretFailure{"ResourceExistsException", "Use PutSecretValue to create another version."} }
		if err != nil { return SecretMutationResult{}, err }
		if subtle.ConstantTimeCompare(prior, []byte(material.plaintext())) != 1 { return SecretMutationResult{}, secretFailure{"ResourceExistsException", "A version ID cannot replace an existing value."} }
		if err = tx.Commit(); err != nil { return SecretMutationResult{}, err }
		return SecretMutationResult{ARN: existing, Name: name, VersionId: token}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) { return SecretMutationResult{}, err }
	var count int
	arn := ""
	for attempt:=0; attempt<8; attempt++ {
		candidate := fmt.Sprintf("arn:aws:secretsmanager:%s:000000000000:secret:%s-%s", region, name, s.suffix())
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_secret_arns WHERE arn=?", candidate).Scan(&count); err != nil { return SecretMutationResult{}, err }
		if count == 0 { arn=candidate; break }
	}
	if arn == "" { return SecretMutationResult{}, errors.New("secret ARN allocation failed") }
	if err = secretAuthorize(authorize, "CreateSecret", arn); err != nil { return SecretMutationResult{}, err }
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_secrets").Scan(&count); err != nil { return SecretMutationResult{}, err }
	if count >= 100 { return SecretMutationResult{}, secretFailure{"LimitExceededException", "Simulator secret limit reached."} }
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_secret_arns").Scan(&count); err != nil { return SecretMutationResult{}, err }
	if count >= 10000 { return SecretMutationResult{}, secretFailure{"LimitExceededException", "Simulator lifetime ARN reservation limit reached."} }
	if err = secretValueQuota(ctx, tx, len(material.plaintext())); err != nil { return SecretMutationResult{}, err }
	now := s.now().UnixMilli()
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_secret_arns VALUES(?,?,?,?)", arn, region, name, now); err != nil { return SecretMutationResult{}, err }
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_secrets VALUES(?,?,?,?,?,?)", region, arn, name, description, now, now); err != nil { return SecretMutationResult{}, err }
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_secret_versions VALUES(?,?,?,?,?)", region, arn, token, []byte(material.plaintext()), now); err != nil { return SecretMutationResult{}, err }
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_secret_stages VALUES(?,?,?,?)", region, arn, "AWSCURRENT", token); err != nil { return SecretMutationResult{}, err }
	if err = tx.Commit(); err != nil { return SecretMutationResult{}, err }
	return SecretMutationResult{ARN: arn, Name: name, VersionId: token}, nil
}

func secretValueQuota(ctx context.Context, tx *sql.Tx, newBytes int) error {
	var count, stored int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(value)),0) FROM aws_secret_versions").Scan(&count, &stored); err != nil { return err }
	if count>=10000 || stored+int64(newBytes)>64<<20 { return secretFailure{"LimitExceededException", "Simulator retained secret storage limit reached."} }
	return nil
}
