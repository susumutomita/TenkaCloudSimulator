package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Route53Store owns global, public-only local DNS exercise state. It never
// contacts DNS servers. Hosts must allocate a separate database per team.
type Route53Store struct{ db *sql.DB }
type Route53Zone struct {
	ID              string `xml:"Id"`
	Name            string
	CallerReference string
	Config          route53Config
	Count           int `xml:"ResourceRecordSetCount"`
}
type route53Config struct {
	Comment     string `xml:"Comment,omitempty"`
	PrivateZone bool
}
type Route53Record struct {
	Name   string
	Type   string
	TTL    *int64
	Values []string `xml:"ResourceRecords>ResourceRecord>Value"`
}
type Route53Change struct {
	Action string
	Record Route53Record `xml:"ResourceRecordSet"`
}
type Route53Receipt struct {
	ID          string `xml:"Id"`
	Status      string
	SubmittedAt string
	Comment     string `xml:"Comment,omitempty"`
}
type route53Error struct{ code, message string }

func (e route53Error) Error() string    { return e.message }
func r53err(code, message string) error { return route53Error{code, message} }
func r53invalid(message string) error   { return r53err("InvalidInput", message) }

const route53NS = "ns.simulator.invalid."

func NewRoute53Store(db *sql.DB) (*Route53Store, error) {
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS aws_route53_zones(id TEXT PRIMARY KEY,name TEXT NOT NULL,reference TEXT NOT NULL UNIQUE,comment TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS aws_route53_records(zone TEXT NOT NULL REFERENCES aws_route53_zones(id) ON DELETE CASCADE,name TEXT NOT NULL,type TEXT NOT NULL,document TEXT NOT NULL,PRIMARY KEY(zone,name,type));
 CREATE TABLE IF NOT EXISTS aws_route53_changes(id TEXT PRIMARY KEY,document TEXT NOT NULL);`)
	if e != nil {
		return nil, e
	}
	return &Route53Store{db}, nil
}
func r53id(prefix string) (string, error) {
	b := make([]byte, 15)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return prefix + strings.ToUpper(hex.EncodeToString(b)), nil
}
func r53name(s string) (string, error) {
	// Validate the original bytes before Unicode case folding can turn an
	// unsupported spelling (for example Kelvin sign) into an ASCII DNS name.
	for i := 0; i < len(s); i++ {
		if s[i] >= 128 {
			return "", r53invalid("Only ASCII DNS names are supported.")
		}
	}
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".")
	if len(s) < 1 || len(s) > 253 {
		return "", r53invalid("Name must be a bounded ASCII DNS name.")
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", r53invalid("Invalid DNS label.")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", r53invalid("Only ASCII DNS labels are supported; wildcards and escaped names are unsupported.")
			}
		}
	}
	return s + ".", nil
}
func r53ttl(n int64) *int64 { return &n }
func r53defaults(name string) []Route53Record {
	return []Route53Record{{name, "NS", r53ttl(172800), []string{route53NS}}, {name, "SOA", r53ttl(900), []string{route53NS + " hostmaster.simulator.invalid. 1 7200 900 1209600 86400"}}}
}
func r53validateRecord(r Route53Record, zone string) (Route53Record, error) {
	var e error
	r.Name, e = r53name(r.Name)
	if e != nil {
		return r, e
	}
	if r.Name != zone && !strings.HasSuffix(r.Name, "."+zone) {
		return r, r53invalid("Record name must be inside the selected zone.")
	}
	if !slices.Contains([]string{"A", "AAAA", "CNAME", "TXT", "MX"}, r.Type) {
		return r, r53invalid("Only simple A, AAAA, CNAME, TXT and MX changes are supported.")
	}
	if r.TTL == nil || *r.TTL < 0 || *r.TTL > 2147483647 || len(r.Values) < 1 || len(r.Values) > 100 {
		return r, r53invalid("TTL and 1 to 100 resource records are required.")
	}
	if r.Type == "CNAME" && (r.Name == zone || len(r.Values) != 1) {
		return r, r53invalid("CNAME requires one target and cannot use the zone apex.")
	}
	r.Values = slices.Clone(r.Values)
	for i, v := range r.Values {
		if len(v) == 0 || len(v) > 1024 || !validText(v) {
			return r, r53invalid("Invalid resource record value.")
		}
		switch r.Type {
		case "A", "AAAA":
			ip, err := netip.ParseAddr(v)
			if err != nil || ip.Zone() != "" || r.Type == "A" && !ip.Is4() || r.Type == "AAAA" && (!ip.Is6() || ip.Is4In6()) {
				return r, r53invalid("Address does not match record type.")
			}
			v = ip.String()
		case "CNAME":
			v, e = r53name(v)
			if e != nil {
				return r, e
			}
		case "MX":
			parts := strings.Fields(v)
			if len(parts) != 2 {
				return r, r53invalid("MX requires preference and a domain.")
			}
			n, err := strconv.Atoi(parts[0])
			if err != nil || n < 0 || n > 65535 {
				return r, r53invalid("Invalid MX preference.")
			}
			target, err := r53name(parts[1])
			if err != nil {
				return r, err
			}
			v = strconv.Itoa(n) + " " + target
		case "TXT":
			if len(v) > 257 || len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' || strings.ContainsAny(v[1:len(v)-1], "\"\\") {
				return r, r53invalid("TXT supports one quoted ASCII string up to 255 bytes without escapes.")
			}
			for _, c := range v {
				if c < 32 || c > 126 {
					return r, r53invalid("TXT requires printable ASCII.")
				}
			}
		}
		r.Values[i] = v
	}
	sort.Strings(r.Values)
	for i := 1; i < len(r.Values); i++ {
		if r.Values[i] == r.Values[i-1] {
			return r, r53invalid("Duplicate resource records are unsupported.")
		}
	}
	return r, nil
}
func r53zone(ctx context.Context, tx *sql.Tx, id string) (Route53Zone, error) {
	var z Route53Zone
	e := tx.QueryRowContext(ctx, "SELECT id,name,reference,comment FROM aws_route53_zones WHERE id=?", id).Scan(&z.ID, &z.Name, &z.CallerReference, &z.Config.Comment)
	if errors.Is(e, sql.ErrNoRows) {
		return z, r53err("NoSuchHostedZone", "Hosted zone does not exist.")
	}
	if e != nil {
		return z, e
	}
	e = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_route53_records WHERE zone=?", id).Scan(&z.Count)
	z.ID = "/hostedzone/" + z.ID
	return z, e
}
func r53records(ctx context.Context, tx *sql.Tx, id string) ([]Route53Record, error) {
	rows, e := tx.QueryContext(ctx, "SELECT r.name,r.type,r.document,z.name FROM aws_route53_records r JOIN aws_route53_zones z ON z.id=r.zone WHERE r.zone=?", id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Route53Record{}
	for rows.Next() {
		var raw, keyName, keyType, zoneName string
		var r Route53Record
		if e = rows.Scan(&keyName, &keyType, &raw, &zoneName); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &r); e != nil {
			return nil, e
		}
		if r.Name != keyName || r.Type != keyType {
			return nil, fmt.Errorf("Route53 persisted record key mismatch")
		}
		var canonical Route53Record
		if r.Type == "NS" || r.Type == "SOA" {
			for _, def := range r53defaults(zoneName) {
				if def.Type == r.Type {
					canonical = def
				}
			}
		} else {
			canonical, e = r53validateRecord(r, zoneName)
			if e != nil {
				return nil, fmt.Errorf("Route53 persisted record is invalid")
			}
		}
		if !r53same(r, canonical) {
			return nil, fmt.Errorf("Route53 persisted record is not canonical")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func r53same(a, b Route53Record) bool {
	return a.Name == b.Name && a.Type == b.Type && a.TTL != nil && b.TTL != nil && *a.TTL == *b.TTL && slices.Equal(a.Values, b.Values)
}
func r53saveRecord(ctx context.Context, tx *sql.Tx, id string, r Route53Record) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	res, e := tx.ExecContext(ctx, "INSERT INTO aws_route53_records(zone,name,type,document) VALUES(?,?,?,?)", id, r.Name, r.Type, string(b))
	return r53ack(res, e)
}
func r53ack(res sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("Route53 expected one affected row, got %d", n)
	}
	return nil
}
func r53receipt(ctx context.Context, tx *sql.Tx, comment string) (Route53Receipt, error) {
	var count int
	if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_route53_changes").Scan(&count); e != nil {
		return Route53Receipt{}, e
	}
	// Retain the newest 10,000 receipts. Eviction is inside the mutation
	// transaction: cleanup remains possible and a later failure restores the
	// evicted receipt along with every resource write.
	if count > 10000 {
		return Route53Receipt{}, fmt.Errorf("Route53 receipt bound is damaged")
	}
	if count == 10000 {
		res, e := tx.ExecContext(ctx, "DELETE FROM aws_route53_changes WHERE rowid=(SELECT min(rowid) FROM aws_route53_changes)")
		if e = r53ack(res, e); e != nil {
			return Route53Receipt{}, e
		}
	}
	id, e := r53id("C")
	if e != nil {
		return Route53Receipt{}, e
	}
	r := Route53Receipt{"/change/" + id, "INSYNC", time.Now().UTC().Format(time.RFC3339Nano), comment}
	b, e := json.Marshal(r)
	if e != nil {
		return r, e
	}
	res, e := tx.ExecContext(ctx, "INSERT INTO aws_route53_changes VALUES(?,?)", id, string(b))
	if e = r53ack(res, e); e != nil {
		return r, e
	}
	// AFTER triggers may rewrite/delete a row despite a successful row count.
	var actual string
	if e = tx.QueryRowContext(ctx, "SELECT document FROM aws_route53_changes WHERE id=?", id).Scan(&actual); e != nil {
		return r, e
	}
	if actual != string(b) {
		return r, fmt.Errorf("Route53 receipt acknowledgment failed")
	}
	var retained int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_route53_changes").Scan(&retained); e != nil {
		return r, e
	}
	expected := count + 1
	if count == 10000 {
		expected = 10000
	}
	if retained != expected {
		return r, fmt.Errorf("Route53 receipt retention acknowledgment failed")
	}
	return r, nil
}
func (s *Route53Store) create(ctx context.Context, name, reference, comment string) (Route53Zone, Route53Receipt, error) {
	var z Route53Zone
	var receipt Route53Receipt
	name, e := r53name(name)
	if e == nil && strings.Count(name, ".") < 2 {
		e = r53invalid("Public zones require at least two DNS labels.")
	}
	if e != nil {
		return z, receipt, e
	}
	if len(reference) < 1 || len(reference) > 128 || !validText(reference) || len(comment) > 256 || !validText(comment) {
		return z, receipt, r53invalid("CallerReference (1..128) and Comment (0..256) must be valid text.")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return z, receipt, e
	}
	defer tx.Rollback()
	var old string
	e = tx.QueryRowContext(ctx, "SELECT id FROM aws_route53_zones WHERE reference=?", reference).Scan(&old)
	if e == nil {
		return z, receipt, r53err("HostedZoneAlreadyExists", "CallerReference has already been used; use GetHostedZone for the existing zone.")
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return z, receipt, e
	}
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_route53_zones").Scan(&count); e != nil {
		return z, receipt, e
	}
	if count >= 100 {
		return z, receipt, r53err("TooManyHostedZones", "Simulator permits 100 public zones.")
	}
	id, e := r53id("Z")
	if e != nil {
		return z, receipt, e
	}
	res, e := tx.ExecContext(ctx, "INSERT INTO aws_route53_zones VALUES(?,?,?,?)", id, name, reference, comment)
	if e = r53ack(res, e); e != nil {
		return z, receipt, e
	}
	for _, r := range r53defaults(name) {
		if e = r53saveRecord(ctx, tx, id, r); e != nil {
			return z, receipt, e
		}
	}
	receipt, e = r53receipt(ctx, tx, "")
	if e != nil {
		return z, receipt, e
	}
	z, e = r53zone(ctx, tx, id)
	if e != nil {
		return z, receipt, e
	}
	if z.ID != "/hostedzone/"+id || z.Name != name || z.CallerReference != reference || z.Config.Comment != comment || z.Config.PrivateZone || z.Count != 2 {
		return z, receipt, fmt.Errorf("Route53 zone creation acknowledgment failed")
	}
	saved, e := r53records(ctx, tx, id)
	if e != nil {
		return z, receipt, e
	}
	if len(saved) != 2 {
		return z, receipt, fmt.Errorf("Route53 default record count mismatch")
	}
	expected := r53defaults(name)
	for _, actual := range saved {
		matched := false
		for _, want := range expected {
			if r53same(actual, want) {
				matched = true
			}
		}
		if !matched {
			return z, receipt, fmt.Errorf("Route53 default record acknowledgment failed")
		}
	}
	e = tx.Commit()
	return z, receipt, e
}
func (s *Route53Store) snapshot(ctx context.Context, id string) (Route53Zone, []Route53Record, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return Route53Zone{}, nil, e
	}
	defer tx.Rollback()
	z, e := r53zone(ctx, tx, id)
	if e != nil {
		return z, nil, e
	}
	rr, e := r53records(ctx, tx, id)
	if e != nil {
		return z, nil, e
	}
	return z, rr, tx.Commit()
}
func (s *Route53Store) change(ctx context.Context, id, comment string, changes []Route53Change) (Route53Receipt, error) {
	var receipt Route53Receipt
	if len(changes) < 1 || len(changes) > 100 || len(comment) > 256 || !validText(comment) {
		return receipt, r53invalid("Batch requires 1..100 changes and a bounded comment.")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return receipt, e
	}
	defer tx.Rollback()
	z, e := r53zone(ctx, tx, id)
	if e != nil {
		return receipt, e
	}
	records, e := r53records(ctx, tx, id)
	if e != nil {
		return receipt, e
	}
	state := map[string]Route53Record{}
	for _, r := range records {
		state[r.Name+"|"+r.Type] = r
	}
	for _, c := range changes {
		r, err := r53validateRecord(c.Record, z.Name)
		if err != nil {
			return receipt, err
		}
		key := r.Name + "|" + r.Type
		old, exists := state[key]
		switch c.Action {
		case "CREATE":
			if exists {
				return receipt, r53err("InvalidChangeBatch", "Record already exists.")
			}
			state[key] = r
		case "UPSERT":
			state[key] = r
		case "DELETE":
			if !exists || old.TTL == nil || *old.TTL != *r.TTL || !slices.Equal(old.Values, r.Values) {
				return receipt, r53err("InvalidChangeBatch", "DELETE must exactly match an existing record.")
			}
			delete(state, key)
		default:
			return receipt, r53invalid("Action must be CREATE, UPSERT or DELETE.")
		}
	}
	if len(state) > 1000 {
		return receipt, r53err("InvalidChangeBatch", "Simulator permits 1000 RRsets per zone.")
	}
	for _, r := range state {
		if r.Type != "CNAME" {
			if _, ok := state[r.Name+"|CNAME"]; ok {
				return receipt, r53err("InvalidChangeBatch", "CNAME cannot coexist with other record types.")
			}
		}
	}
	// Publish only after the entire batch passes. Same-transaction receipt failure
	// rolls back all records as well. Intermediate batch states are never visible.
	res, e := tx.ExecContext(ctx, "DELETE FROM aws_route53_records WHERE zone=?", id)
	if e != nil {
		return receipt, e
	}
	n, e := res.RowsAffected()
	if e != nil || n != int64(len(records)) {
		return receipt, fmt.Errorf("Route53 record deletion acknowledgment failed")
	}
	for _, r := range state {
		if e = r53saveRecord(ctx, tx, id, r); e != nil {
			return receipt, e
		}
	}
	receipt, e = r53receipt(ctx, tx, comment)
	if e != nil {
		return receipt, e
	}
	saved, e := r53records(ctx, tx, id)
	if e != nil {
		return receipt, e
	}
	if len(saved) != len(state) {
		return receipt, fmt.Errorf("Route53 final batch count mismatch")
	}
	for _, actual := range saved {
		if !r53same(actual, state[actual.Name+"|"+actual.Type]) {
			return receipt, fmt.Errorf("Route53 final batch acknowledgment failed")
		}
	}
	return receipt, tx.Commit()
}
func (s *Route53Store) remove(ctx context.Context, id string) (Route53Receipt, error) {
	var receipt Route53Receipt
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return receipt, e
	}
	defer tx.Rollback()
	z, e := r53zone(ctx, tx, id)
	if e != nil {
		return receipt, e
	}
	if z.Count != 2 {
		return receipt, r53err("HostedZoneNotEmpty", "Delete non-default records before deleting the zone.")
	}
	res, e := tx.ExecContext(ctx, "DELETE FROM aws_route53_zones WHERE id=?", id)
	if e = r53ack(res, e); e != nil {
		return receipt, e
	}
	receipt, e = r53receipt(ctx, tx, "")
	if e != nil {
		return receipt, e
	}
	// Verify final state after receipt triggers too; affected-row counts alone
	// cannot prove foreign-key cascades removed children or prevent resurrection.
	var remaining int
	if e = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM aws_route53_zones WHERE id=?)+(SELECT count(*) FROM aws_route53_records WHERE zone=?)", id, id).Scan(&remaining); e != nil {
		return receipt, e
	}
	if remaining != 0 {
		return receipt, fmt.Errorf("Route53 zone cleanup acknowledgment failed")
	}
	return receipt, tx.Commit()
}
