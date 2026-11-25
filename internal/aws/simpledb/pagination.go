package simpledb

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

type cursor struct{ Account, Region, After string }

func (s *Store) encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return s.signCursor(b, "")
}
func (s *Store) signCursor(b []byte, purpose string) string {
	m := hmac.New(sha256.New, s.tokenKey[:])
	m.Write([]byte(purpose))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Store) decodeCursor(token, a, r string) (string, error) {
	b, e := s.openCursor(token, "")
	if e != nil {
		return "", e
	}
	bad := storeError("InvalidNextToken")
	var c cursor
	if json.Unmarshal(b, &c) != nil || c.Account != a || c.Region != r || !domainName.MatchString(c.After) {
		return "", bad
	}
	return c.After, nil
}

func (s *Store) openCursor(token, purpose string) ([]byte, error) {
	bad := storeError("InvalidNextToken")
	if len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
		return nil, bad
	}
	p := strings.Split(token, ".")
	if len(p) != 2 {
		return nil, bad
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(p[0])
	if e != nil {
		return nil, bad
	}
	sig, e := base64.RawURLEncoding.Strict().DecodeString(p[1])
	if e != nil {
		return nil, bad
	}
	m := hmac.New(sha256.New, s.tokenKey[:])
	m.Write([]byte(purpose))
	m.Write(b)
	if !hmac.Equal(sig, m.Sum(nil)) {
		return nil, bad
	}
	return b, nil
}

// Tokens are opaque and scoped to this Store instance. Stable-state traversal
// is supported; snapshots across concurrent changes/reopened Stores are not.
func (s *Store) ListDomains(ctx context.Context, a, r string, limit int, token string) ([]string, string, error) {
	if ctx.Err() != nil {
		return nil, "", storeError("Cancelled")
	}
	if err := scope(a, r); err != nil {
		return nil, "", err
	}
	if limit < 0 || limit > 100 {
		return nil, "", storeError("InvalidParameterValue")
	}
	if limit == 0 {
		limit = 100
	}
	after := ""
	var err error
	if token != "" {
		after, err = s.decodeCursor(token, a, r)
		if err != nil {
			return nil, "", err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM aws_simpledb_domains WHERE account=? AND region=? AND name>? ORDER BY name LIMIT ?`, a, r, after, limit+1)
	if err != nil {
		return nil, "", failure(ctx, err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			return nil, "", failure(ctx, err)
		}
		out = append(out, n)
	}
	if err = rows.Err(); err != nil {
		return nil, "", failure(ctx, err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = s.encodeCursor(cursor{a, r, out[len(out)-1]})
	}
	return out, next, nil
}
