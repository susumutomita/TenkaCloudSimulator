package trace

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"
)

type Config struct {
	Project, Owner string
	Subject        func(context.Context) (string, bool)
	Now            func() time.Time
}

type Engine struct {
	db      *sql.DB
	config  Config
	pageKey [32]byte
}

type apiError struct {
	code            int
	status, message string
}

func (e *apiError) Error() string { return e.message }
func invalid(message string) error {
	return &apiError{http.StatusBadRequest, "INVALID_ARGUMENT", message}
}
func exhausted() error {
	return &apiError{http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Local span storage is full."}
}

var projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func New(db *sql.DB, cfg Config) (*Engine, error) {
	if db == nil || !projectID.MatchString(cfg.Project) || cfg.Owner == "" || len(cfg.Owner) > 256 || cfg.Subject == nil {
		return nil, errors.New("Cloud Trace requires a database, project, owner and authenticated subject")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	e := &Engine{db: db, config: cfg}
	if _, err := rand.Read(e.pageKey[:]); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS gcp_trace_projects(project TEXT PRIMARY KEY, owner TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS gcp_trace_spans(
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		project TEXT NOT NULL REFERENCES gcp_trace_projects(project),
		trace_id TEXT NOT NULL, observed INTEGER NOT NULL, start_time TEXT NOT NULL, end_time TEXT NOT NULL, document TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS gcp_trace_by_id ON gcp_trace_spans(project,trace_id,sequence);
	CREATE INDEX IF NOT EXISTS gcp_trace_by_window ON gcp_trace_spans(project,start_time,end_time);
	CREATE INDEX IF NOT EXISTS gcp_trace_by_age ON gcp_trace_spans(project,observed);`)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("INSERT INTO gcp_trace_projects(project,owner) VALUES(?,?) ON CONFLICT(project) DO NOTHING", cfg.Project, cfg.Owner); err != nil {
		return nil, err
	}
	var owner string
	if err = tx.QueryRow("SELECT owner FROM gcp_trace_projects WHERE project=?", cfg.Project).Scan(&owner); err != nil {
		return nil, err
	}
	if owner != cfg.Owner {
		return nil, errors.New("Cloud Trace stored project owner differs from configuration")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) record(ctx context.Context, traces []traceRecord, now time.Time) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := e.checkOwner(ctx, tx); err != nil {
		return err
	}
	project := e.config.Project
	if _, err = tx.ExecContext(ctx, "DELETE FROM gcp_trace_spans WHERE project=? AND observed<=?", project, now.Add(-retention).Unix()); err != nil {
		return err
	}
	var total int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM gcp_trace_spans WHERE project=?", project).Scan(&total); err != nil {
		return err
	}
	additions := make(map[string]int)
	for _, t := range traces {
		additions[t.ID] += len(t.Spans)
		total += len(t.Spans)
	}
	if total > maxProjectSpans {
		return exhausted()
	}
	for id, n := range additions {
		var old int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM gcp_trace_spans WHERE project=? AND trace_id=?", project, id).Scan(&old); err != nil {
			return err
		}
		if old+n > maxTraceSpans {
			return exhausted()
		}
	}
	// Verify the acknowledged documents after the entire batch has been inserted.
	// A later insertion can trigger a SQLite change affecting an earlier row.
	type receipt struct {
		sequence                      int64
		traceID, start, end, document string
	}
	var written []receipt
	for _, t := range traces {
		for _, s := range t.Spans {
			doc, err := json.Marshal(s)
			if err != nil {
				return err
			}
			res, err := tx.ExecContext(ctx, "INSERT INTO gcp_trace_spans(project,trace_id,observed,start_time,end_time,document) VALUES(?,?,?,?,?,?)", project, t.ID, now.Unix(), s.Start, s.End, string(doc))
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return errors.New("Cloud Trace span insertion was not acknowledged")
			}
			seq, err := res.LastInsertId()
			if err != nil {
				return err
			}
			written = append(written, receipt{seq, t.ID, s.Start, s.End, string(doc)})
		}
	}
	var observed int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM gcp_trace_spans WHERE project=?", project).Scan(&observed); err != nil {
		return err
	}
	if observed != total {
		return errors.New("Cloud Trace batch changed an unexpected number of observations")
	}
	for _, r := range written {
		var matches int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM gcp_trace_spans WHERE sequence=? AND project=? AND trace_id=? AND observed=? AND start_time=? AND end_time=? AND document=?", r.sequence, project, r.traceID, now.Unix(), r.start, r.end, r.document).Scan(&matches); err != nil {
			return err
		}
		if matches != 1 {
			return errors.New("Cloud Trace stored span does not match the accepted observation")
		}
	}
	return tx.Commit()
}

func (e *Engine) readTrace(ctx context.Context, tx *sql.Tx, id string, maxSeq, cutoff int64, roots bool) (traceRecord, error) {
	out := traceRecord{Project: e.config.Project, ID: id}
	rows, err := tx.QueryContext(ctx, "SELECT start_time,end_time,document FROM gcp_trace_spans WHERE project=? AND trace_id=? AND sequence<=? AND observed>? ORDER BY sequence", e.config.Project, id, maxSeq, cutoff)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var start, end, raw string
		if err = rows.Scan(&start, &end, &raw); err != nil {
			return out, err
		}
		var s span
		if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return out, err
		}
		if s.Start != start || s.End != end || !validSpanID(s.ID) || s.Name == "" {
			return out, errors.New("Cloud Trace stored span is inconsistent")
		}
		count++
		if count > maxTraceSpans {
			return out, errors.New("Cloud Trace stored span count exceeds local limit")
		}
		if !roots || s.Parent == "" || s.Parent == "0" {
			out.Spans = append(out.Spans, s)
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if count == 0 {
		return out, &apiError{http.StatusNotFound, "NOT_FOUND", "Trace not found."}
	}
	return out, nil
}

type listQuery struct {
	Start, End, View string
	PageSize         int
}

type page struct {
	Query listQuery
	After string
	High  int64
}

type listResponse struct {
	Traces []traceRecord `json:"traces"`
	Next   string        `json:"nextPageToken,omitempty"`
}

func (e *Engine) list(ctx context.Context, tx *sql.Tx, p page, cutoff int64) (listResponse, error) {
	result := listResponse{Traces: []traceRecord{}}
	// Match the inclusive interval against any stored span. Get and COMPLETE
	// read the full trace at the same high-water mark, including duplicate IDs.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT trace_id FROM gcp_trace_spans
		WHERE project=? AND sequence<=? AND observed>? AND trace_id>? AND start_time<=? AND end_time>=?
		ORDER BY trace_id LIMIT ?`, e.config.Project, p.High, cutoff, p.After, p.Query.End, p.Query.Start, p.Query.PageSize+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return result, err
		}
		if !validTraceID(id) {
			return result, errors.New("Cloud Trace stored invalid trace ID")
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	if len(ids) > p.Query.PageSize {
		ids = ids[:p.Query.PageSize]
		p.After = ids[len(ids)-1]
		result.Next, err = e.seal(p)
		if err != nil {
			return result, err
		}
	}
	for _, id := range ids {
		t := traceRecord{Project: e.config.Project, ID: id}
		if p.Query.View != "MINIMAL" {
			t, err = e.readTrace(ctx, tx, id, p.High, cutoff, p.Query.View == "ROOTSPAN")
			if err != nil {
				return result, err
			}
		}
		result.Traces = append(result.Traces, t)
	}
	return result, nil
}
