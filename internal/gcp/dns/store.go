package dns

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
)

func (e *Engine) initialize() error {
	tx, err := e.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema := `CREATE TABLE IF NOT EXISTS gcp_dns_authority(key BLOB NOT NULL);
 INSERT INTO gcp_dns_authority(key) SELECT randomblob(32) WHERE NOT EXISTS(SELECT 1 FROM gcp_dns_authority);
 CREATE TABLE IF NOT EXISTS gcp_dns_projects(project TEXT PRIMARY KEY,owner TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS gcp_dns_zones(
 id INTEGER PRIMARY KEY AUTOINCREMENT,project TEXT NOT NULL,name TEXT NOT NULL,dns_name TEXT NOT NULL,
 description TEXT NOT NULL,created TEXT NOT NULL,UNIQUE(project,name),
 FOREIGN KEY(project) REFERENCES gcp_dns_projects(project) ON DELETE RESTRICT);
 CREATE TABLE IF NOT EXISTS gcp_dns_rrsets(
 zone INTEGER NOT NULL,name TEXT NOT NULL,type TEXT NOT NULL,ttl INTEGER NOT NULL,data TEXT NOT NULL,
 PRIMARY KEY(zone,name,type),FOREIGN KEY(zone) REFERENCES gcp_dns_zones(id) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS gcp_dns_history(
 zone INTEGER NOT NULL,seq INTEGER NOT NULL,document TEXT NOT NULL,
 PRIMARY KEY(zone,seq),FOREIGN KEY(zone) REFERENCES gcp_dns_zones(id) ON DELETE CASCADE);`
	for _, statement := range strings.Split(schema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	var owner string
	err = tx.QueryRow("SELECT owner FROM gcp_dns_projects WHERE project=?", e.cfg.Project).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM gcp_dns_projects").Scan(&count); err != nil {
			return err
		}
		if count >= 64 {
			return budget()
		}
		result, insertErr := tx.Exec("INSERT INTO gcp_dns_projects(project,owner) VALUES(?,?)", e.cfg.Project, e.cfg.Owner)
		err = changedOne(result, insertErr)
	} else if err == nil && owner != e.cfg.Owner {
		return errors.New("Cloud DNS project owner migration is unsupported")
	}
	if err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT key FROM gcp_dns_authority").Scan(&e.tokenKey); err != nil {
		return err
	}
	if len(e.tokenKey) != 32 {
		return errors.New("invalid persisted Cloud DNS token authority")
	}
	return tx.Commit()
}

type zone struct {
	ID                                 int64
	Name, Domain, Description, Created string
}

func (z zone) resource() map[string]any {
	return map[string]any{"kind": "dns#managedZone", "id": strconv.FormatInt(z.ID, 10), "name": z.Name, "dnsName": z.Domain, "description": z.Description, "visibility": "public", "creationTime": z.Created, "nameServers": []string{"ns1.tenkacloud.invalid."}}
}
func (e *Engine) loadZone(tx *sql.Tx, name string) (zone, error) {
	var z zone
	// Names begin with a letter; a canonical decimal ID therefore cannot alias a
	// different resource's name. Project restriction is part of the same query.
	err := tx.QueryRow("SELECT id,name,dns_name,description,created FROM gcp_dns_zones WHERE project=? AND (name=? OR CAST(id AS TEXT)=?)", e.cfg.Project, name, name).Scan(&z.ID, &z.Name, &z.Domain, &z.Description, &z.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return z, absent()
	}
	return z, err
}
func loadRecords(tx *sql.Tx, id int64) (map[string]record, error) {
	rows, err := tx.Query("SELECT name,type,ttl,data FROM gcp_dns_rrsets WHERE zone=?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]record{}
	for rows.Next() {
		var r record
		var seconds int64
		var data string
		if err = rows.Scan(&r.Name, &r.Type, &seconds, &data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &r.Data); err != nil {
			return nil, err
		}
		r.TTL = &seconds
		r.Kind = "dns#resourceRecordSet"
		out[r.key()] = r
	}
	return out, rows.Err()
}
func insertRecord(tx *sql.Tx, zoneID int64, r record) error {
	data, err := encode(r.Data)
	if err != nil {
		return err
	}
	result, err := tx.Exec("INSERT INTO gcp_dns_rrsets(zone,name,type,ttl,data) VALUES(?,?,?,?,?)", zoneID, r.Name, r.Type, *r.TTL, data)
	return changedOne(result, err)
}
func readHistory(tx *sql.Tx, id int64) ([]map[string]any, error) {
	rows, err := tx.Query("SELECT document FROM gcp_dns_history WHERE zone=? ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var doc string
		if err = rows.Scan(&doc); err != nil {
			return nil, err
		}
		var entry map[string]any
		if err = json.Unmarshal([]byte(doc), &entry); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// An ignored SQL write must not produce an apparently successful API receipt.
func changedOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("Cloud DNS state write did not change exactly one row")
	}
	return nil
}

// The global byte budget includes reserved space for deletion receipts. Moving
// one RRset from live state to a cleanup receipt consumes less than its reserved
// 2*(data+name+type+256) bytes, so reaching the budget cannot block cleanup.
// Project/authority metadata is separately bounded by New's 64-project limit.
func stateBudget(tx *sql.Tx) error {
	var bytes int64
	err := tx.QueryRow(`SELECT
 (SELECT COALESCE(SUM(length(CAST(document AS BLOB))),0) FROM gcp_dns_history) +
 (SELECT COALESCE(SUM(2*(length(CAST(data AS BLOB))+length(name)+length(type)+256)),0) FROM gcp_dns_rrsets) +
 (SELECT COALESCE(SUM(length(CAST(description AS BLOB))+length(name)+length(dns_name)+128),0) FROM gcp_dns_zones)`).Scan(&bytes)
	if err != nil {
		return err
	}
	if bytes > 2<<20 {
		return budget()
	}
	return nil
}

// Preserve raw history documents together with their lookup sequences: checking
// only list JSON would miss a trigger moving a row to a different change ID.
type historyDocument struct {
	sequence int64
	document string
}

func storedHistory(tx *sql.Tx, zoneID int64) ([]historyDocument, error) {
	rows, err := tx.Query("SELECT seq,document FROM gcp_dns_history WHERE zone=? ORDER BY seq", zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []historyDocument{}
	for rows.Next() {
		var r historyDocument
		if err = rows.Scan(&r.sequence, &r.document); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Verify after every write, including receipt insertion (whose AFTER triggers
// can change records again). The same transaction protects this observation
// until COMMIT. RowsAffected alone says nothing about the final stored graph.
func verifyChangeState(tx *sql.Tx, e *Engine, z zone, wantRecords map[string]record, wantHistory []historyDocument) error {
	current, err := e.loadZone(tx, strconv.FormatInt(z.ID, 10))
	if err != nil {
		return err
	}
	if current != z {
		return errors.New("Cloud DNS zone changed during record mutation")
	}
	actual, err := loadRecords(tx, z.ID)
	if err != nil {
		return err
	}
	if len(actual) != len(wantRecords) {
		return errors.New("Cloud DNS final RRset collection differs from intended change")
	}
	for key, want := range wantRecords {
		got, ok := actual[key]
		if !ok || !sameRecord(got, want) {
			return errors.New("Cloud DNS final RRset values differ from intended change")
		}
	}
	history, err := storedHistory(tx, z.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(history, wantHistory) {
		return errors.New("Cloud DNS final history differs from intended receipt sequence")
	}
	return nil
}
