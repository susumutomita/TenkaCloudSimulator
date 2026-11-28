package catalog

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// The local history retains at most this many requests per team database.
// An unreviewed request does not change a B2BI profile's enforced capacity.
const maxQuotaRequestRecords = 1000

func (e *Engine) initQuotaRequestHistory() error {
	if _, err := rand.Read(e.quotaHistoryKey[:]); err != nil {
		return err
	}
	tx, err := e.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS aws_b2bi_profile_quota_requests(
		seq INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE, region TEXT NOT NULL,
		requester TEXT NOT NULL, desired REAL NOT NULL CHECK(desired>0 AND desired<=10000000000),
		status TEXT NOT NULL CHECK(status IN ('PENDING','NOT_APPROVED')), created_ms INTEGER NOT NULL);
		CREATE UNIQUE INDEX IF NOT EXISTS aws_b2bi_profile_quota_pending
			ON aws_b2bi_profile_quota_requests(region) WHERE status='PENDING';`); err != nil {
		return err
	}
	return tx.Commit()
}

type quotaIncrease struct {
	seq                   int64
	id, requester, status string
	desired               float64
	createdMS             int64
}

const quotaIncreaseColumns = "seq,id,requester,status,desired,created_ms"

func (rec *quotaIncrease) scan(row interface{ Scan(...any) error }) error {
	return row.Scan(&rec.seq, &rec.id, &rec.requester, &rec.status, &rec.desired, &rec.createdMS)
}

func (q *request) quotaIncreaseOutput(rec quotaIncrease) map[string]any {
	when := float64(rec.createdMS) / 1000
	return map[string]any{
		"Id": rec.id, "Requester": rec.requester, "ServiceCode": "b2bi", "QuotaCode": b2biProfilesQuota,
		"QuotaName": "Profiles", "QuotaArn": q.profileQuotaARN(),
		"GlobalQuota": false, "DesiredValue": rec.desired, "Created": when, "LastUpdated": when,
		"Status": rec.status, "QuotaRequestedAtLevel": "ACCOUNT",
	}
}

func (q *request) supportedIncreaseQuota() bool {
	return q.input["ServiceCode"] == "b2bi" && q.input["QuotaCode"] == b2biProfilesQuota &&
		b2biQuotaRegion(q.region) && q.e.b2biProfileLimit() > 0
}

func (q *request) requestQuotaIncrease() (map[string]any, error) {
	if err := q.authorize(q.formatARN(q.resource(), "", "", nil)); err != nil {
		return nil, err
	}
	if !q.supportedIncreaseQuota() {
		return nil, &fault{kind: "notfound", message: "The requested quota is unavailable in this Region."}
	}
	if _, ok := q.input["ContextId"]; ok {
		return nil, invalid("This quota accepts account-level requests only.")
	}
	desired := q.input["DesiredValue"].(float64)
	if desired <= float64(q.e.b2biProfileLimit()) {
		return nil, invalid("DesiredValue must exceed the currently applied quota.")
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count, pending int
	err = tx.QueryRowContext(q.ctx, `SELECT count(*),coalesce(max(region=? AND status='PENDING'),0)
		FROM aws_b2bi_profile_quota_requests`, q.region).Scan(&count, &pending)
	if err != nil {
		return nil, err
	}
	if pending != 0 {
		return nil, &fault{kind: "conflict", code: "ResourceAlreadyExistsException", message: "This quota already has a pending increase request."}
	}
	if count >= maxQuotaRequestRecords {
		return nil, &fault{kind: "limit", code: "QuotaExceededException", message: "The local quota request history is full."}
	}
	id, err := newID("uuid", "")
	if err != nil {
		return nil, err
	}
	rec := quotaIncrease{id: id, requester: q.principal, desired: desired, status: "PENDING", createdMS: q.now().UnixMilli()}
	if q.input["SupportCaseAllowed"] == false {
		rec.status = "NOT_APPROVED"
	}
	result, err := tx.ExecContext(q.ctx, `INSERT INTO aws_b2bi_profile_quota_requests(id,region,requester,desired,status,created_ms)
		VALUES(?,?,?,?,?,?)`, rec.id, q.region, rec.requester, rec.desired, rec.status, rec.createdMS)
	if err != nil {
		return nil, err
	}
	rec.seq, err = result.LastInsertId()
	if err != nil {
		return nil, err
	}
	var saved quotaIncrease
	err = saved.scan(tx.QueryRowContext(q.ctx, "SELECT "+quotaIncreaseColumns+" FROM aws_b2bi_profile_quota_requests WHERE region=? AND id=?", q.region, id))
	if err != nil {
		return nil, err
	}
	if saved != rec {
		return nil, errors.New("saved quota increase does not match acknowledged request")
	}
	if err := q.ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"RequestedQuota": q.quotaIncreaseOutput(saved)}, nil
}

func (q *request) getQuotaIncrease() (map[string]any, error) {
	if err := q.authorize("*"); err != nil {
		return nil, err
	}
	var rec quotaIncrease
	err := rec.scan(q.e.store.db.QueryRowContext(q.ctx, "SELECT "+quotaIncreaseColumns+" FROM aws_b2bi_profile_quota_requests WHERE region=? AND id=?", q.region, q.input["RequestId"]))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &fault{kind: "notfound", message: "The quota increase request does not exist."}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"RequestedQuota": q.quotaIncreaseOutput(rec)}, nil
}

// Pagination proceeds in descending insertion order, so later requests cannot shift
// an issued page. Continuations are tied to this process, actor, operation and filters.
func (q *request) quotaHistoryToken(before int64) string {
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(before))
	mac := hmac.New(sha256.New, q.e.quotaHistoryKey[:])
	mac.Write([]byte(q.op.Name + "\x00" + q.region + "\x00" + q.principal + "\x00" + fingerprint(q.input, "NextToken", "MaxResults")))
	mac.Write(raw[:])
	return base64.StdEncoding.EncodeToString(mac.Sum(raw[:]))
}

func (q *request) quotaHistoryBoundary() (int64, error) {
	token, supplied := q.input["NextToken"].(string)
	if !supplied {
		return math.MaxInt64, nil
	}
	data, err := base64.StdEncoding.Strict().DecodeString(token)
	if err == nil && len(data) == 8+sha256.Size {
		before := int64(binary.BigEndian.Uint64(data[:8]))
		if before > 0 && hmac.Equal([]byte(token), []byte(q.quotaHistoryToken(before))) {
			return before, nil
		}
	}
	return 0, &fault{kind: "token", code: "InvalidPaginationTokenException", message: "The pagination token is invalid for this request."}
}

func (q *request) listQuotaIncreaseHistory() (map[string]any, error) {
	byQuota := q.op.Name == "ListRequestedServiceQuotaChangeHistoryByQuota"
	resource := "*"
	if byQuota {
		resource = q.formatARN(q.resource(), "", "", nil)
	}
	if err := q.authorize(resource); err != nil {
		return nil, err
	}
	if byQuota && !q.supportedIncreaseQuota() {
		return nil, &fault{kind: "notfound", message: "The requested quota is unavailable in this Region."}
	}
	before, err := q.quotaHistoryBoundary()
	if err != nil {
		return nil, err
	}
	limit := int64(100)
	if value, ok := q.input["MaxResults"].(int64); ok {
		limit = value
	}
	out := map[string]any{"RequestedQuotas": []map[string]any{}}
	if q.input["QuotaRequestedAtLevel"] == "RESOURCE" || (q.input["ServiceCode"] != nil && q.input["ServiceCode"] != "b2bi") {
		return out, nil
	}
	firstMS, lastMS := int64(math.MinInt64), int64(math.MaxInt64)
	if !byQuota {
		now := q.now()
		firstMS, lastMS = now.Add(-90*24*time.Hour).UnixMilli(), now.UnixMilli()
	}
	status, _ := q.input["Status"].(string)
	rows, err := q.e.store.db.QueryContext(q.ctx, "SELECT "+quotaIncreaseColumns+` FROM aws_b2bi_profile_quota_requests
		WHERE region=? AND seq<? AND created_ms BETWEEN ? AND ? AND (?='' OR status=?) ORDER BY seq DESC LIMIT ?`, q.region, before, firstMS, lastMS, status, status, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	var last int64
	for rows.Next() {
		var rec quotaIncrease
		if err := rec.scan(rows); err != nil {
			return nil, err
		}
		if int64(len(items)) == limit {
			out["NextToken"] = q.quotaHistoryToken(last)
			break
		}
		last = rec.seq
		items = append(items, q.quotaIncreaseOutput(rec))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out["RequestedQuotas"] = items
	return out, nil
}
