package simpledb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
)

func attributes(ctx context.Context, tx *sql.Tx, a, r, d, i string) (map[string][]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT attributes FROM aws_simpledb_items WHERE account=? AND region=? AND domain=? AND name=?`, a, r, d, i).Scan(&raw)
	out := map[string][]string{}
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("invalid stored SimpleDB attributes")
	}
	return out, nil
}
func (s *Store) Put(ctx context.Context, a, r, d, i string, values map[string][]string, replace map[string]bool) error {
	return s.put(ctx, a, r, d, i, values, replace, nil)
}

type putCondition struct {
	name, value string
	absent      bool
}

func (c *putCondition) check(current map[string][]string) error {
	if c == nil {
		return nil
	}
	values := current[c.name]
	if len(values) > 1 {
		return storeError("MultiValuedAttribute")
	}
	if c.absent {
		if len(values) > 0 {
			return storeError("ConditionalCheckFailed")
		}
		return nil
	}
	if len(values) == 0 {
		return storeError("AttributeDoesNotExist")
	}
	if values[0] != c.value {
		return storeError("ConditionalCheckFailed")
	}
	return nil
}

func (s *Store) put(ctx context.Context, a, r, d, i string, values map[string][]string, replace map[string]bool, condition *putCondition) error {
	if ctx.Err() != nil {
		return storeError("Cancelled")
	}
	if err := itemScope(a, r, d, i); err != nil {
		return err
	}
	if len(values) == 0 {
		return storeError("InvalidParameterValue")
	}
	// Copy and validate the complete input before entering the mutation transaction.
	input := make(map[string][]string, len(values))
	repl := make(map[string]bool, len(replace))
	for name, vs := range values {
		if !validText(name, false) || len(vs) == 0 {
			return storeError("InvalidParameterValue")
		}
		for _, v := range vs {
			if !validText(v, true) {
				return storeError("InvalidParameterValue")
			}
		}
		input[name] = append([]string{}, vs...)
	}
	for name, v := range replace {
		if _, ok := input[name]; !ok {
			return storeError("InvalidParameterValue")
		}
		repl[name] = v
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := requireDomain(ctx, tx, a, r, d); err != nil {
			return err
		}
		current, err := attributes(ctx, tx, a, r, d, i)
		if err != nil {
			return err
		}
		// Evaluate in the same transaction as the update, never via a prior Get.
		if err := condition.check(current); err != nil {
			return err
		}
		for name, vs := range input {
			set := map[string]bool{}
			if !repl[name] {
				for _, v := range current[name] {
					set[v] = true
				}
			}
			for _, v := range vs {
				set[v] = true
			}
			merged := make([]string, 0, len(set))
			for v := range set {
				merged = append(merged, v)
			}
			sort.Strings(merged)
			current[name] = merged
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO aws_simpledb_items(account,region,domain,name,attributes) VALUES(?,?,?,?,?) ON CONFLICT(account,region,domain,name) DO UPDATE SET attributes=excluded.attributes`, a, r, d, i, string(encoded)); err != nil {
			return err
		}
		var saved string
		if err = tx.QueryRowContext(ctx, `SELECT attributes FROM aws_simpledb_items WHERE account=? AND region=? AND domain=? AND name=?`, a, r, d, i).Scan(&saved); err != nil {
			return err
		}
		if saved != string(encoded) {
			return errors.New("SimpleDB write did not persist")
		}
		return nil
	})
}
func (s *Store) Get(ctx context.Context, a, r, d, i string, names []string) (map[string][]string, error) {
	if ctx.Err() != nil {
		return nil, storeError("Cancelled")
	}
	if err := itemScope(a, r, d, i); err != nil {
		return nil, err
	}
	filter := map[string]bool{}
	for _, name := range names {
		if !validText(name, false) {
			return nil, storeError("InvalidParameterValue")
		}
		filter[name] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, failure(ctx, err)
	}
	defer tx.Rollback()
	if err = requireDomain(ctx, tx, a, r, d); err != nil {
		return nil, failure(ctx, err)
	}
	out, err := attributes(ctx, tx, a, r, d, i)
	if err != nil {
		return nil, failure(ctx, err)
	}
	if len(filter) > 0 {
		for name := range out {
			if !filter[name] {
				delete(out, name)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, failure(ctx, err)
	}
	return out, nil
}
