package trace

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (e *Engine) seal(p page) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	signature := hmac.New(sha256.New, e.pageKey[:])
	signature.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(raw, signature.Sum(nil)...)), nil
}

func (e *Engine) parsePage(q listQuery, encoded string) (page, error) {
	p := page{Query: q}
	if encoded == "" {
		return p, nil
	}
	if len(encoded) > 2048 {
		return p, invalid("Invalid page token.")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) <= sha256.Size {
		return p, invalid("Invalid page token.")
	}
	body, actual := raw[:len(raw)-sha256.Size], raw[len(raw)-sha256.Size:]
	signature := hmac.New(sha256.New, e.pageKey[:])
	signature.Write(body)
	if !hmac.Equal(actual, signature.Sum(nil)) || json.Unmarshal(body, &p) != nil || p.Query != q || !validTraceID(p.After) || p.High <= 0 {
		return p, invalid("Invalid page token for this trace query.")
	}
	return p, nil
}

func listOptions(v url.Values) (listQuery, error) {
	var q listQuery
	start, err := stamp(v.Get("startTime"))
	if err != nil {
		return q, err
	}
	end, err := stamp(v.Get("endTime"))
	if err != nil {
		return q, err
	}
	if end.Before(start) {
		return q, invalid("endTime precedes startTime.")
	}
	q.Start, q.End = start.Format(stampLayout), end.Format(stampLayout)
	q.View = v.Get("view")
	switch q.View {
	case "", "VIEW_TYPE_UNSPECIFIED":
		q.View = "MINIMAL"
	case "MINIMAL", "ROOTSPAN", "COMPLETE":
	default:
		return q, invalid("Unknown trace view.")
	}
	if v.Has("filter") || v.Has("orderBy") && v.Get("orderBy") != "trace_id" {
		return q, &apiError{http.StatusNotImplemented, "UNIMPLEMENTED", "The local list supports the trace_id order and no filters."}
	}
	q.PageSize = 25
	if v.Has("pageSize") {
		size, err := strconv.ParseInt(v.Get("pageSize"), 10, 32)
		if err != nil {
			return q, invalid("Invalid pageSize.")
		}
		if size > 0 {
			q.PageSize = min(int(size), 50)
		}
	}
	return q, nil
}

func queryOptions(r *http.Request, isList bool) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, invalid("Malformed query parameters.")
	}
	for name, items := range values {
		if len(items) != 1 {
			return nil, invalid("Repeated query parameter.")
		}
		switch name {
		case "alt":
			if items[0] != "json" {
				return nil, invalid("Only the JSON representation is supported.")
			}
		case "prettyPrint":
			if items[0] != "true" && items[0] != "false" {
				return nil, invalid("Invalid prettyPrint value.")
			}
		case "startTime", "endTime", "view", "pageSize", "pageToken", "filter", "orderBy":
			if !isList {
				return nil, invalid("This query parameter applies only to ListTraces.")
			}
		default:
			return nil, invalid("Unsupported query parameter.")
		}
	}
	return values, nil
}

func (e *Engine) checkOwner(ctx context.Context, tx *sql.Tx) error {
	subject, ok := e.config.Subject(ctx)
	if !ok || subject != e.config.Owner {
		return &apiError{403, "PERMISSION_DENIED", "The local project owner is required."}
	}
	var owner string
	err := tx.QueryRowContext(ctx, "SELECT owner FROM gcp_trace_projects WHERE project=?", e.config.Project).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != subject {
		return &apiError{403, "PERMISSION_DENIED", "The stored project owner does not match."}
	}
	return nil
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply, err := e.handle(w, r)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err != nil {
		var f *apiError
		if !errors.As(err, &f) {
			f = &apiError{500, "INTERNAL", "The local trace operation did not complete."}
		}
		w.WriteHeader(f.code)
		reply = map[string]any{"error": map[string]any{"code": f.code, "status": f.status, "message": f.message}}
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func (e *Engine) handle(w http.ResponseWriter, r *http.Request) (any, error) {
	ctx := r.Context()
	subject, ok := e.config.Subject(ctx)
	if !ok || subject != e.config.Owner {
		return nil, &apiError{403, "PERMISSION_DENIED", "The local project owner is required."}
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "cloudtrace" || parts[1] != "v1" || parts[2] != "projects" || parts[4] != "traces" {
		return nil, &apiError{501, "UNIMPLEMENTED", "Unsupported local Cloud Trace method or path."}
	}
	if parts[3] != e.config.Project {
		return nil, &apiError{403, "PERMISSION_DENIED", "Project is outside the local scope."}
	}
	get := r.Method == http.MethodGet && len(parts) == 6
	list := r.Method == http.MethodGet && len(parts) == 5
	patch := r.Method == http.MethodPatch && len(parts) == 5
	if !get && !list && !patch {
		return nil, &apiError{501, "UNIMPLEMENTED", "Unsupported local Cloud Trace method or path."}
	}
	values, err := queryOptions(r, list)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return nil, invalid("The trace request exceeds the local limit or could not be read.")
	}
	now := e.config.Now().UTC()
	if patch {
		mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mt != "application/json" {
			return nil, invalid("PatchTraces requires application/json.")
		}
		traces, err := decodeBatch(raw, e.config.Project, now)
		if err != nil {
			return nil, err
		}
		if err = e.record(ctx, traces, now); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		return nil, invalid("A read requires an empty request body.")
	}
	if get && !validTraceID(parts[5]) {
		return nil, invalid("Invalid trace ID.")
	}
	var cursor page
	if list {
		q, err := listOptions(values)
		if err != nil {
			return nil, err
		}
		cursor, err = e.parsePage(q, values.Get("pageToken"))
		if err != nil {
			return nil, err
		}
	}
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = e.checkOwner(ctx, tx); err != nil {
		return nil, err
	}
	var reply any
	cutoff := now.Add(-retention).Unix()
	if get {
		reply, err = e.readTrace(ctx, tx, parts[5], math.MaxInt64, cutoff, false)
	} else {
		if cursor.After == "" {
			if err = tx.QueryRowContext(ctx, "SELECT coalesce(max(sequence),0) FROM gcp_trace_spans WHERE project=?", e.config.Project).Scan(&cursor.High); err != nil {
				return nil, err
			}
		}
		reply, err = e.list(ctx, tx, cursor, cutoff)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return reply, nil
}
