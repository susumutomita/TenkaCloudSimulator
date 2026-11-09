package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Profile composition and new zero-inline service-trust roles enter native IAM.
// A nil marker preserves existing one-inline CFN identities and their cleanup.
// Saved generations are never inferred from a currently reusable physical name.
type stackNativeIAM struct {
	Generation   string
	RoleName     string `json:",omitempty"`
	RoleID       string `json:",omitempty"`
	MetadataHash string `json:",omitempty"`
	StateHash    string `json:",omitempty"`
}

func iamStackNameAvailable(ctx context.Context, tx *sql.Tx, kind, name, stackID string) error {
	var tables, claims int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_cfn_ownership'").Scan(&tables); err != nil {
		return err
	}
	if tables == 0 {
		return nil
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_cfn_ownership WHERE kind=? AND lower(physical_id)=lower(?) AND stack_id<>?", kind, name, stackID).Scan(&claims); err != nil {
		return err
	}
	if claims != 0 {
		return ssmFailure{"EntityAlreadyExists", "The IAM name is reserved by a stack, including drifted resources."}
	}
	return nil
}

func compileStackNativeIAM(stackName, stackID, logical, kind, capability string, p map[string]any) (string, *stackNativeIAM, error) {
	fail := func(message string) (string, *stackNativeIAM, error) { return "", nil, fmt.Errorf("%s", message) }
	if capability != "CAPABILITY_IAM" && capability != "CAPABILITY_NAMED_IAM" {
		return fail("Acknowledge CAPABILITY_IAM for native IAM resources.")
	}
	allowed := map[string]bool{"RoleName": true, "AssumeRolePolicyDocument": true, "Policies": true, "MaxSessionDuration": true, "Description": true, "Path": true, "ManagedPolicyArns": true, "Tags": true}
	nameKey, max := "RoleName", 64
	profile := kind == "AWS::IAM::InstanceProfile"
	if profile {
		allowed = map[string]bool{"InstanceProfileName": true, "Path": true, "Roles": true}
		nameKey, max = "InstanceProfileName", 128
	}
	for key := range p {
		if !allowed[key] {
			return fail("Unsupported native IAM property: " + key)
		}
	}
	if raw, ok := p["Path"]; ok && raw != "/" {
		return fail("Only root-path native IAM resources are supported.")
	}
	name := ""
	if raw, ok := p[nameKey]; ok {
		var valid bool
		name, valid = raw.(string)
		if !valid || name == "" {
			return fail(nameKey + " must be a nonempty string.")
		}
		if capability != "CAPABILITY_NAMED_IAM" {
			return fail("Explicit IAM names require CAPABILITY_NAMED_IAM.")
		}
	} else {
		base := stackName + "-" + logical
		if len(base) > max-17 {
			base = base[:max-17]
		}
		sum := sha256.Sum256([]byte(stackID + "\x00" + logical))
		name = base + "-" + hex.EncodeToString(sum[:8])
	}
	state := &stackNativeIAM{}
	if profile {
		if !iamInlineName.MatchString(name) {
			return fail("Invalid native instance profile name.")
		}
		roles, ok := p["Roles"].([]any)
		if !ok || len(roles) > 1 {
			return fail("Roles must be an array with zero or one native role name.")
		}
		if len(roles) == 1 {
			state.RoleName = stringValue(roles[0])
			if !iamRoleName.MatchString(state.RoleName) {
				return fail("Use a native role name in Roles.")
			}
		}
		return name, state, nil
	}
	if !iamRoleName.MatchString(name) || strings.EqualFold(name, core.Administrator) {
		return fail("Invalid or reserved native role name.")
	}
	for _, key := range []string{"Tags"} {
		if raw, exists := p[key]; exists {
			list, ok := raw.([]any)
			if !ok || len(list) != 0 {
				return fail("Native CFN roles do not support " + key + ".")
			}
		}
	}
	if raw, exists := p["ManagedPolicyArns"]; exists {
		list, ok := raw.([]any)
		if !ok || len(list) > 1 {
			return fail("Native CFN roles support only one pinned AmazonSSMManagedInstanceCore attachment.")
		}
		if len(list) == 1 && list[0] != iamSSMCoreARN {
			return fail("Unsupported native managed policy ARN.")
		}
	}
	trust, err := json.Marshal(p["AssumeRolePolicyDocument"])
	if err != nil {
		return fail("Invalid trust policy.")
	}
	if _, err = iamTrust(string(trust)); err != nil {
		return "", nil, err
	}
	var document struct{ Statement iamTrustStatements }
	if err = json.Unmarshal(trust, &document); err != nil {
		return "", nil, err
	}
	rawStatements := object(p["AssumeRolePolicyDocument"])["Statement"]
	rawList, ok := rawStatements.([]any)
	if !ok {
		rawList = []any{rawStatements}
	}
	for _, raw := range rawList {
		fields := object(raw)
		_, condition := fields["Condition"]
		_, aws := object(fields["Principal"])["AWS"]
		if condition || aws {
			return fail("Native CFN roles support Service trust without AWS principals or Conditions.")
		}
	}
	for _, statement := range document.Statement {
		if len(statement.Principal.Service) == 0 || len(statement.Principal.AWS) != 0 || len(statement.Condition) != 0 {
			return fail("Native CFN roles support explicit service trust without Conditions only.")
		}
	}
	if raw, ok := p["Description"]; ok {
		if _, ok = raw.(string); !ok {
			return fail("Description must be a string.")
		}
	}
	duration := 3600
	if raw, ok := p["MaxSessionDuration"]; ok {
		var e error
		duration, e = strconv.Atoi(stringValue(raw))
		if e != nil || duration < 3600 || duration > 43200 {
			return fail("MaxSessionDuration must be 3600–43200.")
		}
	}
	// Reuse the native description range without performing a write.
	if err = iamRoleDescription(stringValue(p["Description"])); err != nil {
		return "", nil, err
	}
	if raw, exists := p["Policies"]; exists {
		policies, ok := raw.([]any)
		if !ok || len(policies) > 10 {
			return fail("Native roles support zero to ten inline policies.")
		}
		names := map[string]bool{}
		for _, raw := range policies {
			policy := object(raw)
			if len(policy) != 2 || !iamInlineName.MatchString(stringValue(policy["PolicyName"])) || names[stringValue(policy["PolicyName"])] {
				return fail("Use distinct named inline policies with only PolicyName and PolicyDocument.")
			}
			names[stringValue(policy["PolicyName"])] = true
			data, err := json.Marshal(policy["PolicyDocument"])
			if err != nil {
				return "", nil, err
			}
			if len(data) > 32<<10 {
				return fail("Inline policy exceeds 32 KiB.")
			}
			if _, err = core.ParsePolicy(data); err != nil {
				return "", nil, err
			}
		}
	}
	return name, state, nil
}

// Called under Create's write transaction, before authorization and writes.
// Existing role names are canonicalized and their incarnation is captured;
// newly compiled roles are matched exactly to the same stack's declaration.
func admitStackIAM(tx *sql.Tx, record *StackRecord) error {
	declared := map[string]StackResource{}
	for _, r := range record.Resources {
		if r.Type == "AWS::IAM::Role" {
			declared[strings.ToLower(r.PhysicalID)] = r
		}
	}
	for i := range record.Resources {
		r := &record.Resources[i]
		if r.Type != "AWS::IAM::InstanceProfile" || r.NativeIAM == nil || r.NativeIAM.RoleName == "" {
			continue
		}
		state := r.NativeIAM
		if own, ok := declared[strings.ToLower(state.RoleName)]; ok {
			if own.NativeIAM == nil || own.PhysicalID != state.RoleName {
				return ssmFailure{"ValidationError", "Instance profiles require an exact native role reference."}
			}
			continue
		}
		role, err := iamProfileRole(context.Background(), tx, state.RoleName)
		if err != nil {
			return err
		}
		state.RoleName, state.RoleID = role.RoleName, role.RoleID
	}
	return nil
}

func createStackIAM(tx *sql.Tx, record StackRecord, r *StackResource) error {
	ctx := context.Background()
	if r.NativeIAM == nil {
		return fmt.Errorf("native IAM marker missing")
	}
	if err := iamStackNameAvailable(ctx, tx, r.Type, r.PhysicalID, record.ID); err != nil {
		return err
	}
	if err := iamExactlyOne(tx.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID)); err != nil {
		return err
	}
	if r.Type == "AWS::IAM::Role" {
		trust, err := json.Marshal(r.Properties["AssumeRolePolicyDocument"])
		if err != nil {
			return err
		}
		duration := 3600
		if raw, ok := r.Properties["MaxSessionDuration"]; ok {
			duration, err = strconv.Atoi(stringValue(raw))
			if err != nil {
				return err
			}
		}
		role, err := iamCreateRoleTx(ctx, tx, r.PhysicalID, string(trust), stringValue(r.Properties["Description"]), duration, record.ID)
		if err != nil {
			return err
		}
		r.NativeIAM.Generation = role.RoleID
		policies, _ := r.Properties["Policies"].([]any)
		for _, raw := range policies {
			p := object(raw)
			data, err := json.Marshal(p["PolicyDocument"])
			if err != nil {
				return err
			}
			if err = iamPutRolePolicyTx(ctx, tx, role.RoleName, stringValue(p["PolicyName"]), string(data), role.RoleID); err != nil {
				return err
			}
		}
		managed, _ := r.Properties["ManagedPolicyArns"].([]any)
		for _, raw := range managed {
			if err = iamManagedAttachmentTx(ctx, tx, role.RoleName, stringValue(raw), true, []string{role.RoleID}); err != nil {
				return err
			}
		}
		if err = stackIAMRequestedManaged(ctx, tx, role, *r, false); err != nil {
			return err
		}
		// Capture only the requested role state, not a trigger-modified state
		// that happens to remain valid native IAM data.
		after, err := iamReadRole(ctx, tx, role.RoleName, false)
		if err != nil {
			return err
		}
		if after != role {
			return iamManagedBad()
		}
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM aws_iam_role_policies WHERE role_name=?", role.RoleName).Scan(&count); err != nil {
			return err
		}
		if count != len(policies) {
			return iamManagedBad()
		}
		for _, raw := range policies {
			policy := object(raw)
			expected, err := json.Marshal(policy["PolicyDocument"])
			if err != nil {
				return err
			}
			var saved string
			if err = tx.QueryRow("SELECT document FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, stringValue(policy["PolicyName"])).Scan(&saved); err != nil {
				return err
			}
			if saved != string(expected) {
				return iamManagedBad()
			}
		}
	} else {
		p, err := iamCreateInstanceProfileTx(ctx, tx, r.PhysicalID, record.ID)
		if err != nil {
			return err
		}
		r.NativeIAM.Generation = p.InstanceProfileID
		if r.NativeIAM.RoleName != "" {
			role, err := iamProfileRole(ctx, tx, r.NativeIAM.RoleName)
			if err != nil {
				return err
			}
			if role.RoleName != r.NativeIAM.RoleName || r.NativeIAM.RoleID != "" && role.RoleID != r.NativeIAM.RoleID {
				return iamProfileBad()
			}
			r.NativeIAM.RoleID = role.RoleID
			if err = iamChangeInstanceProfileRoleTx(ctx, tx, p.InstanceProfileName, p.InstanceProfileID, role.RoleName, role.RoleID, true); err != nil {
				return err
			}
		}
	}
	meta, state, _, err := stackIAMFingerprint(tx, *r)
	if err != nil {
		return err
	}
	r.NativeIAM.MetadataHash, r.NativeIAM.StateHash = meta, state
	return nil
}

func iamCFNHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func stackIAMFingerprint(tx *sql.Tx, r StackResource) (string, string, bool, error) {
	if r.NativeIAM == nil || r.Type != "AWS::IAM::Role" && r.Type != "AWS::IAM::InstanceProfile" {
		return "", "", false, fmt.Errorf("invalid native IAM resource marker")
	}
	ctx := context.Background()
	var deleted int
	var metadata any
	var rowsQuery string
	if r.Type == "AWS::IAM::Role" {
		role, err := iamReadRole(ctx, tx, r.PhysicalID, true)
		if err != nil {
			return "", "", false, err
		}
		if role.RoleName != r.PhysicalID || role.RoleID != r.NativeIAM.Generation {
			return "", "", false, iamManagedBad()
		}
		metadata = role
		rowsQuery = "SELECT name,document FROM aws_iam_role_policies WHERE role_name=? ORDER BY name"
		if err = tx.QueryRow("SELECT deleted FROM aws_iam_roles WHERE name=?", r.PhysicalID).Scan(&deleted); err != nil {
			return "", "", false, err
		}
	} else {
		p, err := iamProfileRead(ctx, tx, r.PhysicalID, true)
		if err != nil {
			return "", "", false, err
		}
		if p.InstanceProfileName != r.PhysicalID || p.InstanceProfileID != r.NativeIAM.Generation {
			return "", "", false, iamProfileBad()
		}
		// The profile owns its membership, not the referenced role's policies/trust.
		metadata = []any{p.InstanceProfileName, p.InstanceProfileID, p.CreateDate}
		rowsQuery = "SELECT role_name,role_id FROM aws_iam_instance_profile_roles WHERE profile_name=? ORDER BY role_name"
		if err = tx.QueryRow("SELECT deleted FROM aws_iam_instance_profiles WHERE name=?", r.PhysicalID).Scan(&deleted); err != nil {
			return "", "", false, err
		}
	}
	if deleted != 0 && deleted != 1 {
		return "", "", false, iamProfileBad()
	}
	parts := []any{metadata}
	rows, err := tx.Query(rowsQuery, r.PhysicalID)
	if err != nil {
		return "", "", false, err
	}
	for rows.Next() {
		var a, b string
		if err = rows.Scan(&a, &b); err != nil {
			rows.Close()
			return "", "", false, err
		}
		parts = append(parts, []string{a, b})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", "", false, err
	}
	if r.Type == "AWS::IAM::Role" {
		role, err := iamReadRole(ctx, tx, r.PhysicalID, true)
		if err != nil {
			return "", "", false, err
		}
		if err = stackIAMRequestedManaged(ctx, tx, role, r, deleted == 1); err != nil {
			return "", "", false, err
		}
		managed, err := iamManagedRolePolicies(ctx, tx, role)
		if err != nil {
			return "", "", false, err
		}
		// Preserve old zero-managed fingerprints across upgrade.
		for _, p := range managed {
			parts = append(parts, []string{p.Arn, p.PolicyID, p.DefaultVersionID, p.versions[0].Document})
		}
		var aggregate string
		err = tx.QueryRow("SELECT document FROM identity_policies WHERE name=?", r.PhysicalID).Scan(&aggregate)
		if err != nil && err != sql.ErrNoRows {
			return "", "", false, err
		}
		parts = append(parts, aggregate)
	}
	return iamCFNHash(metadata), iamCFNHash(parts), deleted == 1, nil
}
func checkStackIAM(tx *sql.Tx, record StackRecord, phase string) error {
	for _, r := range record.Resources {
		if r.NativeIAM == nil {
			continue
		}
		meta, state, deleted, err := stackIAMFingerprint(tx, r)
		if err != nil {
			return err
		}
		if r.NativeIAM.Generation == "" || r.NativeIAM.MetadataHash == "" || meta != r.NativeIAM.MetadataHash || !deleted && state != r.NativeIAM.StateHash {
			return ssmFailure{"ValidationError", "Native IAM stack resource drifted; cleanup refused."}
		}
		if phase == "created" && deleted {
			return fmt.Errorf("native IAM creation was undone")
		}
		if phase == "deleted" || deleted {
			var children int
			query := "SELECT count(*) FROM aws_iam_instance_profile_roles WHERE profile_name=?"
			args := []any{r.PhysicalID}
			if r.Type == "AWS::IAM::Role" {
				query = "SELECT (SELECT count(*) FROM aws_iam_role_policies WHERE role_name=?)+(SELECT count(*) FROM aws_iam_role_managed_policies WHERE role_name=?)+(SELECT count(*) FROM identity_policies WHERE lower(name)=lower(?))+(SELECT count(*) FROM aws_iam_instance_profile_roles WHERE role_name=?)"
				args = []any{r.PhysicalID, r.PhysicalID, r.PhysicalID, r.PhysicalID}
			}
			if err = tx.QueryRow(query, args...).Scan(&children); err != nil {
				return err
			}
			if !deleted || children != 0 {
				return fmt.Errorf("native IAM cleanup postcondition failed")
			}
		}
	}
	return nil
}
func deleteStackIAM(tx *sql.Tx, r StackResource) error {
	ctx := context.Background()
	_, _, retired, err := stackIAMFingerprint(tx, r)
	if err != nil {
		return err
	}
	if retired {
		return nil
	}
	if r.Type == "AWS::IAM::InstanceProfile" {
		if r.NativeIAM.RoleName != "" {
			if err = iamChangeInstanceProfileRoleTx(ctx, tx, r.PhysicalID, r.NativeIAM.Generation, r.NativeIAM.RoleName, r.NativeIAM.RoleID, false); err != nil {
				return err
			}
		}
		return iamDeleteInstanceProfileTx(ctx, tx, r.PhysicalID, r.NativeIAM.Generation)
	}
	policies, _ := r.Properties["Policies"].([]any)
	for _, raw := range policies {
		if err = iamDeleteRolePolicyTx(ctx, tx, r.PhysicalID, stringValue(object(raw)["PolicyName"]), r.NativeIAM.Generation); err != nil {
			return err
		}
	}
	managed, _ := r.Properties["ManagedPolicyArns"].([]any)
	for _, raw := range managed {
		if err = iamManagedAttachmentTx(ctx, tx, r.PhysicalID, stringValue(raw), false, []string{r.NativeIAM.Generation}); err != nil {
			return err
		}
	}
	return iamDeleteRoleTx(ctx, tx, r.PhysicalID, r.NativeIAM.Generation)
}

func stackHasTrust(p map[string]any) bool { _, exists := p["AssumeRolePolicyDocument"]; return exists }

// The serialized generation fence and ownership must survive all subsequent
// writes, including event/history triggers, before CreateStack can acknowledge.
func checkStackIAMDocument(tx *sql.Tx, record StackRecord) error {
	native := false
	for _, r := range record.Resources {
		native = native || r.NativeIAM != nil
	}
	if !native {
		return nil
	}
	expected, err := json.Marshal(record)
	if err != nil {
		return err
	}
	var saved string
	if err = tx.QueryRow("SELECT document FROM aws_cfn_stacks WHERE id=?", record.ID).Scan(&saved); err != nil {
		return err
	}
	if saved != string(expected) {
		return fmt.Errorf("native IAM stack document acknowledgement failed")
	}
	for _, r := range record.Resources {
		if r.NativeIAM == nil {
			continue
		}
		var claims int
		if err = tx.QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=? AND stack_id=?", stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID).Scan(&claims); err != nil {
			return err
		}
		if claims != 1 {
			return fmt.Errorf("native IAM stack ownership acknowledgement failed")
		}
	}
	return nil
}

func compileStackLegacyIAMRole(name, id, key string, properties map[string]any, capabilities string) (string, error) {
	fail := func(message string) (string, error) { return "", fmt.Errorf("%s", message) }

	if capabilities != "CAPABILITY_IAM" && capabilities != "CAPABILITY_NAMED_IAM" {
		return fail("Acknowledge CAPABILITY_IAM for identity-policy resources.")
	}
	for k := range properties {
		switch k {
		case "RoleName", "AssumeRolePolicyDocument", "Policies", "Tags", "MaxSessionDuration", "ManagedPolicyArns":
		default:
			return fail("Unsupported role property: " + k)
		}
	}
	// Sign-in/trust metadata is retained for catalog fidelity, never evaluated as authorization.
	if managed, exists := properties["ManagedPolicyArns"]; exists {
		list, ok := managed.([]any)
		if !ok {
			return fail("ManagedPolicyArns must be an array.")
		}
		for _, arn := range list {
			if stringValue(arn) != "arn:aws:iam::aws:policy/SignInLocalDevelopmentAccess" && stringValue(arn) != "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole" {
				return fail("Managed policy permissions are unsupported.")
			}
		}
	}
	physical := stringValue(properties["RoleName"])
	if physical == "" {
		base := name + "-" + key
		if len(base) > 47 {
			base = base[:47]
		}
		// IAM is global. Include this unique stack ID (and logical ID),
		// not just a region-scoped stack name, in generated role names.
		sum := sha256.Sum256([]byte(id + "\x00" + key))
		physical = base + "-" + hex.EncodeToString(sum[:8])
	} else if capabilities != "CAPABILITY_NAMED_IAM" {
		return fail("Explicit RoleName requires CAPABILITY_NAMED_IAM.")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(physical) || physical == core.Administrator {
		return fail("Role name must be 1–64 non-reserved local identity characters.")
	}
	policies, ok := properties["Policies"].([]any)
	if !ok || len(policies) != 1 {
		return fail("Exactly one inline identity policy is supported per role.")
	}
	encoded, e := json.Marshal(object(policies[0])["PolicyDocument"])
	if e != nil {
		return fail("Invalid identity policy.")
	}
	if _, e = core.ParsePolicy(encoded); e != nil {
		return fail(e.Error())
	}
	properties["SimulatorPolicy"] = string(encoded)
	return physical, nil
}

func stackIAMRolePhysicalName(name, id, key string, p map[string]any) string {
	physical := stringValue(p["RoleName"])
	if physical != "" {
		return physical
	}
	base := name + "-" + key
	if len(base) > 47 {
		base = base[:47]
	}
	sum := sha256.Sum256([]byte(id + "\x00" + key))
	return base + "-" + hex.EncodeToString(sum[:8])
}

// Select native ownership only for a new zero-inline role form or an active
// in-template profile composition. Existing one-inline metadata-only roles keep
// their contract, including AWS account trust and the two legacy metadata ARNs.
func compileStackIAMRoles(record *StackRecord, declared []StackResource, capability string) error {
	targets := map[string][]string{}
	active := map[string]bool{}
	for _, r := range record.Resources {
		active[r.LogicalID] = true
		if r.Type == "AWS::IAM::InstanceProfile" && r.NativeIAM.RoleName != "" {
			targets[strings.ToLower(r.NativeIAM.RoleName)] = append(targets[strings.ToLower(r.NativeIAM.RoleName)], r.NativeIAM.RoleName)
		}
	}
	for _, r := range declared {
		target, required := targets[strings.ToLower(r.PhysicalID)]
		required = required && active[r.LogicalID]
		if required {
			for _, name := range target {
				if name != r.PhysicalID {
					return fmt.Errorf("Instance profiles require exact in-template role names")
				}
			}
		}
		policies, hasPolicies := r.Properties["Policies"]
		list, listOK := policies.([]any)
		zero := stackHasTrust(r.Properties) && (!hasPolicies || listOK && len(list) == 0)
		var native *stackNativeIAM
		var err error
		if required || zero {
			_, native, err = compileStackNativeIAM(record.Name, record.ID, r.LogicalID, r.Type, capability, r.Properties)
		} else {
			_, err = compileStackLegacyIAMRole(record.Name, record.ID, r.LogicalID, r.Properties, capability)
		}
		if err != nil {
			return err
		}
		for i := range record.Resources {
			if record.Resources[i].LogicalID == r.LogicalID {
				record.Resources[i].NativeIAM = native
			}
		}
	}
	return nil
}

// Compare requested children before capture and at every acknowledgement. A valid
// but unsolicited attachment must not become owned merely by fingerprinting it.
func stackIAMRequestedManaged(ctx context.Context, tx *sql.Tx, role IAMRole, r StackResource, retired bool) error {
	all, err := iamManagedRolePolicies(ctx, tx, role)
	if err != nil {
		return err
	}
	requested, _ := r.Properties["ManagedPolicyArns"].([]any)
	// Requested builtin pins outlive the role's attachments and retirement.
	// Check them even at final cleanup ACK, after stack/event/history writes.
	for _, raw := range requested {
		arn, ok := raw.(string)
		if !ok || arn != iamSSMCoreARN {
			return iamManagedBad()
		}
		if _, err := iamManagedRead(ctx, tx, arn); err != nil {
			return err
		}
	}
	if retired {
		if len(all) != 0 {
			return iamManagedBad()
		}
		return nil
	}
	if len(all) != len(requested) {
		return ssmFailure{"DeleteConflict", "Native IAM managed attachments differ from the requested stack state."}
	}
	for i, p := range all {
		if p.Arn != iamSSMCoreARN || stringValue(requested[i]) != p.Arn {
			return iamManagedBad()
		}
	}
	return nil
}
