package aws

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"go.yaml.in/yaml/v3"
)

type StackStore struct{ db *sql.DB }
type StackResource struct {
	LogicalID, Type, PhysicalID string
	Properties                  map[string]any
	StateHash                   string            `json:",omitempty"`
	StateParts                  map[string]string `json:",omitempty"`
}
type StackRecord struct {
	ClientToken               string
	RequestHash               string
	Tags                      map[string]string
	Name, ID, Region, Created string
	Outputs                   map[string]string
	Resources                 []StackResource
	Parameters                map[string]string
}

func NewStackStore(db *sql.DB) (*StackStore, error) {
	if _, err := NewAPIGatewayStore(db); err != nil {
		return nil, err
	}
	if _, err := NewLambdaPermissionStore(db); err != nil {
		return nil, err
	}
	if _, err := NewIAMRoleStore(db); err != nil {
		return nil, err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_cfn_stacks(region TEXT NOT NULL,name TEXT NOT NULL,id TEXT NOT NULL UNIQUE,document TEXT NOT NULL,PRIMARY KEY(region,name)); CREATE TABLE IF NOT EXISTS aws_cfn_ownership(namespace TEXT NOT NULL,kind TEXT NOT NULL,physical_id TEXT NOT NULL,stack_id TEXT NOT NULL REFERENCES aws_cfn_stacks(id) ON DELETE RESTRICT,PRIMARY KEY(namespace,kind,physical_id));`)
	return &StackStore{db}, err
}
func taggedYAML(n *yaml.Node) (any, error) {
	nodes := 0
	return stackTaggedYAMLNode(n, 0, &nodes)
}
func stackTaggedYAMLNode(n *yaml.Node, depth int, nodes *int) (any, error) {
	if n == nil || depth > stackConditionalDepth || *nodes >= stackConditionalNodes {
		return nil, fmt.Errorf("template exceeds local depth/node bound")
	}
	*nodes++
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil, fmt.Errorf("invalid template document")
		}
		return stackTaggedYAMLNode(n.Content[0], depth+1, nodes)
	}
	if n.Tag == "!Equals" || n.Tag == "!If" {
		if n.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("condition shorthand requires a sequence")
		}
		values := []any{}
		for _, child := range n.Content {
			value, err := stackTaggedYAMLNode(child, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		key := "Fn::Equals"
		if n.Tag == "!If" {
			key = "Fn::If"
		}
		return map[string]any{key: values}, nil
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") && n.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("intrinsic shorthand must be scalar")
	}
	if n.Tag == "!Ref" {
		return map[string]any{"Ref": n.Value}, nil
	}
	if n.Tag == "!Sub" {
		return map[string]any{"Fn::Sub": n.Value}, nil
	}
	if n.Tag == "!GetAtt" {
		return map[string]any{"Fn::GetAtt": n.Value}, nil
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		return nil, fmt.Errorf("unsupported intrinsic tag %s", n.Tag)
	}
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if _, ok := m[k]; ok {
				return nil, fmt.Errorf("duplicate template key %s", k)
			}
			v, e := stackTaggedYAMLNode(n.Content[i+1], depth+1, nodes)
			if e != nil {
				return nil, e
			}
			m[k] = v
		}
		return m, nil
	case yaml.SequenceNode:
		a := []any{}
		for _, c := range n.Content {
			v, e := stackTaggedYAMLNode(c, depth+1, nodes)
			if e != nil {
				return nil, e
			}
			a = append(a, v)
		}
		return a, nil
	case yaml.ScalarNode:
		if n.Tag == "!!bool" {
			return n.Value == "true", nil
		}
		if n.Tag == "!!null" {
			return nil, nil
		}
		return n.Value, nil
	}
	return nil, fmt.Errorf("unsupported YAML node")
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func stringValue(v any) string    { s, _ := v.(string); return s }

var subExpression = regexp.MustCompile(`\$\{([^}]+)\}`)

func resolve(v any, refs map[string]string, attributes map[string]string) (any, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return x, nil
	case nil:
		return nil, nil
	case []any:
		r := []any{}
		for _, c := range x {
			v, e := resolve(c, refs, attributes)
			if e != nil {
				return nil, e
			}
			r = append(r, v)
		}
		return r, nil
	case map[string]any:
		if raw, ok := x["Ref"]; ok {
			key := stringValue(raw)
			v, found := refs[key]
			if !found || len(x) != 1 {
				return nil, fmt.Errorf("unknown Ref %s", key)
			}
			return v, nil
		}
		if raw, ok := x["Fn::GetAtt"]; ok {
			key := stringValue(raw)
			if a, ok := raw.([]any); ok && len(a) == 2 {
				key = stringValue(a[0]) + "." + stringValue(a[1])
			}
			v, found := attributes[key]
			if !found || len(x) != 1 {
				return nil, fmt.Errorf("unknown GetAtt %s", key)
			}
			return v, nil
		}
		if raw, ok := x["Fn::Sub"]; ok {
			source, valid := raw.(string)
			if !valid || len(x) != 1 {
				return nil, fmt.Errorf("only scalar Fn::Sub is supported")
			}
			var failed string
			out := subExpression.ReplaceAllStringFunc(source, func(match string) string {
				key := match[2 : len(match)-1]
				if value, ok := refs[key]; ok {
					return value
				}
				if value, ok := attributes[key]; ok {
					return value
				}
				failed = key
				return ""
			})
			if failed != "" {
				return nil, fmt.Errorf("unknown substitution %s", failed)
			}
			return out, nil
		}
		r := map[string]any{}
		for k, c := range x {
			if strings.HasPrefix(k, "Fn::") {
				return nil, fmt.Errorf("unsupported intrinsic %s", k)
			}
			value, e := resolve(c, refs, attributes)
			if e != nil {
				return nil, e
			}
			r[k] = value
		}
		return r, nil
	}
	return nil, fmt.Errorf("unsupported template value")
}

// resourceOrder rejects cycles and resolves forward Ref/GetAtt/Sub dependencies
// before evaluating properties. Logical-ID spelling must not change behavior.
func resourceOrder(resources map[string]any) ([]string, error) {
	keys := []string{}
	for key := range resources {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := map[string]int{}
	ordered := []string{}
	var visit func(string) error
	visit = func(key string) error {
		if state[key] == 2 {
			return nil
		}
		if state[key] == 1 {
			return fmt.Errorf("cyclic resource dependency: %s", key)
		}
		state[key] = 1
		dependencies := map[string]bool{}
		add := func(reference string) {
			name, _, _ := strings.Cut(reference, ".")
			if _, found := resources[name]; found {
				dependencies[name] = true
			}
		}
		var walk func(any)
		walk = func(value any) {
			switch v := value.(type) {
			case []any:
				for _, item := range v {
					walk(item)
				}
			case map[string]any:
				for field, item := range v {
					switch field {
					case "Ref":
						add(stringValue(item))
					case "Fn::GetAtt":
						if array, ok := item.([]any); ok && len(array) == 2 {
							add(stringValue(array[0]))
						} else {
							add(stringValue(item))
						}
					case "Fn::Sub":
						for _, match := range subExpression.FindAllStringSubmatch(stringValue(item), -1) {
							add(match[1])
						}
					default:
						walk(item)
					}
				}
			}
		}
		raw := object(resources[key])
		if explicit, exists := raw["DependsOn"]; exists {
			var names []any
			switch v := explicit.(type) {
			case string:
				names = []any{v}
			case []any:
				names = v
			default:
				return fmt.Errorf("DependsOn must be a logical ID or array of logical IDs")
			}
			if len(names) == 0 {
				return fmt.Errorf("DependsOn must not be empty")
			}
			for _, value := range names {
				name, valid := value.(string)
				if _, found := resources[name]; !valid || !found {
					return fmt.Errorf("unknown DependsOn logical ID")
				}
				dependencies[name] = true
			}
		}
		walk(raw["Properties"])
		names := []string{}
		for dependency := range dependencies {
			names = append(names, dependency)
		}
		sort.Strings(names)
		for _, dependency := range names {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[key] = 2
		ordered = append(ordered, key)
		return nil
	}
	for _, key := range keys {
		if err := visit(key); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

func compileStack(region, name, body string, parameters map[string]string, capabilities string) (StackRecord, error) {
	fail := func(message string) (StackRecord, error) {
		return StackRecord{}, ssmFailure{"ValidationError", message}
	}
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,127}$`).MatchString(name) || len(body) == 0 || len(body) > 51200 {
		return fail("Use a valid stack name and TemplateBody of 1–51200 bytes.")
	}
	var node yaml.Node
	if yaml.Unmarshal([]byte(body), &node) != nil || len(node.Content) == 0 {
		return fail("Invalid YAML/JSON template.")
	}
	value, err := taggedYAML(&node)
	if err != nil {
		return fail(err.Error())
	}
	template := object(value)
	for k := range template {
		switch k {
		case "AWSTemplateFormatVersion", "Description", "Parameters", "Conditions", "Resources", "Outputs":
		default:
			return fail("Unsupported template section: " + k)
		}
	}
	resources := object(template["Resources"])
	if len(resources) == 0 || len(resources) > 100 {
		return fail("Require 1–100 supported resources.")
	}
	for _, value := range resources {
		r := object(value)
		kind := stringValue(r["Type"])
		if kind != "AWS::SSM::Parameter" && kind != "AWS::IAM::Role" && kind != "AWS::S3::Bucket" && kind != "AWS::Lambda::Function" && !stackAPIKind(kind) {
			return fail("Unsupported resource type: " + kind)
		}
		for k := range r {
			if k != "Type" && k != "Properties" && k != "DependsOn" && k != "Condition" {
				return fail("Unsupported resource attribute: " + k)
			}
		}
	}
	bytes := make([]byte, 16)
	if _, err = rand.Read(bytes); err != nil {
		return StackRecord{}, err
	}
	id := fmt.Sprintf("arn:aws:cloudformation:%s:000000000000:stack/%s/%s", region, name, hex.EncodeToString(bytes))
	record := StackRecord{Name: name, ID: id, Region: region, Created: time.Now().UTC().Format(time.RFC3339), Outputs: map[string]string{}, Parameters: map[string]string{}}
	refs := map[string]string{"AWS::Region": region, "AWS::AccountId": "000000000000", "AWS::StackName": name, "AWS::StackId": id, "AWS::Partition": "aws", "AWS::URLSuffix": "amazonaws.com"}
	attrs := map[string]string{}
	definitions := object(template["Parameters"])
	if _, exists := template["Parameters"]; exists && definitions == nil {
		return fail("Parameters must be an object.")
	}
	values, e := stackParameterValues(definitions, parameters)
	if e != nil {
		return fail(e.Error())
	}
	for key, value := range values {
		refs[key] = value
		record.Parameters[key] = value
	}
	conditionDefinitions := object(template["Conditions"])
	if _, exists := template["Conditions"]; exists && conditionDefinitions == nil {
		return fail("Conditions must be an object.")
	}
	conditions, e := stackConditionValues(conditionDefinitions, refs)
	if e != nil {
		return fail(e.Error())
	}
	enabled := map[string]bool{}
	for key, raw := range resources {
		if !stackConditionalName.MatchString(key) {
			return fail("Invalid resource logical ID.")
		}
		if _, exists := refs[key]; exists {
			return fail("Resource logical ID conflicts with parameter.")
		}
		selected, err := stackConditionalEnabled(object(raw), conditions)
		if err != nil {
			return fail(err.Error())
		}
		enabled[key] = selected
	}
	// Compile every declaration purely before filtering the native record. This
	// prevents conditions from hiding unsupported resource types/properties.
	prepared := map[string]any{}
	for key, raw := range resources {
		r := object(raw)
		selected, omit, err := stackSelectConditional(r["Properties"], conditions)
		if err != nil || omit {
			return fail("Invalid conditional resource properties.")
		}
		if err = stackConditionalLinks(r["Properties"], resources, refs, enabled, false); err != nil {
			return fail(err.Error())
		}
		if err = stackConditionalLinks(selected, resources, refs, enabled, enabled[key]); err != nil {
			return fail(err.Error())
		}
		if err = stackConditionalDependency(r["DependsOn"], resources, enabled, enabled[key]); err != nil {
			return fail(err.Error())
		}
		copy := map[string]any{}
		for field, value := range r {
			if field != "Condition" {
				copy[field] = value
			}
		}
		copy["Properties"] = selected
		prepared[key] = copy
	}
	resources = prepared
	keys, err := resourceOrder(resources)
	if err != nil {
		return fail(err.Error())
	}
	for _, key := range keys {
		raw := object(resources[key])
		kind := stringValue(raw["Type"])

		resolved, e := resolve(raw["Properties"], refs, attrs)
		if e != nil {
			return fail(e.Error())
		}
		properties := object(resolved)
		if resolved != nil && properties == nil {
			return fail("Resource Properties must be an object.")
		}
		physical := ""
		if stackAPIKind(kind) {
			physical, e = compileStackAPI(region, id, key, kind, properties, attrs)
			if e != nil {
				return fail(e.Error())
			}
		} else if kind == "AWS::SSM::Parameter" {
			for k := range properties {
				switch k {
				case "Name", "Value", "Type", "Description", "Tier":
				default:
					return fail("Unsupported parameter property: " + k)
				}
			}
			physical = stringValue(properties["Name"])
			parameterType := stringValue(properties["Type"])
			value := stringValue(properties["Value"])
			if !validParameterName(physical) || parameterType != "String" && parameterType != "StringList" || len(value) == 0 || len(value) > 4096 {
				return fail("Invalid or unsupported SSM parameter properties.")
			}
			if tier := stringValue(properties["Tier"]); tier != "" && tier != "Standard" {
				return fail("Only Standard parameters are supported.")
			}
			if parameterType == "StringList" {
				for _, item := range strings.Split(value, ",") {
					if strings.TrimSpace(item) == "" {
						return fail("StringList elements cannot be empty.")
					}
				}
			}
			if len(stringValue(properties["Description"])) > 1024 {
				return fail("Parameter description exceeds 1024 bytes.")
			}
			attrs[key+".Value"] = value
		} else if kind == "AWS::Lambda::Function" {
			f, e := compileStackFunction(region, name, id, key, properties)
			if e != nil {
				return StackRecord{}, e
			}
			physical = f.Name
			attrs[key+".Arn"] = lambdaARN(region, physical)
		} else if kind == "AWS::S3::Bucket" {
			physical, e = compileS3Bucket(name, id, key, properties)
			if e != nil {
				return StackRecord{}, e
			}
			attrs[key+".Arn"] = "arn:aws:s3:::" + physical
		} else {
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
			physical = stringValue(properties["RoleName"])
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
			attrs[key+".Arn"] = "arn:aws:iam::000000000000:role/" + physical
		}
		refs[key] = physical
		if kind == "AWS::ApiGateway::Stage" {
			refs[key] = stringValue(properties["StageName"])
		}
		if enabled[key] {
			record.Resources = append(record.Resources, StackResource{LogicalID: key, Type: kind, PhysicalID: physical, Properties: properties})
		}
	}
	outputs := object(template["Outputs"])
	if _, exists := template["Outputs"]; exists && outputs == nil {
		return fail("Outputs must be an object.")
	}
	for key, raw := range outputs {
		output, ok := raw.(map[string]any)
		if !ok {
			return fail("Output must be an object.")
		}
		if _, exists := output["Value"]; !exists {
			return fail("Output requires Value.")
		}
		if raw, exists := output["Description"]; exists {
			if _, ok := raw.(string); !ok {
				return fail("Output Description must be a string.")
			}
		}
		for field := range output {
			if field != "Value" && field != "Description" && field != "Condition" {
				return fail("Unsupported output attribute: " + field)
			}
		}
		selected, e := stackConditionalEnabled(output, conditions)
		if e != nil {
			return fail(e.Error())
		}
		projected, omit, e := stackSelectConditional(output["Value"], conditions)
		if e != nil || omit {
			return fail("Invalid conditional output value.")
		}
		if e = stackConditionalLinks(output["Value"], resources, refs, enabled, false); e != nil {
			return fail(e.Error())
		}
		if !selected {
			continue
		}
		if e = stackConditionalLinks(projected, resources, refs, enabled, true); e != nil {
			return fail(e.Error())
		}
		v, e := resolve(projected, refs, attrs)
		if e != nil {
			return fail(e.Error())
		}
		s, ok := v.(string)
		if !ok {
			return fail("Outputs must resolve to strings.")
		}
		record.Outputs[key] = s
	}
	return record, nil
}
func (s *StackStore) Create(record StackRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_cfn_stacks").Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return ssmFailure{"LimitExceededException", "Simulator stack limit reached."}
	}
	document, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO aws_cfn_stacks VALUES(?,?,?,?)", record.Region, record.Name, record.ID, string(document)); err != nil {
		return ssmFailure{"AlreadyExistsException", "Stack already exists."}
	}
	for _, r := range record.Resources {
		if stackAPIKind(r.Type) {
			if err = claimStackAPI(tx, record, r); err != nil {
				return err
			}
			if err = createStackAPI(context.Background(), tx, record, r); err != nil {
				return err
			}
			continue
		}
		table, limit := "aws_ssm_parameters", 1000
		if r.Type == "AWS::S3::Bucket" {
			table, limit = "buckets", core.MaxBuckets
		}
		if r.Type == "AWS::Lambda::Function" {
			table, limit = "aws_lambda_functions", 100
		}
		if r.Type == "AWS::IAM::Role" {
			table, limit = "identity_policies", 1000
			var reserved int
			if err = tx.QueryRow("SELECT (SELECT count(*) FROM aws_iam_roles WHERE name=?)+(SELECT count(*) FROM identity_policies WHERE lower(name)=lower(?))", r.PhysicalID, r.PhysicalID).Scan(&reserved); err != nil {
				return err
			}
			if reserved != 0 {
				return ssmFailure{"AlreadyExistsException", "Role name belongs to a native or existing local identity."}
			}
		}
		if err = tx.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			return err
		}
		if count >= limit {
			return ssmFailure{"LimitExceededException", "Simulator resource limit reached."}
		}
		namespace := stackResourceNamespace(record.Region, r.Type)
		if _, err = tx.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", namespace, r.Type, r.PhysicalID, record.ID); err != nil {
			return ssmFailure{"AlreadyExistsException", "Resource belongs to an existing stack, including drifted resources."}
		}
		if r.Type == "AWS::SSM::Parameter" {
			_, err = tx.Exec("INSERT INTO aws_ssm_parameters VALUES(?,?,?,?,?,?,?)", record.Region, r.PhysicalID, stringValue(r.Properties["Type"]), stringValue(r.Properties["Value"]), stringValue(r.Properties["Description"]), 1, float64(time.Now().UnixMilli())/1000)
		} else if r.Type == "AWS::Lambda::Function" {
			var f LambdaFunction
			f, err = compileStackFunction(record.Region, record.Name, record.ID, r.LogicalID, r.Properties)
			if err == nil {
				// Persisted physical IDs remain authoritative across code upgrades.
				f.Name = r.PhysicalID
				err = saveLambdaFunction(tx, &f, true, "")
			}
		} else if r.Type == "AWS::S3::Bucket" {
			err = createStackBucket(tx, record.Region, r)
		} else if r.Type == "AWS::IAM::Role" {
			_, err = tx.Exec("INSERT INTO identity_policies VALUES(?,?)", r.PhysicalID, stringValue(r.Properties["SimulatorPolicy"]))
		} else {
			return ssmFailure{"ValidationError", "Unsupported stored stack resource type."}
		}
		if err != nil {
			return ssmFailure{"AlreadyExistsException", "A resource already exists or its creation was rejected."}
		}
	}
	if err = saveStackAPIFingerprints(tx, &record); err != nil {
		return err
	}
	return tx.Commit()
}

type stackQuery interface {
	Query(string, ...any) (*sql.Rows, error)
}

func (s *StackStore) Records(region, name string) ([]StackRecord, error) {
	return stackRecords(s.db, region, name)
}
func stackRecords(query stackQuery, region, name string) ([]StackRecord, error) {
	rows, err := query.Query("SELECT document FROM aws_cfn_stacks WHERE region=? AND (?='' OR name=? OR id=?) ORDER BY name", region, name, name, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []StackRecord{}
	for rows.Next() {
		var data string
		var record StackRecord
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(data), &record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if name != "" && len(records) == 0 {
		return nil, ssmFailure{"ValidationError", "Stack does not exist."}
	}
	return records, nil
}
func (s *StackStore) Delete(region, name string) error {
	return s.deleteChecked(region, name, nil)
}
func (s *StackStore) deleteChecked(region, name string, authorize func(StackRecord) error) error {
	// Acquire the immediate write transaction before reading the stack. A stale
	// delete must never remove a replacement stack that reclaimed physical names.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	records, err := stackRecords(tx, region, name)
	if err != nil {
		return err
	}
	if len(records) != 1 {
		return ssmFailure{"ValidationError", "Specify exactly one stack."}
	}

	// Resolve and authorize the exact ID while holding the same write lock;
	// authorizing a name before this transaction would permit replacement races.
	if authorize != nil {
		if err = authorize(records[0]); err != nil {
			return err
		}
	}
	if err = checkStackAPIFingerprints(tx, records[0]); err != nil {
		return err
	}
	for i := len(records[0].Resources) - 1; i >= 0; i-- {
		r := records[0].Resources[i]
		namespace := stackResourceNamespace(region, r.Type)
		var owned int
		if err = tx.QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE namespace=? AND kind=? AND physical_id=? AND stack_id=?", namespace, r.Type, r.PhysicalID, records[0].ID).Scan(&owned); err != nil {
			return err
		}
		if owned != 1 {
			return ssmFailure{"ValidationError", "Stack resource ownership changed; deletion refused."}
		}
		if stackAPIKind(r.Type) {
			err = deleteStackAPI(context.Background(), tx, region, r)
		} else if r.Type == "AWS::SSM::Parameter" {
			_, err = tx.Exec("DELETE FROM aws_ssm_parameters WHERE region=? AND name=?", region, r.PhysicalID)
		} else if r.Type == "AWS::Lambda::Function" {
			_, err = tx.Exec("DELETE FROM aws_lambda_functions WHERE region=? AND name=?", region, r.PhysicalID)
		} else if r.Type == "AWS::S3::Bucket" {
			err = s3DeleteBucket(tx, r.PhysicalID)
			if core.Code(err) == "NoSuchBucket" {
				err = nil
			} else if core.Code(err) == "BucketNotEmpty" {
				return ssmFailure{"ValidationError", "S3 bucket retains objects, versions or delete markers; empty it explicitly before DeleteStack."}
			}
		} else if r.Type == "AWS::IAM::Role" {
			_, err = tx.Exec("DELETE FROM identity_policies WHERE name=?", r.PhysicalID)
		} else {
			return ssmFailure{"ValidationError", "Unsupported stored stack resource type."}
		}
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec("DELETE FROM aws_cfn_ownership WHERE stack_id=?", records[0].ID); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM aws_cfn_stacks WHERE id=?", records[0].ID); err != nil {
		return err
	}
	return tx.Commit()
}
func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func cfnResponse(w http.ResponseWriter, action, result string) {
	w.Header().Set("Content-Type", "text/xml")
	_, _ = fmt.Fprintf(w, `<?xml version="1.0"?><%sResponse xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><%sResult>%s</%sResult><ResponseMetadata><RequestId>local-simulator</RequestId></ResponseMetadata></%sResponse>`, action, action, result, action, action)
}
func cfnError(w http.ResponseWriter, err error) {
	code, message, status := "InternalFailure", "Simulator stack operation failed.", 500
	var e ssmFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
		if code == "AccessDenied" {
			status = 403
		}
		if code == "NotImplemented" {
			status = 501
		}
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<ErrorResponse xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error><RequestId>local-simulator</RequestId></ErrorResponse>`, xmlText(code), xmlText(message))
}
func (g *Gateway) serveCFN(w http.ResponseWriter, r *http.Request) {
	fail := func(message string) { cfnError(w, ssmFailure{"ValidationError", message}) }
	if r.URL.Path != "/" || r.URL.RawQuery != "" {
		fail("Use the root CloudFormation Query endpoint.")
		return
	}
	region := "us-east-1"
	if v := r.Header.Get("X-Simulator-Region"); v != "" {
		region = v
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, ok := strings.Cut(a, "Credential=")
		parts := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(parts[0], "TCSIM") {
			cfnError(w, ssmFailure{"AccessDenied", "Use unsigned requests or dummy TCSIM credentials."})
			return
		}
		if len(parts) > 3 {
			if v := r.Header.Get("X-Simulator-Region"); v != "" && v != parts[2] {
				fail("Region scope mismatch.")
				return
			}
			region = parts[2]
		}
	}
	valid := false
	for _, v := range Regions {
		if region == v {
			valid = true
		}
	}
	if !valid || r.Header.Get("X-Amz-Security-Token") != "" {
		fail("Unsupported region or session token.")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail("Query request exceeds 1 MiB.")
		return
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		fail("Invalid Query request.")
		return
	}
	action := form.Get("Action")
	if action != "CreateStack" && action != "DescribeStacks" && action != "DeleteStack" {
		cfnError(w, ssmFailure{"NotImplemented", "Only CreateStack, DescribeStacks and DeleteStack are supported."})
		return
	}
	if form.Get("Version") != "2010-05-15" {
		fail("Use CloudFormation API version 2010-05-15.")
		return
	}
	for key, values := range form {
		if len(values) != 1 {
			fail("Repeated Query fields are unsupported.")
			return
		}
		if strings.Contains(key, ".member.") {
			parts := strings.Split(key, ".")
			index, e := strconv.Atoi(parts[2])
			maximum := 100
			if parts[0] == "Tags" {
				maximum = 50
			}
			if parts[0] == "Capabilities" {
				maximum = 1
			}
			if e != nil || index < 1 || index > maximum || strconv.Itoa(index) != parts[2] {
				fail("Unsupported member index.")
				return
			}
			if parts[0] == "Parameters" && (form.Get("Parameters.member."+parts[2]+".ParameterKey") == "" || !form.Has("Parameters.member."+parts[2]+".ParameterValue")) {
				fail("Parameter members require both key and value.")
				return
			}
			if parts[0] == "Tags" && (form.Get("Tags.member."+parts[2]+".Key") == "" || !form.Has("Tags.member."+parts[2]+".Value")) {
				fail("Tag members require both key and value.")
				return
			}
		}
		switch key {
		case "TemplateBody", "ClientRequestToken":
			if action != "CreateStack" {
				fail("CreateStack-only field.")
				return
			}
		case "Action", "Version", "StackName":
		default:
			if action != "CreateStack" || !regexp.MustCompile(`^Parameters\.member\.[0-9]+\.(ParameterKey|ParameterValue)$|^Capabilities\.member\.[0-9]+$|^Tags\.member\.[0-9]+\.(Key|Value)$`).MatchString(key) {
				fail("Unsupported Query field: " + key)
				return
			}
		}
	}
	// Exercise IAM permission is not permission to read host-private answers.
	if action == "DescribeStacks" && r.Header.Get("X-Simulator-Access-Role") == "participant" {
		cfnError(w, ssmFailure{"AccessDenied", "Stack outputs are reserved for the host."})
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	// Snapshot the caller's exercise policy once, like other provider requests.
	// Authorization against resource IDs is deferred until the target is known.
	var policy core.Policy
	if principal != core.Administrator {
		var found bool
		policy, found, err = g.Identities.Policy(principal)
		if err != nil {
			cfnError(w, err)
			return
		}
		if !found {
			cfnError(w, ssmFailure{"AccessDenied", "Unknown exercise identity."})
			return
		}
	}
	authorizeAction := func(action, resource string) error {
		if principal == core.Administrator {
			return nil
		}
		if core.Evaluate(policy, action, resource) != "allowed" {
			return ssmFailure{"AccessDenied", "Identity policy does not allow " + action + " on " + resource + "."}
		}
		return nil
	}
	authorize := func(resource string) error { return authorizeAction("cloudformation:"+action, resource) }

	name := form.Get("StackName")
	switch action {
	case "CreateStack":
		parameters := map[string]string{}
		for i := 1; i <= 100; i++ {
			key := form.Get(fmt.Sprintf("Parameters.member.%d.ParameterKey", i))
			if key != "" {
				if _, ok := parameters[key]; ok {
					fail("Duplicate parameter name.")
					return
				}
				parameters[key] = form.Get(fmt.Sprintf("Parameters.member.%d.ParameterValue", i))
			}
		}
		record, err := compileStack(region, name, form.Get("TemplateBody"), parameters, form.Get("Capabilities.member.1"))
		record.Tags = map[string]string{}
		for i := 1; i <= 50; i++ {
			key := form.Get(fmt.Sprintf("Tags.member.%d.Key", i))
			if key != "" {
				if _, exists := record.Tags[key]; exists {
					fail("Duplicate tag key.")
					return
				}
				record.Tags[key] = form.Get(fmt.Sprintf("Tags.member.%d.Value", i))
			}
		}
		if err == nil {
			record.ClientToken = form.Get("ClientRequestToken")
			input, _ := json.Marshal([]any{form.Get("TemplateBody"), parameters, record.Tags, form.Get("Capabilities.member.1")})
			digest := sha256.Sum256(input)
			record.RequestHash = hex.EncodeToString(digest[:])
			if record.ClientToken != "" {
				existing, e := g.Stacks.Records(region, name)
				if e == nil && len(existing) == 1 && existing[0].ClientToken == record.ClientToken {
					if existing[0].RequestHash != record.RequestHash {
						fail("ClientRequestToken was reused with a different request.")
						return
					}
					if err = authorize(existing[0].ID); err != nil {
						cfnError(w, err)
						return
					}
					cfnResponse(w, action, "<StackId>"+xmlText(existing[0].ID)+"</StackId>")
					return
				}
			}
			if err = authorize(record.ID); err == nil {
				err = stackResourcePermissions(record, false, authorizeAction)
				if err == nil {
					err = g.Stacks.Create(record)
				}
			}
		}
		if err != nil {
			cfnError(w, err)
			return
		}
		cfnResponse(w, action, "<StackId>"+xmlText(record.ID)+"</StackId>")
	case "DescribeStacks":
		if name == "" {
			if err = authorize("*"); err != nil {
				cfnError(w, err)
				return
			}
		}
		records, err := g.Stacks.Records(region, name)
		if err != nil {
			cfnError(w, err)
			return
		}
		var out strings.Builder
		out.WriteString("<Stacks>")
		for _, record := range records {
			if err = authorize(record.ID); err != nil {
				cfnError(w, err)
				return
			}
			out.WriteString("<member><StackId>" + xmlText(record.ID) + "</StackId><StackName>" + xmlText(record.Name) + "</StackName><CreationTime>" + record.Created + "</CreationTime><StackStatus>CREATE_COMPLETE</StackStatus><Outputs>")
			keys := []string{}
			for k := range record.Outputs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out.WriteString("<member><OutputKey>" + xmlText(k) + "</OutputKey><OutputValue>" + xmlText(record.Outputs[k]) + "</OutputValue></member>")
			}
			out.WriteString("</Outputs><Tags>")
			for k, v := range record.Tags {
				out.WriteString("<member><Key>" + xmlText(k) + "</Key><Value>" + xmlText(v) + "</Value></member>")
			}
			out.WriteString("</Tags></member>")
		}
		out.WriteString("</Stacks>")
		cfnResponse(w, action, out.String())
	case "DeleteStack":
		if name == "" {
			fail("Specify a stack name or ID.")
			return
		}
		if err = g.Stacks.deleteChecked(region, name, func(record StackRecord) error {
			if e := authorize(record.ID); e != nil {
				return e
			}
			return stackResourcePermissions(record, true, authorizeAction)
		}); err != nil {
			cfnError(w, err)
			return
		}
		cfnResponse(w, action, "")
	}
}
