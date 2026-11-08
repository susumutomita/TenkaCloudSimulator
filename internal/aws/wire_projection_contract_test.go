package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// These literal fixtures specify the supported AWS response fields and the
// established local storage representation. No historical third-party DTO is
// imported as a second implementation or used to generate expected values.
func TestOwnedWireJSONLiteralCompatibility(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"API zero", RESTAPI{}, `{"id":"","name":"","createdDate":0,"rootResourceId":"","endpointConfiguration":{"types":null}}`},
		{"API optional empty", RESTAPI{Tags: map[string]string{}, EndpointConfiguration: RESTEndpoint{Types: []string{}}}, `{"id":"","name":"","createdDate":0,"rootResourceId":"","endpointConfiguration":{"types":[]}}`},
		{"API concrete", RESTAPI{ID: "0123456789", Name: "practice", Description: "<local>&", CreatedDate: 1700000000.125, RootResourceID: "abcdefghij", Tags: map[string]string{"z": "last", "a": "first"}, EndpointConfiguration: RESTEndpoint{Types: []string{"REGIONAL"}}}, `{"id":"0123456789","name":"practice","description":"\u003clocal\u003e\u0026","createdDate":1700000000.125,"rootResourceId":"abcdefghij","tags":{"a":"first","z":"last"},"endpointConfiguration":{"types":["REGIONAL"]}}`},
		{"resource root", RESTResource{ID: "root000001", Path: "/", ResourceMethods: map[string]RESTMethod{}}, `{"id":"root000001","path":"/"}`},
		{"method false retained", RESTMethod{HTTPMethod: "GET", AuthorizationType: "NONE", RequestParameters: map[string]bool{"method.request.path.proxy": false}}, `{"requestParameters":{"method.request.path.proxy":false},"httpMethod":"GET","authorizationType":"NONE","apiKeyRequired":false}`},
		{"method nil optional", RESTMethod{}, `{"httpMethod":"","authorizationType":"","apiKeyRequired":false}`},
		{"method explicit integration", RESTMethod{MethodIntegration: &RESTIntegration{}}, `{"httpMethod":"","authorizationType":"","apiKeyRequired":false,"methodIntegration":{"type":"","httpMethod":"","uri":"","timeoutInMillis":0}}`},
		{"resource sorted methods", RESTResource{ID: "child00001", ParentID: "root000001", PathPart: "child", Path: "/child", ResourceMethods: map[string]RESTMethod{"POST": {HTTPMethod: "POST", AuthorizationType: "NONE"}, "GET": {HTTPMethod: "GET", AuthorizationType: "NONE"}}}, `{"id":"child00001","parentId":"root000001","pathPart":"child","path":"/child","resourceMethods":{"GET":{"httpMethod":"GET","authorizationType":"NONE","apiKeyRequired":false},"POST":{"httpMethod":"POST","authorizationType":"NONE","apiKeyRequired":false}}}`},
		{"integration", RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: "arn:aws:lambda:us-east-1:000000000000:function:app", TimeoutInMillis: 29000}, `{"type":"AWS_PROXY","httpMethod":"POST","uri":"arn:aws:lambda:us-east-1:000000000000:function:app","timeoutInMillis":29000}`},
		{"deployment empty", RESTDeployment{}, `{"id":"","createdDate":0}`},
		{"stage empty optional", RESTStage{StageName: "prod", DeploymentID: "deploy0001", CreatedDate: 1.25, LastUpdatedDate: 2.5, Variables: map[string]string{}}, `{"stageName":"prod","deploymentId":"deploy0001","createdDate":1.25,"lastUpdatedDate":2.5}`},
		{"stage map sort", RESTStage{Variables: map[string]string{"z": "", "a": "日本語"}}, `{"stageName":"","deploymentId":"","createdDate":0,"lastUpdatedDate":0,"variables":{"a":"日本語","z":""}}`},
		{"KMS zero fields", KMSKeyMetadata{}, `{"AWSAccountId":"","KeyId":"","Arn":"","CreationDate":0,"Enabled":false,"Description":"","KeyUsage":"","KeyState":"","Origin":"","KeyManager":"","KeySpec":"","EncryptionAlgorithms":null,"MultiRegion":false}`},
		{"KMS empty algorithms", KMSKeyMetadata{EncryptionAlgorithms: []string{}}, `{"AWSAccountId":"","KeyId":"","Arn":"","CreationDate":0,"Enabled":false,"Description":"","KeyUsage":"","KeyState":"","Origin":"","KeyManager":"","KeySpec":"","EncryptionAlgorithms":[],"MultiRegion":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := json.Marshal(test.value)
			if err != nil || string(got) != test.want {
				t.Fatalf("wire fixture mismatch: %s; %v", got, err)
			}
			destination := reflect.New(reflect.TypeOf(test.value))
			if err := json.Unmarshal([]byte(test.want), destination.Interface()); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(destination.Elem().Interface())
			if err != nil || string(encoded) != test.want {
				t.Fatal("legacy JSON failed decode/re-encode", err)
			}
		})
	}
}

func TestOwnedWireProjectionRealEncodingFailures(t *testing.T) {
	for _, value := range []any{RESTAPI{CreatedDate: math.NaN()}, RESTDeployment{CreatedDate: math.Inf(1)}, RESTStage{LastUpdatedDate: math.Inf(-1)}, KMSKeyMetadata{CreationDate: math.NaN()}} {
		data, err := json.Marshal(value)
		var unsupported *json.UnsupportedValueError
		if err == nil || !errors.As(err, &unsupported) || len(data) != 0 {
			t.Fatal("invalid number produced a JSON response")
		}
	}
	data, err := marshalAWSOwnedWireObject(awsOwnedWireField{"retained", nil, false}, awsOwnedWireField{"omitted", math.NaN(), true}, awsOwnedWireField{"zero", false, false})
	if err != nil || string(data) != `{"retained":null,"zero":false}` {
		t.Fatal("explicit omission/null/false contract", err)
	}
	if data, err = marshalAWSOwnedWireObject(awsOwnedWireField{"first", "ok", false}, awsOwnedWireField{"later", math.NaN(), false}); err == nil || len(data) != 0 {
		t.Fatal("a later encoding failure published partial JSON")
	}
}

func TestOwnedWireIAMXMLFixturesAndCompatibility(t *testing.T) {
	zero, err := xml.Marshal(IAMRole{})
	wantZero := `<IAMRole><RoleName></RoleName><RoleId></RoleId><Arn></Arn><Path></Path><CreateDate>0001-01-01T00:00:00Z</CreateDate><AssumeRolePolicyDocument></AssumeRolePolicyDocument><MaxSessionDuration>0</MaxSessionDuration></IAMRole>`
	if err != nil || string(zero) != wantZero {
		t.Fatal("zero XML contract changed", string(zero), err)
	}
	role := IAMRole{RoleName: "reader", RoleID: "AROA0123456789ABCDEF", Arn: "arn:aws:iam::000000000000:role/reader", Path: "/", CreateDate: time.Date(2026, 11, 5, 10, 20, 30, 123000000, time.UTC), AssumeRolePolicyDocument: "%7B%22Statement%22%3A%22a%26b%22%7D", Description: "learn & <practice>", MaxSessionDuration: 3600}
	var buffer bytes.Buffer
	encoder := xml.NewEncoder(&buffer)
	if err = encoder.EncodeElement(role, xml.StartElement{Name: xml.Name{Local: "member"}}); err != nil {
		t.Fatal(err)
	}
	want := `<member><RoleName>reader</RoleName><RoleId>AROA0123456789ABCDEF</RoleId><Arn>arn:aws:iam::000000000000:role/reader</Arn><Path>/</Path><CreateDate>2026-11-05T10:20:30.123Z</CreateDate><AssumeRolePolicyDocument>%7B%22Statement%22%3A%22a%26b%22%7D</AssumeRolePolicyDocument><Description>learn &amp; &lt;practice&gt;</Description><MaxSessionDuration>3600</MaxSessionDuration></member>`
	if buffer.String() != want {
		t.Fatal("native member wrapper/order/escaping changed", buffer.String())
	}
	var decoded IAMRole
	if err = xml.Unmarshal([]byte(want), &decoded); err != nil || !reflect.DeepEqual(decoded, role) {
		t.Fatal("role XML fixture cannot be read", err)
	}
	decoded.RoleName = "preserved"
	if err = xml.Unmarshal([]byte(`<Role><Unknown><RoleName>not-adopted</RoleName></Unknown><RoleId>AROAUPDATED00000000</RoleId></Role>`), &decoded); err != nil || decoded.RoleName != "preserved" || decoded.RoleID != "AROAUPDATED00000000" {
		t.Fatal("unknown/absent XML compatibility changed", err)
	}
	for _, invalid := range []string{`<Role><CreateDate>not-a-date</CreateDate></Role>`, `<Role><MaxSessionDuration>bad</MaxSessionDuration></Role>`, `<Role><RoleId>truncated`} {
		if xml.Unmarshal([]byte(invalid), &IAMRole{}) == nil {
			t.Fatal("malformed XML accepted")
		}
	}
	if _, err = xml.Marshal(IAMRole{CreateDate: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("invalid timestamp encoding suppressed")
	}
	if err = xml.NewEncoder(ownedWireFailWriter{}).EncodeElement(role, xml.StartElement{Name: xml.Name{Local: "Role"}}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("XML writer error suppressed", err)
	}
}

type ownedWireFailWriter struct{}

func (ownedWireFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestOwnedWirePersistedRESTJSONNoMigration(t *testing.T) {
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	apiDoc := `{"id":"api0000001","name":"persisted","description":"old document","createdDate":1700000000.25,"rootResourceId":"root000001","endpointConfiguration":{"types":["REGIONAL"]}}`
	resourceDoc := `{"id":"root000001","path":"/","resourceMethods":{"GET":{"httpMethod":"GET","authorizationType":"NONE","apiKeyRequired":false}}}`
	deploymentDoc := `{"id":"deploy0001","createdDate":1700000001.5,"description":"frozen"}`
	stageDoc := `{"stageName":"prod","deploymentId":"deploy0001","createdDate":1700000002,"lastUpdatedDate":1700000002,"variables":{"mode":"legacy"}}`
	for _, row := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO aws_apigateway_apis VALUES(?,?,?)`, []any{"us-east-1", "api0000001", apiDoc}},
		{`INSERT INTO aws_apigateway_resources VALUES(?,?,?,?,?,?)`, []any{"us-east-1", "api0000001", "root000001", nil, "/", resourceDoc}},
		{`INSERT INTO aws_apigateway_deployments VALUES(?,?,?,?,?)`, []any{"us-east-1", "api0000001", "deploy0001", deploymentDoc, "[" + resourceDoc + "]"}},
		{`INSERT INTO aws_apigateway_stages VALUES(?,?,?,?,?)`, []any{"us-east-1", "api0000001", "prod", "deploy0001", stageDoc}},
	} {
		if _, err = db.Database().Exec(row.query, row.args...); err != nil {
			t.Fatal(err)
		}
	}
	before := ownedWireDatabaseFrame(t, db)
	reopened, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := reopened.API(context.Background(), "us-east-1", "api0000001")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := reopened.Resource(context.Background(), "us-east-1", "api0000001", "root000001")
	if err != nil {
		t.Fatal(err)
	}
	deployment, snapshot, err := reopened.Deployment(context.Background(), "us-east-1", "api0000001", "deploy0001")
	if err != nil || len(snapshot) != 1 {
		t.Fatal("legacy snapshot unavailable", err)
	}
	stage, err := reopened.Stage(context.Background(), "us-east-1", "api0000001", "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		value any
		want  string
	}{{api, apiDoc}, {resource, resourceDoc}, {deployment, deploymentDoc}, {stage, stageDoc}, {snapshot, "[" + resourceDoc + "]"}} {
		data, e := json.Marshal(fixture.value)
		if e != nil || string(data) != fixture.want {
			t.Fatal("persisted bytes/projection changed", e)
		}
	}
	if !reflect.DeepEqual(before, ownedWireDatabaseFrame(t, db)) {
		t.Fatal("read/projection migrated the saved REST graph")
	}
	// Keep nested request validation with the standard strict decoder. A custom
	// UnmarshalJSON must not accidentally bypass DisallowUnknownFields.
	for _, body := range []string{`{"name":"bad","endpointConfiguration":{"types":["REGIONAL"],"extra":true}}`, `{"name":"bad","endpointConfiguration":{"types":42}}`} {
		w := httptest.NewRecorder()
		store.serveControl(w, httptest.NewRequest("POST", "/restapis", strings.NewReader(body)), "us-east-1", func(string, string) bool { return true })
		if w.Code != 400 || !reflect.DeepEqual(before, ownedWireDatabaseFrame(t, db)) {
			t.Fatal("strict request compatibility or saved frame changed", w.Code)
		}
	}
}

// Capture values without printing private key material on a failed assertion.
func ownedWireDatabaseFrame(t *testing.T, db *core.Store) map[string][][]any {
	t.Helper()
	frame := map[string][][]any{}
	for _, table := range []string{"aws_apigateway_apis", "aws_apigateway_resources", "aws_apigateway_deployments", "aws_apigateway_stages", "aws_iam_roles", "aws_iam_role_policies", "identity_policies", "aws_kms_keys"} {
		var exists int
		if err := db.Database().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists == 0 {
			continue
		}
		rows, err := db.Database().Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err = rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = bytes.Clone(b)
				}
			}
			frame[table] = append(frame[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return frame
}

func TestOwnedWireNativeIAMAndKMSLeaveDurableStateUntouched(t *testing.T) {
	db, s3 := s3TestState(t)
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewKMSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(context.Background(), "projection-role", lambdaServiceTrust, "demo & <safe>", 3600)
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.CreateKey(context.Background(), "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	before := ownedWireDatabaseFrame(t, db)
	g := &Gateway{Store: db, S3: s3, Roles: roles, KMS: keys, Identities: core.NewIdentities(db)}
	form := url.Values{"Action": {"GetRole"}, "Version": {"2010-05-08"}, "RoleName": {role.RoleName}}
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("X-Simulator-Service", "iam")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<RoleId>"+role.RoleID+"</RoleId>") || !strings.Contains(w.Body.String(), "<Description>demo &amp; &lt;safe&gt;</Description>") || !strings.Contains(w.Body.String(), "<AssumeRolePolicyDocument>%7B%22Version%22") || strings.Contains(w.Body.String(), "%257B") {
		t.Fatal("native role wire projection changed", w.Code)
	}
	w, result := kmsHTTP(t, g, "DescribeKey", `{"KeyId":"`+key.KeyID+`"}`, "")
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(result["KeyMetadata"], &fields); err != nil {
		t.Fatal(err)
	}
	wantNames := []string{"AWSAccountId", "KeyId", "Arn", "CreationDate", "Enabled", "Description", "KeyUsage", "KeyState", "Origin", "KeyManager", "KeySpec", "EncryptionAlgorithms", "MultiRegion"}
	if w.Code != 200 || len(fields) != len(wantNames) {
		t.Fatal("native KMS metadata shape changed", w.Code)
	}
	for _, name := range wantNames {
		if _, ok := fields[name]; !ok {
			t.Fatal("missing KMS native field", name)
		}
	}
	if string(fields["Enabled"]) != "true" || string(fields["MultiRegion"]) != "false" || string(fields["Description"]) != `""` || string(fields["EncryptionAlgorithms"]) != `["SYMMETRIC_DEFAULT"]` {
		t.Fatal("KMS false/empty/array semantics changed")
	}
	if !reflect.DeepEqual(before, ownedWireDatabaseFrame(t, db)) {
		t.Fatal("native metadata response changed durable role/key state")
	}
}
