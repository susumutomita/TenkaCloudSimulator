package aws

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// ParameterStore owns AWS SSM state; it never shares S3's resource namespace.
type ParameterStore struct{ db *sql.DB }
type Parameter struct {
	Name             string
	Type             string
	Value            string
	Version          int64
	LastModifiedDate float64
	ARN              string
	DataType         string
	Description      string `json:"-"`
}

func NewParameterStore(db *sql.DB) (*ParameterStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ssm_parameters(region TEXT NOT NULL,name TEXT NOT NULL,type TEXT NOT NULL,value TEXT NOT NULL,description TEXT NOT NULL,version INTEGER NOT NULL CHECK(version>0),modified REAL NOT NULL,PRIMARY KEY(region,name));`)
	return &ParameterStore{db}, err
}
func (s *ParameterStore) Put(region, name, kind, value string, description *string, overwrite bool) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRow("SELECT version FROM aws_ssm_parameters WHERE region=? AND name=?", region, name).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if err == nil && !overwrite {
		return 0, ssmFailure{"ParameterAlreadyExists", "Use Overwrite to replace an existing parameter."}
	}
	if err == sql.ErrNoRows {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM aws_ssm_parameters").Scan(&count); err != nil {
			return 0, err
		}
		if count >= 1000 {
			return 0, ssmFailure{"ParameterLimitExceeded", "Simulator parameter limit reached."}
		}
	}
	version++
	_, err = tx.Exec(`INSERT INTO aws_ssm_parameters VALUES(?,?,?,?,COALESCE(?,''),?,?) ON CONFLICT(region,name) DO UPDATE SET type=excluded.type,value=excluded.value,description=COALESCE(?,aws_ssm_parameters.description),version=excluded.version,modified=excluded.modified`, region, name, kind, value, description, version, float64(time.Now().UnixMilli())/1000, description)
	if err != nil {
		return 0, err
	}
	return version, tx.Commit()
}
func (s *ParameterStore) List(region string) ([]Parameter, error) {
	return s.ListContext(context.Background(), region)
}
func (s *ParameterStore) ListContext(ctx context.Context, region string) ([]Parameter, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name,type,value,description,version,modified FROM aws_ssm_parameters WHERE region=? ORDER BY name", region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Parameter{}
	for rows.Next() {
		var p Parameter
		if err = rows.Scan(&p.Name, &p.Type, &p.Value, &p.Description, &p.Version, &p.LastModifiedDate); err != nil {
			return nil, err
		}
		p.ARN = fmt.Sprintf("arn:aws:ssm:%s:000000000000:parameter/%s", region, trimParameterSlash(p.Name))
		p.DataType = "text"
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *ParameterStore) Get(region, name string) (Parameter, error) {
	return s.GetContext(context.Background(), region, name)
}
func (s *ParameterStore) GetContext(ctx context.Context, region, name string) (Parameter, error) {
	values, err := s.ListContext(ctx, region)
	if err != nil {
		return Parameter{}, err
	}
	for _, p := range values {
		if p.Name == name {
			return p, nil
		}
	}
	return Parameter{}, ssmFailure{"ParameterNotFound", "The specified parameter does not exist."}
}
func (s *ParameterStore) Delete(region, name string) error {
	r, err := s.db.Exec("DELETE FROM aws_ssm_parameters WHERE region=? AND name=?", region, name)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ssmFailure{"ParameterNotFound", "The specified parameter does not exist."}
	}
	return err
}

func parameterDeleteNames(names []string) ([]string, error) {
	if len(names) < 1 || len(names) > 10 {
		return nil, ssmFailure{"ValidationException", "Names requires 1–10 names."}
	}
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if !validParameterName(name) {
			return nil, ssmFailure{"ValidationException", "Use a parameter name of up to 1011 characters and at most 15 path levels; labels/versions/ARN selectors are not supported."}
		}
		if !slices.Contains(unique, name) {
			unique = append(unique, name)
		}
	}
	return unique, nil
}

// DeleteMany commits the batch together and reports each distinct name once,
// preserving first occurrence order within each result. Atomicity, duplicate
// handling and ordering are simulator guarantees, not AWS compatibility claims.
// It retains the existing name subset and does not impose AWS's recreation delay.
// The caller must authorize ssm:DeleteParameters for every name before calling.
func (s *ParameterStore) DeleteMany(ctx context.Context, region string, names []string) ([]string, []string, error) {
	names, err := parameterDeleteNames(names)
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(Regions, region) {
		return nil, nil, ssmFailure{"ValidationException", "Unsupported simulator region."}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	deleted, missing := []string{}, []string{}
	for _, name := range names {
		var exists int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM aws_ssm_parameters WHERE region=? AND name=?", region, name).Scan(&exists)
		if err == sql.ErrNoRows {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		deleted = append(deleted, name)
	}
	for _, name := range deleted {
		result, err := tx.ExecContext(ctx, "DELETE FROM aws_ssm_parameters WHERE region=? AND name=?", region, name)
		if err != nil {
			return nil, nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		if changed != 1 {
			return nil, nil, fmt.Errorf("parameter deletion did not remove its target")
		}
	}
	// Check after every mutation: a later trigger must not resurrect an earlier
	// target while the API still reports that it was deleted.
	for _, name := range deleted {
		var exists int
		err = tx.QueryRowContext(ctx, "SELECT 1 FROM aws_ssm_parameters WHERE region=? AND name=?", region, name).Scan(&exists)
		if err == nil {
			return nil, nil, fmt.Errorf("deleted parameter still exists")
		}
		if err != sql.ErrNoRows {
			return nil, nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, nil, err
	}
	return deleted, missing, nil
}
