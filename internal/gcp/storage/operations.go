package storage

import (
	"context"
	"crypto/md5" // Native Cloud Storage MD5 metadata, not an authentication primitive.
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"math"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func requireOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("Storage mutation did not affect exactly one row")
	}
	return nil
}

func (s *Store) bucketDTO(b bucket) map[string]any {
	return map[string]any{"kind": "storage#bucket", "id": b.Name, "name": b.Name, "projectNumber": s.projects[b.Project], "location": b.Location, "storageClass": "STANDARD", "metageneration": "1", "timeCreated": b.Created, "updated": b.Created}
}
func (s *Store) createBucket(ctx context.Context, project string, input bucketInput) (any, error) {
	if !validBucket(input.Name) {
		return nil, invalid("Invalid or unsupported bucket name.")
	}
	if input.StorageClass != "" && input.StorageClass != "STANDARD" {
		return nil, invalid("Only STANDARD storage is implemented.")
	}
	location := strings.ToUpper(input.Location)
	if location == "" {
		location = "US"
	}
	switch location {
	case "US", "EU", "US-EAST1", "US-WEST1", "ASIA-NORTHEAST1", "EUROPE-WEST1":
	default:
		return nil, invalid("Unsupported local bucket location.")
	}
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		if err := s.permit(ctx, tx, actor, "storage.buckets.create", Resource{Project: project, Bucket: input.Name}); err != nil {
			return nil, err
		}
		if s.projects[project] == "" {
			return nil, invalid("Use a configured synthetic project ID.")
		}
		var owner string
		err := tx.QueryRowContext(ctx, "SELECT project FROM gcp_storage_buckets WHERE environment=? AND name=?", s.environment, input.Name).Scan(&owner)
		if err == nil {
			// Never disclose a peer project's ownership/conflict to a refused caller.
			if e := s.permit(ctx, tx, actor, "storage.buckets.get", Resource{Project: owner, Bucket: input.Name, Exists: true}); e != nil {
				return nil, e
			}
			return nil, &apiError{409, "conflict", "The globally named bucket already exists."}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		var count int64
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM gcp_storage_buckets WHERE environment=?", s.environment).Scan(&count); err != nil {
			return nil, err
		}
		if count >= s.limits.Buckets {
			return nil, quotaExceeded()
		}
		b := bucket{Name: input.Name, Project: project, Location: location, Created: time.Now().UTC().Format(time.RFC3339Nano)}
		if err = requireOne(tx.ExecContext(ctx, "INSERT INTO gcp_storage_buckets(environment,name,project,location,created) VALUES(?,?,?,?,?)", s.environment, b.Name, b.Project, b.Location, b.Created)); err != nil {
			return nil, err
		}
		return s.bucketDTO(b), nil
	})
}
func (s *Store) bucketOperation(ctx context.Context, method, name string) (any, error) {
	permission := "storage.buckets.get"
	if method == http.MethodDelete {
		permission = "storage.buckets.delete"
	} else if method != http.MethodGet {
		return nil, &apiError{405, "methodNotAllowed", "Bucket supports GET or DELETE."}
	}
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		b, err := s.loadBucket(ctx, tx, actor, permission, name, "")
		if err != nil {
			return nil, err
		}
		if method == http.MethodGet {
			return s.bucketDTO(b), nil
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM gcp_storage_objects WHERE environment=? AND bucket=?", s.environment, name).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, &apiError{409, "conflict", "Bucket is not empty."}
		}
		if err = requireOne(tx.ExecContext(ctx, "DELETE FROM gcp_storage_buckets WHERE environment=? AND name=?", s.environment, name)); err != nil {
			return nil, err
		}
		return nil, nil
	})
}

type page struct{ Kind, Scope, Prefix, Delimiter, Actor, Cursor string }

func actorBinding(a Actor) string {
	sum := sha256.Sum256([]byte(a.Environment + "\x00" + a.Subject))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func listOptions(q url.Values, kind, scope string, actor Actor) (int, page, error) {
	limit := 1000
	if values, present := q["maxResults"]; present {
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > 1000 || strconv.Itoa(n) != values[0] {
			return 0, page{}, invalid("maxResults must be an integer from 1 to 1000.")
		}
		limit = n
	}
	p := page{Kind: kind, Scope: scope, Prefix: q.Get("prefix"), Delimiter: q.Get("delimiter"), Actor: actorBinding(actor)}
	if len(p.Prefix) > 1024 || !utf8.ValidString(p.Prefix) || strings.ContainsAny(p.Prefix, "\x00\r\n") || p.Delimiter != "" && p.Delimiter != "/" {
		return 0, p, invalid("Invalid prefix or unsupported delimiter.")
	}
	if raw := q.Get("pageToken"); raw != "" {
		if len(raw) > 8192 {
			return 0, p, invalid("Invalid listing token.")
		}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var got page
		if err != nil || strictJSON(data, &got) != nil {
			return 0, p, invalid("Invalid listing token.")
		}
		cursor := got.Cursor
		got.Cursor = ""
		if got != p || cursor == "" {
			return 0, p, invalid("Listing token belongs to another scope, actor or filter.")
		}
		p.Cursor = cursor
	}
	return limit, p, nil
}
func nextPage(p page, cursor string) string {
	p.Cursor = cursor
	data, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(data)
}
func (s *Store) listBuckets(ctx context.Context, q url.Values) (any, error) {
	project := q.Get("project")
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		if err := s.permit(ctx, tx, actor, "storage.buckets.list", Resource{Project: project}); err != nil {
			return nil, err
		}
		if s.projects[project] == "" {
			return nil, invalid("Use a configured synthetic project ID.")
		}
		limit, p, err := listOptions(q, "buckets", project, actor)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT name,project,location,created FROM gcp_storage_buckets WHERE environment=? AND project=? ORDER BY name", s.environment, project)
		if err != nil {
			return nil, err
		}
		var buckets []bucket
		for rows.Next() {
			var b bucket
			if err = rows.Scan(&b.Name, &b.Project, &b.Location, &b.Created); err != nil {
				rows.Close()
				return nil, err
			}
			if b.Name > p.Cursor && strings.HasPrefix(b.Name, p.Prefix) {
				buckets = append(buckets, b)
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		out := map[string]any{"kind": "storage#buckets", "items": []any{}}
		items := []any{}
		for i, b := range buckets {
			if i == limit {
				out["nextPageToken"] = nextPage(p, buckets[i-1].Name)
				break
			}
			if err = s.permit(ctx, tx, actor, "storage.buckets.list", Resource{Project: b.Project, Bucket: b.Name, Exists: true}); err != nil {
				return nil, err
			}
			items = append(items, s.bucketDTO(b))
		}
		out["items"] = items
		return out, nil
	})
}

type storedObject struct {
	Bucket, Name, ContentType, Metadata, CRC32C, MD5, Created, Updated string
	Generation                                                         int64
	Data                                                               []byte
}

func checksums(data []byte) (string, string) {
	var crc [4]byte
	binary.BigEndian.PutUint32(crc[:], crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
	md := md5.Sum(data)
	return base64.StdEncoding.EncodeToString(crc[:]), base64.StdEncoding.EncodeToString(md[:])
}
func (o storedObject) etag() string {
	return base64.RawStdEncoding.EncodeToString([]byte(strconv.FormatInt(o.Generation, 10)))
}
func (o storedObject) dto() map[string]any {
	metadata := map[string]string{}
	_ = json.Unmarshal([]byte(o.Metadata), &metadata)
	return map[string]any{"kind": "storage#object", "id": o.Bucket + "/" + o.Name + "/" + strconv.FormatInt(o.Generation, 10), "bucket": o.Bucket, "name": o.Name, "generation": strconv.FormatInt(o.Generation, 10), "metageneration": "1", "contentType": o.ContentType, "storageClass": "STANDARD", "size": strconv.Itoa(len(o.Data)), "crc32c": o.CRC32C, "md5Hash": o.MD5, "etag": o.etag(), "timeCreated": o.Created, "updated": o.Updated, "metadata": metadata}
}
func (s *Store) readObject(ctx context.Context, tx *sql.Tx, bucket, name string) (storedObject, error) {
	o := storedObject{Bucket: bucket, Name: name}
	err := tx.QueryRowContext(ctx, "SELECT data,content_type,metadata,generation,crc32c,md5,created,updated FROM gcp_storage_objects WHERE environment=? AND bucket=? AND name=?", s.environment, bucket, name).Scan(&o.Data, &o.ContentType, &o.Metadata, &o.Generation, &o.CRC32C, &o.MD5, &o.Created, &o.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return o, absent()
	}
	if err != nil {
		return o, err
	}
	crc, md := checksums(o.Data)
	var metadata map[string]string
	_, _, mimeError := mime.ParseMediaType(o.ContentType)
	if o.Generation <= 0 || crc != o.CRC32C || md != o.MD5 || int64(len(o.Data)) > s.limits.ObjectBytes || strictJSON([]byte(o.Metadata), &metadata) != nil || validateMetadata(metadata) != nil || mimeError != nil || len(o.ContentType) > 256 || strings.ContainsAny(o.ContentType, "\r\n") {
		return o, errors.New("corrupt persisted Storage object")
	}
	return o, nil
}
func (s *Store) putObject(ctx context.Context, bucket string, input objectInput, data []byte, match *int64) (any, error) {
	if data == nil {
		data = []byte{}
	}
	crc, md := checksums(data)
	if input.CRC32C != "" && input.CRC32C != crc || input.MD5 != "" && input.MD5 != md {
		return nil, invalid("Uploaded media does not match the supplied checksum.")
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return nil, err
	}
	if input.Metadata == nil {
		metadata = []byte("{}")
	}
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		b, err := s.loadBucket(ctx, tx, actor, "storage.objects.create", bucket, input.Name)
		if err != nil {
			return nil, err
		}
		old, err := s.readObject(ctx, tx, bucket, input.Name)
		exists := err == nil
		var missing *apiError
		if err != nil && (!errors.As(err, &missing) || missing.status != 404) {
			return nil, err
		}
		if exists {
			if err = s.permit(ctx, tx, actor, "storage.objects.delete", Resource{Project: b.Project, Bucket: bucket, Object: input.Name, Exists: true}); err != nil {
				return nil, err
			}
		}
		if match != nil && (!exists && *match != 0 || exists && old.Generation != *match) {
			return nil, conditionFailed()
		}
		var count, total int64
		if err = tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(SUM(length(data)+length(CAST(metadata AS BLOB))),0) FROM gcp_storage_objects WHERE environment=?", s.environment).Scan(&count, &total); err != nil {
			return nil, err
		}
		oldBytes := int64(0)
		if exists {
			oldBytes = int64(len(old.Data) + len(old.Metadata))
		}
		if int64(len(data)) > s.limits.ObjectBytes || !exists && count >= s.limits.Objects || total-oldBytes > s.limits.StoredBytes-int64(len(data)+len(metadata)) {
			return nil, quotaExceeded()
		}
		var generation int64
		if err = tx.QueryRowContext(ctx, "SELECT next_generation FROM gcp_storage_sequence WHERE environment=?", s.environment).Scan(&generation); err != nil {
			return nil, err
		}
		if generation < 0 || generation == math.MaxInt64 {
			return nil, errors.New("invalid or exhausted local generation sequence")
		}
		generation++
		if err = requireOne(tx.ExecContext(ctx, "UPDATE gcp_storage_sequence SET next_generation=? WHERE environment=?", generation, s.environment)); err != nil {
			return nil, err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		o := storedObject{Bucket: bucket, Name: input.Name, Data: data, ContentType: input.ContentType, Metadata: string(metadata), Generation: generation, CRC32C: crc, MD5: md, Created: now, Updated: now}
		err = requireOne(tx.ExecContext(ctx, `INSERT INTO gcp_storage_objects(environment,bucket,name,data,content_type,metadata,generation,crc32c,md5,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(environment,bucket,name) DO UPDATE SET data=excluded.data,content_type=excluded.content_type,metadata=excluded.metadata,generation=excluded.generation,crc32c=excluded.crc32c,md5=excluded.md5,created=excluded.created,updated=excluded.updated`, s.environment, bucket, input.Name, o.Data, o.ContentType, o.Metadata, o.Generation, o.CRC32C, o.MD5, o.Created, o.Updated))
		if err != nil {
			return nil, err
		}
		return o.dto(), nil
	})
}
func (s *Store) objectOperation(ctx context.Context, method, bucket, name string, match, selected *int64) (any, error) {
	permission := "storage.objects.get"
	if method == http.MethodDelete {
		permission = "storage.objects.delete"
	} else if method != http.MethodGet {
		return nil, &apiError{405, "methodNotAllowed", "Object supports GET or DELETE."}
	}
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		if _, err := s.loadBucket(ctx, tx, actor, permission, bucket, name); err != nil {
			return nil, err
		}
		o, err := s.readObject(ctx, tx, bucket, name)
		if err != nil {
			return nil, err
		}
		if selected != nil && o.Generation != *selected {
			return nil, absent()
		}
		if match != nil && o.Generation != *match {
			return nil, conditionFailed()
		}
		if method == http.MethodGet {
			return o, nil
		}
		result, err := tx.ExecContext(ctx, "DELETE FROM gcp_storage_objects WHERE environment=? AND bucket=? AND name=? AND generation=?", s.environment, bucket, name, o.Generation)
		if err != nil {
			return nil, err
		}
		deleted, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if deleted != 1 {
			return nil, errors.New("object disappeared during transaction")
		}
		return nil, nil
	})
}
func (s *Store) listObjects(ctx context.Context, bucketName string, q url.Values) (any, error) {
	return s.transaction(ctx, func(tx *sql.Tx, actor Actor) (any, error) {
		b, err := s.loadBucket(ctx, tx, actor, "storage.objects.list", bucketName, "")
		if err != nil {
			return nil, err
		}
		limit, p, err := listOptions(q, "objects", b.Project+"/"+bucketName, actor)
		if err != nil {
			return nil, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT name FROM gcp_storage_objects WHERE environment=? AND bucket=? ORDER BY name", s.environment, bucketName)
		if err != nil {
			return nil, err
		}
		var names []string
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			names = append(names, name)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		type entry struct {
			name   string
			prefix bool
		}
		entries := map[string]entry{}
		for _, name := range names {
			if !strings.HasPrefix(name, p.Prefix) {
				continue
			}
			key := name
			isPrefix := false
			if p.Delimiter != "" {
				if i := strings.Index(strings.TrimPrefix(name, p.Prefix), p.Delimiter); i >= 0 {
					key = p.Prefix + strings.TrimPrefix(name, p.Prefix)[:i+len(p.Delimiter)]
					isPrefix = true
				}
			}
			if key > p.Cursor {
				entries[key] = entry{key, isPrefix}
			}
		}
		var keys []string
		for key := range entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := map[string]any{"kind": "storage#objects", "items": []any{}, "prefixes": []string{}}
		items := []any{}
		prefixes := []string{}
		for i, key := range keys {
			if i == limit {
				out["nextPageToken"] = nextPage(p, keys[i-1])
				break
			}
			if entries[key].prefix {
				prefixes = append(prefixes, key)
				continue
			}
			o, e := s.readObject(ctx, tx, bucketName, key)
			if e != nil {
				return nil, e
			}
			items = append(items, o.dto())
		}
		out["items"] = items
		out["prefixes"] = prefixes
		return out, nil
	})
}
