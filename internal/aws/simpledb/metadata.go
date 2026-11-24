package simpledb

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// DomainStats describes the committed contents observed in one read snapshot.
type DomainStats struct {
	ItemCount                int64
	ItemNamesSizeBytes       int64
	AttributeNameCount       int64
	AttributeNamesSizeBytes  int64
	AttributeValueCount      int64
	AttributeValuesSizeBytes int64
	Timestamp                int64
}

// DomainMetadata reads statistics for an existing domain in the caller's scope.
func (s *Store) DomainMetadata(ctx context.Context, a, r, d string) (DomainStats, error) {
	if err := ctx.Err(); err != nil {
		return DomainStats{}, failure(ctx, err)
	}
	if err := domainScope(a, r, d); err != nil {
		return DomainStats{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DomainStats{}, failure(ctx, err)
	}
	defer tx.Rollback()
	if err := requireDomain(ctx, tx, a, r, d); err != nil {
		return DomainStats{}, failure(ctx, err)
	}

	rows, err := tx.QueryContext(ctx, `SELECT name, attributes FROM aws_simpledb_items WHERE account=? AND region=? AND domain=?`, a, r, d)
	if err != nil {
		return DomainStats{}, failure(ctx, err)
	}
	defer rows.Close()
	var stats DomainStats
	names := make(map[string]struct{})
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return DomainStats{}, failure(ctx, err)
		}
		var attributes map[string][]string
		if err := json.Unmarshal(raw, &attributes); err != nil {
			return DomainStats{}, failure(ctx, err)
		}
		if attributes == nil {
			return DomainStats{}, errors.New("invalid stored SimpleDB attributes")
		}
		stats.ItemCount++
		stats.ItemNamesSizeBytes += int64(len(name))
		for name, values := range attributes {
			if _, ok := names[name]; !ok {
				names[name] = struct{}{}
				stats.AttributeNameCount++
				stats.AttributeNamesSizeBytes += int64(len(name))
			}
			stats.AttributeValueCount += int64(len(values))
			for _, value := range values {
				stats.AttributeValuesSizeBytes += int64(len(value))
			}
		}
	}
	if err := rows.Err(); err != nil {
		return DomainStats{}, failure(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return DomainStats{}, failure(ctx, err)
	}
	stats.Timestamp = time.Now().Unix()
	return stats, nil
}
