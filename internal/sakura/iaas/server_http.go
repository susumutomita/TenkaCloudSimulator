package iaas

import (
	"database/sql"
	"net/http"
)

// ServerHandler is a fail-closed integration boundary, not implemented Server
// metadata or a VM capability. The future leaf owns storage and HTTP handling;
// the provider's trusted Actor mapping and literal dispatch need not change.
// Until that independently accepted implementation exists, every request retains
// the previous Disk-only provider's non-success response and denial order.
type ServerHandler struct {
	db      *sql.DB
	config  Config
	extract ActorExtractor
	// Reuse only the existing error envelope/serial generator. No Disk store,
	// Disk operations, schema initialization or success response is involved.
	errors DiskHandler
}

// NewServerHandler borrows db without querying, mutating or closing it.
func NewServerHandler(db *sql.DB, c Config, extract ActorExtractor) (*ServerHandler, error) {
	if db == nil || extract == nil || !validConfig(c) {
		return nil, ErrInvalid
	}
	return &ServerHandler{db: db, config: c, extract: extract}, nil
}

func (h *ServerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, ok := h.extract(r.Context())
	if !ok || a.Subject != h.config.Owner || a.Environment != h.config.Environment || a.Project != h.config.Project || a.Zone != h.config.Zone {
		h.errors.fail(w, 403, "forbidden", "Local disk owner authority required")
		return
	}
	if !dummyBasic(r) {
		h.errors.fail(w, 401, "unauthorized", "The fixed local dummy Basic marker is required")
		return
	}
	if r.Context().Err() != nil {
		h.errors.storeError(w, r.Context().Err())
		return
	}
	if r.URL.EscapedPath() != r.URL.Path {
		h.errors.fail(w, 400, "bad_request", "Encoded path syntax is unsupported")
		return
	}
	h.errors.fail(w, 404, "not_found", "Local route or zone not found")
}
