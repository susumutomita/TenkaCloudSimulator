package appengine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type rule struct {
	Priority                         int64
	Action, SourceRange, Description string
}

func ruleResource(r rule) map[string]any {
	out := map[string]any{"priority": r.Priority, "action": r.Action, "sourceRange": r.SourceRange}
	if r.Description != "" {
		out["description"] = r.Description
	}
	return out
}

// validRange accepts "*", one IP address or a CIDR range.
func validRange(value string) bool {
	if value == "*" {
		return true
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return true
	}
	_, err := netip.ParsePrefix(value)
	return err == nil
}

// matches reports whether a rule's source range contains the address.
func (r rule) matches(address netip.Addr) bool {
	if r.SourceRange == "*" {
		return true
	}
	address = address.Unmap()
	if a, err := netip.ParseAddr(r.SourceRange); err == nil {
		return a.Unmap() == address
	}
	p, err := netip.ParsePrefix(r.SourceRange)
	return err == nil && p.Masked().Contains(address)
}

func loadRules(tx *sql.Tx, project string) ([]rule, error) {
	rows, err := tx.Query("SELECT priority, action, source_range, description FROM gcp_appengine_firewall WHERE project = ? ORDER BY priority", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rule{}
	for rows.Next() {
		var r rule
		if err := rows.Scan(&r.Priority, &r.Action, &r.SourceRange, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// allowed evaluates rules in priority order; the first match decides.
func allowed(rules []rule, address netip.Addr) bool {
	for _, r := range rules {
		if r.matches(address) {
			return r.Action == "ALLOW"
		}
	}
	return false
}

// parseRule validates one FirewallRule body. defaultAllowed accepts the
// default rule's priority, as batchUpdate does.
func (e *Engine) parseRule(raw map[string]json.RawMessage, defaultAllowed bool) (rule, error) {
	body, err := e.fields(raw, "FirewallRule", "priority", "action", "sourceRange", "description")
	if err != nil {
		return rule{}, err
	}
	var r rule
	if value, ok := body["priority"]; !ok || json.Unmarshal(value, &r.Priority) != nil {
		return r, invalid("priority is required: an integer from 1 through 2147483646.")
	}
	if r.Priority < 1 || r.Priority > defaultRule || r.Priority == defaultRule && !defaultAllowed {
		return r, invalid("priority must be from 1 through 2147483646; 2147483647 is the default rule.")
	}
	for name, target := range map[string]*string{"action": &r.Action, "sourceRange": &r.SourceRange, "description": &r.Description} {
		if value, ok := body[name]; ok {
			if *target, err = decodeString(value, name); err != nil {
				return r, err
			}
		}
	}
	if r.Action != "ALLOW" && r.Action != "DENY" {
		return r, invalid("action must be ALLOW or DENY.")
	}
	if !validRange(r.SourceRange) {
		return r, invalid("sourceRange must be *, an IP address or a CIDR range.")
	}
	if r.Priority == defaultRule && r.SourceRange != "*" {
		return r, invalid("The default rule must match every address (sourceRange *).")
	}
	if utf8.RuneCountInString(r.Description) > 400 || !utf8.ValidString(r.Description) {
		return r, invalid("description is limited to 400 characters.")
	}
	return r, nil
}

func ruleID(q *request) (int64, error) {
	p, err := strconv.ParseInt(q.path["ingressRulesId"], 10, 64)
	if err != nil || p < 1 || p > defaultRule {
		return 0, invalid("The ingress rule ID is its priority, an integer from 1 through 2147483647.")
	}
	return p, nil
}

func findRule(rules []rule, priority int64) (rule, bool) {
	for _, r := range rules {
		if r.Priority == priority {
			return r, true
		}
	}
	return rule{}, false
}

func (e *Engine) createRule(q *request) (any, error) {
	r, err := e.parseRule(q.body, false)
	if err != nil {
		return nil, err
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if _, found := findRule(rules, r.Priority); found {
			return nil, exists("A firewall rule with priority " + strconv.FormatInt(r.Priority, 10) + " already exists.")
		}
		if len(rules) > maxFirewallRules {
			return nil, exhausted("The local App Engine simulator keeps at most 100 firewall rules besides the default rule.")
		}
		if _, err := tx.Exec("INSERT INTO gcp_appengine_firewall(project, priority, action, source_range, description) VALUES(?, ?, ?, ?, ?)", e.cfg.Project, r.Priority, r.Action, r.SourceRange, r.Description); err != nil {
			return nil, err
		}
		return ruleResource(r), nil
	})
}

func (e *Engine) listRules(q *request) (any, error) {
	size, after, err := pageParams(q)
	if err != nil {
		return nil, err
	}
	var address netip.Addr
	if raw := q.param("matchingAddress"); raw != "" {
		if address, err = netip.ParseAddr(raw); err != nil {
			return nil, invalid("matchingAddress must be an IP address.")
		}
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if address.IsValid() {
			matching := []rule{}
			for _, r := range rules {
				if r.matches(address) {
					matching = append(matching, r)
				}
			}
			rules = matching
		}
		items, token := page(rules, func(r rule) string { return priorityKey(r.Priority) }, size, after)
		out := []any{}
		for _, r := range items {
			out = append(out, ruleResource(r))
		}
		return listResponse("ingressRules", out, token), nil
	})
}

func (e *Engine) getRule(q *request) (any, error) {
	priority, err := ruleID(q)
	if err != nil {
		return nil, err
	}
	return e.read(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		r, found := findRule(rules, priority)
		if !found {
			return nil, notFound("Firewall rule " + q.path["ingressRulesId"] + " was not found.")
		}
		return ruleResource(r), nil
	})
}

func (e *Engine) patchRule(q *request) (any, error) {
	priority, err := ruleID(q)
	if err != nil {
		return nil, err
	}
	paths, err := mask(q, "action", "sourceRange", "description")
	if err != nil {
		return nil, err
	}
	body, err := e.patchBody(q.body, "FirewallRule", paths)
	if err != nil {
		return nil, err
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		r, found := findRule(rules, priority)
		if !found {
			return nil, notFound("Firewall rule " + q.path["ingressRulesId"] + " was not found.")
		}
		candidate := map[string]json.RawMessage{}
		for name, value := range map[string]string{"action": r.Action, "sourceRange": r.SourceRange, "description": r.Description} {
			candidate[name], _ = json.Marshal(value)
		}
		for _, name := range paths {
			if value, ok := body[name]; ok {
				candidate[name] = value
			} else {
				candidate[name] = json.RawMessage(`""`)
			}
		}
		candidate["priority"], _ = json.Marshal(r.Priority)
		updated, err := e.parseRule(candidate, true)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec("UPDATE gcp_appengine_firewall SET action = ?, source_range = ?, description = ? WHERE project = ? AND priority = ?", updated.Action, updated.SourceRange, updated.Description, e.cfg.Project, priority); err != nil {
			return nil, err
		}
		return ruleResource(updated), nil
	})
}

func (e *Engine) deleteRule(q *request) (any, error) {
	priority, err := ruleID(q)
	if err != nil {
		return nil, err
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if _, found := findRule(rules, priority); !found {
			return nil, notFound("Firewall rule " + q.path["ingressRulesId"] + " was not found.")
		}
		if priority == defaultRule {
			return nil, failed("The default firewall rule cannot be deleted; change its action instead.")
		}
		_, err = tx.Exec("DELETE FROM gcp_appengine_firewall WHERE project = ? AND priority = ?", e.cfg.Project, priority)
		return map[string]any{}, err
	})
}

// batchUpdateRules replaces every rule. Without a rule at the default
// priority, the default rule allows all traffic, as the official method adds
// an "allow all" rule at the end.
func (e *Engine) batchUpdateRules(q *request) (any, error) {
	body, err := e.fields(q.body, "BatchUpdateIngressRulesRequest", "ingressRules")
	if err != nil {
		return nil, err
	}
	var raws []map[string]json.RawMessage
	if value, ok := body["ingressRules"]; ok {
		if err := json.Unmarshal(value, &raws); err != nil {
			return nil, invalid("ingressRules must be a list of FirewallRule objects.")
		}
	}
	if len(raws) > maxFirewallRules+1 {
		return nil, invalid("The local App Engine simulator keeps at most 100 firewall rules besides the default rule.")
	}
	rules, seen := []rule{}, map[int64]bool{}
	defaultAction := "ALLOW"
	for _, raw := range raws {
		if raw == nil {
			return nil, invalid("ingressRules must not contain null.")
		}
		r, err := e.parseRule(raw, true)
		if err != nil {
			return nil, err
		}
		if seen[r.Priority] {
			return nil, invalid("Each ingress rule needs a distinct priority; " + strconv.FormatInt(r.Priority, 10) + " repeats.")
		}
		seen[r.Priority] = true
		if r.Priority == defaultRule {
			defaultAction = r.Action
			continue
		}
		rules = append(rules, r)
	}
	if len(rules) > maxFirewallRules {
		return nil, invalid("The local App Engine simulator keeps at most 100 firewall rules besides the default rule.")
	}
	return e.write(q.ctx, func(tx *sql.Tx) (any, error) {
		if _, err := loadApplication(tx, e.cfg.Project); err != nil {
			return nil, err
		}
		if _, err := tx.Exec("DELETE FROM gcp_appengine_firewall WHERE project = ? AND priority <> ?", e.cfg.Project, defaultRule); err != nil {
			return nil, err
		}
		if _, err := tx.Exec("UPDATE gcp_appengine_firewall SET action = ? WHERE project = ? AND priority = ?", defaultAction, e.cfg.Project, defaultRule); err != nil {
			return nil, err
		}
		for _, r := range rules {
			if _, err := tx.Exec("INSERT INTO gcp_appengine_firewall(project, priority, action, source_range, description) VALUES(?, ?, ?, ?, ?)", e.cfg.Project, r.Priority, r.Action, r.SourceRange, r.Description); err != nil {
				return nil, err
			}
		}
		all, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		sort.Slice(all, func(i, j int) bool { return all[i].Priority < all[j].Priority })
		out := []any{}
		for _, r := range all {
			out = append(out, ruleResource(r))
		}
		return map[string]any{"ingressRules": out}, nil
	})
}

func clientAddress(remote string) (netip.Addr, error) {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap(), nil
	}
	a, err := netip.ParseAddr(strings.Trim(remote, "[]"))
	if err != nil {
		return netip.Addr{}, errors.New("unparseable client address")
	}
	return a.Unmap(), nil
}
