package dns

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) execute(tx *sql.Tx, c call, input any) (any, error) {
	switch c.id {
	case "managedZones.create":
		return e.createZone(tx, input.(zoneInput))
	case "managedZones.list":
		rows, err := tx.Query("SELECT id,name,dns_name,description,created FROM gcp_dns_zones WHERE project=? ORDER BY name", e.cfg.Project)
		if err != nil {
			return nil, err
		}
		items := []any{}
		for rows.Next() {
			var z zone
			if err = rows.Scan(&z.ID, &z.Name, &z.Domain, &z.Description, &z.Created); err != nil {
				rows.Close()
				return nil, err
			}
			if filter := c.query.Get("dnsName"); filter == "" || filter == z.Domain {
				items = append(items, z.resource())
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		return e.page(items, "managedZones", "zones", c.query)
	}
	z, err := e.loadZone(tx, c.path["zone"])
	if err != nil {
		return nil, err
	}
	switch c.id {
	case "managedZones.get":
		return z.resource(), nil
	case "managedZones.delete":
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM gcp_dns_rrsets WHERE zone=? AND NOT(name=? AND type IN ('NS','SOA'))", z.ID, z.Domain).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, &failure{400, "containerNotEmpty", "Remove all user RRsets before deleting the zone."}
		}
		result, err := tx.Exec("DELETE FROM gcp_dns_zones WHERE id=?", z.ID)
		return nil, changedOne(result, err)
	case "changes.create":
		return e.applyChange(tx, z, input.(changeInput))
	case "changes.get":
		var doc string
		err = tx.QueryRow("SELECT document FROM gcp_dns_history WHERE zone=? AND CAST(seq AS TEXT)=?", z.ID, c.path["change"]).Scan(&doc)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, absent()
		}
		if err != nil {
			return nil, err
		}
		var result map[string]any
		err = json.Unmarshal([]byte(doc), &result)
		return result, err
	case "changes.list":
		history, err := readHistory(tx, z.ID)
		if err != nil {
			return nil, err
		}
		items := []any{}
		if c.query.Get("sortOrder") == "descending" {
			for i := len(history) - 1; i >= 0; i-- {
				items = append(items, history[i])
			}
		} else {
			for _, h := range history {
				items = append(items, h)
			}
		}
		return e.page(items, "changes", "zone/"+strconv.FormatInt(z.ID, 10)+"/changes", c.query)
	case "resourceRecordSets.get", "resourceRecordSets.list":
		records, err := loadRecords(tx, z.ID)
		if err != nil {
			return nil, err
		}
		if c.id == "resourceRecordSets.get" {
			r, ok := records[c.path["name"]+"/"+c.path["type"]]
			if !ok {
				return nil, absent()
			}
			return r, nil
		}
		ordered := []record{}
		for _, r := range records {
			if c.query.Get("name") != "" && c.query.Get("name") != r.Name || c.query.Get("type") != "" && c.query.Get("type") != r.Type {
				continue
			}
			ordered = append(ordered, r)
		}
		sort.Slice(ordered, func(i, j int) bool { return dnsOrder(ordered[i]) < dnsOrder(ordered[j]) })
		items := []any{}
		for _, r := range ordered {
			items = append(items, r)
		}
		return e.page(items, "rrsets", "zone/"+strconv.FormatInt(z.ID, 10)+"/rrsets", c.query)
	}
	return nil, &failure{501, "notImplemented", "Unsupported Cloud DNS method."}
}
func (e *Engine) createZone(tx *sql.Tx, in zoneInput) (any, error) {
	var found int
	if err := tx.QueryRow("SELECT count(*) FROM gcp_dns_zones WHERE project=? AND name=?", e.cfg.Project, in.Name).Scan(&found); err != nil {
		return nil, err
	}
	if found != 0 {
		return nil, duplicate()
	}
	if err := tx.QueryRow("SELECT count(*) FROM gcp_dns_zones WHERE project=?", e.cfg.Project).Scan(&found); err != nil {
		return nil, err
	}
	if found >= maxZones {
		return nil, budget()
	}
	created := e.cfg.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.Exec("INSERT INTO gcp_dns_zones(project,name,dns_name,description,created) VALUES(?,?,?,?,?)", e.cfg.Project, in.Name, in.DNSName, *in.Description, created)
	if err != nil {
		return nil, err
	}
	if err = changedOne(result, nil); err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, r := range initialRecords(in.DNSName) {
		if err = insertRecord(tx, id, r); err != nil {
			return nil, err
		}
	}
	if err = stateBudget(tx); err != nil {
		return nil, err
	}
	return (zone{id, in.Name, in.DNSName, *in.Description, created}).resource(), nil
}
func (e *Engine) applyChange(tx *sql.Tx, z zone, in changeInput) (any, error) {
	records, err := loadRecords(tx, z.ID)
	if err != nil {
		return nil, err
	}
	for _, set := range [][]record{in.Add, in.Delete} {
		for _, r := range set {
			if r.Name != z.Domain && !strings.HasSuffix(r.Name, "."+z.Domain) {
				return nil, bad("RRset name must belong to the managed zone.")
			}
			if r.Name == z.Domain && (r.Type == "NS" || r.Type == "SOA" || r.Type == "CNAME") {
				return nil, bad("Synthetic apex NS/SOA are protected and apex CNAME is invalid.")
			}
		}
	}
	// Validate the complete final graph in memory before the first SQL mutation.
	// A deletion is an exact compare-and-delete, including TTL and the rdata set.
	for _, r := range in.Delete {
		old, exists := records[r.key()]
		if !exists || !sameRecord(old, r) {
			return nil, precondition()
		}
		delete(records, r.key())
	}
	for _, r := range in.Add {
		if _, exists := records[r.key()]; exists {
			return nil, duplicate()
		}
		records[r.key()] = r
	}
	if len(records) > maxRecords {
		return nil, budget()
	}
	names := map[string]int{}
	cname := map[string]bool{}
	for _, r := range records {
		names[r.Name]++
		if r.Type == "CNAME" {
			cname[r.Name] = true
		}
	}
	for name := range cname {
		if names[name] != 1 {
			return nil, bad("A CNAME cannot coexist with another RRset at the same name.")
		}
	}
	beforeHistory, err := storedHistory(tx, z.ID)
	if err != nil {
		return nil, err
	}
	var changes, sequence int64
	if err = tx.QueryRow("SELECT count(*),COALESCE(MAX(seq),0)+1 FROM gcp_dns_history WHERE zone=?", z.ID).Scan(&changes, &sequence); err != nil {
		return nil, err
	}
	// Reserve a finite cleanup path after the normal history budget: deletion-only
	// changes can remove at most maxRecords-2 user RRsets, then the zone is deletable.
	if changes >= maxChanges && len(in.Add) != 0 {
		return nil, budget()
	}
	for _, r := range in.Delete {
		result, err := tx.Exec("DELETE FROM gcp_dns_rrsets WHERE zone=? AND name=? AND type=?", z.ID, r.Name, r.Type)
		if err = changedOne(result, err); err != nil {
			return nil, err
		}
	}
	for _, r := range in.Add {
		if err = insertRecord(tx, z.ID, r); err != nil {
			return nil, err
		}
	}
	if in.Add == nil {
		in.Add = []record{}
	}
	if in.Delete == nil {
		in.Delete = []record{}
	}
	// 'done' records a synchronous local commit, never DNS serving/propagation.
	receipt := map[string]any{"kind": "dns#change", "id": strconv.FormatInt(sequence, 10), "startTime": e.cfg.Now().UTC().Format(time.RFC3339Nano), "status": "done", "isServing": false, "additions": in.Add, "deletions": in.Delete}
	document, err := encode(receipt)
	if err != nil {
		return nil, err
	}
	result, err := tx.Exec("INSERT INTO gcp_dns_history(zone,seq,document) VALUES(?,?,?)", z.ID, sequence, document)
	if err = changedOne(result, err); err != nil {
		return nil, err
	}
	expectedHistory := append(beforeHistory, historyDocument{sequence, document})
	if err = verifyChangeState(tx, e, z, records, expectedHistory); err != nil {
		return nil, err
	}
	return receipt, stateBudget(tx)
}
