package aws

import (
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
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
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_sqs_queues(region TEXT NOT NULL,name TEXT NOT NULL,created REAL NOT NULL,visibility_timeout INTEGER NOT NULL,delay_seconds INTEGER NOT NULL,retention_seconds INTEGER NOT NULL,PRIMARY KEY(region,name));
CREATE TABLE IF NOT EXISTS aws_sqs_messages(id TEXT NOT NULL,region TEXT NOT NULL,queue TEXT NOT NULL,body TEXT NOT NULL,attributes TEXT NOT NULL,sent REAL NOT NULL,visible REAL NOT NULL,receive_count INTEGER NOT NULL,receipt TEXT NOT NULL,PRIMARY KEY(region,queue,id));`)
	return &SQSStore{db}, err
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

// purgeExpired removes messages older than the queue retention period.
func (s *SQSStore) purgeExpired(q SQSQueue) {
	_, _ = s.db.Exec("DELETE FROM aws_sqs_messages WHERE region=? AND queue=? AND sent < ?", q.Region, q.Name, sqsNow()-float64(q.RetentionSeconds))
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
	s.purgeExpired(q)
	encoded, err := json.Marshal(attrs)
	if err != nil {
		return SQSMessage{}, err
	}
	now := sqsNow()
	m := SQSMessage{ID: newSQSID(), Body: body, Attributes: attrs, Sent: now, ReceiptHandle: ""}
	m.MD5OfBody = md5Hex([]byte(body))
	if len(attrs) > 0 {
		m.MD5OfAttributes = MD5OfMessageAttributes(attrs)
	}
	delay := q.DelaySeconds
	if delaySeconds >= 0 {
		delay = delaySeconds
	}
	_, err = s.db.Exec("INSERT INTO aws_sqs_messages VALUES(?,?,?,?,?,?,?,?,?)",
		m.ID, region, q.Name, body, string(encoded), now, now+float64(delay), 0, "")
	return m, err
}

// Receive atomically claims up to maxN currently visible messages: each gets a
// fresh receipt handle, an incremented receive count and a new invisibility
// deadline of now + visibilityTimeout seconds.
func (s *SQSStore) Receive(region string, q SQSQueue, maxN, visibilityTimeout int) ([]SQSMessage, error) {
	s.purgeExpired(q)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := sqsNow()
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
		_ = json.Unmarshal([]byte(r.attrs), &m.Attributes)
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
	r, err := s.db.Exec("DELETE FROM aws_sqs_messages WHERE region=? AND queue=? AND receipt<>'' AND receipt=?", region, queue, receipt)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sqsFailure{"ReceiptHandleIsInvalid", "The input receipt handle is invalid."}
	}
	return nil
}

func (s *SQSStore) ChangeVisibility(region, queue, receipt string, timeout int) error {
	r, err := s.db.Exec("UPDATE aws_sqs_messages SET visible=? WHERE region=? AND queue=? AND receipt<>'' AND receipt=?", sqsNow()+float64(timeout), region, queue, receipt)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return sqsFailure{"ReceiptHandleIsInvalid", "The input receipt handle is invalid."}
	}
	return nil
}

// Counts returns (visible, notVisible) message counts for GetQueueAttributes.
func (s *SQSStore) Counts(region string, q SQSQueue) (int, int, error) {
	q.Region = region
	s.purgeExpired(q)
	var visible, notVisible int
	now := sqsNow()
	err := s.db.QueryRow("SELECT COALESCE(SUM(CASE WHEN visible<=? THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN visible>? THEN 1 ELSE 0 END),0) FROM aws_sqs_messages WHERE region=? AND queue=?", now, now, region, q.Name).Scan(&visible, &notVisible)
	return visible, notVisible, err
}
