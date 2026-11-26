package cognitoidp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

type listCursor struct {
	Scope Scope
	Actor Actor
	Limit int
	After string
}

func (s *Store) sign(b []byte) []byte {
	mac := hmac.New(sha256.New, s.cursorKey[:])
	mac.Write([]byte("cognito-idp/ListUserPools/v1\x00"))
	mac.Write(b)
	return mac.Sum(nil)
}
func (s *Store) encode(c listCursor) string {
	body, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(s.sign(body))
}
func (s *Store) decode(token string, scope Scope, actor Actor, limit int) (string, error) {
	if len(token) > 1024 || strings.ContainsAny(token, "\r\n") {
		return "", errInvalid
	}
	content, mac, ok := strings.Cut(token, ".")
	if !ok {
		return "", errInvalid
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(content)
	if err != nil {
		return "", errInvalid
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(mac)
	if err != nil || !hmac.Equal(sig, s.sign(b)) {
		return "", errInvalid
	}
	var c listCursor
	if json.Unmarshal(b, &c) != nil || c.Scope != scope || c.Actor != actor || c.Limit != limit || !scope.validPoolID(c.After) {
		return "", errInvalid
	}
	return c.After, nil
}

func (s *Store) list(ctx context.Context, scope Scope, actor Actor, limit int, token string) ([]pool, string, error) {
	if !scope.valid() || !actor.valid() || limit < 1 || limit > maxPage {
		return nil, "", errInvalid
	}
	after := ""
	if token != "" {
		var err error
		after, err = s.decode(token, scope, actor, limit)
		if err != nil {
			return nil, "", err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pool_id,name,created_ms FROM aws_cognito_user_pools
		WHERE account=? AND region=? AND pool_id>? ORDER BY pool_id LIMIT ?`,
		scope.Account, scope.Region, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]pool, 0, limit)
	for rows.Next() {
		var p pool
		if err := rows.Scan(&p.ID, &p.Name, &p.CreatedMS); err != nil {
			return nil, "", err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(out) <= limit {
		return out, "", nil
	}
	return out[:limit], s.encode(listCursor{scope, actor, limit, out[limit-1].ID}), nil
}
