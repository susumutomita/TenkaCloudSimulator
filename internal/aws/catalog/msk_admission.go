package catalog

import (
	"context"
	"database/sql"
	"errors"
)

// Attached and tagged creation checks all affected resources against one
// revision of the same caller's policy before looking up the configuration.
func (q *request) authorizeMSKCreate(r row) error {
	software, _ := r.Document["CurrentBrokerSoftwareInfo"].(map[string]any)
	configuration, _ := software["ConfigurationArn"].(string)
	if configuration == "" && len(r.Tags) == 0 {
		return q.authorize(r.ARN)
	}
	if q.e.cfg.AuthorizeSnapshot == nil {
		return errors.New("MSK compound creation requires a policy snapshot")
	}
	decide, err := q.e.cfg.AuthorizeSnapshot(q.ctx, q.principal)
	if err != nil {
		return err
	}
	if decide == nil {
		return errors.New("MSK compound creation received an empty policy snapshot")
	}
	if err := q.checkDecision(decide("kafka:CreateCluster", r.ARN)); err != nil {
		return err
	}
	if configuration != "" {
		if err := q.checkDecision(decide("kafka:CreateCluster", configuration)); err != nil {
			return err
		}
	}
	if len(r.Tags) > 0 {
		return q.checkDecision(decide("kafka:TagResource", r.ARN))
	}
	return nil
}

// MSK has no external work to complete after cancellation. Bind preliminary
// resource lookups and list reads to the request, including pool admission.
func (q *request) readStore() querier {
	if q.b.spec.Model == "kafka" {
		return mskReader{q.ctx, q.e.store.db}
	}
	return q.e.store.db
}

type mskReader struct {
	ctx context.Context
	db  *sql.DB
}

func (r mskReader) QueryRow(query string, args ...any) *sql.Row {
	return r.db.QueryRowContext(r.ctx, query, args...)
}

func (r mskReader) Query(query string, args ...any) (*sql.Rows, error) {
	return r.db.QueryContext(r.ctx, query, args...)
}
