package aws

import (
	"encoding/json"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"strings"
	"testing"
)

func TestCompileTemplateRejectsUnsupportedSemantics(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"Resources": map[string]any{"P": map[string]any{"Type": "AWS::SSM::Parameter", "Properties": map[string]any{"Name": "/safe/hello", "Type": "String", "Value": "safe"}}}}
	}
	cases := []func(map[string]any){
		func(m map[string]any) { m["Transform"] = "AWS::Serverless" },
		func(m map[string]any) { m["Resources"] = map[string]any{} },
		func(m map[string]any) {
			m["Resources"].(map[string]any)["P"].(map[string]any)["Type"] = "AWS::Lambda::Function"
		},
		func(m map[string]any) { m["Resources"].(map[string]any)["P"].(map[string]any)["DependsOn"] = "X" },
	}
	for _, property := range []map[string]any{{"Unknown": "x"}, {"Name": "bad name"}, {"Type": "SecureString"}, {"Value": ""}, {"Tier": "Advanced"}, {"Type": "StringList", "Value": "a,,b"}, {"Description": strings.Repeat("x", 1025)}, {"Value": map[string]any{"Ref": "missing"}}, {"Value": map[string]any{"Fn::GetAtt": "missing.Value"}}, {"Value": map[string]any{"Fn::Sub": "${missing}"}}, {"Value": map[string]any{"Fn::Sub": []any{"x"}}}, {"Value": map[string]any{"Fn::Join": []any{}}}} {
		p := property
		cases = append(cases, func(m map[string]any) {
			props := m["Resources"].(map[string]any)["P"].(map[string]any)["Properties"].(map[string]any)
			for k, v := range p {
				props[k] = v
			}
		})
	}
	for _, parameter := range []map[string]any{{"Type": "Number"}, {"Type": "String"}, {"Type": "String", "Default": "x", "AllowedPattern": "["}, {"Type": "String", "Default": "x", "AllowedPattern": "abc"}, {"Type": "String", "Default": "x", "MinLength": "bad"}, {"Type": "String", "Default": "x", "MaxLength": "0"}, {"Type": "String", "Default": "x", "AllowedValues": []any{"x"}}} {
		p := parameter
		cases = append(cases, func(m map[string]any) { m["Parameters"] = map[string]any{"Input": p} })
	}
	for _, out := range []map[string]any{{"Value": "x", "Export": map[string]any{"Name": "x"}}, {"Value": true}, {"Value": map[string]any{"Ref": "missing"}}} {
		o := out
		cases = append(cases, func(m map[string]any) { m["Outputs"] = map[string]any{"Out": o} })
	}
	for i, change := range cases {
		m := base()
		change(m)
		body, _ := json.Marshal(m)
		if _, err := compileStack("us-east-1", "safe", string(body), nil, "CAPABILITY_IAM"); err == nil {
			t.Errorf("case %d accepted %s", i, body)
		}
	}
	for _, body := range []string{"", "[", "Resources: {}\nResources: {}", "Resources: &alias\n  P: *alias", "Resources: !Unknown x", "Resources: !Sub [a,b]"} {
		if _, err := compileStack("us-east-1", "safe", body, nil, ""); err == nil {
			t.Fatal("accepted", body)
		}
	}
	body, _ := json.Marshal(base())
	if _, err := compileStack("us-east-1", "!bad", string(body), nil, ""); err == nil {
		t.Fatal("bad name")
	}
	if _, err := compileStack("us-east-1", "safe", string(body), map[string]string{"unknown": "x"}, ""); err == nil {
		t.Fatal("unknown parameter")
	}
}
func TestRoleSubsetAndResolution(t *testing.T) {
	policy := map[string]any{"Version": "2012-10-17", "Statement": map[string]any{"Effect": "Allow", "Action": "ssm:GetParameter", "Resource": "*"}}
	role := func() map[string]any {
		return map[string]any{"Policies": []any{map[string]any{"PolicyName": "read", "PolicyDocument": policy}}}
	}
	encode := func(props map[string]any) string {
		b, _ := json.Marshal(map[string]any{"Parameters": map[string]any{"V": map[string]any{"Type": "String", "Default": "safe", "MinLength": 1, "MaxLength": 10, "NoEcho": true}}, "Resources": map[string]any{"Role": map[string]any{"Type": "AWS::IAM::Role", "Properties": props}}, "Outputs": map[string]any{"Role": map[string]any{"Value": map[string]any{"Fn::Sub": "${Role.Arn}/${AWS::StackName}/${V}"}}}})
		return string(b)
	}
	for _, change := range []func(map[string]any){func(p map[string]any) { p["RoleName"] = "simulator-admin" }, func(p map[string]any) { p["Policies"] = []any{} }, func(p map[string]any) {
		p["Policies"] = []any{map[string]any{"PolicyDocument": map[string]any{"unsupported": true}}}
	}, func(p map[string]any) { p["Unsupported"] = true }, func(p map[string]any) { p["ManagedPolicyArns"] = "invalid" }, func(p map[string]any) { p["ManagedPolicyArns"] = []any{"arn:aws:iam::aws:policy/AdministratorAccess"} }} {
		props := role()
		change(props)
		if _, err := compileStack("us-east-1", "safe", encode(props), nil, "CAPABILITY_NAMED_IAM"); err == nil {
			t.Fatal("unsupported role accepted", props)
		}
	}
	if _, err := compileStack("us-east-1", "safe", encode(role()), nil, ""); err == nil {
		t.Fatal("unacknowledged IAM")
	}
	props := role()
	props["ManagedPolicyArns"] = []any{"arn:aws:iam::aws:policy/SignInLocalDevelopmentAccess"}
	record, err := compileStack("us-east-1", strings.Repeat("s", 100), encode(props), nil, "CAPABILITY_IAM")
	if err != nil || len(record.Resources[0].PhysicalID) > 64 {
		t.Fatal(record, err)
	}
	for _, v := range []any{[]any{"x", true, nil, map[string]any{"Ref": "known"}}, map[string]any{"regular": true}} {
		if _, err := resolve(v, map[string]string{"known": "value"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := resolve(123, nil, nil); err == nil {
		t.Fatal("unsupported value")
	}
}
func TestStackPersistenceRollbackAndQuotas(t *testing.T) {
	path := t.TempDir() + "/state.sqlite"
	coreStore, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewParameterStore(coreStore.Database())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStackStore(coreStore.Database())
	if err != nil {
		t.Fatal(err)
	}
	template := `Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /safe/hello, Type: String, Value: dummy}`
	template = strings.ReplaceAll(template, `\n`, "\n")
	record, err := compileStack("us-east-1", "safe", template, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Put("us-east-1", "/safe/hello", "String", "existing", nil, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(record); err == nil {
		t.Fatal("resource conflict accepted")
	}
	rows, _ := s.Records("us-east-1", "")
	if len(rows) != 0 {
		t.Fatal("failed stack persisted")
	}
	if err = p.Delete("us-east-1", "/safe/hello"); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(record); err != nil {
		t.Fatal(err)
	}
	coreStore.Close()
	coreStore, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	p, _ = NewParameterStore(coreStore.Database())
	s, _ = NewStackStore(coreStore.Database())
	rows, err = s.Records("us-east-1", record.ID)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if _, err = p.Get("us-east-1", "/safe/hello"); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete("us-east-1", record.ID); err != nil {
		t.Fatal(err)
	}
	db := coreStore.Database()
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO aws_ssm_parameters SELECT 'us-east-1','/quota/'||x,'String','x','',1,1 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Put("us-east-1", "/full", "String", "x", nil, false); err == nil {
		t.Fatal("parameter quota bypass")
	}
	if err = s.Create(record); err == nil {
		t.Fatal("stack bypassed parameter quota")
	}
	if rows, _ = s.Records("us-east-1", ""); len(rows) != 0 {
		t.Fatal("quota failure left stack")
	}
	_, err = db.Exec(`DELETE FROM aws_ssm_parameters;WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100) INSERT INTO aws_cfn_stacks SELECT 'us-east-1','quota-'||x,'id-'||x,'{}' FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create(record); err == nil {
		t.Fatal("stack quota bypass")
	}
	coreStore.Close()
	if _, err = p.List("us-east-1"); err == nil {
		t.Fatal("closed DB")
	}
	if _, err = p.Get("us-east-1", "x"); err == nil {
		t.Fatal("closed DB")
	}
	if _, err = p.Put("us-east-1", "x", "String", "x", nil, false); err == nil {
		t.Fatal("closed DB")
	}
	if err = p.Delete("us-east-1", "x"); err == nil {
		t.Fatal("closed DB")
	}
	if _, err = s.Records("us-east-1", ""); err == nil {
		t.Fatal("closed DB")
	}
	if err = s.Delete("us-east-1", "x"); err == nil {
		t.Fatal("closed DB")
	}
	if err = s.Create(record); err == nil {
		t.Fatal("closed DB")
	}
	if _, err = NewStackStore(db); err == nil {
		t.Fatal("closed DB")
	}
	if _, err = NewParameterStore(db); err == nil {
		t.Fatal("closed DB")
	}
}

func TestProviderStorageFailuresDoNotReportSuccess(t *testing.T) {
	c, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	db := c.Database()
	p, _ := NewParameterStore(db)
	s, _ := NewStackStore(db)
	// Corrupt persisted document is a state error, never an empty stack result.
	if _, err = db.Exec(`INSERT INTO aws_cfn_stacks VALUES('us-east-1','bad','bad-id','{')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Records("us-east-1", "bad"); err == nil {
		t.Fatal("corrupt document accepted")
	}
	if _, err = db.Exec(`DELETE FROM aws_cfn_stacks; DROP TABLE aws_ssm_parameters`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Put("us-east-1", "/safe", "String", "x", nil, false); err == nil {
		t.Fatal("missing table accepted")
	}
	// The failed resource insert must also roll back its stack and ownership claim.
	record := StackRecord{Name: "safe", Region: "us-east-1", ID: "safe-id", Resources: []StackResource{{Type: "AWS::SSM::Parameter", PhysicalID: "/safe", Properties: map[string]any{"Type": "String", "Value": "x"}}}}
	if err = s.Create(record); err == nil {
		t.Fatal("missing resource table accepted")
	}
	rows, err := s.Records("us-east-1", "")
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	_, err = NewParameterStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DROP TABLE aws_ssm_parameters`); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete("us-east-1", "safe"); err == nil {
		t.Fatal("failed delete succeeded")
	}
	rows, err = s.Records("us-east-1", "safe")
	if err != nil || len(rows) != 1 {
		t.Fatal("failed deletion lost stack", err)
	}
	if _, err = db.Exec(`DROP TABLE aws_cfn_ownership; DROP TABLE aws_cfn_stacks`); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(record); err == nil {
		t.Fatal("missing stacks table accepted")
	}
	if (ssmFailure{"test", "message"}).Error() != "message" {
		t.Fatal("error text")
	}
}

func TestResourceReferencesUseDependencyOrder(t *testing.T) {
	template := `Resources:
  A:
    Type: AWS::SSM::Parameter
    Properties: {Name: /dependency/a, Type: String, Value: !Sub '${Z.Value}/${M}'}
  M:
    Type: AWS::SSM::Parameter
    Properties: {Name: /dependency/m, Type: String, Value: {Fn::GetAtt: [Z, Value]}}
  Z:
    Type: AWS::SSM::Parameter
    Properties: {Name: /dependency/z, Type: String, Value: source}
Outputs:
  Result: {Value: !GetAtt A.Value}
`
	record, err := compileStack("us-east-1", "forward", template, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if record.Outputs["Result"] != "source//dependency/m" || record.Resources[0].LogicalID != "Z" {
		t.Fatal(record)
	}
	cross := `Resources:
  A:
    Type: AWS::SSM::Parameter
    Properties: {Name: /dependency/arn, Type: String, Value: !GetAtt Z.Arn}
  Z:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: read
          PolicyDocument: {Version: '2012-10-17', Statement: [{Effect: Allow, Action: 'ssm:GetParameter', Resource: '*'}]}
Outputs:
  Result: {Value: !GetAtt A.Value}
`
	record, err = compileStack("us-east-1", "forward", cross, nil, "CAPABILITY_IAM")
	if err != nil || record.Outputs["Result"] != "arn:aws:iam::000000000000:role/"+record.Resources[0].PhysicalID {
		t.Fatal(record, err)
	}
	for _, value := range []string{"!Ref A", "!GetAtt A.Value", "!Sub '${A.Value}'"} {
		body := strings.Replace(template, "{Fn::GetAtt: [Z, Value]}", value, 1)
		if _, err = compileStack("us-east-1", "cycle", body, nil, ""); err == nil || !strings.Contains(err.Error(), "cyclic") {
			t.Fatal("cycle not rejected", err)
		}
	}
}
func TestStackDeletionValidatesResourceClaims(t *testing.T) {
	c, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, _ := NewParameterStore(c.Database())
	s, _ := NewStackStore(c.Database())
	record := StackRecord{Name: "owner", ID: "owner-id", Region: "us-east-1", Resources: []StackResource{{Type: "AWS::SSM::Parameter", PhysicalID: "/owner", Properties: map[string]any{"Type": "String", "Value": "sentinel"}}}}
	if err = s.Create(record); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Database().Exec("DELETE FROM aws_cfn_ownership"); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete("us-east-1", record.ID); err == nil {
		t.Fatal("missing ownership accepted")
	}
	if got, err := p.Get("us-east-1", "/owner"); err != nil || got.Value != "sentinel" {
		t.Fatal("ownership mismatch deleted bytes", err)
	}
}

func TestGeneratedRolesRemainUniqueAcrossRegionalStacks(t *testing.T) {
	c, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = NewParameterStore(c.Database())
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewStackStore(c.Database())
	if err != nil {
		t.Fatal(err)
	}
	template := `Resources:
  Parameter:
    Type: AWS::SSM::Parameter
    Properties: {Name: /regional/hello, Type: String, Value: regional sentinel}
  Viewer:
    Type: AWS::IAM::Role
    Properties:
      Policies:
        - PolicyName: read
          PolicyDocument: {Version: '2012-10-17', Statement: [{Effect: Allow, Action: 'ssm:GetParameter', Resource: '*'}]}
Outputs:
  Role: {Value: !GetAtt Viewer.Arn}
`
	a, err := compileStack("us-east-1", "same-name", template, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	b, err := compileStack("ap-northeast-1", "same-name", template, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if a.Outputs["Role"] == b.Outputs["Role"] {
		t.Fatal("regional stacks generated the same global role")
	}
	if err = s.Create(a); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(b); err != nil {
		t.Fatal("independent regional stack collided", err)
	}
	if err = s.Delete(a.Region, a.ID); err != nil {
		t.Fatal(err)
	}
	role := strings.TrimPrefix(b.Outputs["Role"], "arn:aws:iam::000000000000:role/")
	if decision, err := core.NewIdentities(c).Decision(role, "ssm:GetParameter", "*"); err != nil || decision != "allowed" {
		t.Fatal("deletion damaged other region role", decision, err)
	}
	parameters, _ := NewParameterStore(c.Database())
	if got, err := parameters.Get(b.Region, "/regional/hello"); err != nil || got.Value != "regional sentinel" {
		t.Fatal("deletion damaged other region parameter", got, err)
	}
	if _, err = s.Records(b.Region, b.ID); err != nil {
		t.Fatal(err)
	}
	// Explicit names still intentionally occupy IAM's global namespace.
	named := strings.Replace(template, "    Properties:\n      Policies:", "    Properties:\n      RoleName: SharedExplicitRole\n      Policies:", 1)
	if _, err = compileStack("us-east-1", "named-a", named, nil, "CAPABILITY_IAM"); err == nil {
		t.Fatal("named IAM acknowledgement not required")
	}
	first, err := compileStack("us-east-1", "named-a", named, nil, "CAPABILITY_NAMED_IAM")
	if err != nil {
		t.Fatal(err)
	}
	second, err := compileStack("eu-west-1", "named-b", named, nil, "CAPABILITY_NAMED_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create(first); err != nil {
		t.Fatal(err)
	}
	if err = s.Create(second); err == nil {
		t.Fatal("explicit global role collision accepted")
	}
	if records, err := s.Records(second.Region, ""); err != nil || len(records) != 0 {
		t.Fatal("failed named stack persisted", records, err)
	}
	if err = s.Delete(first.Region, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(b.Region, b.ID); err != nil {
		t.Fatal(err)
	}
}
