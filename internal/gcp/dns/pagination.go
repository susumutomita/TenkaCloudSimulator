package dns

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
)

type pageCursor struct {
	Scope  string `json:"scope"`
	Offset int    `json:"offset"`
}

// Page tokens use a persisted database-local HMAC authority. They are not
// authentication: every request still checks owner/project first. Pagination is
// bounded offset traversal, not a stable snapshot across intervening mutations.
func (e *Engine) page(items []any, kind, scope string, q url.Values) (map[string]any, error) {
	size := 50
	if raw, ok := q["maxResults"]; ok {
		n, err := strconv.Atoi(raw[0])
		if err != nil || n < 1 || n > 100 {
			return nil, bad("maxResults must be 1..100.")
		}
		size = n
	}
	filters := url.Values{}
	for k, v := range q {
		if k != "maxResults" && k != "pageToken" && k != "alt" && k != "prettyPrint" {
			filters[k] = v
		}
	}
	scope = e.cfg.Project + "/" + scope + "?" + filters.Encode()
	offset := 0
	if token, ok := q["pageToken"]; ok {
		if len(token[0]) > 4096 {
			return nil, bad("Invalid pageToken.")
		}
		data, err := base64.RawURLEncoding.DecodeString(token[0])
		if err != nil || len(data) <= sha256.Size {
			return nil, bad("Invalid pageToken.")
		}
		raw, signature := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
		mac := hmac.New(sha256.New, e.tokenKey)
		mac.Write(raw)
		var p pageCursor
		if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(raw, &p) != nil || p.Scope != scope || p.Offset < 1 || p.Offset > len(items) {
			return nil, bad("pageToken does not match this collection/filter/generation.")
		}
		offset = p.Offset
	}
	end := min(offset+size, len(items))
	out := map[string]any{kind: items[offset:end]}
	if end < len(items) {
		raw, _ := json.Marshal(pageCursor{scope, end})
		mac := hmac.New(sha256.New, e.tokenKey)
		mac.Write(raw)
		out["nextPageToken"] = base64.RawURLEncoding.EncodeToString(append(raw, mac.Sum(nil)...))
	}
	return out, nil
}
