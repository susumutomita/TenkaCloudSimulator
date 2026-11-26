package queue

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Config names trusted host fixtures, not ARM-provisioned accounts or Azure RBAC.
type Config struct {
	Account, Owner, Environment string
	Now                         func() time.Time
}
type AuthenticatedActor struct{ ExerciseIdentity string }
type Handler struct {
	db     *sql.DB
	cfg    Config
	actor  func(context.Context) (AuthenticatedActor, bool)
	txGate chan struct{}
}

var queueName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var accountName = regexp.MustCompile(`^[a-z0-9]{3,24}$`)

func NewOwnedHandler(db *sql.DB, cfg Config, actor func(context.Context) (AuthenticatedActor, bool)) (*Handler, error) {
	if db == nil || actor == nil || !accountName.MatchString(cfg.Account) || cfg.Owner == "" || cfg.Environment == "" {
		return nil, errors.New("Queue requires database, authenticated actor and account/owner/environment")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := initialize(db, cfg); err != nil {
		return nil, err
	}
	return &Handler{db: db, cfg: cfg, actor: actor, txGate: make(chan struct{}, 1)}, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("x-ms-version", apiVersion)
	actor, ok := h.actor(r.Context())
	if !ok {
		respondError(w, problem(401, "AuthenticationFailed"))
		return
	}
	if actor.ExerciseIdentity != h.cfg.Owner {
		respondError(w, problem(403, "AuthorizationFailure"))
		return
	}
	if r.Header.Get("x-ms-version") != apiVersion {
		respondError(w, problem(400, "InvalidHeaderValue"))
		return
	}
	if r.ContentLength > maxRequest {
		respondError(w, problem(413, "RequestBodyTooLarge"))
		return
	}
	for key := range r.Header {
		if strings.HasPrefix(strings.ToLower(key), "x-ms-meta-") {
			respondError(w, problem(501, "NotImplemented"))
			return
		}
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(p) < 2 || len(p) > 4 || p[0] != h.cfg.Account {
		respondError(w, problem(404, "ResourceNotFound"))
		return
	}
	if !queueName.MatchString(p[1]) || strings.Contains(p[1], "--") {
		respondError(w, problem(400, "InvalidResourceName"))
		return
	}
	if len(p) > 2 && p[2] != "messages" {
		respondError(w, problem(501, "NotImplemented"))
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		respondError(w, problem(400, "InvalidQueryParameterValue"))
		return
	}
	allowed := ""
	switch {
	case len(p) == 2 && (r.Method == "PUT" || r.Method == "DELETE"):
	case len(p) == 3 && r.Method == "POST":
		allowed = " visibilitytimeout messagettl "
	case len(p) == 3 && r.Method == "GET":
		allowed = " peekonly numofmessages visibilitytimeout "
	case len(p) == 3 && r.Method == "DELETE":
	case len(p) == 4 && r.Method == "PUT":
		allowed = " popreceipt visibilitytimeout "
	case len(p) == 4 && r.Method == "DELETE":
		allowed = " popreceipt "
	default:
		respondError(w, problem(501, "NotImplemented"))
		return
	}
	for k, v := range q {
		if !strings.Contains(allowed, " "+k+" ") {
			respondError(w, problem(501, "NotImplemented"))
			return
		}
		if len(v) != 1 {
			respondError(w, problem(400, "InvalidQueryParameterValue"))
			return
		}
	}
	var text *string
	if (len(p) == 3 && r.Method == "POST") || (len(p) == 4 && r.Method == "PUT") {
		text, err = textBody(r.Body, len(p) == 4)
		if err != nil {
			respondError(w, err)
			return
		}
	}
	// Keep a leaf's operations serial, but allow a waiting request to leave
	// without acquiring the gate or entering SQLite after cancellation.
	ctx := r.Context()
	select {
	case <-ctx.Done():
		respondError(w, ctx.Err())
		return
	case h.txGate <- struct{}{}:
	}
	defer func() { <-h.txGate }()
	if err = ctx.Err(); err != nil {
		respondError(w, err)
		return
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		respondError(w, err)
		return
	}
	defer tx.Rollback()
	var account, environment, owner string
	err = tx.QueryRow("SELECT account,environment,owner FROM azure_queue_scope WHERE singleton=1").Scan(&account, &environment, &owner)
	if err != nil || account != h.cfg.Account || environment != h.cfg.Environment || owner != h.cfg.Owner {
		respondError(w, problem(500, "InternalError"))
		return
	}
	status, body, headers, err := h.execute(tx, r.Method, p, q, text)
	if err != nil {
		respondError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		respondError(w, err)
		return
	}
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	if body != nil {
		respondXML(w, status, body)
	} else {
		w.WriteHeader(status)
	}
}
func (h *Handler) execute(tx *sql.Tx, method string, p []string, q url.Values, text *string) (int, any, map[string]string, error) {
	fail := func(err error) (int, any, map[string]string, error) { return 0, nil, nil, err }
	name := p[1]
	now := h.cfg.Now().Unix()
	if len(p) == 2 && method == "PUT" {
		var count int
		if err := tx.QueryRow("SELECT count(*) FROM azure_queues").Scan(&count); err != nil {
			return fail(err)
		}
		if err := queueExists(tx, name); err == nil {
			return 204, nil, nil, nil
		} else {
			var e *apiError
			if !errors.As(err, &e) {
				return fail(err)
			}
		}
		if count >= 1000 {
			return fail(problem(409, "QuotaExceeded"))
		}
		err := checkedExec(tx, 1, "INSERT INTO azure_queues VALUES(?)", name)
		if err == nil {
			err = queueExists(tx, name)
		}
		return 201, nil, nil, err
	}
	if err := queueExists(tx, name); err != nil {
		return fail(err)
	}
	if len(p) == 2 {
		err := checkedDelete(tx, "azure_queues", "name=?", name)
		if err == nil {
			var remaining int
			err = tx.QueryRow("SELECT count(*) FROM azure_queue_messages WHERE queue=?", name).Scan(&remaining)
			if err == nil && remaining != 0 {
				err = errors.New("Queue cascade left messages")
			}
		}
		return 204, nil, nil, err
	}
	// Puts and content updates check account-wide quotas. Reclaim expired rows
	// from that same scope in this transaction, even if their queue stays idle.
	// Other operations only need to clean up the queue they address.
	var sweepErr error
	if (len(p) == 3 && method == "POST") || (len(p) == 4 && method == "PUT" && text != nil) {
		sweepErr = checkedDelete(tx, "azure_queue_messages", "expires<=?", now)
	} else {
		sweepErr = checkedDelete(tx, "azure_queue_messages", "queue=? AND expires<=?", name, now)
	}
	if sweepErr != nil {
		return fail(sweepErr)
	}
	if len(p) == 3 && method == "DELETE" {
		err := checkedDelete(tx, "azure_queue_messages", "queue=?", name)
		return 204, nil, nil, err
	}
	if len(p) == 3 && method == "POST" {
		ttl, err := integer(q, "messagettl", week, -1, week)
		if err != nil || ttl == 0 {
			if err == nil {
				err = problem(400, "InvalidQueryParameterValue")
			}
			return fail(err)
		}
		visibility, err := integer(q, "visibilitytimeout", 0, 0, week)
		if err != nil {
			return fail(err)
		}
		expires := now + ttl
		if ttl == -1 {
			expires = 253402300799
		}
		if now+visibility >= expires {
			return fail(problem(400, "InvalidQueryParameterValue"))
		}
		var count, size int64
		if err = tx.QueryRow("SELECT count(*),coalesce(sum(length(CAST(text AS BLOB))),0) FROM azure_queue_messages").Scan(&count, &size); err != nil {
			return fail(err)
		}
		if count >= 10000 || size+int64(len(*text)) > 32<<20 {
			return fail(problem(409, "QuotaExceeded"))
		}
		id, err := messageID()
		if err != nil {
			return fail(err)
		}
		receipt, err := opaque()
		if err != nil {
			return fail(err)
		}
		m := storedMessage{ID: id, Text: *text, Receipt: receipt, Inserted: now, Expires: expires, Visible: now + visibility}
		err = checkedExec(tx, 1, "INSERT INTO azure_queue_messages(queue,id,text,inserted,expires,visible,receipt,dequeues) VALUES(?,?,?,?,?,?,?,0)", name, m.ID, m.Text, m.Inserted, m.Expires, m.Visible, m.Receipt)
		if err == nil {
			err = verifyMessage(tx, name, m)
		}
		if err != nil {
			return fail(err)
		}
		return 201, messageList{Messages: []wireMessage{m.wire(false, true)}}, nil, nil
	}
	if len(p) == 3 && method == "GET" {
		peek := q.Get("peekonly") == "true"
		if v, ok := q["peekonly"]; ok && v[0] != "true" {
			return fail(problem(400, "InvalidQueryParameterValue"))
		}
		if peek && q.Has("visibilitytimeout") {
			return fail(problem(400, "InvalidQueryParameterValue"))
		}
		count, err := integer(q, "numofmessages", 1, 1, 32)
		if err != nil {
			return fail(err)
		}
		visibility, err := integer(q, "visibilitytimeout", 30, 1, week)
		if err != nil {
			return fail(err)
		}
		rows, err := tx.Query("SELECT id,text,receipt,inserted,expires,visible,dequeues FROM azure_queue_messages WHERE queue=? AND visible<=? AND expires>? ORDER BY seq LIMIT ?", name, now, now, count)
		if err != nil {
			return fail(err)
		}
		var messages []storedMessage
		for rows.Next() {
			var m storedMessage
			if err = rows.Scan(&m.ID, &m.Text, &m.Receipt, &m.Inserted, &m.Expires, &m.Visible, &m.Count); err != nil {
				rows.Close()
				return fail(err)
			}
			messages = append(messages, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fail(err)
		}
		result := messageList{}
		for _, m := range messages {
			if !peek {
				m.Count++
				m.Visible = now + visibility
				m.Receipt, err = opaque()
				if err != nil {
					return fail(err)
				}
				err = checkedExec(tx, 1, "UPDATE azure_queue_messages SET dequeues=?,visible=?,receipt=? WHERE id=?", m.Count, m.Visible, m.Receipt, m.ID)
				if err == nil {
					err = verifyMessage(tx, name, m)
				}
				if err != nil {
					return fail(err)
				}
			}
			result.Messages = append(result.Messages, m.wire(true, !peek))
		}
		return 200, result, nil, nil
	}
	if len(p) == 4 {
		if q.Get("popreceipt") == "" {
			return fail(problem(400, "MissingRequiredQueryParameter"))
		}
		m, err := liveMessage(tx, name, p[3], q.Get("popreceipt"), now)
		if err != nil {
			return fail(err)
		}
		if method == "DELETE" {
			err = checkedDelete(tx, "azure_queue_messages", "queue=? AND id=?", name, m.ID)
			return 204, nil, nil, err
		}
		if !q.Has("visibilitytimeout") {
			return fail(problem(400, "MissingRequiredQueryParameter"))
		}
		visibility, err := integer(q, "visibilitytimeout", 0, 0, week)
		if err != nil {
			return fail(err)
		}
		if now+visibility > m.Expires {
			return fail(problem(400, "InvalidQueryParameterValue"))
		}
		if text != nil {
			var size int64
			if err := tx.QueryRow("SELECT coalesce(sum(length(CAST(text AS BLOB))),0) FROM azure_queue_messages").Scan(&size); err != nil {
				return fail(err)
			}
			if size-int64(len(m.Text))+int64(len(*text)) > 32<<20 {
				return fail(problem(409, "QuotaExceeded"))
			}
			m.Text = *text
		}
		m.Receipt, err = opaque()
		if err != nil {
			return fail(err)
		}
		m.Visible = now + visibility
		err = checkedExec(tx, 1, "UPDATE azure_queue_messages SET text=?,visible=?,receipt=? WHERE id=?", m.Text, m.Visible, m.Receipt, m.ID)
		if err == nil {
			err = verifyMessage(tx, name, m)
		}
		if err != nil {
			return fail(err)
		}
		return 204, nil, map[string]string{"x-ms-popreceipt": m.Receipt, "x-ms-time-next-visible": stamp(m.Visible)}, nil
	}
	return fail(problem(501, "NotImplemented"))
}
