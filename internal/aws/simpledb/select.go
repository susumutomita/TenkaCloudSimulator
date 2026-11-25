package simpledb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"slices"
	"strconv"
	"time"
)

// The candidate bound includes nonmatches. A selective query can yield an empty
// page with a token; clients must continue until no token is returned.
const (
	selectScanBudget = 128
	selectBodyBudget = 1_000_000 - 8192 // reserve the envelope and maximum signed cursor
	selectPurpose    = "SimpleDB:Select:v1\x00"
)

type querySelectItem struct {
	XMLName    xml.Name         `xml:"Item"`
	Name       queryText        `xml:"Name"`
	Attributes []queryAttribute `xml:"Attribute"`
}
type querySelectResult struct {
	Items []querySelectItem `xml:"Item"`
	Next  string            `xml:"NextToken,omitempty"`
}
type selectCursor struct {
	Query string
	After []byte
}

func selectScope(sc Scope, expr string, consistent bool) string {
	raw, _ := json.Marshal([4]string{sc.Account, sc.Region, expr, strconv.FormatBool(consistent)})
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}
func (s *Store) selectItems(parent context.Context, sc Scope, p selectPlan, expr string, consistent bool, token string) (_ *querySelectResult, err error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	defer func() {
		if err != nil {
			if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				err = storeError("QueryTimeout")
			} else {
				err = failure(parent, err)
			}
		}
	}()
	qs := selectScope(sc, expr, consistent)
	after := ""
	if token != "" {
		raw, e := s.openCursor(token, selectPurpose)
		if e != nil {
			return nil, e
		}
		var c selectCursor
		if json.Unmarshal(raw, &c) != nil || c.Query != qs || !validText(string(c.After), false) {
			return nil, storeError("InvalidNextToken")
		}
		after = string(c.After)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if e = requireDomain(ctx, tx, sc.Account, sc.Region, p.domain); e != nil {
		return nil, e
	}
	columns := "name, attributes"
	if p.namesOnly && p.attr == "" {
		columns = "name, ''"
	}
	stmt := "SELECT " + columns + " FROM aws_simpledb_items WHERE account=? AND region=? AND domain=?"
	args := []any{sc.Account, sc.Region, p.domain}
	if p.nameOp != "" {
		switch p.nameOp {
		case "=":
			stmt += " AND name=?"
		case "<":
			stmt += " AND name<?"
		case "<=":
			stmt += " AND name<=?"
		case ">":
			stmt += " AND name>?"
		case ">=":
			stmt += " AND name>=?"
		default:
			return nil, errors.New("invalid internal item comparison")
		}
		args = append(args, p.value)
	}
	if token != "" {
		if p.desc {
			stmt += " AND name<?"
		} else {
			stmt += " AND name>?"
		}
		args = append(args, after)
	}
	if p.desc {
		stmt += " ORDER BY name DESC LIMIT ?"
	} else {
		stmt += " ORDER BY name ASC LIMIT ?"
	}
	args = append(args, selectScanBudget+1)
	rows, e := tx.QueryContext(ctx, stmt, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	result := &querySelectResult{}
	consumed, size, readBytes := 0, 0, 0
	more := false
	for rows.Next() {
		if consumed >= selectScanBudget || readBytes >= 1<<20 {
			more = true
			break
		}
		var name, raw string
		if e = rows.Scan(&name, &raw); e != nil {
			return nil, e
		}
		item := querySelectItem{Name: xmlText(name)}
		if !p.namesOnly || p.attr != "" {
			var attrs map[string][]string
			if e = json.Unmarshal([]byte(raw), &attrs); e != nil {
				return nil, e
			}
			if attrs == nil {
				return nil, errors.New("invalid stored SimpleDB attributes")
			}
			if p.attr != "" && !slices.Contains(attrs[p.attr], p.value) {
				after = name
				consumed++
				readBytes += len(raw)
				continue
			}
			if !p.namesOnly {
				item.Attributes = wireAttributes(attrs)
			}
		}
		if len(result.Items) == p.limit {
			more = true
			break
		}
		wire, e := xml.Marshal(item)
		if e != nil {
			return nil, e
		}
		if len(wire) > selectBodyBudget && len(result.Items) == 0 {
			return nil, storeError("ItemTooLargeForSelect")
		}
		if size+len(wire) > selectBodyBudget {
			more = true
			break
		}
		result.Items = append(result.Items, item)
		size += len(wire)
		readBytes += len(raw)
		consumed++
		after = name
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	if more {
		raw, _ := json.Marshal(selectCursor{qs, []byte(after)})
		result.Next = s.signCursor(raw, selectPurpose)
	}
	return result, nil
}
