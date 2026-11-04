package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

func (s *CloudWatchLogsStore) Groups(ctx context.Context, region, prefix, token string, limit int) ([]LogsGroup, string, error) {
	if err := validLogsRegion(region); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 50 {
		return nil, "", logsInvalid("limit must be between 1 and 50.")
	}
	scope := logsScope("groups", region, prefix)
	c := logsCursor{Scope: scope, Expires: s.now().Add(24 * time.Hour).UnixMilli()}
	var err error
	if token != "" {
		c, err = s.decodeCursor(token, scope)
		if err != nil {
			return nil, "", err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT g.name,g.created,COALESCE((SELECT sum(length(CAST(e.message AS BLOB))) FROM aws_logs_events e WHERE e.region=g.region AND e.group_name=g.name),0) FROM aws_logs_groups g WHERE g.region=? AND substr(g.name,1,length(?))=? AND g.name>? ORDER BY g.name LIMIT ?`, region, prefix, prefix, c.Name, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	result := []LogsGroup{}
	for rows.Next() {
		var g LogsGroup
		if err = rows.Scan(&g.Name, &g.Created, &g.Bytes); err != nil {
			return nil, "", err
		}
		g.LogGroupARN = logsGroupARN(region, g.Name)
		g.ARN = g.LogGroupARN + ":*"
		g.Class = "STANDARD"
		result = append(result, g)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(result) > limit {
		result = result[:limit]
		c.Name = result[len(result)-1].Name
		c.Expires = s.now().Add(24 * time.Hour).UnixMilli()
		next = s.encodeCursor(c)
	}
	return result, next, nil
}

type LogsStreamsQuery struct {
	Prefix, Token, OrderBy string
	Descending             bool
	Limit                  int
}

func (s *CloudWatchLogsStore) Streams(ctx context.Context, region, group string, q LogsStreamsQuery) ([]LogsStream, string, error) {
	if err := validLogsRegion(region); err != nil {
		return nil, "", err
	}
	if !logsGroupName.MatchString(group) {
		return nil, "", logsInvalid("Invalid log group name.")
	}
	if q.Limit < 1 || q.Limit > 50 {
		return nil, "", logsInvalid("limit must be between 1 and 50.")
	}
	if q.OrderBy == "" {
		q.OrderBy = "LogStreamName"
	}
	if q.OrderBy != "LogStreamName" && q.OrderBy != "LastEventTime" || q.OrderBy == "LastEventTime" && q.Prefix != "" {
		return nil, "", logsInvalid("Use LogStreamName or LastEventTime; a prefix cannot be combined with LastEventTime.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var generation string
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM aws_logs_groups WHERE region=? AND name=?", region, group).Scan(&generation); errors.Is(err, sql.ErrNoRows) {
		return nil, "", logsMissing()
	}
	if err != nil {
		return nil, "", err
	}
	scope := logsScope("streams", region, group, q.Prefix, q.OrderBy, q.Descending)
	c := logsCursor{Scope: scope, Generation: generation, Expires: s.now().Add(24 * time.Hour).UnixMilli()}
	if q.Token != "" {
		c, err = s.decodeCursor(q.Token, scope)
		if err != nil {
			return nil, "", err
		}
		if c.Generation != generation {
			return nil, "", logsInvalid("Pagination token belongs to a deleted log group.")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.name,s.created,min(e.timestamp),max(e.timestamp),max(e.ingested) FROM aws_logs_streams s LEFT JOIN aws_logs_events e ON e.region=s.region AND e.group_name=s.group_name AND e.stream_name=s.name WHERE s.region=? AND s.group_name=? AND substr(s.name,1,length(?))=? GROUP BY s.name,s.created ORDER BY s.name`, region, group, q.Prefix, q.Prefix)
	if err != nil {
		return nil, "", err
	}
	result := []LogsStream{}
	for rows.Next() {
		var st LogsStream
		var first, last, ingested sql.NullInt64
		if err = rows.Scan(&st.Name, &st.Created, &first, &last, &ingested); err != nil {
			rows.Close()
			return nil, "", err
		}
		if first.Valid {
			st.First = &first.Int64
			st.Last = &last.Int64
			st.Ingested = &ingested.Int64
		}
		st.ARN = logsStreamARN(region, group, st.Name)
		result = append(result, st)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	// Local last-event metadata is immediate; AWS may update it eventually.
	key := func(st LogsStream) int64 {
		if st.Last != nil {
			return *st.Last
		}
		return -1
	}
	cmp := func(a, b LogsStream) int {
		if q.OrderBy == "LastEventTime" && key(a) != key(b) {
			if key(a) < key(b) {
				return -1
			}
			return 1
		}
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	}
	slices.SortFunc(result, cmp)
	if q.Descending {
		slices.Reverse(result)
	}
	filtered := result[:0]
	for _, st := range result {
		if q.Token != "" {
			cursor := LogsStream{Name: c.Name, Last: &c.Timestamp}
			comparison := cmp(st, cursor)
			if !q.Descending && comparison <= 0 || q.Descending && comparison >= 0 {
				continue
			}
		}
		filtered = append(filtered, st)
	}
	result = filtered
	next := ""
	if len(result) > q.Limit {
		result = result[:q.Limit]
		last := result[len(result)-1]
		c.Name = last.Name
		c.Timestamp = key(last)
		c.Expires = s.now().Add(24 * time.Hour).UnixMilli()
		next = s.encodeCursor(c)
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	return result, next, nil
}

type LogsEventsQuery struct {
	Start, End    *int64
	StartFromHead bool
	Limit         int
	Token         string
}
type LogsEventsPage struct {
	Events   []LogsEvent `json:"events"`
	Forward  string      `json:"nextForwardToken"`
	Backward string      `json:"nextBackwardToken"`
}

func (s *CloudWatchLogsStore) Events(ctx context.Context, region, group, stream string, q LogsEventsQuery) (LogsEventsPage, error) {
	page := LogsEventsPage{Events: []LogsEvent{}}
	if err := validLogsRegion(region); err != nil {
		return page, err
	}
	if !logsGroupName.MatchString(group) || !validLogsStream(stream) {
		return page, logsInvalid("Invalid log group or stream name.")
	}
	if q.Limit < 1 || q.Limit > 10000 || q.Start != nil && *q.Start < 0 || q.End != nil && *q.End < 0 || q.Start != nil && q.End != nil && *q.End <= *q.Start {
		return page, logsInvalid("Use limit 1–10000 and a nonnegative startTime before exclusive endTime.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	var generation string
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?", region, group, stream).Scan(&generation); errors.Is(err, sql.ErrNoRows) {
		return page, logsMissing()
	}
	if err != nil {
		return page, err
	}
	scope := logsScope("events", region, group, stream, q.Start, q.End)
	c := logsCursor{Scope: scope, Generation: generation, Direction: "f", Timestamp: -1, Expires: s.now().Add(24 * time.Hour).UnixMilli()}
	if !q.StartFromHead {
		c.Direction = "b"
		c.Timestamp = math.MaxInt64
		c.ID = math.MaxInt64
	}
	if q.Token != "" {
		c, err = s.decodeCursor(q.Token, scope)
		if err != nil {
			return page, err
		}
		if c.Generation != generation || c.Direction != "f" && c.Direction != "b" {
			return page, logsInvalid("Pagination token belongs to another stream generation.")
		}
		if c.Direction == "f" && !q.StartFromHead {
			return page, logsInvalid("A nextForwardToken requires startFromHead=true.")
		}
	}
	order, operator := "ASC", ">"
	if c.Direction == "b" {
		order, operator = "DESC", "<"
	}
	query := `SELECT id,timestamp,ingested,message FROM aws_logs_events WHERE region=? AND group_name=? AND stream_name=? AND (? IS NULL OR timestamp>=?) AND (? IS NULL OR timestamp<?) AND (timestamp ` + operator + ` ? OR (timestamp=? AND id ` + operator + ` ?)) ORDER BY timestamp ` + order + `,id ` + order + ` LIMIT ?`
	rows, err := tx.QueryContext(ctx, query, region, group, stream, q.Start, q.Start, q.End, q.End, c.Timestamp, c.Timestamp, c.ID, q.Limit)
	if err != nil {
		return page, fmt.Errorf("read log events: %w", err)
	}
	bytes := 0
	for rows.Next() {
		var e LogsEvent
		if err = rows.Scan(&e.id, &e.Timestamp, &e.IngestionTime, &e.Message); err != nil {
			rows.Close()
			return page, err
		}
		if bytes+len(e.Message)+26 > 1048576 {
			break
		}
		bytes += len(e.Message) + 26
		page.Events = append(page.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	if c.Direction == "b" {
		slices.Reverse(page.Events)
	}
	f, b := c, c
	f.Direction = "f"
	b.Direction = "b"
	if len(page.Events) > 0 {
		first, last := page.Events[0], page.Events[len(page.Events)-1]
		b.Timestamp, b.ID = first.Timestamp, first.id
		f.Timestamp, f.ID = last.Timestamp, last.id
		f.Expires = s.now().Add(24 * time.Hour).UnixMilli()
		b.Expires = f.Expires
	}
	if len(page.Events) == 0 {
		// Preserve the exhausted direction exactly. Its opposite must include
		// the boundary event when the caller reverses, rather than skip it.
		if c.Direction == "b" {
			f.Timestamp, f.ID = -1, 0
			f.Expires = s.now().Add(24 * time.Hour).UnixMilli()
		} else {
			b.Timestamp, b.ID = math.MaxInt64, math.MaxInt64
			b.Expires = s.now().Add(24 * time.Hour).UnixMilli()
		}
	}
	page.Forward, page.Backward = s.encodeCursor(f), s.encodeCursor(b)
	if err = tx.Commit(); err != nil {
		return page, fmt.Errorf("commit log event read: %w", err)
	}
	return page, nil
}
