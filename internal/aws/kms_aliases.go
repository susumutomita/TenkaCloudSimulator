// SPDX-License-Identifier: MIT
// Original TenkaCloudSimulator implementation of the bounded customer-alias API.
package aws

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// KMSAliasAuthorizer must be supplied by trusted request composition. The
// transaction belongs to this operation; callbacks must not commit or close it.
// Callbacks are read-only policy decisions: use their captured immutable policy
// or reads on this tx; do not mutate state or call nested public store methods.
// Wire credentials and alias names cannot choose this authority.
type KMSAliasAuthorizer func(context.Context, *sql.Tx, string, string) (bool, error)

type KMSAliasStore struct {
	db     *sql.DB
	region string
}

type KMSAlias struct {
	AliasArn        string  `json:"AliasArn"`
	AliasName       string  `json:"AliasName"`
	CreationDate    float64 `json:"CreationDate"`
	LastUpdatedDate float64 `json:"LastUpdatedDate"`
	TargetKeyId     string  `json:"TargetKeyId"`
}
type KMSAliasList struct {
	Aliases    []KMSAlias `json:"Aliases"`
	Truncated  bool       `json:"Truncated"`
	NextMarker string     `json:"NextMarker,omitempty"`
}

var kmsAliasNamePattern = regexp.MustCompile(`^alias/[a-zA-Z0-9/_-]+$`)

func kmsCustomerAlias(name string) bool {
	return len(name) <= 256 && kmsAliasNamePattern.MatchString(name) && !strings.HasPrefix(name, "alias/aws/")
}
func kmsAliasARN(region, name string) string {
	return "arn:aws:kms:" + region + ":" + kmsAccount + ":" + name
}
func kmsAliasDenied() error {
	return kmsFailure("AccessDeniedException", "Local KMS alias permission denied.")
}
func kmsAliasAuthorize(ctx context.Context, tx *sql.Tx, authorize KMSAliasAuthorizer, action string, resources ...string) error {
	if authorize == nil {
		return kmsAliasDenied()
	}
	for _, resource := range resources {
		allowed, err := authorize(ctx, tx, action, resource)
		if err != nil {
			return err
		}
		if !allowed {
			return kmsAliasDenied()
		}
	}
	return nil
}

// NewKMSAliasStore borrows the actual KMS store's database. Its additive alias
// schema never changes the key ledger or installs key-table triggers. The current
// supported key lifecycle has immutable UUIDs and creation times (no DeleteKey).
// The concrete parent FK and saved creation-time snapshot are rechecked in the
// operation transaction; arbitrary external database tampering is not modeled.
func NewKMSAliasStore(keys *KMSStore, region string) (*KMSAliasStore, error) {
	if keys == nil || keys.db == nil || !kmsRegion(region) {
		return nil, kmsInvalid()
	}
	tx, err := keys.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	s, err := newKMSAliasStoreTx(context.Background(), tx, keys, region)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

// newKMSAliasStoreTx initializes only inside the caller's transaction. HTTP
// keeps this tentative schema/cursor state in the same transaction as the actual
// resource authorization and operation; any refusal rolls everything back.
func newKMSAliasStoreTx(ctx context.Context, tx *sql.Tx, keys *KMSStore, region string) (*KMSAliasStore, error) {
	if keys == nil || keys.db == nil || !kmsRegion(region) {
		return nil, kmsInvalid()
	}
	if tx == nil {
		return nil, kmsInvalid()
	}
	var err error
	var keyTable int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_kms_keys'`).Scan(&keyTable); err != nil {
		return nil, err
	}
	if keyTable != 1 {
		return nil, kmsCorrupt("missing key ledger")
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS aws_kms_aliases (
 region TEXT NOT NULL,name TEXT NOT NULL,key_id TEXT NOT NULL,target_created REAL NOT NULL,
 created REAL NOT NULL,updated REAL NOT NULL,PRIMARY KEY(region,name),
 FOREIGN KEY(region,key_id) REFERENCES aws_kms_keys(region,key_id) ON DELETE RESTRICT);
 CREATE TABLE IF NOT EXISTS aws_kms_alias_state (
 region TEXT PRIMARY KEY,revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>=0),
 secret BLOB NOT NULL CHECK(typeof(secret)='blob' AND length(secret)=32));`)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO aws_kms_alias_state SELECT ?,0,randomblob(32) WHERE NOT EXISTS(SELECT 1 FROM aws_kms_alias_state WHERE region=?)`, region, region); err != nil {
		return nil, err
	}
	s := &KMSAliasStore{keys.db, region}
	if _, _, err = s.stateTx(ctx, tx); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *KMSAliasStore) UsesDatabase(db *sql.DB) bool { return s != nil && s.db != nil && s.db == db }
func (s *KMSAliasStore) stateTx(ctx context.Context, tx *sql.Tx) (int64, []byte, error) {
	var revision int64
	var secret []byte
	err := tx.QueryRowContext(ctx, `SELECT revision,secret FROM aws_kms_alias_state WHERE region=?`, s.region).Scan(&revision, &secret)
	if err != nil {
		return 0, nil, err
	}
	if revision < 0 || revision == math.MaxInt64 || len(secret) != 32 {
		return 0, nil, kmsCorrupt("alias cursor authority")
	}
	return revision, secret, nil
}
func (s *KMSAliasStore) changedTx(ctx context.Context, tx *sql.Tx) error {
	if _, _, err := s.stateTx(ctx, tx); err != nil {
		return err
	}
	return kmsRequireOne(tx.ExecContext(ctx, `UPDATE aws_kms_alias_state SET revision=revision+1 WHERE region=?`, s.region))
}
func (s *KMSAliasStore) keyTx(ctx context.Context, tx *sql.Tx, value string) (kmsStored, error) {
	id, err := kmsID(s.region, value)
	if err != nil {
		return kmsStored{}, err
	}
	return kmsRead(ctx, tx, s.region, id)
}
func (s *KMSAliasStore) readTx(ctx context.Context, tx *sql.Tx, name string) (KMSAlias, error) {
	var alias KMSAlias
	var targetCreated float64
	if !kmsCustomerAlias(name) {
		return alias, kmsInvalid()
	}
	err := tx.QueryRowContext(ctx, `SELECT key_id,target_created,created,updated FROM aws_kms_aliases WHERE region=? AND name=?`, s.region, name).Scan(&alias.TargetKeyId, &targetCreated, &alias.CreationDate, &alias.LastUpdatedDate)
	if errors.Is(err, sql.ErrNoRows) {
		return alias, kmsFailure("NotFoundException", "KMS alias does not exist in this local region.")
	}
	if err != nil {
		return alias, err
	}
	current, err := s.keyTx(ctx, tx, alias.TargetKeyId)
	if err != nil {
		return KMSAlias{}, err
	}
	if targetCreated != current.metadata.CreationDate || alias.CreationDate <= 0 || math.IsNaN(alias.CreationDate) || math.IsInf(alias.CreationDate, 0) || alias.LastUpdatedDate < alias.CreationDate || math.IsNaN(alias.LastUpdatedDate) || math.IsInf(alias.LastUpdatedDate, 0) {
		return KMSAlias{}, kmsCorrupt("alias state")
	}
	alias.AliasName = name
	alias.AliasArn = kmsAliasARN(s.region, name)
	return alias, nil
}
func (s *KMSAliasStore) capacityTx(ctx context.Context, tx *sql.Tx, keyID string, newAlias bool) error {
	var total, perKey int
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN region=? AND key_id=? THEN 1 ELSE 0 END),0) FROM aws_kms_aliases`, s.region, keyID).Scan(&total, &perKey); err != nil {
		return err
	}
	additional := 0
	if newAlias {
		additional = 1
	}
	if total+additional > 1000 || perKey+1 > 50 {
		return kmsFailure("LimitExceededException", "Local KMS aliases are limited to 1000 total and 50 per key.")
	}
	return nil
}

// CreateAlias uses only a concrete key ID/ARN: aliases cannot target aliases.
func (s *KMSAliasStore) CreateAlias(ctx context.Context, name, target string, authorize KMSAliasAuthorizer) error {
	if name == "" {
		return kmsInvalid()
	}
	if !kmsCustomerAlias(name) {
		return kmsFailure("InvalidAliasNameException", "The customer alias name is invalid.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.createAliasTx(ctx, tx, name, target, authorize); err != nil {
		return err
	}
	return tx.Commit()
}

// createAliasTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) createAliasTx(ctx context.Context, tx *sql.Tx, name, target string, authorize KMSAliasAuthorizer) error {
	if name == "" {
		return kmsInvalid()
	}
	if !kmsCustomerAlias(name) {
		return kmsFailure("InvalidAliasNameException", "The customer alias name is invalid.")
	}
	id, err := kmsID(s.region, target)
	if err != nil {
		return err
	}
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:CreateAlias", kmsAliasARN(s.region, name), kmsARN(s.region, id)); err != nil {
		return err
	}
	key, err := s.keyTx(ctx, tx, id)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_kms_aliases WHERE region=? AND name=?`, s.region, name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return kmsFailure("AlreadyExistsException", "KMS alias already exists in this local region.")
	}
	if err = s.capacityTx(ctx, tx, id, true); err != nil {
		return err
	}
	now := float64(time.Now().UnixMilli()) / 1000
	if err = kmsRequireOne(tx.ExecContext(ctx, `INSERT INTO aws_kms_aliases VALUES(?,?,?,?,?,?)`, s.region, name, id, key.metadata.CreationDate, now, now)); err != nil {
		return err
	}
	if err = s.changedTx(ctx, tx); err != nil {
		return err
	}
	return nil
}
func (s *KMSAliasStore) UpdateAlias(ctx context.Context, name, target string, authorize KMSAliasAuthorizer) error {
	if !kmsCustomerAlias(name) {
		return kmsInvalid()
	}
	_, err := kmsID(s.region, target)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.updateAliasTx(ctx, tx, name, target, authorize); err != nil {
		return err
	}
	return tx.Commit()
}

// updateAliasTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) updateAliasTx(ctx context.Context, tx *sql.Tx, name, target string, authorize KMSAliasAuthorizer) error {
	if !kmsCustomerAlias(name) {
		return kmsInvalid()
	}
	id, err := kmsID(s.region, target)
	if err != nil {
		return err
	}
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:UpdateAlias", kmsAliasARN(s.region, name)); err != nil {
		return err
	}
	old, err := s.readTx(ctx, tx, name)
	if err != nil {
		return err
	}
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:UpdateAlias", kmsARN(s.region, old.TargetKeyId), kmsARN(s.region, id)); err != nil {
		return err
	}
	next, err := s.keyTx(ctx, tx, id)
	if err != nil {
		return err
	}
	// Both native key types are currently SYMMETRIC_DEFAULT/ENCRYPT_DECRYPT.
	prior, err := s.keyTx(ctx, tx, old.TargetKeyId)
	if err != nil {
		return err
	}
	if prior.metadata.KeySpec != next.metadata.KeySpec || prior.metadata.KeyUsage != next.metadata.KeyUsage {
		return kmsInvalid()
	}
	if old.TargetKeyId != id {
		if err = s.capacityTx(ctx, tx, id, false); err != nil {
			return err
		}
	}
	now := float64(time.Now().UnixMilli()) / 1000
	if now < old.LastUpdatedDate {
		now = old.LastUpdatedDate
	}
	if err = kmsRequireOne(tx.ExecContext(ctx, `UPDATE aws_kms_aliases SET key_id=?,target_created=?,updated=? WHERE region=? AND name=?`, id, next.metadata.CreationDate, now, s.region, name)); err != nil {
		return err
	}
	if err = s.changedTx(ctx, tx); err != nil {
		return err
	}
	return nil
}
func (s *KMSAliasStore) DeleteAlias(ctx context.Context, name string, authorize KMSAliasAuthorizer) error {
	if !kmsCustomerAlias(name) {
		return kmsInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.deleteAliasTx(ctx, tx, name, authorize); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteAliasTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) deleteAliasTx(ctx context.Context, tx *sql.Tx, name string, authorize KMSAliasAuthorizer) error {
	if !kmsCustomerAlias(name) {
		return kmsInvalid()
	}
	var err error
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:DeleteAlias", kmsAliasARN(s.region, name)); err != nil {
		return err
	}
	alias, err := s.readTx(ctx, tx, name)
	if err != nil {
		return err
	}
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:DeleteAlias", kmsARN(s.region, alias.TargetKeyId)); err != nil {
		return err
	}
	if err = kmsRequireOne(tx.ExecContext(ctx, `DELETE FROM aws_kms_aliases WHERE region=? AND name=?`, s.region, name)); err != nil {
		return err
	}
	if err = s.changedTx(ctx, tx); err != nil {
		return err
	}
	return nil
}

// ResolveAliasTx is a trusted composition port, not an additional public API.
// The caller owns tx and MUST authorize the requested native operation on the
// returned concrete key ARN in this SAME transaction before using key material.
// This helper never commits/rolls back and does not itself grant crypto access.
func (s *KMSAliasStore) ResolveAliasTx(ctx context.Context, tx *sql.Tx, value string) (string, error) {
	if tx == nil {
		return "", kmsInvalid()
	}
	prefix := "arn:aws:kms:" + s.region + ":" + kmsAccount + ":"
	if strings.HasPrefix(value, "arn:") {
		if !strings.HasPrefix(value, prefix) {
			return "", kmsInvalid()
		}
		value = strings.TrimPrefix(value, prefix)
	}
	alias, err := s.readTx(ctx, tx, value)
	if err != nil {
		return "", err
	}
	return alias.TargetKeyId, nil
}

type kmsAliasCursor struct {
	Region   string
	Filter   string
	Limit    int
	Revision int64
	After    string
}

func kmsAliasMarker(cursor kmsAliasCursor, secret []byte) string {
	data, _ := json.Marshal(cursor)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(data)
	return "kmsa1." + base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *KMSAliasStore) ListAliases(ctx context.Context, keyID string, limit int, marker string, authorize KMSAliasAuthorizer) (KMSAliasList, error) {
	result := KMSAliasList{Aliases: []KMSAlias{}}
	if limit < 1 || limit > 100 || len(marker) > 1024 {
		return result, kmsInvalid()
	}
	// The embedded SQLite driver does not support sql.TxOptions.ReadOnly.
	// This transaction issues only SELECTs; use the established provider BeginTx.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	result, err = s.listAliasesTx(ctx, tx, keyID, limit, marker, authorize)
	if err != nil {
		return KMSAliasList{Aliases: []KMSAlias{}}, err
	}
	if err = tx.Commit(); err != nil {
		return KMSAliasList{Aliases: []KMSAlias{}}, err
	}
	return result, nil
}

// listAliasesTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) listAliasesTx(ctx context.Context, tx *sql.Tx, keyID string, limit int, marker string, authorize KMSAliasAuthorizer) (KMSAliasList, error) {
	result := KMSAliasList{Aliases: []KMSAlias{}}
	if limit < 1 || limit > 100 || len(marker) > 1024 {
		return result, kmsInvalid()
	}
	// The embedded SQLite driver does not support sql.TxOptions.ReadOnly.
	// This transaction issues only SELECTs; use the established provider BeginTx.
	var err error
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:ListAliases", "*"); err != nil {
		return result, err
	}
	filter := ""
	if keyID != "" {
		filter, err = kmsID(s.region, keyID)
		if err != nil {
			return result, err
		}
		if _, err = s.keyTx(ctx, tx, filter); err != nil {
			return result, err
		}
	}
	revision, secret, err := s.stateTx(ctx, tx)
	if err != nil {
		return result, err
	}
	after := ""
	if marker != "" {
		parts := strings.Split(marker, ".")
		var cursor kmsAliasCursor
		if len(parts) != 3 || parts[0] != "kmsa1" {
			return result, kmsFailure("InvalidMarkerException", "Invalid local KMS alias marker.")
		}
		data, e := base64.RawURLEncoding.DecodeString(parts[1])
		signature, e2 := base64.RawURLEncoding.DecodeString(parts[2])
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write(data)
		if e != nil || e2 != nil || !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(data, &cursor) != nil || cursor.Region != s.region || cursor.Filter != filter || cursor.Limit != limit || cursor.Revision != revision || !kmsCustomerAlias(cursor.After) {
			return result, kmsFailure("InvalidMarkerException", "Invalid or stale local KMS alias marker.")
		}
		after = cursor.After
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM aws_kms_aliases WHERE region=? AND name>? AND (?='' OR key_id=?) ORDER BY name LIMIT ?`, s.region, after, filter, filter, limit+1)
	if err != nil {
		return result, err
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return result, err
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(names) > limit {
		result.Truncated = true
		names = names[:limit]
		result.NextMarker = kmsAliasMarker(kmsAliasCursor{s.region, filter, limit, revision, names[len(names)-1]}, secret)
	}
	for _, name := range names {
		alias, e := s.readTx(ctx, tx, name)
		if e != nil {
			return KMSAliasList{Aliases: []KMSAlias{}}, e
		}
		result.Aliases = append(result.Aliases, alias)
	}
	return result, nil
}

// cryptoKeyTx resolves only customer aliases or concrete key IDs/ARNs. This is
// identifier selection, not an authorization grant or a separate DescribeKey API.
func (s *KMSAliasStore) cryptoKeyTx(ctx context.Context, tx *sql.Tx, selected string) (string, error) {
	if strings.HasPrefix(selected, "alias/") || strings.Contains(selected, ":alias/") {
		return s.ResolveAliasTx(ctx, tx, selected)
	}
	return kmsID(s.region, selected)
}

// Encrypt keeps alias selection, concrete-key permission, material read, counter
// acknowledgement and final publication in one transaction. The authorizer must
// use its immutable caller policy or the provided tx, never start another DB tx.
func (s *KMSAliasStore) Encrypt(ctx context.Context, selected string, contextMap map[string]string, plaintext []byte, authorize KMSAliasAuthorizer) ([]byte, string, error) {
	if len(plaintext) < 1 || len(plaintext) > 4096 {
		return nil, "", kmsInvalid()
	}
	if _, err := kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	blob, arn, err := s.encryptAliasTx(ctx, tx, selected, contextMap, plaintext, authorize)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return blob, arn, nil
}

// encryptAliasTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) encryptAliasTx(ctx context.Context, tx *sql.Tx, selected string, contextMap map[string]string, plaintext []byte, authorize KMSAliasAuthorizer) ([]byte, string, error) {
	if len(plaintext) < 1 || len(plaintext) > 4096 {
		return nil, "", kmsInvalid()
	}
	if _, err := kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	var err error
	id, err := s.cryptoKeyTx(ctx, tx, selected)
	if err != nil {
		return nil, "", err
	}
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:Encrypt", kmsARN(s.region, id)); err != nil {
		return nil, "", err
	}
	blob, arn, err := kmsEncryptTx(ctx, tx, s.region, id, contextMap, plaintext)
	if err != nil {
		return nil, "", err
	}
	return blob, arn, nil
}

// Decrypt's omitted KeyId selects the ciphertext UUID, never the alias's current
// target. An explicit alias must still point to that UUID after resolution; old
// ciphertext does not silently switch keys when an alias is retargeted.
func (s *KMSAliasStore) Decrypt(ctx context.Context, selected string, contextMap map[string]string, blob []byte, authorize KMSAliasAuthorizer) ([]byte, string, error) {
	region, _, _, err := kmsBlob(blob)
	if err != nil {
		return nil, "", err
	}
	if region != s.region {
		return nil, "", kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request.")
	}
	if _, err = kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	plaintext, arn, err := s.decryptAliasTx(ctx, tx, selected, contextMap, blob, authorize)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return plaintext, arn, nil
}

// decryptAliasTx borrows tx; it never begins, commits or rolls back it.
func (s *KMSAliasStore) decryptAliasTx(ctx context.Context, tx *sql.Tx, selected string, contextMap map[string]string, blob []byte, authorize KMSAliasAuthorizer) ([]byte, string, error) {
	region, id, encrypted, err := kmsBlob(blob)
	if err != nil {
		return nil, "", err
	}
	if region != s.region {
		return nil, "", kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request.")
	}
	if _, err = kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	requested := id
	if selected != "" {
		requested, err = s.cryptoKeyTx(ctx, tx, selected)
		if err != nil {
			return nil, "", err
		}
	}
	// Match native selected-key authorization before a mismatch response. An
	// unauthorized caller must not learn whether an alias matches ciphertext.
	if err = kmsAliasAuthorize(ctx, tx, authorize, "kms:Decrypt", kmsARN(s.region, requested)); err != nil {
		return nil, "", err
	}
	if requested != id {
		return nil, "", kmsFailure("IncorrectKeyException", "The specified key does not match this ciphertext.")
	}
	plaintext, arn, err := kmsDecryptTx(ctx, tx, s.region, id, contextMap, encrypted)
	if err != nil {
		return nil, "", err
	}
	return plaintext, arn, nil
}

// serveAliasRequest is the bounded native JSON1.1 composition hook. Existing
// concrete-key operations retain their old path. The authorizer is the native
// request's already-captured immutable policy, never a fresh identities DB read.
func (s *KMSStore) serveAliasRequest(w http.ResponseWriter, r *http.Request, region, op string, request kmsRequest, authorize func(string, string) bool) bool {
	control := op == "CreateAlias" || op == "UpdateAlias" || op == "DeleteAlias" || op == "ListAliases"
	aliasCrypto := (op == "Encrypt" || op == "Decrypt") && (strings.HasPrefix(request.KeyId, "alias/") || strings.Contains(request.KeyId, ":alias/"))
	if !control && !aliasCrypto {
		return false
	}
	if s == nil || s.db == nil {
		kmsWriteError(w, kmsInternal())
		return true
	}
	if authorize == nil {
		kmsWriteError(w, kmsAliasDenied())
		return true
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	defer tx.Rollback()
	aliases, err := newKMSAliasStoreTx(r.Context(), tx, s, region)
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	auth := func(_ context.Context, _ *sql.Tx, action, resource string) (bool, error) {
		return authorize(action, resource), nil
	}
	var response any
	switch op {
	case "CreateAlias":
		err = aliases.createAliasTx(r.Context(), tx, request.AliasName, request.TargetKeyId, auth)
	case "UpdateAlias":
		err = aliases.updateAliasTx(r.Context(), tx, request.AliasName, request.TargetKeyId, auth)
	case "DeleteAlias":
		err = aliases.deleteAliasTx(r.Context(), tx, request.AliasName, auth)
	case "ListAliases":
		response, err = aliases.listAliasesTx(r.Context(), tx, request.KeyId, request.Limit, request.Marker, auth)
	case "Encrypt":
		var blob []byte
		var arn string
		blob, arn, err = aliases.encryptAliasTx(r.Context(), tx, request.KeyId, request.EncryptionContext, request.Plaintext, auth)
		response = map[string]any{"CiphertextBlob": blob, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
	case "Decrypt":
		var plaintext []byte
		var arn string
		plaintext, arn, err = aliases.decryptAliasTx(r.Context(), tx, request.KeyId, request.EncryptionContext, request.CiphertextBlob, auth)
		response = map[string]any{"Plaintext": plaintext, "KeyId": arn, "EncryptionAlgorithm": "SYMMETRIC_DEFAULT"}
	}
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	if err = tx.Commit(); err != nil {
		kmsWriteError(w, err)
		return true
	}
	kmsJSON(w, http.StatusOK, response)
	return true
}
