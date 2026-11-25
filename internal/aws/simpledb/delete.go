package simpledb

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// An absent map entry leaves that attribute alone. A nil value set removes
// the entire attribute, including when other indices name individual values.
type deleteSelections map[string]map[string]bool

func (d deleteSelections) add(name string, value []string) {
	if len(value) == 0 {
		d[name] = nil
		return
	}
	set, exists := d[name]
	if !exists {
		set = make(map[string]bool)
		d[name] = set
	}
	if set != nil {
		set[value[0]] = true
	}
}

// Inputs have passed Query validation. As with Put, read, condition, and
// mutation share one write transaction so another caller cannot interleave.
func (s *Store) deleteAttributes(ctx context.Context, a, r, domain, item string, remove deleteSelections, expected *writeCondition) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := requireDomain(ctx, tx, a, r, domain); err != nil {
			return err
		}
		current, err := attributes(ctx, tx, a, r, domain, item)
		if err != nil {
			return err
		}
		if err := expected.check(current); err != nil {
			return err
		}
		if len(remove) == 0 {
			clear(current)
		} else {
			for name, removeValues := range remove {
				if removeValues == nil {
					delete(current, name)
					continue
				}
				left := slices.DeleteFunc(current[name], func(v string) bool { return removeValues[v] })
				if len(left) == 0 {
					delete(current, name)
				} else {
					current[name] = left
				}
			}
		}
		if len(current) == 0 {
			if _, err = tx.ExecContext(ctx, `DELETE FROM aws_simpledb_items WHERE account=? AND region=? AND domain=? AND name=?`, a, r, domain, item); err != nil {
				return err
			}
			var remaining int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_simpledb_items WHERE account=? AND region=? AND domain=? AND name=?`, a, r, domain, item).Scan(&remaining); err != nil {
				return err
			}
			if remaining != 0 {
				return errors.New("SimpleDB item deletion did not persist")
			}
			return nil
		}
		return saveAttributes(ctx, tx, a, r, domain, item, current)
	})
}
