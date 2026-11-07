package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var stsAWSPrincipal = regexp.MustCompile(`^arn:aws:iam::[0-9]{12}:(root|user/[A-Za-z0-9_+=,.@/-]+|role/[A-Za-z0-9_+=,.@/-]+)$`)
var stsAccount = regexp.MustCompile(`^[0-9]{12}$`)

func stsValidateTrustStatement(s iamTrustStatement) error {
	bad := ssmFailure{"MalformedPolicyDocument", "Only explicit AWS or Service principals and StringEquals sts:ExternalId trust conditions are supported."}
	if len(s.Principal.AWS) > 0 && len(s.Principal.Service) > 0 {
		return bad
	}
	for _, p := range s.Principal.AWS {
		if !stsAWSPrincipal.MatchString(p) && !stsAccount.MatchString(p) {
			return bad
		}
	}
	if s.Condition != nil {
		if len(s.Principal.AWS) == 0 || len(s.Condition) != 1 {
			return bad
		}
		eq, ok := s.Condition["StringEquals"]
		if !ok || len(eq) != 1 {
			return bad
		}
		ids, ok := eq["sts:ExternalId"]
		if !ok || len(ids) == 0 || len(ids) > 10 {
			return bad
		}
		for _, id := range ids {
			if !stsValidExternalID(id) {
				return bad
			}
		}
	}
	return nil
}
func stsTrustAllows(document string, caller SDKIdentity, external string) (bool, error) {
	if _, err := iamTrust(document); err != nil {
		return false, err
	}
	var policy struct{ Statement iamTrustStatements }
	if err := json.Unmarshal([]byte(document), &policy); err != nil {
		return false, err
	}
	callerARN := caller.ARN
	if strings.HasPrefix(callerARN, "arn:aws:sts::"+caller.Account+":assumed-role/") {
		callerARN = "arn:aws:iam::" + caller.Account + ":role/" + caller.Principal
	}
	allowed := false
	for _, s := range policy.Statement {
		match := false
		for _, p := range s.Principal.AWS {
			match = match || p == callerARN || p == caller.Account || p == "arn:aws:iam::"+caller.Account+":root"
		}
		if s.Condition != nil {
			same := false
			for _, v := range s.Condition["StringEquals"]["sts:ExternalId"] {
				same = same || external != "" && external == v
			}
			match = match && same
		}
		if match {
			if s.Effect == "Deny" {
				return false, nil
			}
			allowed = true
		}
	}
	return allowed, nil
}

// Resolve only persisted native roles or an extant CloudFormation-owned role.
// Legacy identity names alone never constitute an assumable role.
func (g *Gateway) stsRole(ctx context.Context, arn string) (IAMRole, error) {
	prefix := "arn:aws:iam::000000000000:role/"
	name := strings.TrimPrefix(arn, prefix)
	if !strings.HasPrefix(arn, prefix) || !iamRoleName.MatchString(name) {
		return IAMRole{}, ssmFailure{"AccessDenied", "Role is not available."}
	}
	if g.Roles != nil {
		owned, err := g.Roles.Owns(ctx, name)
		if err != nil {
			return IAMRole{}, err
		}
		if owned {
			r, err := g.Roles.Get(ctx, name)
			if err == nil && r.Arn != arn {
				return IAMRole{}, ssmFailure{"AccessDenied", "Role is not available."}
			}
			return r, err
		}
	}
	if g.Stacks == nil {
		return IAMRole{}, ssmFailure{"AccessDenied", "Role is not available."}
	}
	var document, stackID string
	err := g.Stacks.db.QueryRowContext(ctx, `SELECT s.document,s.id FROM aws_cfn_stacks s JOIN aws_cfn_ownership o ON s.id=o.stack_id JOIN identity_policies i ON i.name=o.physical_id WHERE o.kind='AWS::IAM::Role' AND o.physical_id=?`, name).Scan(&document, &stackID)
	if err == sql.ErrNoRows {
		return IAMRole{}, ssmFailure{"AccessDenied", "Role is not available."}
	}
	if err != nil {
		return IAMRole{}, err
	}
	var record StackRecord
	if err = json.Unmarshal([]byte(document), &record); err != nil {
		return IAMRole{}, err
	}
	if record.ID != stackID {
		return IAMRole{}, fmt.Errorf("inconsistent stack generation")
	}
	for _, r := range record.Resources {
		if r.Type == "AWS::IAM::Role" && r.PhysicalID == name {
			trust, err := json.Marshal(r.Properties["AssumeRolePolicyDocument"])
			if err != nil {
				return IAMRole{}, err
			}
			duration := 3600
			if v, ok := r.Properties["MaxSessionDuration"]; ok {
				switch value := v.(type) {
				case float64:
					if value < 3600 || value > 43200 || value != float64(int(value)) {
						return IAMRole{}, ssmFailure{"ValidationError", "Invalid role duration."}
					}
					duration = int(value)
				case string:
					parsed, err := strconv.Atoi(value)
					if err != nil || parsed < 3600 || parsed > 43200 || strconv.Itoa(parsed) != value {
						return IAMRole{}, ssmFailure{"ValidationError", "Invalid role duration."}
					}
					duration = parsed
				default:
					return IAMRole{}, ssmFailure{"ValidationError", "Invalid role duration."}
				}
			}
			sum := sha256.Sum256([]byte(stackID + "\x00" + r.LogicalID))
			return IAMRole{RoleName: name, Arn: arn, RoleID: "AROA" + strings.ToUpper(hex.EncodeToString(sum[:8])), AssumeRolePolicyDocument: string(trust), MaxSessionDuration: duration}, nil
		}
	}
	return IAMRole{}, fmt.Errorf("missing stack role metadata")
}

// Reject duplicate keys before decoding trust. Last-key-wins is ambiguous for
// authorization documents, including nested Principal and Condition objects.
func stsUniqueJSON(data []byte) bool {
	d := json.NewDecoder(strings.NewReader(string(data)))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 32 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		return false
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

// stsSessionPolicy binds current authorization to the issued role generation.
// Each successful branch obtains generation and policy with one SQL statement,
// so a concurrent delete/recreate cannot mix old identity with replacement policy.
func (g *Gateway) stsSessionPolicy(ctx context.Context, expected IAMRole) (core.Policy, error) {
	deny := core.Policy{Version: "2012-10-17"}
	bad := fmt.Errorf("role session generation is unavailable")
	if expected.Arn != "arn:aws:iam::000000000000:role/"+expected.RoleName || !iamRoleName.MatchString(expected.RoleName) || expected.RoleID == "" {
		return deny, bad
	}
	if g.Roles != nil {
		var id, name string
		var deleted int
		var document sql.NullString
		err := g.Roles.db.QueryRowContext(ctx, `SELECT r.role_id,r.name,r.deleted,p.document FROM aws_iam_roles r LEFT JOIN identity_policies p ON p.name=r.name WHERE r.name=?`, expected.RoleName).Scan(&id, &name, &deleted, &document)
		if err == nil {
			if id != expected.RoleID || name != expected.RoleName || deleted != 0 {
				return deny, bad
			}
			if !document.Valid {
				return deny, nil
			}
			return core.ParsePolicy([]byte(document.String))
		}
		if err != sql.ErrNoRows {
			return deny, err
		}
	}
	if g.Stacks == nil {
		return deny, bad
	}
	var raw, stackID, policy string
	err := g.Stacks.db.QueryRowContext(ctx, `SELECT s.document,s.id,p.document FROM aws_cfn_stacks s JOIN aws_cfn_ownership o ON o.stack_id=s.id JOIN identity_policies p ON p.name=o.physical_id WHERE o.kind='AWS::IAM::Role' AND o.physical_id=?`, expected.RoleName).Scan(&raw, &stackID, &policy)
	if err != nil {
		return deny, err
	}
	var record StackRecord
	if err = json.Unmarshal([]byte(raw), &record); err != nil {
		return deny, err
	}
	if record.ID != stackID {
		return deny, bad
	}
	for _, resource := range record.Resources {
		if resource.Type == "AWS::IAM::Role" && resource.PhysicalID == expected.RoleName {
			sum := sha256.Sum256([]byte(stackID + "\x00" + resource.LogicalID))
			id := "AROA" + strings.ToUpper(hex.EncodeToString(sum[:8]))
			if id != expected.RoleID {
				return deny, bad
			}
			return core.ParsePolicy([]byte(policy))
		}
	}
	return deny, bad
}
