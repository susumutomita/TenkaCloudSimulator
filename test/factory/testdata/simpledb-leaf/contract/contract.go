// Package contract is independent TestWriter support, never simulator runtime.
package contract

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"path/filepath"
	"reflect"
	"sort"
	"time"
)

// Store is a data-layer contract: no server, Gateway, token, HTTP, IAM or SDK.
// Caller owns db lifetime. Every call is scoped; errors expose Code() string.
// Put validates the whole request before mutation. Get returns copies of sets.
type Store interface {
	CreateDomain(context.Context, string, string, string) error
	ListDomains(context.Context, string, string, int, string) ([]string, string, error)
	DeleteDomain(context.Context, string, string, string) error
	Put(context.Context, string, string, string, string, map[string][]string, map[string]bool) error
	Get(context.Context, string, string, string, string, []string) (map[string][]string, error)
}
type Factory func(*sql.DB) (Store, error)
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}
type Report struct {
	Surface string   `json:"surface"`
	Cases   []Result `json:"cases"`
}

//go:embed vectors.json
var vectorBytes []byte

type vectors struct {
	Version                       int
	Account, Region, Domain, Item string
	Initial, Expected             map[string][]string
	Cases                         []string
	PageCount                     int `json:"page_fixture_count"`
}

func loadVectors() vectors {
	var v vectors
	if err := json.Unmarshal(vectorBytes, &v); err != nil || v.Version != 1 || len(v.Cases) != 7 || v.PageCount != 101 {
		panic("HARNESS_ERROR immutable vectors invalid")
	}
	return v
}

var fixed = loadVectors()
var Required = fixed.Cases

type failure struct{ kind, message string }

func (e failure) Error() string { return e.message }
func check(ok bool, msg string) error {
	if !ok {
		return failure{"PRODUCT_CONTRACT_RED", msg}
	}
	return nil
}
func Code(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return ""
}
func WantCode(err error, code string) error {
	return check(err != nil && Code(err) == code, fmt.Sprintf("error code=%q want=%q", Code(err), code))
}
func same(a, b map[string][]string) bool {
	normal := func(m map[string][]string) map[string][]string {
		o := map[string][]string{}
		for k, v := range m {
			v = append([]string{}, v...)
			sort.Strings(v)
			o[k] = v
		}
		return o
	}
	return reflect.DeepEqual(normal(a), normal(b))
}

type env struct {
	path    string
	db      *sql.DB
	store   Store
	factory Factory
	ctx     context.Context
}

func (e *env) open() error {
	db, err := sql.Open("sqlite3", e.path+"?_pragma=foreign_keys(1)")
	if err != nil {
		return failure{"HARNESS_ERROR", err.Error()}
	}
	e.db = db
	if err = db.PingContext(e.ctx); err != nil {
		return failure{"HARNESS_ERROR", err.Error()}
	}
	if e.factory == nil {
		return failure{"BINDING_UNESTABLISHED", "nil factory"}
	}
	e.store, err = e.factory(db)
	if err != nil {
		return failure{"PRODUCT_CONTRACT_RED", "subject constructor: " + err.Error()}
	}
	if e.store == nil || (reflect.ValueOf(e.store).Kind() == reflect.Pointer && reflect.ValueOf(e.store).IsNil()) {
		return failure{"BINDING_UNESTABLISHED", "nil production subject"}
	}
	return nil
}
func (e *env) close() error {
	if e.db == nil {
		return nil
	}
	d := e.db
	e.db = nil
	if err := d.Close(); err != nil {
		return failure{"HARNESS_ERROR", "database close: " + err.Error()}
	}
	return nil
}

var account = fixed.Account
var region = fixed.Region
var domain = fixed.Domain
var item = fixed.Item

func (e *env) create(name string) error { return e.store.CreateDomain(e.ctx, account, region, name) }
func (e *env) get() (map[string][]string, error) {
	return e.store.Get(e.ctx, account, region, domain, item, nil)
}

var initial = fixed.Initial
var expected = fixed.Expected

func (e *env) seed() error {
	if err := e.create(domain); err != nil {
		return err
	}
	return e.store.Put(e.ctx, account, region, domain, item, initial, nil)
}
func caseRun(id string, e *env) error {
	switch id {
	case "domain-lifecycle":
		if err := e.create(domain); err != nil {
			return err
		}
		if err := e.create(domain); err != nil {
			return err
		}
		names, next, err := e.store.ListDomains(e.ctx, account, region, 100, "")
		if err != nil {
			return err
		}
		if err = check(len(names) == 1 && names[0] == domain && next == "", "create must persist, duplicate must not add a domain"); err != nil {
			return err
		}
		for i := 0; i < 2; i++ {
			if err = e.store.DeleteDomain(e.ctx, account, region, domain); err != nil {
				return err
			}
		}
		names, next, err = e.store.ListDomains(e.ctx, account, region, 100, "")
		if err != nil {
			return err
		}
		return check(len(names) == 0 && next == "", "delete must remove domain")
	case "item-workflow":
		if err := e.seed(); err != nil {
			return err
		}
		if err := e.store.Put(e.ctx, account, region, domain, item, initial, nil); err != nil {
			return err
		}
		got, err := e.get()
		if err != nil {
			return err
		}
		if err = check(same(got, expected), "Put union/dedup or escaped value changed"); err != nil {
			return err
		}
		got["colour"][0] = "caller-change"
		again, err := e.get()
		if err != nil {
			return err
		}
		if err = check(same(again, expected), "Get returned a mutable alias of saved state"); err != nil {
			return err
		}
		if err = e.store.Put(e.ctx, account, region, domain, item, map[string][]string{"colour": {"red"}}, map[string]bool{"colour": true}); err != nil {
			return err
		}
		got, err = e.get()
		if err != nil {
			return err
		}
		if err = check(same(got, map[string][]string{"colour": {"red"}, "label": {"<&日本語>\""}}), "Replace must replace only the named value set"); err != nil {
			return err
		}
		got, err = e.store.Get(e.ctx, account, region, domain, item, []string{"colour"})
		if err != nil {
			return err
		}
		return check(same(got, map[string][]string{"colour": {"red"}}), "filter must not disclose other attributes")
	case "restart-cascade":
		if err := e.seed(); err != nil {
			return err
		}
		if err := e.close(); err != nil {
			return err
		}
		if err := e.open(); err != nil {
			return err
		}
		got, err := e.get()
		if err != nil {
			return err
		}
		if err = check(same(got, expected), "acknowledged write lost on database restart"); err != nil {
			return err
		}
		if err = e.store.DeleteDomain(e.ctx, account, region, domain); err != nil {
			return err
		}
		if err = e.close(); err != nil {
			return err
		}
		if err = e.open(); err != nil {
			return err
		}
		if _, err = e.get(); WantCode(err, "NoSuchDomain") != nil {
			return WantCode(err, "NoSuchDomain")
		}
		if err = e.create(domain); err != nil {
			return err
		}
		got, err = e.get()
		if err != nil {
			return err
		}
		return check(len(got) == 0, "deleted values resurrected after restart and recreate")
	case "list-boundaries":
		want := map[string]bool{}
		for i := 0; i < 101; i++ {
			name := fmt.Sprintf("page-%03d", i)
			if err := e.create(name); err != nil {
				return err
			}
			want[name] = true
		}
		for _, limit := range []int{0, 100, 1} {
			seen := map[string]bool{}
			tokens := map[string]bool{}
			next := ""
			for page := 0; page < 102; page++ {
				names, token, err := e.store.ListDomains(e.ctx, account, region, limit, next)
				if err != nil {
					return err
				}
				max := limit
				if max == 0 {
					max = 100
				}
				if err = check(len(names) > 0 && len(names) <= max, "page size or progress invalid"); err != nil {
					return err
				}
				if page == 0 && limit != 1 && len(names) != 100 {
					return failure{"PRODUCT_CONTRACT_RED", "default/max100 must split101 as100+1"}
				}
				for _, n := range names {
					if err = check(want[n] && !seen[n], "foreign or duplicate domain"); err != nil {
						return err
					}
					seen[n] = true
				}
				next = token
				if next == "" {
					break
				}
				if tokens[next] {
					return failure{"PRODUCT_CONTRACT_RED", "nonprogressing token"}
				}
				tokens[next] = true
			}
			if err := check(next == "" && len(seen) == 101, "pagination incomplete"); err != nil {
				return err
			}
		}
		for _, limit := range []int{-1, 101} {
			_, _, err := e.store.ListDomains(e.ctx, account, region, limit, "")
			if err = WantCode(err, "InvalidParameterValue"); err != nil {
				return err
			}
		}
		_, _, err := e.store.ListDomains(e.ctx, account, region, 1, "not-issued")
		if err = WantCode(err, "InvalidNextToken"); err != nil {
			return err
		}
		names, next, err := e.store.ListDomains(e.ctx, account, region, 100, "")
		if err != nil {
			return err
		}
		if len(names) != 100 || next == "" {
			return failure{"PRODUCT_CONTRACT_RED", "rejected list request changed domain count"}
		}
		tail, token, err := e.store.ListDomains(e.ctx, account, region, 100, next)
		if err != nil {
			return err
		}
		if len(tail) != 1 || token != "" {
			return failure{"PRODUCT_CONTRACT_RED", "rejected list request changed final page"}
		}
		seen := map[string]bool{}
		for _, name := range append(names, tail...) {
			if !want[name] || seen[name] {
				return failure{"PRODUCT_CONTRACT_RED", "rejected list changed domain set"}
			}
			seen[name] = true
		}
		return nil
	case "rejected-write-atomicity":
		if err := e.seed(); err != nil {
			return err
		}
		err := e.store.Put(e.ctx, account, region, domain, item, map[string][]string{"colour": {"must-not-commit"}, "": {"invalid"}}, map[string]bool{"colour": true})
		if err = WantCode(err, "InvalidParameterValue"); err != nil {
			return err
		}
		got, err := e.get()
		if err != nil {
			return err
		}
		if err = check(same(got, expected), "rejected mixed Put committed valid prefix"); err != nil {
			return err
		}
		if err = WantCode(e.create("ab"), "InvalidParameterValue"); err != nil {
			return err
		}
		names, _, err := e.store.ListDomains(e.ctx, account, region, 100, "")
		if err != nil {
			return err
		}
		return check(len(names) == 1 && names[0] == domain, "rejected create left ghost domain")
	case "scope-isolation":
		if err := e.seed(); err != nil {
			return err
		}
		for _, scope := range [][2]string{{"111111111111", region}, {account, "us-west-2"}} {
			names, token, err := e.store.ListDomains(e.ctx, scope[0], scope[1], 100, "")
			if err != nil {
				return err
			}
			if err = check(len(names) == 0 && token == "", "List disclosed another scope"); err != nil {
				return err
			}
			got, err := e.store.Get(e.ctx, scope[0], scope[1], domain, item, nil)
			if err = WantCode(err, "NoSuchDomain"); err != nil {
				return err
			}
			if err = check(len(got) == 0, "wrong scope disclosed attributes"); err != nil {
				return err
			}
			err = e.store.Put(e.ctx, scope[0], scope[1], domain, item, map[string][]string{"colour": {"wrong-scope"}}, nil)
			if err = WantCode(err, "NoSuchDomain"); err != nil {
				return err
			}
			_, err = e.store.Get(e.ctx, scope[0], scope[1], domain, item, nil)
			if err = WantCode(err, "NoSuchDomain"); err != nil {
				return failure{"PRODUCT_CONTRACT_RED", "rejected cross-scope Put created state"}
			}
			if err = e.store.DeleteDomain(e.ctx, scope[0], scope[1], domain); err != nil {
				return err
			}
			original, err := e.get()
			if err != nil {
				return err
			}
			if err = check(same(original, expected), "wrong-scope Delete removed original state"); err != nil {
				return err
			}
			if err = e.store.CreateDomain(e.ctx, scope[0], scope[1], domain); err != nil {
				return err
			}
			isolated, err := e.store.Get(e.ctx, scope[0], scope[1], domain, item, nil)
			if err != nil {
				return err
			}
			if err = check(len(isolated) == 0, "same-name new scope adopted old values"); err != nil {
				return err
			}
		}
		got, err := e.get()
		if err != nil {
			return err
		}
		return check(same(got, expected), "wrong-scope rejection mutated original state")
	case "cancelled-write":
		if err := e.seed(); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		err := e.store.Put(ctx, account, region, domain, item, map[string][]string{"colour": {"cancelled"}}, map[string]bool{"colour": true})
		if err = WantCode(err, "Cancelled"); err != nil {
			return err
		}
		got, err := e.get()
		if err != nil {
			return err
		}
		return check(same(got, expected), "cancelled write mutated state")
	}
	return failure{"HARNESS_ERROR", "unknown immutable case"}
}
func Run(dir string, factory Factory, ids []string) Report {
	r := Report{Surface: "leaf-store-contract-only"}
	for _, id := range ids {
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			e := &env{path: filepath.Join(dir, id+".db"), factory: factory, ctx: ctx}
			err := e.open()
			if err == nil {
				err = caseRun(id, e)
			}
			closeErr := e.close()
			result := Result{ID: id, Status: "PASS"}
			if err != nil {
				result.Status = "PRODUCT_CONTRACT_RED"
				var f failure
				if errors.As(err, &f) {
					result.Status = f.kind
				}
				result.Reason = err.Error()
			}
			if closeErr != nil {
				result.Status = "HARNESS_ERROR"
				result.Reason += "; " + closeErr.Error()
			}
			r.Cases = append(r.Cases, result)
		}()
	}
	return r
}
func Encode(r Report) string { b, _ := json.Marshal(r); return string(b) }
