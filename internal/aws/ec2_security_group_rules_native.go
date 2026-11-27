package aws

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/xml"
	"maps"
	"net/netip"
	"slices"
	"strconv"
)

const (
	ec2AuthorizeNativeSGIngress = "AuthorizeSecurityGroupIngress"
	ec2AuthorizeNativeSGEgress  = "AuthorizeSecurityGroupEgress"
	ec2RevokeNativeSGIngress    = "RevokeSecurityGroupIngress"
	ec2RevokeNativeSGEgress     = "RevokeSecurityGroupEgress"
	ec2NativeSGRuleLimit        = 60 // Per direction, independent of the peer direction.
)

// Rules are detached TCP/IPv4 configuration, not rules used by the native
// guest network. They are owned by the canonical native group via one FK;
// there is no copy in the shared CloudFormation ingress registry.
type ec2NativeSGRule struct {
	egress   bool
	from, to int
	cidr     string
}

func ec2NativeSGRuleAction(action string) (egress, authorize, ok bool) {
	switch action {
	case ec2AuthorizeNativeSGIngress:
		return false, true, true
	case ec2AuthorizeNativeSGEgress:
		return true, true, true
	case ec2RevokeNativeSGIngress:
		return false, false, true
	case ec2RevokeNativeSGEgress:
		return true, false, true
	default:
		return false, false, false
	}
}

// EC2 rule operations use their own narrow receipt; no group creation
// fields or fabricated SecurityGroupRule IDs are serialized.
type ec2NativeSGRuleMutationResponse struct {
	XMLName   xml.Name
	XMLNS     string `xml:"xmlns,attr"`
	RequestID string `xml:"requestId"`
	Return    bool   `xml:"return"`
}

func (s *StackStore) executeEC2NativeSGRuleMutation(ctx context.Context, action, region string, q ec2NativeSecurityGroupQuery) (*ec2NativeSGRuleMutationResponse, error) {
	egress, authorize, supported := ec2NativeSGRuleAction(action)
	if !supported || q.rule.egress != egress {
		return nil, ec2Invalid("InvalidParameterValue", "Rule direction did not match the selected operation.")
	}
	if err := s.changeEC2NativeSGRule(ctx, region, q.id, q.rule, authorize); err != nil {
		return nil, err
	}
	return &ec2NativeSGRuleMutationResponse{XMLName: xml.Name{Local: action + "Response"}, Return: true}, nil
}

func parseEC2NativeSGRuleQuery(action string, fields map[string][]string) (ec2NativeSecurityGroupQuery, *ec2Failure) {
	egress, _, ok := ec2NativeSGRuleAction(action)
	var q ec2NativeSecurityGroupQuery
	if !ok {
		return q, ec2Invalid("UnsupportedOperation", "Only native TCP/IPv4 security group rule operations are supported.")
	}
	const protocolKey = "IpPermissions.1.IpProtocol"
	const fromKey = "IpPermissions.1.FromPort"
	const toKey = "IpPermissions.1.ToPort"
	const cidrKey = "IpPermissions.1.IpRanges.1.CidrIp"
	permission := map[string]string{}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		xs := fields[key]
		if len(xs) != 1 {
			return q, ec2Invalid("InvalidParameterValue", "Each security group rule field must be sent once.")
		}
		switch key {
		case "Action", "Version":
		case "DryRun":
			if xs[0] != "false" {
				return q, ec2Invalid("UnsupportedOperation", "Only DryRun=false is supported for native security group rules.")
			}
		case "GroupId", protocolKey, fromKey, toKey, cidrKey:
			permission[key] = xs[0]
		default:
			return q, ec2Invalid("UnsupportedOperation", "Use GroupId and exactly one TCP/IPv4 IpPermissions member with one IpRanges member.")
		}
	}
	for _, key := range []string{"GroupId", protocolKey, fromKey, toKey, cidrKey} {
		if permission[key] == "" {
			return q, ec2Invalid("MissingParameter", "GroupId, protocol, port range and one IPv4 CIDR are required.")
		}
	}
	q.id = permission["GroupId"]
	if !ec2NativeSecurityGroupID.MatchString(q.id) {
		return q, ec2Invalid("InvalidGroupId.Malformed", "GroupId must be an sg- ID.")
	}
	if permission[protocolKey] != "tcp" {
		return q, ec2Invalid("UnsupportedOperation", "Only tcp is supported by the detached security group rule subset.")
	}
	from, err1 := strconv.Atoi(permission[fromKey])
	to, err2 := strconv.Atoi(permission[toKey])
	q.rule = ec2NativeSGRule{egress: egress, from: from, to: to, cidr: permission[cidrKey]}
	if err1 != nil || err2 != nil || strconv.Itoa(from) != permission[fromKey] || strconv.Itoa(to) != permission[toKey] || !q.rule.valid() {
		return q, ec2Invalid("InvalidParameterValue", "Use a canonical IPv4 CIDR and a TCP port range from 1 to 65535.")
	}
	return q, nil
}

func (r ec2NativeSGRule) valid() bool {
	if r.from < 1 || r.to < r.from || r.to > 65535 || len(r.cidr) > 18 {
		return false
	}
	prefix, err := netip.ParsePrefix(r.cidr)
	return err == nil && prefix.Addr().Is4() && prefix.Masked().String() == r.cidr
}

func (r ec2NativeSGRule) within(vpcCIDR string) bool {
	if !r.valid() {
		return false
	}
	parent, err := netip.ParsePrefix(vpcCIDR)
	if err != nil || !parent.Addr().Is4() || parent.Bits() < 16 || parent.Bits() > 28 || parent.Masked().String() != vpcCIDR {
		return false
	}
	prefix, _ := netip.ParsePrefix(r.cidr)
	return prefix.Bits() >= parent.Bits() && parent.Contains(prefix.Addr())
}

func (r ec2NativeSGRule) direction() int {
	if r.egress {
		return 1
	}
	return 0
}

func compareEC2NativeSGRule(a, b ec2NativeSGRule) int {
	if n := cmp.Compare(a.direction(), b.direction()); n != 0 {
		return n
	}
	if n := cmp.Compare(a.from, b.from); n != 0 {
		return n
	}
	if n := cmp.Compare(a.to, b.to); n != 0 {
		return n
	}
	return cmp.Compare(a.cidr, b.cidr)
}

func initEC2NativeSGRules(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ec2_native_security_group_rules(
		region TEXT NOT NULL, group_id TEXT NOT NULL,
		egress INTEGER NOT NULL CHECK(egress IN (0,1)),
		from_port INTEGER NOT NULL CHECK(from_port BETWEEN 1 AND 65535),
		to_port INTEGER NOT NULL CHECK(to_port BETWEEN from_port AND 65535),
		cidr TEXT NOT NULL CHECK(length(cidr) BETWEEN 9 AND 18),
		PRIMARY KEY(region,group_id,egress,from_port,to_port,cidr),
		FOREIGN KEY(region,group_id) REFERENCES aws_ec2_native_security_groups(region,id) ON DELETE CASCADE
	) WITHOUT ROWID`)
	return err
}

func readEC2NativeSGRules(ctx context.Context, tx *sql.Tx, region, id, vpcCIDR string) ([]ec2NativeSGRule, error) {
	rows, err := tx.QueryContext(ctx, `SELECT egress,from_port,to_port,
		CASE WHEN typeof(cidr)='text' AND length(CAST(cidr AS BLOB)) BETWEEN 9 AND 18 THEN cidr ELSE '' END
		FROM aws_ec2_native_security_group_rules WHERE region=? AND group_id=?
		ORDER BY egress,from_port,to_port,cidr LIMIT ?`, region, id, 2*ec2NativeSGRuleLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []ec2NativeSGRule
	var counts [2]int
	for rows.Next() {
		var direction int
		var rule ec2NativeSGRule
		if err := rows.Scan(&direction, &rule.from, &rule.to, &rule.cidr); err != nil {
			return nil, err
		}
		if direction != 0 && direction != 1 {
			return nil, errEC2NativeSecurityGroupState
		}
		rule.egress = direction == 1
		counts[direction]++
		if counts[direction] > ec2NativeSGRuleLimit || !rule.within(vpcCIDR) ||
			(len(rules) > 0 && compareEC2NativeSGRule(rules[len(rules)-1], rule) >= 0) {
			return nil, errEC2NativeSecurityGroupState
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return rules, nil
}

func (s *StackStore) changeEC2NativeSGRule(ctx context.Context, region, groupID string, rule ec2NativeSGRule, authorize bool) error {
	if !ec2NativeSecurityGroupID.MatchString(groupID) || !rule.valid() {
		return ec2Invalid("InvalidParameterValue", "Use a local GroupId and canonical TCP/IPv4 rule.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	group, err := readEC2NativeSecurityGroup(ctx, tx, region, groupID)
	if err != nil {
		return err
	}
	if !rule.within(group.parentCIDR) {
		return ec2Invalid("InvalidParameterValue", "The rule CIDR must be contained in this security group's VPC CIDR.")
	}
	at, found := slices.BinarySearchFunc(group.rules, rule, compareEC2NativeSGRule)
	if found == authorize {
		if authorize {
			return ec2Invalid("InvalidPermission.Duplicate", "This rule already exists in this direction.")
		}
		return ec2Invalid("InvalidPermission.NotFound", "This rule does not exist in this direction.")
	}
	want := slices.Clone(group.rules)
	var result sql.Result
	if authorize {
		count := 0
		for _, prior := range group.rules {
			if prior.egress == rule.egress {
				count++
			}
		}
		if count >= ec2NativeSGRuleLimit {
			return ec2Invalid("RulesPerSecurityGroupLimitExceeded", "The local rule limit for this direction has been reached.")
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO aws_ec2_native_security_group_rules
			(region,group_id,egress,from_port,to_port,cidr) VALUES(?,?,?,?,?,?)`,
			region, groupID, rule.direction(), rule.from, rule.to, rule.cidr)
		want = slices.Insert(want, at, rule)
	} else {
		result, err = tx.ExecContext(ctx, `DELETE FROM aws_ec2_native_security_group_rules
			WHERE region=? AND group_id=? AND egress=? AND from_port=? AND to_port=? AND cidr=?`,
			region, groupID, rule.direction(), rule.from, rule.to, rule.cidr)
		want = slices.Delete(want, at, at+1)
	}
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errEC2NativeSecurityGroupState
	}
	now, err := readEC2NativeSecurityGroup(ctx, tx, region, groupID)
	if err != nil {
		return err
	}
	if now.Name != group.Name || now.VPCID != group.VPCID || now.parentCIDR != group.parentCIDR ||
		now.Description != group.Description || !maps.Equal(now.Tags, group.Tags) || !slices.Equal(now.rules, want) {
		return errEC2NativeSecurityGroupState
	}
	return tx.Commit()
}

func (g ec2NativeSecurityGroup) readback() ec2SecurityGroupRead {
	out := ec2SecurityGroupRead{
		ID: g.ID, Name: g.Name, Description: g.Description, VPC: g.VPCID, Owner: "000000000000",
		Tags: ec2ReadTags(g.Tags), Mode: "detached-config-only",
	}
	for _, r := range g.rules {
		item := ec2SecurityGroupPermission{Protocol: "tcp", FromPort: r.from, ToPort: r.to,
			Ranges: []ec2SecurityGroupIPv4Range{{CIDR: r.cidr}}}
		if r.egress {
			if out.Egress == nil {
				out.Egress = &ec2SecurityGroupPermissionSet{}
			}
			out.Egress.Items = append(out.Egress.Items, item)
		} else {
			out.Ingress = append(out.Ingress, item)
		}
	}
	return out
}
