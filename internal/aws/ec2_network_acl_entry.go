package aws

import (
	"cmp"
	"context"
	"database/sql"
	"math"
	"net/netip"
	"slices"
	"strconv"
)

const (
	ec2CreateNetworkACLEntry  = "CreateNetworkAclEntry"
	ec2ReplaceNetworkACLEntry = "ReplaceNetworkAclEntry"
	ec2DeleteNetworkACLEntry  = "DeleteNetworkAclEntry"
	ec2NetworkACLRuleLimit    = 20 // editable rules per direction, excluding the terminal deny
	ec2NetworkACLTerminalRule = 32767
)

type ec2NetworkACLPortRange struct {
	From int `xml:"from"`
	To   int `xml:"to"`
}

// Kept on the one ACL row. A missing state denotes an unmodified legacy ACL;
// no second rule table or implicit rule adoption is needed on reopen.
type nativeNetworkACLRuleState struct {
	Updates int64
	Entries []ec2NetworkACLEntry
}

func networkACLEntryParameter(action, key string) bool {
	if action == "DeleteNetworkAcl" {
		return false
	}
	switch key {
	case "RuleNumber", "Egress":
		return true
	case "Protocol", "RuleAction", "CidrBlock", "PortRange.From", "PortRange.To":
		return action != ec2DeleteNetworkACLEntry
	}
	return false
}

func networkACLQueryNumber(raw string, min, max int) (int, bool) {
	if len(raw) == 0 || len(raw) > 5 {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n >= min && n <= max && strconv.Itoa(n) == raw
}

// The caller has already rejected unknown and repeated Query fields.
func parseNetworkACLEntry(action string, fields map[string][]string) (ec2NetworkACLEntry, *ec2Failure) {
	var entry ec2NetworkACLEntry
	get := func(key string) string {
		if values := fields[key]; len(values) == 1 {
			return values[0]
		}
		return ""
	}
	if len(fields["RuleNumber"]) == 0 || len(fields["Egress"]) == 0 {
		return entry, ec2Invalid("MissingParameter", "RuleNumber and Egress are required.")
	}
	n, ok := networkACLQueryNumber(get("RuleNumber"), 1, ec2NetworkACLTerminalRule-1)
	if !ok {
		return entry, ec2Invalid("InvalidParameterValue", "RuleNumber must be an integer from 1 to 32766; terminal rules cannot be changed.")
	}
	direction := get("Egress")
	if direction != "true" && direction != "false" {
		return entry, ec2Invalid("InvalidParameterValue", "Egress must be true or false.")
	}
	entry.Rule, entry.Egress = n, direction == "true"
	if action == ec2DeleteNetworkACLEntry {
		return entry, nil
	}
	if len(fields["CidrBlock"]) == 0 || len(fields["Protocol"]) == 0 || len(fields["RuleAction"]) == 0 {
		return entry, ec2Invalid("MissingParameter", "CidrBlock, Protocol and RuleAction are required.")
	}
	cidr := get("CidrBlock")
	if len(cidr) > len("255.255.255.255/32") {
		return entry, ec2Invalid("InvalidParameterValue", "CidrBlock must be an IPv4 prefix.")
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return entry, ec2Invalid("InvalidParameterValue", "CidrBlock must be an IPv4 prefix.")
	}
	entry.CIDR, entry.Protocol, entry.Action = prefix.Masked().String(), get("Protocol"), get("RuleAction")
	if entry.Action != "allow" && entry.Action != "deny" {
		return entry, ec2Invalid("InvalidParameterValue", "RuleAction must be allow or deny.")
	}
	switch entry.Protocol {
	case "-1":
		if len(fields["PortRange.From"]) != 0 || len(fields["PortRange.To"]) != 0 {
			return entry, ec2Invalid("UnsupportedOperation", "PortRange is only supported for TCP and UDP.")
		}
	case "6", "17":
		if len(fields["PortRange.From"]) == 0 || len(fields["PortRange.To"]) == 0 {
			return entry, ec2Invalid("MissingParameter", "PortRange.From and PortRange.To are required for TCP and UDP.")
		}
		from, fromOK := networkACLQueryNumber(get("PortRange.From"), 0, 65535)
		to, toOK := networkACLQueryNumber(get("PortRange.To"), 0, 65535)
		if !fromOK || !toOK || from > to {
			return entry, ec2Invalid("InvalidParameterValue", "PortRange must be ordered integers from 0 to 65535.")
		}
		entry.PortRange = &ec2NetworkACLPortRange{From: from, To: to}
	default:
		return entry, ec2Invalid("UnsupportedOperation", "Only the -1 (all), 6 (TCP) and 17 (UDP) IPv4 rule protocols are supported.")
	}
	return entry, nil
}

func compareNetworkACLEntryKey(a, b ec2NetworkACLEntry) int {
	if a.Egress != b.Egress {
		if a.Egress {
			return 1
		}
		return -1
	}
	return cmp.Compare(a.Rule, b.Rule)
}

func sameNetworkACLEntry(a, b ec2NetworkACLEntry) bool {
	if a.PortRange != b.PortRange {
		if a.PortRange == nil || b.PortRange == nil || *a.PortRange != *b.PortRange {
			return false
		}
	}
	a.PortRange, b.PortRange = nil, nil
	return a == b
}

func validNativeNetworkACLRuleState(r EC2NetworkResource) bool {
	state := r.NativeACLRules
	if r.Kind != ec2NetworkACLKind || r.StackID != ec2NativeOwner || state == nil || state.Updates < 1 ||
		state.Updates != r.Revision-1 || len(state.Entries) > 2*ec2NetworkACLRuleLimit || int64(len(state.Entries)) > state.Updates {
		return false
	}
	var counts [2]int
	for i, entry := range state.Entries {
		if entry.Rule < 1 || entry.Rule >= ec2NetworkACLTerminalRule || entry.Action != "allow" && entry.Action != "deny" ||
			i > 0 && compareNetworkACLEntryKey(state.Entries[i-1], entry) >= 0 {
			return false
		}
		direction := 0
		if entry.Egress {
			direction = 1
		}
		counts[direction]++
		if counts[direction] > ec2NetworkACLRuleLimit {
			return false
		}
		prefix, err := netip.ParsePrefix(entry.CIDR)
		if err != nil || !prefix.Addr().Is4() || prefix.Masked().String() != entry.CIDR {
			return false
		}
		switch entry.Protocol {
		case "-1":
			if entry.PortRange != nil {
				return false
			}
		case "6", "17":
			p := entry.PortRange
			if p == nil || p.From < 0 || p.To > 65535 || p.From > p.To {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func networkACLRecordEntries(state *nativeNetworkACLRuleState) []ec2NetworkACLEntry {
	var saved []ec2NetworkACLEntry
	if state != nil {
		saved = state.Entries
	}
	inboundEnd := 0
	for inboundEnd < len(saved) && !saved[inboundEnd].Egress {
		inboundEnd++
	}
	out := make([]ec2NetworkACLEntry, 0, len(saved)+2)
	terminal := ec2NetworkACLEntry{Rule: ec2NetworkACLTerminalRule, Protocol: "-1", Action: "deny", CIDR: "0.0.0.0/0"}
	out = append(out, saved[:inboundEnd]...)
	out = append(out, terminal)
	out = append(out, saved[inboundEnd:]...)
	terminal.Egress = true
	return append(out, terminal)
}

// Entry writes additionally look at the independent stack declarations in
// the same snapshot, including an unsupported ACL kind left by old data.
func checkNetworkACLEntryOwners(ctx context.Context, tx *sql.Tx, r EC2NetworkResource) error {
	stacks, declared, err := regionalGatewayStacks(ctx, tx, r.Region)
	if err != nil {
		return err
	}
	if declared[r.VPCID] || declared[r.ID] {
		return errNetworkACLState
	}
	for _, stack := range stacks {
		for _, member := range stack.Resources {
			if member.Type == ec2NetworkACLKind && member.PhysicalID == r.ID {
				return errNetworkACLState
			}
		}
	}
	return nil
}

func (s *StackStore) mutateNativeNetworkACLEntry(ctx context.Context, action, region, id string, entry ec2NetworkACLEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := readNativeNetworkACL(ctx, tx, region, id)
	if err != nil {
		return err
	}
	if err := checkNetworkACLEntryOwners(ctx, tx, r); err != nil {
		return err
	}
	state := r.NativeACLRules
	if state == nil {
		state = &nativeNetworkACLRuleState{}
	}
	index, exists := slices.BinarySearchFunc(state.Entries, entry, compareNetworkACLEntryKey)
	switch action {
	case ec2CreateNetworkACLEntry:
		if exists {
			return ec2Invalid("NetworkAclEntryAlreadyExists", "That rule number already exists in this direction.")
		}
		count := 0
		for _, rule := range state.Entries {
			if rule.Egress == entry.Egress {
				count++
			}
		}
		if count >= ec2NetworkACLRuleLimit {
			return ec2Invalid("NetworkAclEntryLimitExceeded", "The local network ACL allows up to 20 editable rules per direction.")
		}
		state.Entries = slices.Insert(state.Entries, index, entry)
	case ec2ReplaceNetworkACLEntry, ec2DeleteNetworkACLEntry:
		if !exists {
			return ec2Invalid("InvalidNetworkAclEntry.NotFound", "That rule number does not exist in this direction.")
		}
		if action == ec2ReplaceNetworkACLEntry {
			if sameNetworkACLEntry(state.Entries[index], entry) {
				return tx.Commit()
			}
			state.Entries[index] = entry
		} else {
			state.Entries = slices.Delete(state.Entries, index, index+1)
		}
	default:
		return ec2Invalid("UnsupportedOperation", "Only native detached network ACL entry configuration is supported.")
	}
	if r.Revision == math.MaxInt64 {
		return errNetworkACLState
	}
	state.Updates++
	r.Revision++
	r.NativeACLRules = state
	if err := networkWrite(ctx, tx, r, false); err != nil {
		return err
	}
	// networkWrite has already verified the exact re-encoded row; recheck the
	// parent and CloudFormation claim before committing any success.
	stored, err := readNativeNetworkACL(ctx, tx, region, id)
	if err != nil {
		return err
	}
	if err := checkNetworkACLEntryOwners(ctx, tx, stored); err != nil {
		return err
	}
	return tx.Commit()
}
