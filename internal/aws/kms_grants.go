package aws

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const kmsGrantSchema = `CREATE TABLE IF NOT EXISTS aws_kms_grants (
 region TEXT NOT NULL, key_id TEXT NOT NULL, grant_id TEXT NOT NULL, role_arn TEXT NOT NULL,
 role_id TEXT NOT NULL, name TEXT NOT NULL, created REAL NOT NULL,
 PRIMARY KEY(region,key_id,grant_id), FOREIGN KEY(region,key_id) REFERENCES aws_kms_keys(region,key_id));
 CREATE UNIQUE INDEX IF NOT EXISTS aws_kms_named_grants ON aws_kms_grants(region,key_id,role_id,name) WHERE name<>'';
 CREATE TABLE IF NOT EXISTS aws_kms_grant_signer (singleton INTEGER PRIMARY KEY CHECK(singleton=1),secret BLOB NOT NULL CHECK(length(secret)=32));
 INSERT OR IGNORE INTO aws_kms_grant_signer(singleton,secret) VALUES(1,randomblob(32));`

const kmsGrantRolePrefix = "arn:aws:iam::" + kmsAccount + ":role/"

var kmsGrantName = regexp.MustCompile(`^[a-zA-Z0-9:/_-]{1,256}$`)
var kmsGrantID = regexp.MustCompile(`^[0-9a-f]{64}$`)

type kmsGrantEntry struct {
	KeyId            string
	GrantId          string
	Name             string `json:",omitempty"`
	CreationDate     float64
	GranteePrincipal string
	IssuingAccount   string
	Operations       []string
}

type kmsGrantPage struct {
	Region, Key, FilterID, Grantee, After string
	Limit                                 int
}

type kmsGrantToken struct{ Region, Key, Grant, Nonce string }

func kmsGrantRoleName(arn string) (string, bool) {
	name, ok := strings.CutPrefix(arn, kmsGrantRolePrefix)
	return name, ok && iamRoleName.MatchString(name)
}

func kmsSignedDescribeRequest(r *http.Request, key string) bool {
	return strings.HasPrefix(key, "TCSIMASIA") && r.Method == http.MethodPost && r.URL.Path == "/" && r.URL.RawPath == "" && r.URL.RawQuery == "" && !r.URL.ForceQuery &&
		len(r.Header.Values("X-Amz-Target")) == 1 && r.Header.Get("X-Amz-Target") == "TrentService.DescribeKey" &&
		len(r.Header.Values("Content-Type")) == 1 && r.Header.Get("Content-Type") == "application/x-amz-json-1.1"
}

func kmsGrantSecret(ctx context.Context, tx *sql.Tx) ([]byte, error) {
	var secret []byte
	if err := tx.QueryRowContext(ctx, "SELECT secret FROM aws_kms_grant_signer WHERE singleton=1").Scan(&secret); err != nil {
		return nil, err
	}
	if len(secret) != 32 {
		return nil, kmsCorrupt("grant signer")
	}
	return secret, nil
}

// Grant tokens and pagination markers use disjoint purpose strings with a
// per-team secret. A grant token alone never identifies or authorizes its holder.
func kmsGrantSeal(secret []byte, purpose string, value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload := purpose + "." + base64.RawURLEncoding.EncodeToString(encoded)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func kmsGrantUnseal(secret []byte, purpose, token string, value any) bool {
	prefix, data, ok := strings.Cut(token, ".")
	raw, signature, found := strings.Cut(data, ".")
	if !ok || !found || prefix != purpose {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(prefix + "." + raw))
	check, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || !hmac.Equal(mac.Sum(nil), check) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	return err == nil && json.Unmarshal(decoded, value) == nil
}

func kmsCreateGrant(ctx context.Context, tx *sql.Tx, region, id string, request kmsRequest) (any, error) {
	roleName, ok := kmsGrantRoleName(request.GranteePrincipal)
	if !ok || len(request.Operations) != 1 || request.Operations[0] != "DescribeKey" || request.Name != "" && !kmsGrantName.MatchString(request.Name) {
		return nil, kmsInvalid()
	}
	key, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return nil, err
	}
	if !key.metadata.Enabled {
		return nil, kmsFailure("DisabledException", "The KMS key is disabled.")
	}
	role, err := iamReadRole(ctx, tx, roleName, false)
	var roleError ssmFailure
	if errors.As(err, &roleError) && roleError.code == "NoSuchEntity" || err == nil && role.Arn != request.GranteePrincipal {
		return nil, kmsFailure("NotFoundException", "The grantee role does not exist.")
	}
	if err != nil {
		return nil, err
	}
	grantID := ""
	if request.Name != "" {
		err = tx.QueryRowContext(ctx, `SELECT grant_id FROM aws_kms_grants WHERE region=? AND key_id=? AND role_id=? AND name=?`, region, id, role.RoleID, request.Name).Scan(&grantID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if grantID == "" {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_kms_grants WHERE region=? AND key_id=?", region, id).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 50000 {
			return nil, kmsFailure("LimitExceededException", "The KMS grants-per-key limit was reached.")
		}
		grantID, err = randomCredential(32)
		if err != nil {
			return nil, err
		}
		err = kmsRequireOne(tx.ExecContext(ctx, `INSERT INTO aws_kms_grants(region,key_id,grant_id,role_arn,role_id,name,created) VALUES(?,?,?,?,?,?,?)`, region, id, grantID, role.Arn, role.RoleID, request.Name, float64(time.Now().UnixMilli())/1000))
		if err != nil {
			return nil, err
		}
	}
	secret, err := kmsGrantSecret(ctx, tx)
	if err != nil {
		return nil, err
	}
	token, err := kmsGrantSeal(secret, "kmsg1", kmsGrantToken{Region: region, Key: id, Grant: grantID, Nonce: rand.Text()})
	if err != nil {
		return nil, err
	}
	return map[string]string{"GrantId": grantID, "GrantToken": token}, nil
}

func kmsListGrants(ctx context.Context, tx *sql.Tx, region, id string, request kmsRequest) (any, error) {
	if request.GrantId != "" && !kmsGrantID.MatchString(request.GrantId) {
		return nil, kmsFailure("InvalidGrantIdException", "Invalid local grant identifier.")
	}
	if _, ok := kmsGrantRoleName(request.GranteePrincipal); request.GranteePrincipal != "" && !ok {
		return nil, kmsInvalid()
	}
	if _, err := kmsRead(ctx, tx, region, id); err != nil {
		return nil, err
	}
	page := kmsGrantPage{Region: region, Key: id, Grantee: request.GranteePrincipal, FilterID: request.GrantId, Limit: request.Limit}
	secret, err := kmsGrantSecret(ctx, tx)
	if err != nil {
		return nil, err
	}
	if request.Marker != "" {
		var saved kmsGrantPage
		if !kmsGrantUnseal(secret, "kmsgp1", request.Marker, &saved) || !kmsGrantID.MatchString(saved.After) {
			return nil, kmsFailure("InvalidMarkerException", "Invalid grant page marker.")
		}
		page.After = saved.After
		if saved != page {
			return nil, kmsFailure("InvalidMarkerException", "Invalid grant page marker.")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT grant_id,name,created,role_arn FROM aws_kms_grants WHERE region=? AND key_id=? AND grant_id>? AND (?='' OR grant_id=?) AND (?='' OR role_arn=?) ORDER BY grant_id LIMIT ?`, region, id, page.After, page.FilterID, page.FilterID, page.Grantee, page.Grantee, page.Limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []kmsGrantEntry{}
	for rows.Next() {
		item := kmsGrantEntry{KeyId: kmsARN(region, id), IssuingAccount: "arn:aws:iam::" + kmsAccount + ":root", Operations: []string{"DescribeKey"}}
		if err := rows.Scan(&item.GrantId, &item.Name, &item.CreationDate, &item.GranteePrincipal); err != nil {
			return nil, err
		}
		grants = append(grants, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]any{"Grants": grants, "Truncated": false}
	if len(grants) > page.Limit {
		page.After = grants[page.Limit-1].GrantId
		marker, err := kmsGrantSeal(secret, "kmsgp1", page)
		if err != nil {
			return nil, err
		}
		result = map[string]any{"Grants": grants[:page.Limit], "Truncated": true, "NextMarker": marker}
	}
	return result, nil
}

func kmsRevokeGrant(ctx context.Context, tx *sql.Tx, region, id, grantID string) (any, error) {
	if !kmsGrantID.MatchString(grantID) {
		return nil, kmsFailure("InvalidGrantIdException", "Invalid local grant identifier.")
	}
	if _, err := kmsRead(ctx, tx, region, id); err != nil {
		return nil, err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_kms_grants WHERE region=? AND key_id=? AND grant_id=?)", region, id, grantID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, kmsFailure("NotFoundException", "The grant does not exist on this KMS key.")
	}
	if err := kmsRequireOne(tx.ExecContext(ctx, "DELETE FROM aws_kms_grants WHERE region=? AND key_id=? AND grant_id=?", region, id, grantID)); err != nil {
		return nil, err
	}
	return nil, nil
}

func kmsGrantDenied() error {
	return kmsFailure("AccessDeniedException", "The caller cannot describe this KMS key.")
}

func kmsDescribeAsRole(w http.ResponseWriter, r *http.Request, tx *sql.Tx, region, id string, request kmsRequest) (any, error) {
	ctx := r.Context()
	w.Header().Set("X-Simulator-IAM-Decision", "implicitDeny")
	identity, auth := SDKIdentityFromContext(ctx)
	policy, known := SDKPolicyFromContext(ctx)
	roleID, session, ok := strings.Cut(identity.UserID, ":")
	if !auth || !known || !ok || roleID == "" || session == "" || identity.Account != kmsAccount || identity.Expires.IsZero() || identity.ARN != "arn:aws:sts::"+kmsAccount+":assumed-role/"+identity.Principal+"/"+session {
		return nil, kmsGrantDenied()
	}
	decision := core.Evaluate(policy, "kms:DescribeKey", kmsARN(region, id))
	if decision == "explicitDeny" {
		w.Header().Set("X-Simulator-IAM-Decision", "explicitDeny")
		return nil, kmsGrantDenied()
	}
	role, err := iamReadRole(ctx, tx, identity.Principal, false)
	var roleError ssmFailure
	if errors.As(err, &roleError) && roleError.code == "NoSuchEntity" || err == nil && role.RoleID != roleID {
		return nil, kmsGrantDenied()
	}
	if err != nil {
		return nil, err
	}
	if len(request.GrantTokens) > 10 {
		return nil, kmsInvalid()
	}
	if len(request.GrantTokens) > 0 {
		secret, err := kmsGrantSecret(ctx, tx)
		if err != nil {
			return nil, err
		}
		for _, encoded := range request.GrantTokens {
			var token kmsGrantToken
			if len(encoded) > 8192 || !kmsGrantUnseal(secret, "kmsg1", encoded, &token) || token.Region != region || token.Key != id {
				return nil, kmsFailure("InvalidGrantTokenException", "Invalid local grant token.")
			}
			var live int
			err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_kms_grants WHERE region=? AND key_id=? AND role_id=? AND grant_id=?", region, id, roleID, token.Grant).Scan(&live)
			if err != nil {
				return nil, err
			}
			if live != 1 {
				return nil, kmsGrantDenied()
			}
		}
	}
	if decision != "allowed" {
		if decision != "implicitDeny" {
			return nil, kmsGrantDenied()
		}
		var live bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_kms_grants WHERE region=? AND key_id=? AND role_id=?)", region, id, roleID).Scan(&live); err != nil {
			return nil, err
		}
		if !live {
			return nil, kmsGrantDenied()
		}
	}
	w.Header().Set("X-Simulator-IAM-Decision", "allowed")
	key, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyMetadata": key.metadata}, nil
}

func (s *KMSStore) serveGrantRequest(w http.ResponseWriter, r *http.Request, region, op string, request kmsRequest, authorize func(string, string) bool) bool {
	_, signed := SDKIdentityFromContext(r.Context())
	if op == "DescribeKey" && !signed && len(request.GrantTokens) > 0 {
		kmsWriteError(w, kmsInvalid())
		return true
	}
	if !signed && op != "CreateGrant" && op != "ListGrants" && op != "RevokeGrant" {
		return false
	}
	if signed && op != "DescribeKey" {
		kmsWriteError(w, kmsGrantDenied())
		return true
	}
	id, err := kmsID(region, request.KeyId)
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	if !signed && (authorize == nil || !authorize("kms:"+op, kmsARN(region, id))) {
		kmsWriteError(w, kmsFailure("AccessDeniedException", "The caller cannot manage grants on this KMS key."))
		return true
	}
	ctx := r.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	defer tx.Rollback()
	var result any
	switch op {
	case "CreateGrant":
		result, err = kmsCreateGrant(ctx, tx, region, id, request)
	case "ListGrants":
		result, err = kmsListGrants(ctx, tx, region, id, request)
	case "RevokeGrant":
		result, err = kmsRevokeGrant(ctx, tx, region, id, request.GrantId)
	case "DescribeKey":
		result, err = kmsDescribeAsRole(w, r, tx, region, id, request)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		kmsWriteError(w, err)
		return true
	}
	kmsJSON(w, http.StatusOK, result)
	return true
}
