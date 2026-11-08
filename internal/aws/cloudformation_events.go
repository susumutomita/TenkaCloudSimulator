package aws

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"strings"
	"time"
)

// StackEvent is deliberately an allowlist: no template, properties, parameter
// values, outputs, request tokens or arbitrary exception strings are retained.
// Events describe committed synchronous local operations, not AWS async work.
type StackEvent struct {
	StackID            string `xml:"StackId"`
	EventID            string `xml:"EventId"`
	StackName          string `xml:"StackName"`
	LogicalResourceID  string `xml:"LogicalResourceId"`
	PhysicalResourceID string `xml:"PhysicalResourceId"`
	ResourceType       string `xml:"ResourceType"`
	Timestamp          string `xml:"Timestamp"`
	ResourceStatus     string `xml:"ResourceStatus"`
}

const stackEventPageSize = 100
const stackDeletedHistoryLimit = 100

func initStackEvents(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_cfn_event_history(
 stack_id TEXT PRIMARY KEY, region TEXT NOT NULL, stack_name TEXT NOT NULL,
 deleted INTEGER NOT NULL DEFAULT 0, last_sequence INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS aws_cfn_events(
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 event_id TEXT NOT NULL UNIQUE, stack_id TEXT NOT NULL REFERENCES aws_cfn_event_history(stack_id) ON DELETE CASCADE,
 logical_id TEXT NOT NULL, physical_id TEXT NOT NULL, resource_type TEXT NOT NULL,
 timestamp TEXT NOT NULL, status TEXT NOT NULL);
 CREATE INDEX IF NOT EXISTS aws_cfn_events_stack ON aws_cfn_events(stack_id,sequence);`)
	return err
}

func beginStackEvents(tx *sql.Tx, record StackRecord) error {
	_, err := tx.Exec(`INSERT INTO aws_cfn_event_history(stack_id,region,stack_name) VALUES(?,?,?) ON CONFLICT(stack_id) DO NOTHING`, record.ID, record.Region, record.Name)
	return err
}

func appendStackEvent(tx *sql.Tx, record StackRecord, resource *StackResource, status string) error {
	logical, physical, kind := record.Name, record.ID, "AWS::CloudFormation::Stack"
	if resource != nil {
		logical, physical, kind = resource.LogicalID, resource.PhysicalID, resource.Type
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO aws_cfn_events(event_id,stack_id,logical_id,physical_id,resource_type,timestamp,status) VALUES(?,?,?,?,?,?,?)`, hex.EncodeToString(id[:]), record.ID, logical, physical, kind, time.Now().UTC().Format(time.RFC3339Nano), status)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("stack event was not recorded")
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return err
	}
	result, err = tx.Exec(`UPDATE aws_cfn_event_history SET last_sequence=? WHERE stack_id=?`, sequence, record.ID)
	if err != nil {
		return err
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("stack event history was not updated")
	}
	return nil
}

func finishStackEvents(tx *sql.Tx, record StackRecord, deleted bool) error {
	status := "CREATE_COMPLETE"
	if deleted {
		status = "DELETE_COMPLETE"
	}
	if err := appendStackEvent(tx, record, nil, status); err != nil {
		return err
	}
	if !deleted {
		return nil
	}
	if _, err := tx.Exec(`UPDATE aws_cfn_event_history SET deleted=1 WHERE stack_id=?`, record.ID); err != nil {
		return err
	}
	// Keep all live generations and the latest 100 deleted generations globally.
	// The max 100 resources per stack bounds each new lifecycle to 404 events.
	_, err := tx.Exec(`DELETE FROM aws_cfn_event_history WHERE deleted=1 AND stack_id NOT IN (SELECT stack_id FROM aws_cfn_event_history WHERE deleted=1 ORDER BY last_sequence DESC LIMIT ?)`, stackDeletedHistoryLimit)
	if err != nil {
		return err
	}
	var retained, total int
	if err = tx.QueryRow(`SELECT (SELECT count(*) FROM aws_cfn_event_history WHERE stack_id=? AND deleted=1),(SELECT count(*) FROM aws_cfn_event_history WHERE deleted=1)`, record.ID).Scan(&retained, &total); err != nil {
		return err
	}
	if retained != 1 || total > stackDeletedHistoryLimit {
		return errors.New("stack event retention postcondition failed")
	}
	return nil
}

// Events resolves and authorizes a generation in one SQLite snapshot. A deleted
// stack is addressable only by its exact ID. Cursors are random stored event IDs,
// scoped to that generation and region, and never grant permission themselves.
// Newly committed events do not shift an existing newest-to-oldest cursor.
func (s *StackStore) Events(region, name, nextToken string, authorize func(string) error) ([]StackEvent, string, error) {
	if name == "" {
		return nil, "", ssmFailure{"ValidationError", "Specify a stack name or ID."}
	}
	if len(nextToken) > 32 {
		return nil, "", ssmFailure{"ValidationError", "Invalid NextToken."}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var id, stackName string
	err = tx.QueryRow(`SELECT h.stack_id,h.stack_name FROM aws_cfn_event_history h WHERE h.region=? AND (h.stack_id=? OR (h.stack_id=(SELECT id FROM aws_cfn_stacks WHERE region=? AND name=?)))`, region, name, region, name).Scan(&id, &stackName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ssmFailure{"ValidationError", "Stack event history does not exist."}
	}
	if err != nil {
		return nil, "", err
	}
	if authorize != nil {
		if err = authorize(id); err != nil {
			return nil, "", err
		}
	}
	before := int64(1<<63 - 1)
	if nextToken != "" {
		err = tx.QueryRow(`SELECT sequence FROM aws_cfn_events WHERE event_id=? AND stack_id=?`, nextToken, id).Scan(&before)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ssmFailure{"ValidationError", "Invalid NextToken for this stack generation."}
		}
		if err != nil {
			return nil, "", err
		}
	}
	rows, err := tx.Query(`SELECT event_id,logical_id,physical_id,resource_type,timestamp,status FROM aws_cfn_events WHERE stack_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?`, id, before, stackEventPageSize+1)
	if err != nil {
		return nil, "", err
	}
	events := []StackEvent{}
	for rows.Next() {
		event := StackEvent{StackID: id, StackName: stackName}
		if err = rows.Scan(&event.EventID, &event.LogicalResourceID, &event.PhysicalResourceID, &event.ResourceType, &event.Timestamp, &event.ResourceStatus); err != nil {
			rows.Close()
			return nil, "", err
		}
		events = append(events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	token := ""
	if len(events) > stackEventPageSize {
		events = events[:stackEventPageSize]
		token = events[len(events)-1].EventID
	}
	return events, token, nil
}

func stackEventsXML(events []StackEvent, token string) (string, error) {
	var out strings.Builder
	out.WriteString("<StackEvents>")
	for _, event := range events {
		data, err := xml.Marshal(struct {
			XMLName xml.Name `xml:"member"`
			StackEvent
		}{StackEvent: event})
		if err != nil {
			return "", err
		}
		out.Write(data)
	}
	out.WriteString("</StackEvents>")
	if token != "" {
		out.WriteString("<NextToken>" + xmlText(token) + "</NextToken>")
	}
	return out.String(), nil
}
