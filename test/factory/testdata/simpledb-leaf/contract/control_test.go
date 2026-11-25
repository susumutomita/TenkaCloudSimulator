package contract

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This model exists only in _test.go. It is a detector control, not product state.
type model struct {
	db     *sql.DB
	mutant string
}
type codeError string

func (e codeError) Error() string { return string(e) }
func (e codeError) Code() string  { return string(e) }

type document map[string]map[string]map[string][]string

func modelFactory(mutant string) Factory {
	return func(db *sql.DB) (Store, error) {
		_, err := db.Exec("CREATE TABLE IF NOT EXISTS oracle_control_state(id INTEGER PRIMARY KEY, doc TEXT NOT NULL)")
		return &model{db, mutant}, err
	}
}
func (m *model) key(a, r, d string) string {
	if m.mutant == "ignore-scope" {
		a = ""
		r = ""
	}
	return a + "\x00" + r + "\x00" + d
}
func (m *model) read(ctx context.Context) (document, error) {
	var raw string
	err := m.db.QueryRowContext(ctx, "SELECT doc FROM oracle_control_state WHERE id=1").Scan(&raw)
	if err == sql.ErrNoRows {
		return document{}, nil
	}
	if err != nil {
		return nil, err
	}
	d := document{}
	err = json.Unmarshal([]byte(raw), &d)
	return d, err
}
func (m *model) save(ctx context.Context, d document) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, "INSERT INTO oracle_control_state(id,doc) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET doc=excluded.doc", string(raw))
	return err
}
func validDomain(d string) bool { return len(d) >= 3 && len(d) <= 255 && !strings.Contains(d, " ") }
func (m *model) CreateDomain(ctx context.Context, a, r, d string) error {
	if !validDomain(d) {
		return codeError("InvalidParameterValue")
	}
	if m.mutant == "noop-create" {
		return nil
	}
	doc, err := m.read(ctx)
	if err != nil {
		return err
	}
	key := m.key(a, r, d)
	if doc[key] == nil {
		doc[key] = map[string]map[string][]string{}
	}
	return m.save(ctx, doc)
}
func (m *model) DeleteDomain(ctx context.Context, a, r, d string) error {
	if m.mutant == "noop-delete" {
		return nil
	}
	doc, err := m.read(ctx)
	if err != nil {
		return err
	}
	if m.mutant == "global-delete" {
		for key := range doc {
			if strings.HasSuffix(key, "\x00"+d) {
				delete(doc, key)
			}
		}
	} else {
		delete(doc, m.key(a, r, d))
	}
	return m.save(ctx, doc)
}
func (m *model) ListDomains(ctx context.Context, a, r string, limit int, token string) ([]string, string, error) {
	if limit < 0 || limit > 100 {
		return nil, "", codeError("InvalidParameterValue")
	}
	if limit == 0 {
		limit = 100
	}
	offset := 0
	if token != "" {
		parts := strings.Split(token, "|")
		if len(parts) != 4 || parts[0] != a || parts[1] != r || parts[2] != strconv.Itoa(limit) {
			return nil, "", codeError("InvalidNextToken")
		}
		var err error
		offset, err = strconv.Atoi(parts[3])
		if err != nil || offset < 0 {
			return nil, "", codeError("InvalidNextToken")
		}
	}
	doc, err := m.read(ctx)
	if err != nil {
		return nil, "", err
	}
	prefix := a + "\x00" + r + "\x00"
	names := []string{}
	for key := range doc {
		if m.mutant == "global-list" {
			parts := strings.Split(key, "\x00")
			names = append(names, parts[len(parts)-1])
			continue
		}
		if strings.HasPrefix(key, prefix) {
			names = append(names, strings.TrimPrefix(key, prefix))
		}
	}
	sort.Strings(names)
	if offset > len(names) {
		return nil, "", codeError("InvalidNextToken")
	}
	end := min(offset+limit, len(names))
	next := ""
	if end < len(names) {
		next = fmt.Sprintf("%s|%s|%d|%d", a, r, limit, end)
	}
	return names[offset:end], next, nil
}
func (m *model) Put(ctx context.Context, a, r, d, item string, attrs map[string][]string, replace map[string]bool) error {
	if ctx.Err() != nil {
		return codeError("Cancelled")
	}
	doc, err := m.read(ctx)
	if err != nil {
		return err
	}
	key := m.key(a, r, d)
	if doc[key] == nil {
		return codeError("NoSuchDomain")
	}
	if m.mutant == "noop-put" {
		return nil
	}
	if m.mutant == "partial-put" && attrs[""] != nil {
		if doc[key][item] == nil {
			doc[key][item] = map[string][]string{}
		}
		doc[key][item]["colour"] = attrs["colour"]
		if err = m.save(ctx, doc); err != nil {
			return err
		}
		return codeError("InvalidParameterValue")
	}
	for name := range attrs {
		if name == "" {
			return codeError("InvalidParameterValue")
		}
	}
	if doc[key][item] == nil {
		doc[key][item] = map[string][]string{}
	}
	for name, values := range attrs {
		seen := map[string]bool{}
		if !replace[name] {
			for _, v := range doc[key][item][name] {
				seen[v] = true
			}
		}
		for _, v := range values {
			seen[v] = true
		}
		out := []string{}
		for v := range seen {
			out = append(out, v)
		}
		doc[key][item][name] = out
	}
	return m.save(ctx, doc)
}
func (m *model) Get(ctx context.Context, a, r, d, item string, names []string) (map[string][]string, error) {
	doc, err := m.read(ctx)
	if err != nil {
		return nil, err
	}
	key := m.key(a, r, d)
	if doc[key] == nil {
		return nil, codeError("NoSuchDomain")
	}
	out := map[string][]string{}
	for name, v := range doc[key][item] {
		include := len(names) == 0
		for _, n := range names {
			include = include || n == name
		}
		if include {
			out[name] = append([]string{}, v...)
		}
	}
	return out, nil
}
func TestLeafOracleModelSQLitePersistence(t *testing.T) {
	report := Run(t.TempDir(), modelFactory(""), Required)
	for _, c := range report.Cases {
		if c.Status != "PASS" {
			t.Errorf("oracle/control failure %+v", c)
		}
	}
}
func TestLeafOracleDetectsMutants(t *testing.T) {
	for mutant, id := range map[string]string{"noop-create": "domain-lifecycle", "noop-put": "item-workflow", "noop-delete": "restart-cascade", "partial-put": "rejected-write-atomicity", "ignore-scope": "scope-isolation", "global-list": "scope-isolation", "global-delete": "scope-isolation"} {
		t.Run(mutant, func(t *testing.T) {
			r := Run(t.TempDir(), modelFactory(mutant), []string{id})
			if len(r.Cases) != 1 || r.Cases[0].Status != "PRODUCT_CONTRACT_RED" {
				t.Fatalf("mutant escaped or harness failed: %+v", r)
			}
		})
	}
}

func TestTypedNilBindingIsNotProductRED(t *testing.T) {
	var nilModel *model
	for name, factory := range map[string]Factory{"nil-factory": nil, "typed-nil": func(*sql.DB) (Store, error) { return nilModel, nil }} {
		t.Run(name, func(t *testing.T) {
			r := Run(t.TempDir(), factory, []string{"domain-lifecycle"})
			if len(r.Cases) != 1 || r.Cases[0].Status != "BINDING_UNESTABLISHED" {
				t.Fatalf("wrong binding classification: %+v", r)
			}
		})
	}
}
