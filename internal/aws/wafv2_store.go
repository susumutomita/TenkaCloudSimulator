package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type WAFStore struct{ db *sql.DB }

func NewWAFStore(db *sql.DB) (*WAFStore, error) {
	var apiSchema int
	if e := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='aws_apigateway_stages'").Scan(&apiSchema); e != nil {
		return nil, e
	}
	if apiSchema != 1 {
		return nil, fmt.Errorf("initialize API Gateway state before WAFv2 state")
	}

	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS aws_wafv2_acls(region TEXT NOT NULL,id TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,lock_token TEXT NOT NULL,PRIMARY KEY(region,id),UNIQUE(region,name));
 CREATE TABLE IF NOT EXISTS aws_wafv2_tags(region TEXT NOT NULL,acl_id TEXT NOT NULL,key TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(region,acl_id,key),FOREIGN KEY(region,acl_id) REFERENCES aws_wafv2_acls(region,id) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS aws_wafv2_associations(region TEXT NOT NULL,resource_arn TEXT NOT NULL,acl_id TEXT NOT NULL,api_id TEXT NOT NULL,stage_name TEXT NOT NULL,PRIMARY KEY(region,resource_arn),FOREIGN KEY(region,acl_id) REFERENCES aws_wafv2_acls(region,id),FOREIGN KEY(region,api_id,stage_name) REFERENCES aws_apigateway_stages(region,api_id,name) ON DELETE RESTRICT);`)
	if e != nil {
		return nil, e
	}
	return &WAFStore{db}, nil
}
func wafNewID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

type wafQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func wafRead(ctx context.Context, q wafQuery, region, id, name string) (WAFWebACL, string, error) {
	var data, token string
	var acl WAFWebACL
	e := q.QueryRowContext(ctx, "SELECT document,lock_token FROM aws_wafv2_acls WHERE region=? AND id=? AND name=?", region, id, name).Scan(&data, &token)
	if errors.Is(e, sql.ErrNoRows) {
		return acl, "", ssmFailure{"WAFNonexistentItemException", "Web ACL does not exist in this region."}
	}
	if e != nil {
		return acl, "", e
	}
	if e = wafDecode([]byte(data), &acl); e != nil {
		return acl, "", fmt.Errorf("invalid persisted WAF document: %w", e)
	}
	if acl.ID != id || acl.Name != name || acl.ARN != wafARN(region, name, id) || !wafUUID.MatchString(token) {
		return acl, "", fmt.Errorf("invalid persisted WAF metadata")
	}
	if e = wafValidate(region, &acl); e != nil {
		return acl, "", fmt.Errorf("invalid persisted WAF rules: %w", e)
	}
	return acl, token, nil
}
func (s *WAFStore) Get(ctx context.Context, region, id, name string) (WAFWebACL, string, error) {
	return wafRead(ctx, s.db, region, id, name)
}
func wafSummary(a WAFWebACL, token string) WAFSummary {
	return WAFSummary{a.Name, a.ID, a.ARN, a.Description, token}
}

// CreateWebACLTx is transaction-aware so CloudFormation shares the same state/validation.
func CreateWebACLTx(ctx context.Context, tx *sql.Tx, region string, acl WAFWebACL) (WAFSummary, error) {
	id, e := wafNewID()
	if e != nil {
		return WAFSummary{}, e
	}
	return CreateWebACLWithIDTx(ctx, tx, region, acl, id)
}

// CreateWebACLWithIDTx is a trusted provisioning seam for IDs resolved before
// a host-owned transaction. Native CreateWebACL never accepts a caller's Id.
func CreateWebACLWithIDTx(ctx context.Context, tx *sql.Tx, region string, acl WAFWebACL, id string) (WAFSummary, error) {
	if !wafUUID.MatchString(id) {
		return WAFSummary{}, wafInvalid("Provisioning requires a valid preallocated Web ACL UUID.")
	}

	if e := wafValidate(region, &acl); e != nil {
		return WAFSummary{}, e
	}
	var n int
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_wafv2_acls WHERE region=? AND (name=? OR id=?)", region, acl.Name, id).Scan(&n); e != nil {
		return WAFSummary{}, e
	}
	if n != 0 {
		return WAFSummary{}, ssmFailure{"WAFDuplicateItemException", "A Web ACL with this name or Id already exists."}
	}
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_wafv2_acls").Scan(&n); e != nil {
		return WAFSummary{}, e
	}
	if n >= 100 {
		return WAFSummary{}, ssmFailure{"WAFLimitsExceededException", "At most 100 Web ACLs are supported per process."}
	}
	token, e := wafNewID()
	if e != nil {
		return WAFSummary{}, e
	}
	acl.ID = id
	acl.ARN = wafARN(region, acl.Name, id)
	if acl.Rules == nil {
		acl.Rules = []WAFRule{}
	}
	data, e := json.Marshal(acl)
	if e != nil {
		return WAFSummary{}, e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO aws_wafv2_acls(region,id,name,document,lock_token) VALUES(?,?,?,?,?)", region, id, acl.Name, string(data), token)
	if e != nil {
		return WAFSummary{}, e
	}
	for _, tag := range acl.Tags {
		if _, e = tx.ExecContext(ctx, "INSERT INTO aws_wafv2_tags(region,acl_id,key,value) VALUES(?,?,?,?)", region, id, tag.Key, tag.Value); e != nil {
			return WAFSummary{}, e
		}
	}
	return wafSummary(acl, token), nil
}
func (s *WAFStore) Create(ctx context.Context, region string, acl WAFWebACL) (WAFSummary, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return WAFSummary{}, e
	}
	defer tx.Rollback()
	a, e := CreateWebACLTx(ctx, tx, region, acl)
	if e != nil {
		return a, e
	}
	return a, tx.Commit()
}
func (s *WAFStore) Update(ctx context.Context, region string, acl WAFWebACL, token string) (string, error) {
	if len(acl.Tags) > 0 {
		return "", wafInvalid("Tags are immutable in this simulator; UpdateWebACL does not accept Tags.")
	}
	if e := wafValidate(region, &acl); e != nil {
		return "", e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	old, lock, e := wafRead(ctx, tx, region, acl.ID, acl.Name)
	if e != nil {
		return "", e
	}
	if token != lock {
		return "", ssmFailure{"WAFOptimisticLockException", "Retrieve the current LockToken before modifying this Web ACL."}
	}
	acl.ARN = old.ARN
	if acl.Rules == nil {
		acl.Rules = []WAFRule{}
	}
	data, e := json.Marshal(acl)
	if e != nil {
		return "", e
	}
	next, e := wafNewID()
	if e != nil {
		return "", e
	}
	result, e := tx.ExecContext(ctx, "UPDATE aws_wafv2_acls SET document=?,lock_token=? WHERE region=? AND id=? AND lock_token=?", string(data), next, region, acl.ID, token)
	if e != nil {
		return "", e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return "", e
	}
	if n != 1 {
		return "", ssmFailure{"WAFOptimisticLockException", "Web ACL changed concurrently."}
	}
	return next, tx.Commit()
}
func DeleteWebACLTx(ctx context.Context, tx *sql.Tx, region, id, name, token string) error {
	_, lock, e := wafRead(ctx, tx, region, id, name)
	if e != nil {
		return e
	}
	if token != lock {
		return ssmFailure{"WAFOptimisticLockException", "Retrieve the current LockToken before deleting this Web ACL."}
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_wafv2_associations WHERE region=? AND acl_id=?", region, id).Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return ssmFailure{"WAFAssociatedItemException", "Disassociate the Web ACL before deleting it."}
	}
	_, e = tx.ExecContext(ctx, "DELETE FROM aws_wafv2_acls WHERE region=? AND id=? AND lock_token=?", region, id, token)
	return e
}
func (s *WAFStore) Delete(ctx context.Context, region, id, name, token string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = DeleteWebACLTx(ctx, tx, region, id, name, token); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *WAFStore) List(ctx context.Context, region, marker string, limit int) ([]WAFSummary, error) {
	rows, e := s.db.QueryContext(ctx, "SELECT document,lock_token FROM aws_wafv2_acls WHERE region=? AND id>? ORDER BY id LIMIT ?", region, marker, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []WAFSummary{}
	for rows.Next() {
		var data, token string
		if e = rows.Scan(&data, &token); e != nil {
			return nil, e
		}
		var a WAFWebACL
		if e = wafDecode([]byte(data), &a); e != nil {
			return nil, e
		}
		if e = wafValidate(region, &a); e != nil {
			return nil, e
		}
		if a.ARN != wafARN(region, a.Name, a.ID) || !wafUUID.MatchString(token) {
			return nil, fmt.Errorf("invalid persisted WAF summary")
		}
		out = append(out, wafSummary(a, token))
	}
	return out, rows.Err()
}

var wafStage = regexp.MustCompile(`^arn:aws:apigateway:([a-z0-9-]+)::/restapis/([a-z0-9]{10})/stages/([A-Za-z0-9_-]{1,128})$`)

func wafStageParts(region, arn string) (string, string, error) {
	p := wafStage.FindStringSubmatch(arn)
	if p == nil || p[1] != region {
		return "", "", wafInvalid("Only same-region API Gateway REST stage ARNs are supported.")
	}
	return p[2], p[3], nil
}
func wafACLParts(region, arn string) (string, string, error) {
	prefix := "arn:aws:wafv2:" + region + ":000000000000:regional/webacl/"
	if !strings.HasPrefix(arn, prefix) {
		return "", "", wafInvalid("Invalid regional Web ACL ARN.")
	}
	p := strings.Split(strings.TrimPrefix(arn, prefix), "/")
	if len(p) != 2 || !wafName.MatchString(p[0]) || !wafUUID.MatchString(p[1]) {
		return "", "", wafInvalid("Invalid regional Web ACL ARN.")
	}
	return p[1], p[0], nil
}
func AssociateWebACLTx(ctx context.Context, tx *sql.Tx, region, resource, arn string) error {
	api, stage, e := wafStageParts(region, resource)
	if e != nil {
		return e
	}
	id, name, e := wafACLParts(region, arn)
	if e != nil {
		return e
	}
	if _, _, e = wafRead(ctx, tx, region, id, name); e != nil {
		return e
	}
	var n int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_apigateway_stages WHERE region=? AND api_id=? AND name=?", region, api, stage).Scan(&n); e != nil {
		return e
	}
	if n != 1 {
		return ssmFailure{"WAFNonexistentItemException", "API Gateway stage does not exist."}
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO aws_wafv2_associations(region,resource_arn,acl_id,api_id,stage_name) VALUES(?,?,?,?,?) ON CONFLICT(region,resource_arn) DO UPDATE SET acl_id=excluded.acl_id", region, resource, id, api, stage)
	return e
}
func (s *WAFStore) Associate(ctx context.Context, region, resource, arn string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = AssociateWebACLTx(ctx, tx, region, resource, arn); e != nil {
		return e
	}
	return tx.Commit()
}
func DisassociateWebACLTx(ctx context.Context, tx *sql.Tx, region, resource string) error {
	if _, _, e := wafStageParts(region, resource); e != nil {
		return e
	}
	_, e := tx.ExecContext(ctx, "DELETE FROM aws_wafv2_associations WHERE region=? AND resource_arn=?", region, resource)
	return e
}
func (s *WAFStore) Disassociate(ctx context.Context, region, resource string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = DisassociateWebACLTx(ctx, tx, region, resource); e != nil {
		return e
	}
	return tx.Commit()
}

// wafForResource reads association and ACL bytes through the same transaction.
func wafForResource(ctx context.Context, q wafQuery, region, resource string) (*WAFWebACL, error) {
	if _, _, e := wafStageParts(region, resource); e != nil {
		return nil, e
	}
	var name, id string
	e := q.QueryRowContext(ctx, "SELECT a.id,a.name FROM aws_wafv2_associations b JOIN aws_wafv2_acls a ON a.region=b.region AND a.id=b.acl_id WHERE b.region=? AND b.resource_arn=?", region, resource).Scan(&id, &name)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	acl, _, e := wafRead(ctx, q, region, id, name)
	if e != nil {
		return nil, e
	}
	return &acl, nil
}

func (s *WAFStore) ForResource(ctx context.Context, region, resource string) (*WAFWebACL, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	acl, e := wafForResource(ctx, tx, region, resource)
	if e != nil {
		return nil, e
	}
	return acl, tx.Commit()
}

// resourceOperation binds the checked ACL and the returned/deleted association
// to one immediate transaction. A concurrent reassociation cannot borrow an
// authorization decision made for another Web ACL.
func (s *WAFStore) resourceOperation(ctx context.Context, region, resource string, remove bool, authorize func(string, string) bool) (*WAFWebACL, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	acl, e := wafForResource(ctx, tx, region, resource)
	if e != nil {
		return nil, e
	}
	arn, operation := "*", "wafv2:GetWebACLForResource"
	if acl != nil {
		arn = acl.ARN
	}
	if remove {
		operation = "wafv2:DisassociateWebACL"
	}
	if authorize == nil || !authorize(operation, arn) {
		return nil, ssmFailure{"AccessDeniedException", "WAF action is not allowed."}
	}
	if remove {
		if !authorize("apigateway:SetWebACL", resource) {
			return nil, ssmFailure{"AccessDeniedException", "API Gateway stage association is not allowed."}
		}
		if e = DisassociateWebACLTx(ctx, tx, region, resource); e != nil {
			return nil, e
		}
	} else if !authorize("wafv2:GetWebACL", arn) {
		return nil, ssmFailure{"AccessDeniedException", "Web ACL read is not allowed."}
	}
	return acl, tx.Commit()
}

func (s *WAFStore) Resources(ctx context.Context, region, arn string) ([]string, error) {
	id, name, e := wafACLParts(region, arn)
	if e != nil {
		return nil, e
	}
	if _, _, e = s.Get(ctx, region, id, name); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT resource_arn FROM aws_wafv2_associations WHERE region=? AND acl_id=? ORDER BY resource_arn", region, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if e = rows.Scan(&a); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *WAFStore) Tags(ctx context.Context, region, arn string) ([]WAFTag, error) {
	id, name, e := wafACLParts(region, arn)
	if e != nil {
		return nil, e
	}
	if _, _, e = s.Get(ctx, region, id, name); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, "SELECT key,value FROM aws_wafv2_tags WHERE region=? AND acl_id=? ORDER BY key", region, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	tags := []WAFTag{}
	for rows.Next() {
		var t WAFTag
		if e = rows.Scan(&t.Key, &t.Value); e != nil {
			return nil, e
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

// GetWebACLTx reads and validates the native state inside a host-owned transaction.
func GetWebACLTx(ctx context.Context, tx *sql.Tx, region, id, name string) (WAFWebACL, string, error) {
	return wafRead(ctx, tx, region, id, name)
}
