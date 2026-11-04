package aws

import (
	"context"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"strings"
	"testing"
)

const apiStackTemplate = `Resources:
  Api:
    Type: AWS::ApiGateway::RestApi
    Properties:
      Name: local-cfn-api
      EndpointConfiguration: {Types: [REGIONAL]}
  Proxy:
    Type: AWS::ApiGateway::Resource
    Properties:
      RestApiId: !Ref Api
      ParentId: !GetAtt Api.RootResourceId
      PathPart: '{proxy+}'
  RootMethod:
    Type: AWS::ApiGateway::Method
    Properties:
      RestApiId: !Ref Api
      ResourceId: !GetAtt Api.RootResourceId
      HttpMethod: ANY
      AuthorizationType: NONE
      Integration:
        Type: AWS_PROXY
        IntegrationHttpMethod: POST
        Uri: !Sub arn:aws:apigateway:${AWS::Region}:lambda:path/2015-03-31/functions/arn:aws:lambda:${AWS::Region}:000000000000:function:app/invocations
  ProxyMethod:
    Type: AWS::ApiGateway::Method
    Properties:
      RestApiId: !Ref Api
      ResourceId: !Ref Proxy
      HttpMethod: ANY
      AuthorizationType: NONE
      RequestParameters: {method.request.path.proxy: true}
      Integration:
        Type: AWS_PROXY
        IntegrationHttpMethod: POST
        Uri: !Sub arn:aws:apigateway:${AWS::Region}:lambda:path/2015-03-31/functions/arn:aws:lambda:${AWS::Region}:000000000000:function:app/invocations
        RequestParameters: {integration.request.path.proxy: method.request.path.proxy}
  Deployment:
    Type: AWS::ApiGateway::Deployment
    DependsOn: [RootMethod, ProxyMethod]
    Properties: {RestApiId: !Ref Api}
  Stage:
    Type: AWS::ApiGateway::Stage
    Properties:
      RestApiId: !Ref Api
      DeploymentId: !Ref Deployment
      StageName: prod
Outputs:
  ApiId: {Value: !Ref Api}
  RootId: {Value: !GetAtt Api.RootResourceId}
  URL: {Value: !Sub 'https://${Api}.execute-api.${AWS::Region}.amazonaws.com/prod'}
`

func TestCFNAPISharedStateRollbackDriftAndIsolation(t *testing.T) {
	ctx := context.Background()
	for _, region := range []string{"ap-northeast-1", "us-east-1"} {
		t.Run(region, func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			native, err := NewAPIGatewayStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStack(region, "nativeapi", apiStackTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			id := record.Outputs["ApiId"]
			api, err := native.API(ctx, region, id)
			if err != nil || api.RootResourceID != record.Outputs["RootId"] {
				t.Fatal(api, err)
			}
			route, err := native.ResolveStage(ctx, region, id, "prod", "/deep/path", "GET")
			if err != nil || route.Resource.Path != "/{proxy+}" {
				t.Fatal(route, err)
			}
			// Separate actual DB/team cannot discover this API.
			other, _, _, _ := s3StackState(t)
			otherAPI, err := NewAPIGatewayStore(other.Database())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = otherAPI.API(ctx, region, id); err == nil {
				t.Fatal("cross-team API visible")
			}
			// An unowned native child is protected from cascading DeleteStack.
			child, err := native.CreateResource(ctx, region, id, api.RootResourceID, "unowned")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Delete(region, record.ID); err == nil {
				t.Fatal("drifted API was destructively deleted")
			}
			if _, err = native.Resource(ctx, region, id, child.ID); err != nil {
				t.Fatal("unowned child lost", err)
			}
			if err = native.DeleteResource(ctx, region, id, child.ID); err != nil {
				t.Fatal(err)
			}
			if err = stacks.Delete(region, record.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = native.API(ctx, region, id); err == nil {
				t.Fatal("API survived cleanup")
			}
			// A failure after API creation rolls back rows and ownership atomically.
			invalid := strings.Replace(apiStackTemplate, "StageName: prod", "StageName: bad.name", 1)
			bad, err := compileStack(region, "invalid", invalid, nil, "")
			if err == nil {
				t.Fatal("invalid stage accepted", bad)
			}
			bad, err = compileStack(region, "rollback", strings.Replace(apiStackTemplate, "StageName: prod", "StageName: prod\n      Variables: {bad: 'contains space'}", 1), nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(bad); err == nil {
				t.Fatal("invalid API created")
			}
			var count int
			if err = db.Database().QueryRow("SELECT count(*) FROM aws_cfn_ownership WHERE stack_id=?", bad.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal(count, err)
			}
		})
	}
}
func TestCFNAPIPermissionsAndUnsupportedProperties(t *testing.T) {
	record, err := compileStack("ap-northeast-1", "permissions", apiStackTemplate, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	if err = stackResourcePermissions(record, false, func(action, resource string) error { seen[action+" "+resource] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if !seen["apigateway:POST arn:aws:apigateway:ap-northeast-1::/restapis"] {
		t.Fatal(seen)
	}
	for _, mutation := range []string{
		strings.Replace(apiStackTemplate, "AuthorizationType: NONE", "AuthorizationType: AWS_IAM", 1),
		strings.Replace(apiStackTemplate, "Type: AWS_PROXY", "Type: HTTP_PROXY", 1),
		strings.Replace(apiStackTemplate, "integration.request.path.proxy: method.request.path.proxy", "integration.request.header.secret: method.request.path.proxy", 1),
		strings.Replace(apiStackTemplate, "Name: local-cfn-api", "Name: local-cfn-api\n      Body: {}", 1),
	} {
		if _, err = compileStack("ap-northeast-1", "reject", mutation, nil, ""); err == nil {
			t.Fatal("unsupported properties accepted")
		}
	}
}

func TestCFNLambdaPermissionUsesNativePolicyPreservesForeignStatements(t *testing.T) {
	db, _, stacks, _ := s3StackState(t)
	functions, err := NewLambdaStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	defer functions.Close()
	fn, err := compileStack("ap-northeast-1", "function", inlineStackTemplate, nil, "CAPABILITY_IAM")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(fn); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(apiStackTemplate, "Outputs:", `  Permission:
    Type: AWS::Lambda::Permission
    Properties:
      FunctionName: cfn-inline-app
      Action: lambda:InvokeFunction
      Principal: apigateway.amazonaws.com
      SourceArn: !Sub 'arn:aws:execute-api:${AWS::Region}:${AWS::AccountId}:${Api}/*/*/*'
Outputs:`, 1)
	record, err := compileStack(fn.Region, "permission", body, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = stacks.Create(record); err != nil {
		t.Fatal(err)
	}
	policies, err := NewLambdaPermissionStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	source := "arn:aws:execute-api:" + fn.Region + ":000000000000:" + record.Outputs["ApiId"] + "/prod/GET/path"
	if allowed, err := policies.AllowsAPI(context.Background(), fn.Region, "cfn-inline-app", source); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	sid := "foreign"
	grant := "arn:aws:execute-api:" + fn.Region + ":000000000000:1111111111/*/*/*"
	if _, err = policies.Add(context.Background(), fn.Region, "cfn-inline-app", sid, "apigateway.amazonaws.com", "lambda:InvokeFunction", grant, ""); err != nil {
		t.Fatal(err)
	}
	for _, r := range record.Resources {
		if r.Type == "AWS::Lambda::Permission" {
			if err = policies.Remove(context.Background(), fn.Region, "cfn-inline-app", r.PhysicalID, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = stacks.Delete(fn.Region, record.ID); err != nil {
		t.Fatal(err)
	}
	policy, _, err := policies.Policy(context.Background(), fn.Region, "cfn-inline-app")
	if err != nil || len(policy.Statement) != 1 || policy.Statement[0].Sid != sid {
		t.Fatal(policy, err)
	}
	if err = stacks.Delete(fn.Region, fn.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCFNAPITransactionHelpersNeverCommitOrRollbackCaller(t *testing.T) {
	db, _, _, _ := s3StackState(t)
	native, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Database().Begin()
	if err != nil {
		t.Fatal(err)
	}
	api, err := createAPITx(context.Background(), tx, "ap-northeast-1", "borrowed", "", nil, "0123456789", "1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = putMethodTx(context.Background(), tx, "ap-northeast-1", api.ID, api.RootResourceID, "unsupported", nil); err == nil {
		t.Fatal("invalid method accepted")
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_apigateway_apis").Scan(&count); err != nil || count != 1 {
		t.Fatal("helper closed caller tx", count, err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = native.API(context.Background(), "ap-northeast-1", api.ID); err == nil {
		t.Fatal("helper committed caller tx")
	}
}

func TestCFNAPICleanupAllowsAlreadyRemovedOwnedRows(t *testing.T) {
	for _, removeAPI := range []bool{false, true} {
		t.Run(fmt.Sprint(removeAPI), func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			ctx := context.Background()
			native, err := NewAPIGatewayStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			record, err := compileStack("ap-northeast-1", "removed", apiStackTemplate, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			if removeAPI {
				err = native.DeleteAPI(ctx, record.Region, record.Outputs["ApiId"])
			} else {
				for _, r := range record.Resources {
					if r.Type == "AWS::ApiGateway::Resource" {
						err = native.DeleteResource(ctx, record.Region, record.Outputs["ApiId"], r.PhysicalID)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal("missing owned rows should not strand stack", err)
			}
		})
	}
}

func TestCFNPermissionMissingFunctionAndRecreatedForeignGrantAreSafe(t *testing.T) {
	for _, recreate := range []bool{false, true} {
		t.Run(fmt.Sprint(recreate), func(t *testing.T) {
			db, _, stacks, _ := s3StackState(t)
			ctx := context.Background()
			functions, err := NewLambdaStore(db.Database())
			if err != nil {
				t.Fatal(err)
			}
			defer functions.Close()
			identities := core.NewIdentities(db)
			policy, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ssm:GetParameter","Resource":"*"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if err = identities.Set("external-role", policy); err != nil {
				t.Fatal(err)
			}
			original := LambdaFunction{Name: "external", Region: "ap-northeast-1", Role: "arn:aws:iam::000000000000:role/external-role", Handler: "index.handler", Code: []byte("stored-original"), Revision: "original"}
			if err = functions.save(&original, true, ""); err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(apiStackTemplate, "Outputs:", `  Permission:
    Type: AWS::Lambda::Permission
    Properties:
      FunctionName: external
      Action: lambda:InvokeFunction
      Principal: apigateway.amazonaws.com
      SourceArn: !Sub 'arn:aws:execute-api:${AWS::Region}:${AWS::AccountId}:${Api}/*/*/*'
Outputs:`, 1)
			record, err := compileStack(original.Region, "missingfunction", body, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = stacks.Create(record); err != nil {
				t.Fatal(err)
			}
			var sid string
			for _, r := range record.Resources {
				if r.Type == "AWS::Lambda::Permission" {
					sid = r.PhysicalID
				}
			}
			if _, err = db.Database().Exec("DELETE FROM aws_lambda_functions WHERE region=? AND name=?", original.Region, original.Name); err != nil {
				t.Fatal(err)
			}
			if recreate {
				replacement := original
				replacement.Revision = "replacement"
				replacement.Code = []byte("foreign-replacement")
				if err = functions.save(&replacement, true, ""); err != nil {
					t.Fatal(err)
				}
				permissions, err := NewLambdaPermissionStore(db.Database())
				if err != nil {
					t.Fatal(err)
				}
				source := "arn:aws:execute-api:" + original.Region + ":000000000000:aaaaaaaaaa/*/*/*"
				if _, err = permissions.Add(ctx, original.Region, original.Name, sid, "apigateway.amazonaws.com", "lambda:InvokeFunction", source, ""); err != nil {
					t.Fatal(err)
				}
				if err = stacks.Delete(record.Region, record.ID); err == nil {
					t.Fatal("replacement's foreign grant removed")
				}
				policy, _, err := permissions.Policy(ctx, original.Region, original.Name)
				if err != nil || len(policy.Statement) != 1 {
					t.Fatal(policy, err)
				}
				if err = permissions.Remove(ctx, original.Region, original.Name, sid, ""); err != nil {
					t.Fatal(err)
				}
			}
			if err = stacks.Delete(record.Region, record.ID); err != nil {
				t.Fatal("absent old grant strands stack", err)
			}
			if recreate {
				replacement, err := functions.get(original.Region, original.Name)
				if err != nil || replacement.Revision != "replacement" {
					t.Fatal("unowned replacement deleted", replacement, err)
				}
			}
		})
	}
}
