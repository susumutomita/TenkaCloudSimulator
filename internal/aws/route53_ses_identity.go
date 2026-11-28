package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Route 53 permits these verification records as CNAME owners, not as zone
// names, CNAME targets, or owners of arbitrary record types.
func r53recordName(raw, kind string) (string, error) {
	if !strings.Contains(raw, "_") || (kind != "" && kind != "CNAME") {
		return r53name(raw)
	}
	for i := range len(raw) {
		if raw[i] >= 128 {
			return "", r53invalid("Only ASCII DNS names are supported.")
		}
	}
	name := strings.TrimSuffix(strings.ToLower(raw), ".")
	selector, domain, ok := strings.Cut(name, "._domainkey.")
	if !ok || len(name) > 253 || len(selector) != 32 || !strings.HasSuffix(domain, ".test") {
		return "", r53invalid("Unsupported DNS record owner.")
	}
	for _, c := range selector {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return "", r53invalid("Invalid local DKIM selector.")
		}
	}
	if _, err := r53name(domain); err != nil {
		return "", err
	}
	return name + ".", nil
}

// LocalSESDomainDNS inspects native records inside the caller's transaction.
// Route 53 allows duplicate public zone names; none can establish authority
// for this local protocol while a same-name alternative exists.
func LocalSESDomainDNS(ctx context.Context, tx *sql.Tx, domain string, selectors []string) (string, bool, error) {
	var count int
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT count(*),COALESCE(min(id),'') FROM aws_route53_zones WHERE name=?", domain+".").Scan(&count, &id); err != nil {
		return "", false, err
	}
	if count != 1 {
		return "", false, nil
	}
	if len(selectors) == 0 {
		return id, false, nil
	}
	if len(selectors) != 3 {
		return "", false, errors.New("invalid saved SES domain verification set")
	}
	for _, selector := range selectors {
		name := selector + "._domainkey." + domain + "."
		if _, err := r53recordName(name, "CNAME"); err != nil {
			return "", false, err
		}
		var doc string
		err := tx.QueryRowContext(ctx, "SELECT document FROM aws_route53_records WHERE zone=? AND name=? AND type='CNAME'", id, name).Scan(&doc)
		if errors.Is(err, sql.ErrNoRows) {
			return id, false, nil
		}
		if err != nil {
			return "", false, err
		}
		var rr Route53Record
		if err = json.Unmarshal([]byte(doc), &rr); err != nil {
			return "", false, err
		}
		if rr.Name != name || rr.Type != "CNAME" || rr.TTL == nil || *rr.TTL < 0 || *rr.TTL > 2147483647 || len(rr.Values) != 1 {
			return "", false, errors.New("invalid saved local DNS verification record")
		}
		if rr.Values[0] != selector+".dkim.amazonses.com." {
			return id, false, nil
		}
	}
	return id, true, nil
}
