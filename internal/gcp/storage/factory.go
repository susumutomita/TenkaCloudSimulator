package storage

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SubjectExtractor is a trusted Go composition seam, not authentication. The
// root adapter validates the common authenticated actor and returns only its
// ExerciseIdentity. It must never derive authority from HTTP headers or roles.
type SubjectExtractor func(context.Context) (exerciseIdentity string, ok bool)

type ProjectBinding struct {
	Project      Project
	OwnerSubject string
}
type FactoryConfig struct {
	Environment    string
	Projects       []ProjectBinding
	Limits         Limits
	ExtractSubject SubjectExtractor
}

type projectHandler struct {
	storage *Store
	extract SubjectExtractor
}

func validSubject(subject string) bool {
	return subject != "" && len(subject) <= 1024 && utf8.ValidString(subject) && strings.TrimSpace(subject) == subject && !strings.ContainsFunc(subject, unicode.IsControl)
}

// NewProjectHandler composes an owner-only synthetic project policy with the
// JSON API leaf. This is not Google IAM, OAuth, or a default administrator grant.
// Project owners persist immutably; no preexisting unbound bucket is adopted.
func NewProjectHandler(db *sql.DB, cfg FactoryConfig) (http.Handler, error) {
	if cfg.ExtractSubject == nil {
		return nil, errors.New("GCP Storage requires an authenticated exercise subject extractor")
	}
	bindings := map[string]ProjectBinding{}
	projects := make([]Project, 0, len(cfg.Projects))
	for _, binding := range cfg.Projects {
		if !validSubject(binding.OwnerSubject) || bindings[binding.Project.ID].OwnerSubject != "" {
			return nil, errors.New("invalid or duplicate immutable synthetic project owner")
		}
		bindings[binding.Project.ID] = binding
		projects = append(projects, binding.Project)
	}
	environment := cfg.Environment
	policy := func(ctx context.Context, tx *sql.Tx, actor Actor, permission string, resource Resource) error {
		if actor.Environment != environment {
			return ErrDenied
		}
		switch permission {
		case "storage.buckets.create", "storage.buckets.list", "storage.buckets.get", "storage.buckets.delete",
			"storage.objects.create", "storage.objects.list", "storage.objects.get", "storage.objects.delete":
		default:
			return ErrDenied
		}
		binding, configured := bindings[resource.Project]
		if !configured {
			return ErrDenied
		}
		var number, owner string
		err := tx.QueryRowContext(ctx, `SELECT p.number,o.subject FROM gcp_storage_projects p JOIN gcp_storage_project_owners o ON o.environment=p.environment AND o.project=p.id WHERE p.environment=? AND p.id=?`, environment, resource.Project).Scan(&number, &owner)
		if err != nil {
			return err
		}
		if number != binding.Project.Number || owner != binding.OwnerSubject {
			return errors.New("persisted synthetic project authority differs from fixed configuration")
		}
		if actor.Subject != owner {
			return ErrDenied
		}
		return nil
	}
	initialize := func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS gcp_storage_project_owners(environment TEXT NOT NULL,project TEXT NOT NULL,subject TEXT NOT NULL,PRIMARY KEY(environment,project),FOREIGN KEY(environment,project) REFERENCES gcp_storage_projects(environment,id) ON DELETE RESTRICT)`); err != nil {
			return err
		}
		rows, err := tx.Query("SELECT project,subject FROM gcp_storage_project_owners WHERE environment=?", environment)
		if err != nil {
			return err
		}
		saved := map[string]string{}
		for rows.Next() {
			var project, subject string
			if err = rows.Scan(&project, &subject); err != nil {
				rows.Close()
				return err
			}
			binding, ok := bindings[project]
			if !ok || binding.OwnerSubject != subject {
				rows.Close()
				return errors.New("immutable synthetic project owner cannot be removed or changed")
			}
			saved[project] = subject
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for project, binding := range bindings {
			if saved[project] != "" {
				continue
			}
			var count int
			if err = tx.QueryRow("SELECT count(*) FROM gcp_storage_buckets WHERE environment=? AND project=?", environment, project).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return errors.New("refusing adoption of preexisting unbound synthetic project buckets")
			}
			if err = requireOne(tx.Exec("INSERT INTO gcp_storage_project_owners(environment,project,subject) VALUES(?,?,?)", environment, project, binding.OwnerSubject)); err != nil {
				return err
			}
		}
		return nil
	}
	store, err := newStore(db, Config{Environment: environment, Projects: projects, Limits: cfg.Limits, AuthorizeTx: policy}, initialize)
	if err != nil {
		return nil, err
	}
	return &projectHandler{storage: store, extract: cfg.ExtractSubject}, nil
}

func (h *projectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	subject, ok := h.extract(r.Context())
	if !ok || !validSubject(subject) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		writeError(w, &apiError{403, "forbidden", "An authenticated exercise actor is required by this provider."})
		return
	}
	// Always replace any leaf actor already in context with the trusted mapping.
	h.storage.ServeHTTP(w, r.WithContext(WithActor(r.Context(), Actor{Subject: subject, Environment: h.storage.environment})))
}
