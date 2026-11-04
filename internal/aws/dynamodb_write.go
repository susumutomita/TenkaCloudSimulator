package aws

import (
	"database/sql"
	"fmt"
)

// A successful SQL statement may affect zero rows (for example RAISE(IGNORE)).
// Do not acknowledge a native write until its exact transaction-local effect is known.
func dynamoExecExact(tx *sql.Tx, expected int64, query string, args ...any) error {
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != expected {
		return fmt.Errorf("DynamoDB write affected an unexpected number of rows")
	}
	return nil
}
