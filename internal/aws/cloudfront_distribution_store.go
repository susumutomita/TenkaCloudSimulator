package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"slices"
	"strings"
	"time"
)

// Trigger side effects are not included in RowsAffected. Native mutations
// require exactly their declared row changes before any acknowledgment.
func cfDistributionWrite(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	var before, after int64
	if err := tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return 0, err
	}
	if after-before != n {
		return 0, errors.New("unexpected CloudFront write effects")
	}
	return n, nil
}
func cfDistributionOne(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	n, err := cfDistributionWrite(ctx, tx, query, args...)
	if err == nil && n != 1 {
		return errors.New("missing CloudFront write")
	}
	return err
}
func cfClearCache(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := cfDistributionWrite(ctx, tx, "DELETE FROM aws_cloudfront_cache WHERE distribution=?", id)
	return err
}
func cfSaveDistribution(ctx context.Context, tx *sql.Tx, id, match string, create bool, c cfDistributionConfig, bucket string) (cfDistributionRow, error) {
	var row cfDistributionRow
	var err error
	if create {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_cloudfront_distributions").Scan(&count); err != nil {
			return row, err
		}
		if count >= 100 {
			return row, cfFailure{400, "TooManyDistributions", "The local distribution limit is 100."}
		}
		var exists bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_cloudfront_distributions WHERE caller=?)", c.CallerReference).Scan(&exists); err != nil {
			return row, err
		}
		if exists {
			return row, cfFailure{409, "DistributionAlreadyExists", "The caller reference already identifies a distribution."}
		}
		id, err = cfNewDistributionID("D")
		if err != nil {
			return row, err
		}
		row.value = cfDistribution{Id: id, ARN: cfDistributionARN + id, Status: "Deployed", DomainName: id + ".cloudfront.invalid"}
	} else {
		row, err = cfReadDistribution(ctx, tx, id)
		if err != nil {
			return row, err
		}
		if err = cfDistributionMatch(row.revision, match); err != nil {
			return row, err
		}
		if c.CallerReference != row.value.DistributionConfig.CallerReference {
			return row, cfInvalid("CallerReference cannot be changed.")
		}
	}
	row.created, row.region, err = cfOriginIdentity(ctx, tx, bucket)
	if err != nil {
		return row, err
	}
	row.bucket = bucket
	row.revision, err = randomCredential(16)
	if err != nil {
		return row, err
	}
	row.value.LastModifiedTime = cfNow()
	row.value.DistributionConfig = c
	row.value.XMLNS = cloudFrontNS
	data, err := json.Marshal(row.value)
	if err != nil {
		return row, err
	}
	if create {
		err = cfDistributionOne(ctx, tx, "INSERT INTO aws_cloudfront_distributions(id,caller,revision,bucket,bucket_created,bucket_region,document) VALUES(?,?,?,?,?,?,?)", id, c.CallerReference, row.revision, bucket, row.created, row.region, string(data))
	} else {
		err = cfClearCache(ctx, tx, id)
		if err == nil {
			err = cfDistributionOne(ctx, tx, "UPDATE aws_cloudfront_distributions SET revision=?,bucket=?,bucket_created=?,bucket_region=?,document=? WHERE id=? AND revision=?", row.revision, bucket, row.created, row.region, string(data), id, match)
		}
	}
	if err != nil {
		return row, err
	}
	check, err := cfReadDistribution(ctx, tx, id)
	if err != nil {
		return row, err
	}
	if check.revision != row.revision || check.bucket != row.bucket || check.created != row.created || check.region != row.region {
		return row, errors.New("CloudFront distribution readback mismatch")
	}
	checkData, err := json.Marshal(check.value)
	if err != nil || string(checkData) != string(data) {
		return row, errors.New("CloudFront configuration readback mismatch")
	}
	return row, nil
}
func cfDeleteDistribution(ctx context.Context, tx *sql.Tx, id, match string) error {
	row, err := cfReadDistribution(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = cfDistributionMatch(row.revision, match); err != nil {
		return err
	}
	if row.value.DistributionConfig.Enabled {
		return cfFailure{409, "DistributionNotDisabled", "Disable the distribution before deletion."}
	}
	if err = cfClearCache(ctx, tx, id); err != nil {
		return err
	}
	if _, err = cfDistributionWrite(ctx, tx, "DELETE FROM aws_cloudfront_invalidations WHERE distribution=?", id); err != nil {
		return err
	}
	if err = cfDistributionOne(ctx, tx, "DELETE FROM aws_cloudfront_distributions WHERE id=? AND revision=?", id, match); err != nil {
		return err
	}
	var remains bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_cloudfront_distributions WHERE id=?)", id).Scan(&remains); err != nil {
		return err
	}
	if remains {
		return errors.New("CloudFront deletion readback mismatch")
	}
	return nil
}
func cfParseInvalidation(data []byte) (cfInvalidationBatch, error) {
	var batch cfInvalidationBatch
	if err := cfCheckDistributionXML(data, "InvalidationBatch"); err != nil {
		return batch, err
	}
	if err := xml.Unmarshal(data, &batch); err != nil {
		return batch, cfInvalid("Invalid invalidation values.")
	}
	if len(batch.CallerReference) < 1 || len(batch.CallerReference) > 128 || strings.TrimSpace(batch.CallerReference) != batch.CallerReference || batch.Paths.Quantity < 1 || batch.Paths.Quantity > 100 || batch.Paths.Quantity != len(batch.Paths.Items) {
		return batch, cfInvalid("Use 1–100 explicit invalidation paths and a bounded caller reference.")
	}
	seen := map[string]bool{}
	for _, path := range batch.Paths.Items {
		if seen[path] || path != "/*" && (!strings.HasPrefix(path, "/") || !cfValidLocalKey(strings.TrimPrefix(path, "/"))) {
			return batch, cfInvalid("Use distinct exact local object paths or /*.")
		}
		seen[path] = true
	}
	batch.XMLNS = cloudFrontNS
	return batch, nil
}
func cfCreateInvalidation(ctx context.Context, tx *sql.Tx, id string, batch cfInvalidationBatch) (cfInvalidation, error) {
	var result cfInvalidation
	if _, err := cfReadDistribution(ctx, tx, id); err != nil {
		return result, err
	}
	var data string
	err := tx.QueryRowContext(ctx, "SELECT document FROM aws_cloudfront_invalidations WHERE distribution=? AND caller=?", id, batch.CallerReference).Scan(&data)
	if err == nil {
		if json.Unmarshal([]byte(data), &result) != nil {
			return result, errors.New("invalid saved invalidation")
		}
		if !slices.Equal(result.InvalidationBatch.Paths.Items, batch.Paths.Items) {
			return result, cfFailure{400, "InvalidationBatchAlreadyExists", "That caller reference was used with different paths."}
		}
		result.XMLNS = cloudFrontNS
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_cloudfront_invalidations WHERE distribution=?", id).Scan(&count); err != nil {
		return result, err
	}
	if count >= 1000 {
		return result, cfFailure{400, "TooManyInvalidationsInProgress", "The local retained invalidation limit is 1000."}
	}
	for _, path := range batch.Paths.Items {
		if path == "/*" {
			err = cfClearCache(ctx, tx, id)
		} else {
			_, err = cfDistributionWrite(ctx, tx, "DELETE FROM aws_cloudfront_cache WHERE distribution=? AND object_key=?", id, path[1:])
		}
		if err != nil {
			return result, err
		}
	}
	identifier, err := cfNewDistributionID("I")
	if err != nil {
		return result, err
	}
	result = cfInvalidation{XMLNS: cloudFrontNS, Id: identifier, Status: "Completed", CreateTime: cfNow(), InvalidationBatch: batch}
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if err = cfDistributionOne(ctx, tx, "INSERT INTO aws_cloudfront_invalidations VALUES(?,?,?,?)", id, identifier, batch.CallerReference, string(encoded)); err != nil {
		return result, err
	}
	var check string
	if err = tx.QueryRowContext(ctx, "SELECT document FROM aws_cloudfront_invalidations WHERE distribution=? AND id=?", id, identifier).Scan(&check); err != nil {
		return result, err
	}
	if check != string(encoded) {
		return result, errors.New("invalidation readback mismatch")
	}
	return result, nil
}

type cfDelivery struct {
	data                     []byte
	contentType, etag, cache string
	age                      int64
}

func cfDeliver(ctx context.Context, tx *sql.Tx, id, principal, key string, allowed func(string, string) string, now time.Time) (cfDelivery, error) {
	var result cfDelivery
	if allowed("cloudfront:GetDistribution", cfDistributionARN+id) != "allowed" {
		return result, cfFailure{403, "AccessDenied", "The caller cannot access this distribution."}
	}
	row, err := cfReadDistribution(ctx, tx, id)
	if err != nil {
		return result, err
	}
	if allowed("s3:GetObject", "arn:aws:s3:::"+row.bucket+"/"+key) != "allowed" {
		return result, cfFailure{403, "AccessDenied", "The caller cannot access this origin object."}
	}
	if !row.value.DistributionConfig.Enabled {
		return result, cfFailure{403, "AccessDenied", "The distribution is disabled."}
	}
	created, region, err := cfOriginIdentity(ctx, tx, row.bucket)
	if err != nil {
		return result, err
	}
	if created != row.created || region != row.region {
		return result, cfFailure{404, "NoSuchOrigin", "The local origin lifetime fingerprint has changed; explicitly update the distribution."}
	}
	seconds := float64(now.UnixNano()) / 1e9
	var inserted, expires float64
	err = tx.QueryRowContext(ctx, "SELECT data,content_type,etag,inserted,expires FROM aws_cloudfront_cache WHERE distribution=? AND revision=? AND principal=? AND object_key=?", id, row.revision, principal, key).Scan(&result.data, &result.contentType, &result.etag, &inserted, &expires)
	if err == nil && seconds < expires && seconds >= inserted {
		result.cache = "Hit"
		result.age = int64(seconds - inserted)
		return result, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if _, err = cfDistributionWrite(ctx, tx, "DELETE FROM aws_cloudfront_cache WHERE expires<=? OR inserted>?", seconds, seconds); err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, "SELECT data,content_type,etag FROM objects WHERE bucket=? AND key=?", row.bucket, key).Scan(&result.data, &result.contentType, &result.etag)
	if errors.Is(err, sql.ErrNoRows) {
		return result, cfFailure{404, "NoSuchKey", "The local origin object does not exist."}
	}
	if err != nil {
		return result, err
	}
	if len(result.data) > 1<<20 {
		return result, cfFailure{502, "ResponseLimitExceeded", "The local distribution supports objects up to one MiB."}
	}
	result.cache = "Miss"
	if row.value.DistributionConfig.DefaultCacheBehavior.DefaultTTL > 0 {
		var count, stored int64
		if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(sum(length(data)),0) FROM aws_cloudfront_cache").Scan(&count, &stored); err != nil {
			return result, err
		}
		if count >= 128 || stored+int64(len(result.data)) > 32<<20 {
			return result, cfFailure{503, "CapacityExceeded", "The local cache capacity is exhausted."}
		}
		if err = cfDistributionOne(ctx, tx, "INSERT INTO aws_cloudfront_cache VALUES(?,?,?,?,?,?,?,?,?)", id, row.revision, principal, key, seconds+float64(row.value.DistributionConfig.DefaultCacheBehavior.DefaultTTL), seconds, result.contentType, result.etag, result.data); err != nil {
			return result, err
		}
	}
	return result, nil
}
