package aws

import (
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

// SQSStore owns AWS SQS state in SQLite; it never shares the S3/SSM namespace.
// Standard queues only: FIFO ordering/deduplication are deliberately not simulated.
type SQSStore struct{ db *sql.DB }

type SQSQueue struct {
	Region            string
	Name              string
	Created           float64 // epoch seconds
	VisibilityTimeout int     // seconds
	DelaySeconds      int     // seconds
	RetentionSeconds  int     // seconds
}

type SQSMessageAttribute struct {
	DataType    string  `json:"DataType"`
	StringValue *string `json:"StringValue,omitempty"`
	BinaryValue *string `json:"BinaryValue,omitempty"` // base64, as in the SQS JSON protocol
}

type SQSMessage struct {
	ID              string
	Body            string
	Attributes      map[string]SQSMessageAttribute
	Sent            float64 // epoch seconds
	ReceiveCount    int
	ReceiptHandle   string
	MD5OfBody       string
	MD5OfAttributes string
}

const maxQueues = 1000

func NewSQSStore(db *sql.DB) (*SQSStore, error) {
	if db == nil {
		return nil, errors.New("SQS requires a provider database")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_sqs_queues(region TEXT NOT NULL,name TEXT NOT NULL,created REAL NOT NULL,visibility_timeout INTEGER NOT NULL,delay_seconds INTEGER NOT NULL,retention_seconds INTEGER NOT NULL,PRIMARY KEY(region,name));
CREATE TABLE IF NOT EXISTS aws_sqs_messages(id TEXT NOT NULL,region TEXT NOT NULL,queue TEXT NOT NULL,body TEXT NOT NULL,attributes TEXT NOT NULL,sent REAL NOT NULL,visible REAL NOT NULL,receive_count INTEGER NOT NULL,receipt TEXT NOT NULL,PRIMARY KEY(region,queue,id));`)
	if err != nil {
		return nil, err
	}
	var orphans int
	if err = db.QueryRow("SELECT count(*) FROM aws_sqs_messages m WHERE NOT EXISTS(SELECT 1 FROM aws_sqs_queues q WHERE q.region=m.region AND q.name=m.queue)").Scan(&orphans); err != nil {
		return nil, err
	}
	if orphans != 0 {
		return nil, errors.New("SQS state contains orphan messages; preserved for explicit repair")
	}
	return &SQSStore{db}, nil
}

func newSQSID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func sqsNow() float64 { return float64(time.Now().UnixMilli()) / 1000 }

var errQueueMissing = sqsFailure{"AWS.SimpleQueueService.NonExistentQueue", "The specified queue does not exist."}

// CreateQueue is idempotent for identical attributes, like real SQS: an
// existing queue with different attributes is an error (QueueNameExists).
func (s *SQSStore) CreateQueue(region string, q SQSQueue) (SQSQueue, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SQSQueue{}, err
	}
	defer tx.Rollback()
	existing, err := scanQueue(tx.QueryRow("SELECT region,name,created,visibility_timeout,delay_seconds,retention_seconds FROM aws_sqs_queues WHERE region=? AND name=?", region, q.Name))
	if err == nil {
		if existing.VisibilityTimeout != q.VisibilityTimeout || existing.DelaySeconds != q.DelaySeconds || existing.RetentionSeconds != q.RetentionSeconds {
			return SQSQueue{}, sqsFailure{"QueueNameExists", "A queue with this name already exists with different attributes."}
		}
		return existing, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return SQSQueue{}, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_sqs_queues").Scan(&count); err != nil {
		return SQSQueue{}, err
	}
	if count >= maxQueues {
		return SQSQueue{}, sqsFailure{"QueueLimitExceeded", "Simulator queue limit reached."}
	}
	q.Region, q.Created = region, sqsNow()
	if _, err = tx.Exec("INSERT INTO aws_sqs_queues VALUES(?,?,?,?,?,?)", q.Region, q.Name, q.Created, q.VisibilityTimeout, q.DelaySeconds, q.RetentionSeconds); err != nil {
		return SQSQueue{}, err
	}
	return q, tx.Commit()
}

func scanQueue(row *sql.Row) (SQSQueue, error) {
	var q SQSQueue
	err := row.Scan(&q.Region, &q.Name, &q.Created, &q.VisibilityTimeout, &q.DelaySeconds, &q.RetentionSeconds)
	return q, err
}

func (s *SQSStore) GetQueue(region, name string) (SQSQueue, error) {
	q, err := scanQueue(s.db.QueryRow("SELECT region,name,created,visibility_timeout,delay_seconds,retention_seconds FROM aws_sqs_queues WHERE region=? AND name=?", region, name))
	if err == sql.ErrNoRows {
		return SQSQueue{}, errQueueMissing
	}
	return q, err
}

func (s *SQSStore) ListQueues(region, prefix string) ([]SQSQueue, error) {
	rows, err := s.db.Query("SELECT region,name,created,visibility_timeout,delay_seconds,retention_seconds FROM aws_sqs_queues WHERE region=? ORDER BY name", region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SQSQueue{}
	for rows.Next() {
		var q SQSQueue
		if err = rows.Scan(&q.Region, &q.Name, &q.Created, &q.VisibilityTimeout, &q.DelaySeconds, &q.RetentionSeconds); err != nil {
			return nil, err
		}
		if prefix == "" || len(q.Name) >= len(prefix) && q.Name[:len(prefix)] == prefix {
			result = append(result, q)
		}
	}
	return result, rows.Err()
}

func (s *SQSStore) DeleteQueue(region, name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec("DELETE FROM aws_sqs_queues WHERE region=? AND name=?", region, name)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return errQueueMissing
	}
	if _, err = tx.Exec("DELETE FROM aws_sqs_messages WHERE region=? AND queue=?", region, name); err != nil {
		return err
	}
	return tx.Commit()
}

// sqsQueueTx and purgeExpiredTx keep parent admission, expiry cleanup and the
// requested mutation in one caller-owned transaction. Cached HTTP queue values
// never authorize a write after another request has deleted that queue.
func sqsQueueTx(tx *sql.Tx, region, name string) (SQSQueue, error) {
	q, err := scanQueue(tx.QueryRow("SELECT region,name,created,visibility_timeout,delay_seconds,retention_seconds FROM aws_sqs_queues WHERE region=? AND name=?", region, name))
	if errors.Is(err, sql.ErrNoRows) {
		return q, errQueueMissing
	}
	return q, err
}
func purgeExpiredTx(tx *sql.Tx, q SQSQueue, now float64) error {
	_, err := tx.Exec("DELETE FROM aws_sqs_messages WHERE region=? AND queue=? AND sent<=?", q.Region, q.Name, now-float64(q.RetentionSeconds))
	return err
}

func md5Hex(b []byte) string { sum := md5.Sum(b); return hex.EncodeToString(sum[:]) }

// MD5OfMessageAttributes follows the AWS SQS checksum algorithm: attributes
// sorted by name, each encoded as length-prefixed name, data type and value,
// with a one-byte transport type (1 = string-like, 2 = binary).
func MD5OfMessageAttributes(attrs map[string]SQSMessageAttribute) string {
	names := make([]string, 0, len(attrs))
	for n := range attrs {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf []byte
	var lenBuf [4]byte
	put := func(b []byte) {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
		buf = append(buf, lenBuf[:]...)
		buf = append(buf, b...)
	}
	for _, n := range names {
		a := attrs[n]
		put([]byte(n))
		put([]byte(a.DataType))
		if a.BinaryValue != nil {
			raw, _ := base64.StdEncoding.DecodeString(*a.BinaryValue)
			buf = append(buf, 2)
			put(raw)
		} else {
			buf = append(buf, 1)
			put([]byte(*a.StringValue))
		}
	}
	return md5Hex(buf)
}

func (s *SQSStore) Send(region string, q SQSQueue, body string, attrs map[string]SQSMessageAttribute, delaySeconds int) (SQSMessage, error) {
	if err := validateSQSBody(body); err != nil {
		return SQSMessage{}, err
	}
	if delaySeconds < -1 || delaySeconds > 900 {
		return SQSMessage{}, sqsFailure{"InvalidParameterValue", "DelaySeconds must be 0–900 or the internal default sentinel."}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return SQSMessage{}, err
	}
	defer tx.Rollback()
	q, err = sqsQueueTx(tx, region, q.Name)
	if err != nil {
		return SQSMessage{}, err
	}
	now := sqsNow()
	if err = purgeExpiredTx(tx, q, now); err != nil {
		return SQSMessage{}, err
	}
	encoded, err := json.Marshal(attrs)
	if err != nil {
		return SQSMessage{}, err
	}
	m := SQSMessage{ID: newSQSID(), Body: body, Attributes: attrs, Sent: now, ReceiptHandle: ""}
	m.MD5OfBody = md5Hex([]byte(body))
	if len(attrs) > 0 {
		m.MD5OfAttributes = MD5OfMessageAttributes(attrs)
	}
	delay := q.DelaySeconds
	if delaySeconds >= 0 {
		delay = delaySeconds
	}
	_, err = tx.Exec("INSERT INTO aws_sqs_messages VALUES(?,?,?,?,?,?,?,?,?)",
		m.ID, region, q.Name, body, string(encoded), now, now+float64(delay), 0, "")
	if err != nil {
		return SQSMessage{}, err
	}
	return m, tx.Commit()
}

// Receive atomically claims up to maxN currently visible messages: each gets a
// fresh receipt handle, an incremented receive count and a new invisibility
// deadline of now + visibilityTimeout seconds.
func (s *SQSStore) Receive(region string, q SQSQueue, maxN, visibilityTimeout int) ([]SQSMessage, error) {
	if maxN < 1 || maxN > 10 || visibilityTimeout < 0 || visibilityTimeout > 43200 {
		return nil, sqsFailure{"InvalidParameterValue", "Use 1–10 messages and a 0–43200 second visibility timeout."}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q, err = sqsQueueTx(tx, region, q.Name)
	if err != nil {
		return nil, err
	}
	now := sqsNow()
	if err = purgeExpiredTx(tx, q, now); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT id,body,attributes,sent,receive_count FROM aws_sqs_messages WHERE region=? AND queue=? AND visible<=? ORDER BY sent,id LIMIT ?", region, q.Name, now, maxN)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, body, attrs string
		sent            float64
		count           int
	}
	var picked []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.body, &r.attrs, &r.sent, &r.count); err != nil {
			rows.Close()
			return nil, err
		}
		picked = append(picked, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	result := []SQSMessage{}
	for _, r := range picked {
		receipt := newSQSID() + newSQSID()
		if _, err = tx.Exec("UPDATE aws_sqs_messages SET visible=?, receive_count=receive_count+1, receipt=? WHERE region=? AND queue=? AND id=?",
			now+float64(visibilityTimeout), receipt, region, q.Name, r.id); err != nil {
			return nil, err
		}
		m := SQSMessage{ID: r.id, Body: r.body, Sent: r.sent, ReceiveCount: r.count + 1, ReceiptHandle: receipt}
		if err = json.Unmarshal([]byte(r.attrs), &m.Attributes); err != nil {
			return nil, err
		}
		for _, a := range m.Attributes {
			base := strings.SplitN(a.DataType, ".", 2)[0]
			if (base == "String" || base == "Number") && a.StringValue != nil && a.BinaryValue == nil {
				continue
			}
			if base == "Binary" && a.BinaryValue != nil && a.StringValue == nil {
				if _, err = base64.StdEncoding.DecodeString(*a.BinaryValue); err != nil {
					return nil, err
				}
				continue
			}
			return nil, errors.New("Corrupt persisted SQS message attributes")
		}
		if m.Attributes == nil {
			m.Attributes = map[string]SQSMessageAttribute{}
		}
		m.MD5OfBody = md5Hex([]byte(r.body))
		if len(m.Attributes) > 0 {
			m.MD5OfAttributes = MD5OfMessageAttributes(m.Attributes)
		}
		result = append(result, m)
	}
	return result, tx.Commit()
}

func (s *SQSStore) DeleteMessage(region, queue, receipt string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q, err := sqsQueueTx(tx, region, queue)
	if err != nil {
		return err
	}
	if err = purgeExpiredTx(tx, q, sqsNow()); err != nil {
		return err
	}
	r, err := tx.Exec("DELETE FROM aws_sqs_messages WHERE region=? AND queue=? AND receipt<>'' AND receipt=?", region, queue, receipt)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return sqsFailure{"ReceiptHandleIsInvalid", "The input receipt handle is invalid."}
	}
	return tx.Commit()
}

func (s *SQSStore) ChangeVisibility(region, queue, receipt string, timeout int) error {
	if timeout < 0 || timeout > 43200 {
		return sqsFailure{"InvalidParameterValue", "VisibilityTimeout must be 0–43200 seconds."}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q, err := sqsQueueTx(tx, region, queue)
	if err != nil {
		return err
	}
	now := sqsNow()
	if err = purgeExpiredTx(tx, q, now); err != nil {
		return err
	}
	var until float64
	err = tx.QueryRow("SELECT visible FROM aws_sqs_messages WHERE region=? AND queue=? AND receipt<>'' AND receipt=?", region, queue, receipt).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return sqsFailure{"ReceiptHandleIsInvalid", "The input receipt handle is invalid."}
	}
	if err != nil {
		return err
	}
	if until <= now {
		return sqsFailure{"MessageNotInflight", "The message is no longer in flight."}
	}
	r, err := tx.Exec("UPDATE aws_sqs_messages SET visible=? WHERE region=? AND queue=? AND receipt=?", now+float64(timeout), region, queue, receipt)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("SQS message disappeared during visibility mutation")
	}
	return tx.Commit()
}

// Counts purges expiry and reads the regional queue in one transaction. Storage
// errors remain infrastructure failures; an ignored purge may expose old data.
func (s *SQSStore) Counts(region string, q SQSQueue) (int, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	q, err = sqsQueueTx(tx, region, q.Name)
	if err != nil {
		return 0, 0, err
	}
	now := sqsNow()
	if err = purgeExpiredTx(tx, q, now); err != nil {
		return 0, 0, err
	}
	var visible, notVisible int
	err = tx.QueryRow("SELECT COALESCE(SUM(CASE WHEN visible<=? THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN visible>? AND receive_count>0 THEN 1 ELSE 0 END),0) FROM aws_sqs_messages WHERE region=? AND queue=?", now, now, region, q.Name).Scan(&visible, &notVisible)
	if err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return visible, notVisible, nil
}
