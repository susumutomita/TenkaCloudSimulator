package aws

import (
	"bytes"
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const maxS3Uploads = 1000
const s3MinPart = 5 << 20

type multipartBucketScope struct{ region, created string }
type multipartScopeKey struct{}

func multipartBucket(tx s3Query, ctx context.Context, bucket string) error {
	var region, created string
	err := tx.QueryRow("SELECT region,created FROM buckets WHERE name=?", bucket).Scan(&region, &created)
	if err == sql.ErrNoRows {
		return core.Fail("NoSuchBucket", "The specified bucket does not exist.")
	}
	if err != nil {
		return err
	}
	if expected, ok := ctx.Value(multipartScopeKey{}).(multipartBucketScope); ok {
		if region != expected.region {
			return core.Fail("IllegalLocationConstraintException", "Multipart bucket region changed; refresh before retrying.")
		}
		if created != expected.created {
			return core.Fail("ConditionalRequestConflict", "The multipart bucket was replaced; refresh before retrying.")
		}
	}
	return nil
}

type S3Upload struct {
	Key         string
	ID          string `xml:"UploadId"`
	Initiated   string
	ContentType string `xml:"-"`
}
type S3Part struct {
	Number   int `xml:"PartNumber"`
	ETag     string
	Modified string `xml:"LastModified,omitempty"`
	Size     int
}

// Check affected rows as well as SQL errors: SQLite IGNORE is not an acknowledgement.
func mpWrite(tx s3Query, query string, args ...any) error {
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return core.Fail("InternalError", "Multipart state write was not acknowledged.")
	}
	return err
}
func validS3Key(key string) bool { return key != "" && len(key) <= 1024 && validText(key) }
func (s *S3Store) CreateUpload(ctx context.Context, bucket, key, contentType string) (S3Upload, error) {
	u := S3Upload{Key: key, ContentType: contentType, Initiated: time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")}
	if !validS3Key(key) || len(contentType) > 1024 || !validText(contentType) {
		return u, core.Fail("InvalidArgument", "Invalid multipart key or Content-Type.")
	}
	tx, err := beginS3Request(ctx, s.db)
	if err != nil {
		return u, err
	}
	defer tx.Rollback()
	if err = multipartBucket(tx, ctx, bucket); err != nil {
		return u, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_s3_uploads").Scan(&count); err != nil {
		return u, err
	}
	if count >= maxS3Uploads {
		return u, core.Fail("ServiceUnavailable", "Simulator multipart upload limit reached.")
	}
	u.ID, err = s3NewID()
	if err != nil {
		return u, err
	}
	if err = mpWrite(tx, "INSERT INTO aws_s3_uploads(id,bucket,key,content_type,initiated) VALUES(?,?,?,?,?)", u.ID, bucket, key, contentType, u.Initiated); err == nil {
		err = tx.Commit()
	}
	return u, err
}
func mpUpload(tx s3Query, bucket, key, id string) (S3Upload, error) {
	var u S3Upload
	err := tx.QueryRow("SELECT key,id,initiated,content_type FROM aws_s3_uploads WHERE bucket=? AND key=? AND id=?", bucket, key, id).Scan(&u.Key, &u.ID, &u.Initiated, &u.ContentType)
	if err == sql.ErrNoRows {
		return u, core.Fail("NoSuchUpload", "The specified multipart upload does not exist.")
	}
	if err == nil {
		_, err = time.Parse(time.RFC3339Nano, u.Initiated)
	}
	return u, err
}
func (s *S3Store) withUpload(ctx context.Context, bucket, key, id string, work func(*s3RequestTx, S3Upload) error) error {
	tx, err := beginS3Request(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = multipartBucket(tx, ctx, bucket); err != nil {
		return err
	}
	u, err := mpUpload(tx, bucket, key, id)
	if err != nil {
		return err
	}
	if err = work(tx, u); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *S3Store) UploadPart(ctx context.Context, bucket, key, id string, number int, data []byte) (S3Part, error) {
	sum := md5.Sum(data)
	p := S3Part{Number: number, ETag: `"` + hex.EncodeToString(sum[:]) + `"`, Modified: time.Now().UTC().Format(time.RFC3339Nano), Size: len(data)}
	if number < 1 || number > 10000 {
		return p, core.Fail("InvalidArgument", "Part number must be 1–10000.")
	}
	if len(data) > core.MaxObjectBytes {
		return p, core.Fail("EntityTooLarge", "Part exceeds the 8 MiB simulator limit.")
	}
	err := s.withUpload(ctx, bucket, key, id, func(tx *s3RequestTx, _ S3Upload) error {
		// Replace before quota accounting; rollback restores the old part on refusal.
		if _, err := tx.Exec("DELETE FROM aws_s3_parts WHERE upload=? AND number=?", id, number); err != nil {
			return err
		}
		if err := s3Quota(tx, len(data), 1); err != nil {
			return err
		}
		if data == nil {
			data = []byte{}
		}
		return mpWrite(tx, "INSERT INTO aws_s3_parts(upload,number,etag,modified,data) VALUES(?,?,?,?,?)", id, number, p.ETag, p.Modified, data)
	})
	return p, err
}
func (s *S3Store) AbortUpload(ctx context.Context, bucket, key, id string) error {
	return s.withUpload(ctx, bucket, key, id, func(tx *s3RequestTx, _ S3Upload) error {
		// Cascading part deletion belongs to the same acknowledged transaction.
		return mpWrite(tx, "DELETE FROM aws_s3_uploads WHERE id=?", id)
	})
}
func (s *S3Store) Uploads(ctx context.Context, bucket string) ([]S3Upload, error) {
	tx, err := beginS3Request(ctx, s.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = multipartBucket(tx, ctx, bucket); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT key,id,initiated FROM aws_s3_uploads WHERE bucket=? ORDER BY key,initiated,id", bucket)
	if err != nil {
		return nil, err
	}
	result := []S3Upload{}
	for rows.Next() {
		var u S3Upload
		if err = rows.Scan(&u.Key, &u.ID, &u.Initiated); err == nil {
			_, err = time.Parse(time.RFC3339Nano, u.Initiated)
		}
		if err != nil {
			break
		}
		result = append(result, u)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err == nil {
		err = tx.Commit()
	}
	return result, err
}
func (s *S3Store) Parts(ctx context.Context, bucket, key, id string) ([]S3Part, error) {
	result := []S3Part{}
	err := s.withUpload(ctx, bucket, key, id, func(tx *s3RequestTx, _ S3Upload) error {
		rows, err := tx.QueryContext(ctx, "SELECT number,etag,modified,length(data) FROM aws_s3_parts WHERE upload=? ORDER BY number", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p S3Part
			if err = rows.Scan(&p.Number, &p.ETag, &p.Modified, &p.Size); err == nil {
				_, err = time.Parse(time.RFC3339Nano, p.Modified)
			}
			if err != nil {
				return err
			}
			result = append(result, p)
		}
		return rows.Err()
	})
	return result, err
}
func (s *S3Store) CompleteUpload(ctx context.Context, bucket, key, id string, parts []S3Part) (S3Version, error) {
	var v S3Version
	if len(parts) == 0 || len(parts) > 10000 {
		return v, core.Fail("MalformedXML", "Complete requires 1–10000 ordered parts.")
	}
	previous := 0
	for _, p := range parts {
		if p.Number <= previous || p.Number > 10000 {
			return v, core.Fail("InvalidPartOrder", "Part numbers must be strictly increasing within 1–10000.")
		}
		previous = p.Number
		if len(p.ETag) != 34 || !strings.HasPrefix(p.ETag, `"`) || !strings.HasSuffix(p.ETag, `"`) {
			return v, core.Fail("InvalidPart", "Part ETag must match the stored quoted MD5.")
		}
	}
	err := s.withUpload(ctx, bucket, key, id, func(tx *s3RequestTx, u S3Upload) error {
		var data, hashes bytes.Buffer
		for i, p := range parts {
			var raw []byte
			var etag string
			err := tx.QueryRow("SELECT etag,data FROM aws_s3_parts WHERE upload=? AND number=?", id, p.Number).Scan(&etag, &raw)
			if err == sql.ErrNoRows || err == nil && etag != p.ETag {
				return core.Fail("InvalidPart", "A requested part or its ETag does not match.")
			}
			if err != nil {
				return err
			}
			digest := md5.Sum(raw)
			if etag != `"`+hex.EncodeToString(digest[:])+`"` {
				return core.Fail("InternalError", "Stored multipart data failed integrity validation.")
			}
			if i < len(parts)-1 && len(raw) < s3MinPart {
				return core.Fail("EntityTooSmall", "Every part except the final part must be at least 5 MiB.")
			}
			if data.Len()+len(raw) > core.MaxObjectBytes {
				return core.Fail("EntityTooLarge", "Completed object exceeds the 8 MiB simulator limit.")
			}
			data.Write(raw)
			hashes.Write(digest[:])
		}
		sum := md5.Sum(hashes.Bytes())
		etag := fmt.Sprintf(`"%x-%d"`, sum, len(parts))
		// Release all parts, including omitted ones, before shared quota accounting.
		if err := mpWrite(tx, "DELETE FROM aws_s3_uploads WHERE id=?", id); err != nil {
			return err
		}
		var err error
		v, err = s3PutTx(tx, bucket, key, u.ContentType, data.Bytes(), "", "", etag)
		return err
	})
	return v, err
}
