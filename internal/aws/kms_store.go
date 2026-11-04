package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

type KMSStore struct{ db *sql.DB }
type kmsStored struct {
	metadata    KMSKeyMetadata
	material    []byte
	encryptions int64
}

func NewKMSStore(db *sql.DB) (*KMSStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_kms_keys (
 region TEXT NOT NULL,key_id TEXT NOT NULL,description TEXT NOT NULL,created REAL NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('Enabled','Disabled')),
 material BLOB NOT NULL CHECK(typeof(material)='blob' AND length(material)=32),
 encryptions INTEGER NOT NULL CHECK(encryptions>=0 AND encryptions<=4294967296),policy TEXT NOT NULL,
 PRIMARY KEY(region,key_id));`)
	return &KMSStore{db}, err
}
func kmsRead(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, region, id string) (kmsStored, error) {
	var row kmsStored
	var description, state, policy string
	var created float64
	err := q.QueryRowContext(ctx, `SELECT description,created,state,material,encryptions,policy FROM aws_kms_keys WHERE region=? AND key_id=?`, region, id).Scan(&description, &created, &state, &row.material, &row.encryptions, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return row, kmsFailure("NotFoundException", "KMS key does not exist in this local region.")
	}
	if err != nil {
		return row, err
	}
	if !kmsRegion(region) || !kmsUUID.MatchString(id) || !kmsDescription(description) || created <= 0 || math.IsNaN(created) || math.IsInf(created, 0) || (state != "Enabled" && state != "Disabled") || len(row.material) != 32 || row.encryptions < 0 || row.encryptions > kmsMaxEncryptions || policy != kmsDefaultPolicy {
		return kmsStored{}, kmsCorrupt("key state")
	}
	row.metadata = kmsMetadata(region, id, description, state, created)
	return row, nil
}
func (s *KMSStore) CreateKey(ctx context.Context, region, description string) (KMSKeyMetadata, error) {
	if !kmsRegion(region) || !kmsDescription(description) {
		return KMSKeyMetadata{}, kmsInvalid()
	}
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		return KMSKeyMetadata{}, err
	}
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		return KMSKeyMetadata{}, err
	}
	uuid[6] = (uuid[6] & 15) | 64
	uuid[8] = (uuid[8] & 63) | 128
	id := fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
	created := float64(time.Now().UnixMilli()) / 1000
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KMSKeyMetadata{}, err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_kms_keys").Scan(&count); err != nil {
		return KMSKeyMetadata{}, err
	}
	if count >= 1000 {
		return KMSKeyMetadata{}, kmsFailure("LimitExceededException", "Local KMS key limit reached.")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO aws_kms_keys VALUES(?,?,?,?,'Enabled',?,0,?)`, region, id, description, created, material, kmsDefaultPolicy)
	if err != nil {
		return KMSKeyMetadata{}, err
	}
	if err = tx.Commit(); err != nil {
		return KMSKeyMetadata{}, err
	}
	return kmsMetadata(region, id, description, "Enabled", created), nil
}
func (s *KMSStore) DescribeKey(ctx context.Context, region, id string) (KMSKeyMetadata, error) {
	parsed, err := kmsID(region, id)
	if err != nil {
		return KMSKeyMetadata{}, err
	}
	row, err := kmsRead(ctx, s.db, region, parsed)
	return row.metadata, err
}
func (s *KMSStore) SetEnabled(ctx context.Context, region, id string, enabled bool) error {
	parsed, err := kmsID(region, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = kmsRead(ctx, tx, region, parsed); err != nil {
		return err
	}
	state := "Disabled"
	if enabled {
		state = "Enabled"
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aws_kms_keys SET state=? WHERE region=? AND key_id=?", state, region, parsed); err != nil {
		return err
	}
	return tx.Commit()
}

type KMSKeyListEntry struct {
	KeyID  string `json:"KeyId"`
	KeyArn string `json:"KeyArn"`
}
type KMSKeyList struct {
	Keys       []KMSKeyListEntry `json:"Keys"`
	Truncated  bool              `json:"Truncated"`
	NextMarker string            `json:"NextMarker,omitempty"`
}

func (s *KMSStore) ListKeys(ctx context.Context, region string, limit int, marker string) (KMSKeyList, error) {
	result := KMSKeyList{Keys: []KMSKeyListEntry{}}
	if !kmsRegion(region) || limit < 1 || limit > 1000 {
		return result, kmsInvalid()
	}
	after := ""
	if marker != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(marker, "kms1."))
		parts := strings.Split(string(decoded), "\x00")
		if err != nil || !strings.HasPrefix(marker, "kms1.") || len(parts) != 2 || parts[0] != region || !kmsUUID.MatchString(parts[1]) {
			return result, kmsInvalid()
		}
		after = parts[1]
	}
	rows, err := s.db.QueryContext(ctx, "SELECT key_id FROM aws_kms_keys WHERE region=? AND key_id>? ORDER BY key_id LIMIT ?", region, after, limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return result, err
		}
		if !kmsUUID.MatchString(id) {
			return result, kmsCorrupt("key identifier")
		}
		result.Keys = append(result.Keys, KMSKeyListEntry{id, kmsARN(region, id)})
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Keys) > limit {
		result.Keys = result.Keys[:limit]
		result.Truncated = true
		result.NextMarker = "kms1." + base64.RawURLEncoding.EncodeToString([]byte(region+"\x00"+result.Keys[limit-1].KeyID))
	}
	return result, nil
}
func (s *KMSStore) Encrypt(ctx context.Context, region, id string, contextMap map[string]string, plaintext []byte) ([]byte, string, error) {
	parsed, err := kmsID(region, id)
	if err != nil {
		return nil, "", err
	}
	if len(plaintext) < 1 || len(plaintext) > 4096 {
		return nil, "", kmsInvalid()
	}
	if _, err = kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	row, err := kmsRead(ctx, tx, region, parsed)
	if err != nil {
		return nil, "", err
	}
	if !row.metadata.Enabled {
		return nil, "", kmsFailure("DisabledException", "KMS key is disabled.")
	}
	if row.encryptions >= kmsMaxEncryptions {
		return nil, "", kmsFailure("LimitExceededException", "Local key encryption limit reached.")
	}
	blob, err := kmsSeal(row.material, region, parsed, contextMap, plaintext)
	if err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aws_kms_keys SET encryptions=encryptions+1 WHERE region=? AND key_id=?", region, parsed); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return blob, row.metadata.Arn, nil
}
func (s *KMSStore) Decrypt(ctx context.Context, region, requestedID string, contextMap map[string]string, blob []byte) ([]byte, string, error) {
	blobRegion, id, encrypted, err := kmsBlob(blob)
	if err != nil {
		return nil, "", err
	}
	if blobRegion != region {
		return nil, "", kmsFailure("InvalidCiphertextException", "The local ciphertext is invalid for this request.")
	}
	if requestedID != "" {
		selected, e := kmsID(region, requestedID)
		if e != nil {
			return nil, "", e
		}
		if selected != id {
			return nil, "", kmsFailure("IncorrectKeyException", "The specified key does not match this ciphertext.")
		}
	}
	if _, err = kmsContext(contextMap); err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	row, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return nil, "", err
	}
	if !row.metadata.Enabled {
		return nil, "", kmsFailure("DisabledException", "KMS key is disabled.")
	}
	plaintext, err := kmsOpen(row.material, region, id, contextMap, encrypted)
	if err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return plaintext, row.metadata.Arn, nil
}
