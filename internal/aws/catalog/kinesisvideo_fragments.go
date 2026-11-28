package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/kinesisvideo"
)

// The existing catalog row is the owner. The generation check also protects
// reads against historical databases where two successive streams could have
// the same ARN. Physical deletion of the parent cascades to the stored media.
const kinesisMediaSchema = `
CREATE TABLE IF NOT EXISTS aws_kinesisvideo_fragments (
 number INTEGER PRIMARY KEY AUTOINCREMENT,
 stream_arn TEXT NOT NULL REFERENCES aws_catalog_resources(arn) ON DELETE CASCADE,
 owner_seq INTEGER NOT NULL, cursor TEXT NOT NULL UNIQUE,
 producer_ms INTEGER NOT NULL, server_ms INTEGER NOT NULL, expires_ms INTEGER NOT NULL,
 media BLOB NOT NULL, digest BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS aws_kinesisvideo_stream_fragments
 ON aws_kinesisvideo_fragments(stream_arn,owner_seq,number);
CREATE TABLE IF NOT EXISTS aws_kinesisvideo_creation_clock (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), last_ms INTEGER NOT NULL
);
INSERT OR IGNORE INTO aws_kinesisvideo_creation_clock VALUES(1, 1);`

func (e *Engine) openKinesisMedia() error {
	tx, err := e.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(kinesisMediaSchema); err != nil {
		return err
	}
	// Old default-key streams were created as metadata with a final /1. They
	// keep their ARN, tags, version and physical identity when the real store
	// becomes available. Explicit custom-key resources remain metadata-only.
	rows, err := tx.Query("SELECT " + columns + ` FROM aws_catalog_resources WHERE service='kinesisvideo' AND kind='stream'
 AND arn LIKE '%/1' AND json_valid(document) AND json_valid(tags)
 AND json_extract(document,'$.Status')='CREATING' AND json_extract(document,'$.KmsKeyId')='alias/aws/kinesisvideo'`)
	if err != nil {
		return err
	}
	var legacy []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if r.Document["Status"] == "CREATING" && r.Document["KmsKeyId"] == "alias/aws/kinesisvideo" && strings.HasSuffix(r.ARN, "/1") {
			if r.Document["StreamARN"] != r.ARN || r.ID != r.Name || r.Name == "" || r.Document["StreamName"] != r.Name {
				rows.Close()
				return fmt.Errorf("inconsistent legacy Kinesis stream")
			}
			r.Document["Status"] = "ACTIVE"
			legacy = append(legacy, r)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range legacy {
		if err := updateRow(tx, r); err != nil {
			return err
		}
	}
	return tx.Commit()
}

var errVideoClockChanged = errors.New("Kinesis stream creation clock advanced")

func (q *request) isVideoStreamCreate() bool {
	return q.b.spec.Model == "kinesisvideo" && q.op.Name == "CreateStream"
}

func (q *request) createVideoStream() (map[string]any, error) {
	// Another creator may advance the persisted time between authorization
	// and insertion. The next attempt authorizes its own new candidate ARN.
	for range 8 {
		result, err := q.create(0)
		if !errors.Is(err, errVideoClockChanged) {
			return result, err
		}
	}
	return nil, Failure("ClientLimitExceededException", "Concurrent stream creation is busy.")
}

func (q *request) videoCreationTime(r *row) (int64, error) {
	var previous int64
	if err := q.e.store.db.QueryRowContext(q.ctx, "SELECT last_ms FROM aws_kinesisvideo_creation_clock WHERE singleton=1").Scan(&previous); err != nil {
		return 0, err
	}
	if previous < 1 || previous == math.MaxInt64 {
		return 0, fmt.Errorf("invalid Kinesis creation clock")
	}
	ms := max(q.now().UnixMilli(), previous+1)
	r.ARN = strings.TrimSuffix(r.ARN, "/*") + "/" + strconv.FormatInt(ms, 10)
	r.Document["StreamARN"] = r.ARN
	r.Document["CreationTime"] = time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	if r.Document["KmsKeyId"] == "alias/aws/kinesisvideo" {
		r.Document["Status"] = "ACTIVE"
	}
	return ms, nil
}

type kinesisReceipt struct {
	number     int64
	cursor     string
	producerMS int64
	serverMS   int64
}

func (r kinesisReceipt) digest(owner row, media []byte) []byte {
	h := sha256.New()
	h.Write([]byte(owner.ARN))
	h.Write([]byte{0})
	h.Write([]byte(r.cursor))
	for _, n := range []int64{owner.Seq, r.number, r.producerMS, r.serverMS} {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(n))
		h.Write(b[:])
	}
	h.Write(media)
	return h.Sum(nil)
}

func kinesisSeconds(ms int64) string { return fmt.Sprintf("%d.%03d", ms/1000, ms%1000) }

// A fragment's deadline is derived from its server capture time and its
// physical owner's retention. Validate that relationship before using a
// stored deadline to free space. Reads touch only the selected stream;
// ingestion reclaims expired records across the team's shared budget.
func pruneVideoFragments(ctx context.Context, tx *sql.Tx, streamARN string, now int64) error {
	const inScope = "(?='' OR f.stream_arn=?)"
	var inconsistent bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_kinesisvideo_fragments f
 LEFT JOIN aws_catalog_resources o ON o.arn=f.stream_arn AND o.seq=f.owner_seq
 WHERE `+inScope+` AND (o.arn IS NULL OR o.service!='kinesisvideo' OR o.kind!='stream'
 OR f.server_ms<0 OR json_type(o.document,'$.DataRetentionInHours') IS NULL
 OR json_type(o.document,'$.DataRetentionInHours') NOT IN ('integer','real')
 OR json_extract(o.document,'$.DataRetentionInHours') NOT BETWEEN 0 AND 87600
 OR f.expires_ms!=f.server_ms+3600000*json_extract(o.document,'$.DataRetentionInHours')))`, streamARN, streamARN).Scan(&inconsistent)
	if err != nil {
		return err
	}
	if inconsistent {
		return fmt.Errorf("inconsistent Kinesis fragment retention")
	}
	expired := inScope + " AND f.expires_ms<=?"
	if _, err := tx.ExecContext(ctx, "DELETE FROM aws_kinesisvideo_fragments AS f WHERE "+expired, streamARN, streamARN, now); err != nil {
		return err
	}
	var remaining bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_kinesisvideo_fragments f WHERE "+expired+")", streamARN, streamARN, now).Scan(&remaining); err != nil {
		return err
	}
	if remaining {
		return fmt.Errorf("expired Kinesis fragment was not removed")
	}
	return nil
}

func (q *request) updateVideoRetention(tx *sql.Tx, owner row) error {
	now := q.now().UnixMilli()
	hours, ok := Number(owner.Document["DataRetentionInHours"])
	if !ok || hours < 0 || hours > 87600 || hours != math.Trunc(hours) {
		return fmt.Errorf("invalid video retention update")
	}
	// Discard already-expired fragments before extending a live retention
	// period, so a later increase cannot resurrect an expired upload.
	if err := pruneVideoFragments(q.ctx, tx, owner.ARN, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(q.ctx, "UPDATE aws_kinesisvideo_fragments SET expires_ms=server_ms+? WHERE stream_arn=? AND owner_seq=?", int64(hours)*time.Hour.Milliseconds(), owner.ARN, owner.Seq); err != nil {
		return err
	}
	if _, err := tx.ExecContext(q.ctx, "DELETE FROM aws_kinesisvideo_fragments WHERE stream_arn=? AND owner_seq=? AND (expires_ms<=? OR ?=0)", owner.ARN, owner.Seq, now, int64(hours)); err != nil {
		return err
	}
	var inconsistent bool
	err := tx.QueryRowContext(q.ctx, `SELECT EXISTS(SELECT 1 FROM aws_kinesisvideo_fragments
 WHERE stream_arn=? AND (owner_seq!=? OR expires_ms<=? OR expires_ms!=server_ms+? OR ?=0))`, owner.ARN, owner.Seq, now, int64(hours)*time.Hour.Milliseconds(), int64(hours)).Scan(&inconsistent)
	if err != nil {
		return err
	}
	if inconsistent {
		return fmt.Errorf("Kinesis retention change did not reach the stored fragments")
	}
	return nil
}

// currentVideoStream rechecks the authenticated physical owner inside the same
// transaction as the read/write and uses that copy's current retention.
func currentVideoStream(ctx context.Context, tx *sql.Tx, owner row) (int64, error) {
	if owner.Service != "kinesisvideo" || owner.Kind != "stream" || owner.Seq < 1 {
		return 0, fmt.Errorf("invalid Kinesis Video Streams owner")
	}
	actual, err := scanRow(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM aws_catalog_resources WHERE service='kinesisvideo' AND region=? AND kind='stream' AND parent='' AND arn=? AND seq=?", owner.Region, owner.ARN, owner.Seq))
	if errors.Is(err, sql.ErrNoRows) {
		return 0, Failure("ResourceNotFoundException", "The stream no longer exists.")
	}
	if err != nil {
		return 0, err
	}
	if actual.ID != actual.Name || actual.Document["StreamARN"] != actual.ARN || actual.Document["StreamName"] != actual.Name {
		return 0, fmt.Errorf("Kinesis video stream identity is inconsistent")
	}
	if actual.Document["Status"] != "ACTIVE" || actual.Document["KmsKeyId"] != "alias/aws/kinesisvideo" {
		return 0, Failure("InvalidArgumentException", "This stream is not backed by the local media store.")
	}
	hours, ok := Number(actual.Document["DataRetentionInHours"])
	if !ok || hours < 0 || hours > 87600 || hours != math.Trunc(hours) {
		return 0, fmt.Errorf("invalid persisted media retention")
	}
	return int64(hours) * time.Hour.Milliseconds(), nil
}

// saveVideoFragment commits one finite upload. A zero retention stream can
// receive a valid fragment, but has no stored number or continuation token.
func (e *Engine) saveVideoFragment(ctx context.Context, owner row, producerStart int64, media []byte) (kinesisReceipt, error) {
	var empty kinesisReceipt
	decoded, err := kinesisvideo.ParseFragment(media)
	if err != nil {
		return empty, Failure("InvalidArgumentException", err.Error())
	}
	if producerStart < 0 || producerStart > math.MaxInt64-decoded.ClusterMilliseconds {
		return empty, Failure("InvalidArgumentException", "Invalid producer start time.")
	}
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	retention, err := currentVideoStream(ctx, tx, owner)
	if err != nil {
		return empty, err
	}
	r := kinesisReceipt{producerMS: producerStart + decoded.ClusterMilliseconds, serverMS: e.cfg.Now().UnixMilli()}
	if r.serverMS < 0 || r.serverMS > math.MaxInt64-retention {
		return empty, fmt.Errorf("invalid local server time")
	}
	if err := pruneVideoFragments(ctx, tx, "", r.serverMS); err != nil {
		return empty, err
	}
	if retention == 0 {
		if err := tx.Commit(); err != nil {
			return empty, err
		}
		return r, nil
	}
	var count, total, streamCount, streamBytes int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(media)),0),
 count(*) FILTER (WHERE stream_arn=? AND owner_seq=?),
 coalesce(sum(length(media)) FILTER (WHERE stream_arn=? AND owner_seq=?),0)
 FROM aws_kinesisvideo_fragments`, owner.ARN, owner.Seq, owner.ARN, owner.Seq).Scan(&count, &total, &streamCount, &streamBytes); err != nil {
		return empty, err
	}
	if count >= 128 || streamCount >= 16 || total+int64(len(media)) > 64<<20 || streamBytes+int64(len(media)) > 32<<20 {
		return empty, Failure("ClientLimitExceededException", "The local retained media budget is full.")
	}
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return empty, err
	}
	r.cursor = base64.RawURLEncoding.EncodeToString(random[:])
	inserted, err := tx.ExecContext(ctx, `INSERT INTO aws_kinesisvideo_fragments
 (stream_arn,owner_seq,cursor,producer_ms,server_ms,expires_ms,media,digest)
 VALUES(?,?,?,?,?,?,?,zeroblob(32))`, owner.ARN, owner.Seq, r.cursor, r.producerMS, r.serverMS, r.serverMS+retention, media)
	if err != nil {
		return empty, err
	}
	r.number, err = inserted.LastInsertId()
	if err != nil {
		return empty, err
	}
	digest := r.digest(owner, media)
	if err := exactlyOne(tx.ExecContext(ctx, "UPDATE aws_kinesisvideo_fragments SET digest=? WHERE number=? AND stream_arn=? AND owner_seq=?", digest, r.number, owner.ARN, owner.Seq)); err != nil {
		return empty, err
	}
	var stored bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_kinesisvideo_fragments
 WHERE number=? AND stream_arn=? AND owner_seq=? AND cursor=? AND producer_ms=?
 AND server_ms=? AND expires_ms=? AND digest=? AND media=?)`, r.number, owner.ARN, owner.Seq,
		r.cursor, r.producerMS, r.serverMS, r.serverMS+retention, digest, media).Scan(&stored); err != nil {
		return empty, err
	}
	if !stored {
		return empty, fmt.Errorf("Kinesis fragment did not reach durable storage")
	}
	if err := tx.Commit(); err != nil {
		return empty, err
	}
	return r, nil
}

// readVideoFragments produces a finite snapshot. A cursor always belongs to a
// still-retained fragment of the same physical stream in this team's database.
func (e *Engine) readVideoFragments(ctx context.Context, owner row, cursor string) ([][]byte, error) {
	tx, err := e.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := currentVideoStream(ctx, tx, owner); err != nil {
		return nil, err
	}
	now := e.cfg.Now().UnixMilli()
	if err := pruneVideoFragments(ctx, tx, owner.ARN, now); err != nil {
		return nil, err
	}
	var after int64
	if cursor != "" {
		r := kinesisReceipt{cursor: cursor}
		var media, digest []byte
		err := tx.QueryRowContext(ctx, `SELECT number,producer_ms,server_ms,CASE WHEN length(media) BETWEEN 1 AND ? THEN media END,digest
 FROM aws_kinesisvideo_fragments WHERE stream_arn=? AND owner_seq=? AND cursor=?`, kinesisvideo.MaxFragmentBytes, owner.ARN, owner.Seq, cursor).Scan(&r.number, &r.producerMS, &r.serverMS, &media, &digest)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Failure("InvalidArgumentException", "The continuation token is not current for the selected stream.")
		}
		if err != nil {
			return nil, err
		}
		if len(media) == 0 || r.producerMS < 0 || r.serverMS < 0 || !bytes.Equal(digest, r.digest(owner, media)) {
			return nil, fmt.Errorf("inconsistent Kinesis continuation fragment")
		}
		after = r.number
	}
	var latest int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce((SELECT server_ms FROM aws_kinesisvideo_fragments
 WHERE stream_arn=? AND owner_seq=? ORDER BY number DESC LIMIT 1),0)`, owner.ARN, owner.Seq).Scan(&latest); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT number,cursor,producer_ms,server_ms,
 CASE WHEN length(media) BETWEEN 1 AND ? THEN media END,digest
 FROM aws_kinesisvideo_fragments WHERE stream_arn=? AND owner_seq=? AND number>? ORDER BY number`, kinesisvideo.MaxFragmentBytes, owner.ARN, owner.Seq, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result [][]byte
	var total int
	for rows.Next() {
		var r kinesisReceipt
		var media, digest []byte
		if err := rows.Scan(&r.number, &r.cursor, &r.producerMS, &r.serverMS, &media, &digest); err != nil {
			return nil, err
		}
		total += len(media)
		if len(result) >= 16 || total > 32<<20 || r.producerMS < 0 || r.serverMS < 0 || len(r.cursor) != 32 || len(media) == 0 || !bytes.Equal(digest, r.digest(owner, media)) {
			return nil, fmt.Errorf("stored Kinesis video fragment is inconsistent")
		}
		f, err := kinesisvideo.ParseFragment(media)
		if err != nil {
			return nil, fmt.Errorf("invalid stored Kinesis video fragment: %w", err)
		}
		lag := max(int64(0), latest-r.serverMS)
		chunk, err := f.MediaChunk(strconv.FormatInt(r.number, 10), kinesisSeconds(r.producerMS), kinesisSeconds(r.serverMS), r.cursor, strconv.FormatInt(lag, 10))
		if err != nil {
			return nil, err
		}
		result = append(result, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
