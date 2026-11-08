package blob

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

type cursor struct{ Environment, Account, Operation, Container, Prefix, After string }

func (s *Service) marker(op Operation, container string, o listOptions, after string) string {
	raw, _ := json.Marshal(cursor{s.config.Environment, s.config.Account, string(op), container, o.Prefix, after})
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Service) after(op Operation, container string, o listOptions) (string, error) {
	if o.Marker == "" {
		return "", nil
	}
	invalid := fail(400, "InvalidQueryParameterValue", "The marker does not match this listing scope.")
	if len(o.Marker) > 16000 {
		return "", invalid
	}
	a, b, ok := strings.Cut(o.Marker, ".")
	if !ok {
		return "", invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		return "", invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return "", invalid
	}
	mac := hmac.New(sha256.New, s.cursorKey)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return "", invalid
	}
	var c cursor
	if json.Unmarshal(raw, &c) != nil || c.Environment != s.config.Environment || c.Account != s.config.Account || c.Operation != string(op) || c.Container != container || c.Prefix != o.Prefix {
		return "", invalid
	}
	return c.After, nil
}
func (s *Service) list(ctx context.Context, op Operation, container string, o listOptions) (page, error) {
	tx, err := s.begin(ctx, op, container, "")
	if err != nil {
		return page{}, err
	}
	defer tx.Rollback()
	after, err := s.after(op, container, o)
	if err != nil {
		return page{}, err
	}
	if op == ListBlobs {
		if _, err = s.parent(tx, container); err != nil {
			return page{}, err
		}
	}
	query := "SELECT name,etag,modified,metadata FROM azure_blob_containers WHERE environment=? AND account=? AND name>? AND substr(name,1,length(?))=? ORDER BY name LIMIT ?"
	args := []any{s.config.Environment, s.config.Account, after, o.Prefix, o.Prefix, o.Limit + 1}
	if op == ListBlobs {
		query = "SELECT name,etag,modified,metadata,content_type,md5,length(body) FROM azure_blob_objects WHERE environment=? AND account=? AND container=? AND name>? AND substr(name,1,length(?))=? ORDER BY name LIMIT ?"
		args = []any{s.config.Environment, s.config.Account, container, after, o.Prefix, o.Prefix, o.Limit + 1}
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page{}, err
	}
	result := page{Items: []entity{}}
	for rows.Next() {
		var e entity
		var raw string
		if op == ListBlobs {
			err = rows.Scan(&e.Name, &e.ETag, &e.Modified, &raw, &e.ContentType, &e.MD5, &e.Length)
		} else {
			err = rows.Scan(&e.Name, &e.ETag, &e.Modified, &raw)
		}
		if err != nil {
			rows.Close()
			return page{}, err
		}
		if !storedTag.MatchString(e.ETag) {
			rows.Close()
			return page{}, errInternal
		}
		if _, err = http.ParseTime(e.Modified); err != nil {
			rows.Close()
			return page{}, errInternal
		}
		if op == ListBlobs && (!validBlob(e.Name) || e.Length < 0 || e.Length > MaxBlobBytes || !validLabel(e.ContentType, 256)) || op == ListContainers && !validContainer(e.Name) {
			rows.Close()
			return page{}, errInternal
		}
		if op == ListBlobs {
			digest, e := base64.StdEncoding.DecodeString(e.MD5)
			if e != nil || len(digest) != md5.Size {
				rows.Close()
				return page{}, errInternal
			}
		}
		e.Metadata, err = decodeMetadata(raw)
		if err != nil {
			rows.Close()
			return page{}, err
		}
		if !strings.HasPrefix(e.Name, o.Prefix) {
			continue
		}
		if len(result.Items) == o.Limit {
			result.Next = s.marker(op, container, o, result.Items[len(result.Items)-1].Name)
			break
		}
		if !o.Metadata {
			e.Metadata = nil
		}
		result.Items = append(result.Items, e)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return page{}, err
	}
	if err = rows.Close(); err != nil {
		return page{}, err
	}
	return result, tx.Commit()
}
