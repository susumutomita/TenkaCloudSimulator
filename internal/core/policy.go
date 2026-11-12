package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Identity policies are simulator state, not connection authentication.
// The caller chooses a principal explicitly; this is not a multi-tenant security boundary.
const Administrator = "simulator-admin"

type StringList []string

func (s *StringList) UnmarshalJSON(data []byte) error {
	var one string
	if json.Unmarshal(data, &one) == nil {
		*s = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type Statement struct {
	Sid       string           `json:"Sid,omitempty"`
	Effect    string           `json:"Effect"`
	Action    StringList       `json:"Action"`
	Resource  StringList       `json:"Resource"`
	Condition PolicyConditions `json:"Condition,omitempty"`
}
type Statements []Statement

func (s *Statements) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '{' {
		var one Statement
		if err := strictJSON(data, &one); err != nil {
			return err
		}
		*s = []Statement{one}
		return nil
	}
	var many []Statement
	if err := strictJSON(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type Policy struct {
	Version   string     `json:"Version"`
	Statement Statements `json:"Statement"`
}

func strictJSON(data []byte, target any) error {
	if err := rejectPolicyDuplicateKeys(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func ParsePolicy(data []byte) (Policy, error) {
	var p Policy
	if err := strictJSON(data, &p); err != nil {
		return p, fmt.Errorf("unsupported or invalid policy: %w", err)
	}
	if p.Version != "2012-10-17" || len(p.Statement) == 0 || len(p.Statement) > 100 {
		return p, fmt.Errorf("use Version 2012-10-17 and 1–100 identity-policy statements")
	}
	for _, s := range p.Statement {
		if s.Effect != "Allow" && s.Effect != "Deny" {
			return p, fmt.Errorf("Effect must be Allow or Deny")
		}
		for _, resource := range s.Resource {
			if !supportedPolicyVariables(resource) {
				return p, fmt.Errorf("unsupported resource policy variable")
			}
		}
		for _, action := range s.Action {
			if strings.Contains(action, "${") {
				return p, fmt.Errorf("policy variables are unsupported in Action")
			}
		}
		if err := s.Condition.validate(); err != nil {
			return p, err
		}
		for _, list := range []StringList{s.Action, s.Resource} {
			if len(list) == 0 || len(list) > 100 {
				return p, fmt.Errorf("Action and Resource require 1–100 values")
			}
			for _, v := range list {
				if len(v) == 0 || len(v) > 1024 {
					return p, fmt.Errorf("policy patterns must be 1–1024 bytes")
				}
			}
		}
	}
	return p, nil
}
func wildcard(pattern, value string) bool {
	quoted := regexp.QuoteMeta(pattern)
	quoted = strings.ReplaceAll(quoted, `\*`, ".*")
	quoted = strings.ReplaceAll(quoted, `\?`, ".")
	return regexp.MustCompile("(?s)^" + quoted + "$").MatchString(value)
}
func Evaluate(policy Policy, action, resource string) string {
	return EvaluateWithContext(policy, action, resource, nil)
}

// EvaluateWithContext accepts only provider-derived context, never request-supplied
// tags or a caller-selected user ID. Missing context does not satisfy conditions.
func EvaluateWithContext(policy Policy, action, resource string, values PolicyContext) string {
	// Typed policies also pass through this boundary; invalid conditional Deny
	// must not disappear beside an otherwise unconditional Allow.
	for _, statement := range policy.Statement {
		// nil means the provider has not implemented trusted context. Do not
		// drop a conditional Deny beside an unconditional Allow in old callers.
		if values == nil && statement.Condition != nil {
			return "implicitDeny"
		}
		if values == nil {
			for _, r := range statement.Resource {
				if strings.Contains(r, "${") {
					return "implicitDeny"
				}
			}
		}
		if statement.Condition.validate() != nil {
			return "implicitDeny"
		}
		for _, resource := range statement.Resource {
			if !supportedPolicyVariables(resource) {
				return "implicitDeny"
			}
		}
		for _, action := range statement.Action {
			if strings.Contains(action, "${") {
				return "implicitDeny"
			}
		}
	}
	allowed := false
	for _, s := range policy.Statement {
		aMatch, rMatch := false, false
		for _, a := range s.Action {
			if wildcard(strings.ToLower(a), strings.ToLower(action)) {
				aMatch = true
				break
			}
		}
		for _, r := range s.Resource {
			if policyValueMatch(r, resource, values, true) {
				rMatch = true
				break
			}
		}
		if aMatch && rMatch && s.Condition.matches(values) {
			if s.Effect == "Deny" {
				return "explicitDeny"
			}
			if s.Effect == "Allow" {
				allowed = true
			}
		}
	}
	if allowed {
		return "allowed"
	}
	return "implicitDeny"
}

type Identities struct{ store *Store }

func NewIdentities(store *Store) *Identities { return &Identities{store: store} }
func (i *Identities) Set(name string, p Policy) error {
	return i.save(name, p, false)
}

// Create never replaces an existing identity, including a concurrent creator.
func (i *Identities) Create(name string, p Policy) error {
	return i.save(name, p, true)
}

func (i *Identities) save(name string, p Policy, createOnly bool) error {
	if name == Administrator {
		return fmt.Errorf("the simulator administrator is reserved")
	}
	tx, err := i.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM identity_policies").Scan(&n); err != nil {
		return err
	}
	if n >= 1000 {
		var exists int
		if err = tx.QueryRow("SELECT 1 FROM identity_policies WHERE name=?", name).Scan(&exists); err == sql.ErrNoRows {
			return Fail("ServiceUnavailable", "Simulator principal limit reached.")
		} else if err != nil {
			return err
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	query := "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document"
	if createOnly {
		query = "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO NOTHING"
	}
	result, err := tx.Exec(query, name, string(data))
	if err != nil {
		return err
	}
	if createOnly {
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return Fail("PreconditionFailed", "An identity with this name already exists.")
		}
	}
	return tx.Commit()
}
func (i *Identities) Delete(name string) error {
	_, err := i.store.db.Exec("DELETE FROM identity_policies WHERE name=?", name)
	return err
}
func (i *Identities) Policy(name string) (Policy, bool, error) {
	return i.PolicyContext(context.Background(), name)
}
func (i *Identities) PolicyContext(ctx context.Context, name string) (Policy, bool, error) {
	var data []byte
	err := i.store.db.QueryRowContext(ctx, "SELECT document FROM identity_policies WHERE name=?", name).Scan(&data)
	if err == sql.ErrNoRows {
		return Policy{}, false, nil
	}
	if err != nil {
		return Policy{}, false, err
	}
	p, err := ParsePolicy(data)
	return p, err == nil, err
}
func (i *Identities) Names() ([]string, error) {
	rows, err := i.store.db.Query("SELECT name FROM identity_policies ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{Administrator}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (i *Identities) Decision(name, action, resource string) (string, error) {
	return i.DecisionContext(context.Background(), name, action, resource)
}
func (i *Identities) DecisionContext(ctx context.Context, name, action, resource string) (string, error) {
	if name == Administrator {
		return "allowed", nil
	}
	p, ok, err := i.PolicyContext(ctx, name)
	if err != nil {
		return "implicitDeny", err
	}
	if !ok {
		return "implicitDeny", nil
	}
	return Evaluate(p, action, resource), nil
}
