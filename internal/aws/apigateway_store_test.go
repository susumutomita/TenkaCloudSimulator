package aws

import (
	"context"
	"testing"
)

func TestPrivateRESTAPIStateIsRegionalAtomicAndPersistent(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := store.CreateAPI(ctx, "us-east-1", "same-name", "dummy description", map[string]string{"Team": "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.ID) != 10 || len(api.RootResourceID) != 10 || api.ID == api.RootResourceID || api.CreatedDate == 0 {
		t.Fatal(api)
	}
	var roots int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_apigateway_resources WHERE region=? AND api_id=? AND path='/'", "us-east-1", api.ID).Scan(&roots); err != nil || roots != 1 {
		t.Fatal(roots, err)
	}
	reopened, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	read, err := reopened.API(ctx, "us-east-1", api.ID)
	if err != nil || read.Name != api.Name || read.Tags["Team"] != "one" || read.RootResourceID != api.RootResourceID {
		t.Fatal(read, err)
	}
	if _, err = reopened.API(ctx, "us-west-2", api.ID); iamCode(err) != "NotFoundException" {
		t.Fatal("wrong-region lookup crossed state", err)
	}
	other, err := store.CreateAPI(ctx, "us-west-2", "same-name", "second region", nil)
	if err != nil || other.ID == api.ID {
		t.Fatal(other, err)
	}
	list, err := store.APIs(ctx, "us-east-1", "", 100)
	if err != nil || len(list) != 1 || list[0].ID != api.ID {
		t.Fatal(list, err)
	}
	if err = store.DeleteAPI(ctx, "us-east-1", api.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.API(ctx, "us-east-1", api.ID); iamCode(err) != "NotFoundException" {
		t.Fatal(err)
	}
	if _, err = store.API(ctx, "us-west-2", other.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_apigateway_resources WHERE api_id=?", api.ID).Scan(&roots); err != nil || roots != 0 {
		t.Fatal("API removal orphaned root resource", roots, err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER reject_api_root BEFORE INSERT ON aws_apigateway_resources BEGIN SELECT RAISE(ABORT,'test root resource failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateAPI(ctx, "us-east-1", "must-rollback", "", nil); err == nil {
		t.Fatal("partial API/root creation reported success")
	}
	list, err = store.APIs(ctx, "us-east-1", "", 100)
	if err != nil || len(list) != 0 {
		t.Fatal("failed root left an API", list, err)
	}
}

func TestPrivateRESTAPIRejectsInvalidMetadataAndCanceledWrites(t *testing.T) {
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []struct {
		region, name string
		tags         map[string]string
	}{{"unknown-region", "api", nil}, {"us-east-1", " ", nil}, {"us-east-1", "api", map[string]string{"aws:owned": "no"}}} {
		if _, err = store.CreateAPI(context.Background(), args.region, args.name, "", args.tags); iamCode(err) != "BadRequestException" {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = store.CreateAPI(ctx, "us-east-1", "late-api", "", nil); err == nil {
		t.Fatal("canceled API creation continued")
	}
	list, err := store.APIs(context.Background(), "us-east-1", "", 100)
	if err != nil || len(list) != 0 {
		t.Fatal(list, err)
	}
}

func TestPrivateRESTResourceTreeAndMethodState(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := store.CreateAPI(ctx, "us-east-1", "resource-state", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "{proxy+}")
	if err != nil || proxy.Path != "/{proxy+}" {
		t.Fatal(proxy, err)
	}
	if _, err = store.CreateResource(ctx, "us-east-1", api.ID, proxy.ID, "child"); iamCode(err) != "BadRequestException" {
		t.Fatal("child of greedy path accepted", err)
	}
	if _, err = store.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "{different}"); iamCode(err) != "ConflictException" {
		t.Fatal("ambiguous variable sibling accepted", err)
	}
	static, err := store.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "wp-json")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateResource(ctx, "us-east-1", api.ID, static.ID, "batch")
	if err != nil || child.Path != "/wp-json/batch" {
		t.Fatal(child, err)
	}
	method, err := store.PutMethod(ctx, "us-east-1", api.ID, proxy.ID, "ANY", map[string]bool{"method.request.path.proxy": true})
	if err != nil || method.AuthorizationType != "NONE" {
		t.Fatal(method, err)
	}
	if _, err = store.PutMethod(ctx, "us-east-1", api.ID, proxy.ID, "ANY", nil); iamCode(err) != "ConflictException" {
		t.Fatal("existing method silently replaced", err)
	}
	read, err := store.Resource(ctx, "us-east-1", api.ID, proxy.ID)
	if err != nil || !read.ResourceMethods["ANY"].RequestParameters["method.request.path.proxy"] {
		t.Fatal(read, err)
	}
	if _, err = store.PutMethod(ctx, "us-east-1", api.ID, static.ID, "GET", map[string]bool{"method.request.path.proxy": true}); iamCode(err) != "BadRequestException" {
		t.Fatal("invalid path binding accepted", err)
	}
	if _, err = store.CreateResource(ctx, "us-east-1", api.ID, static.ID, "../invalid"); iamCode(err) != "BadRequestException" {
		t.Fatal(err)
	}
	if err = store.DeleteResource(ctx, "us-east-1", api.ID, api.RootResourceID); iamCode(err) != "BadRequestException" {
		t.Fatal("root resource deleted", err)
	}
	if err = store.DeleteResource(ctx, "us-east-1", api.ID, static.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Resource(ctx, "us-east-1", api.ID, child.ID); iamCode(err) != "NotFoundException" {
		t.Fatal("descendant orphan remained", err)
	}
	if _, err = store.Resource(ctx, "us-east-1", api.ID, proxy.ID); err != nil {
		t.Fatal("subtree delete damaged a sibling", err)
	}
	resources, err := store.Resources(ctx, "us-east-1", api.ID, "", 100)
	if err != nil || len(resources) != 2 {
		t.Fatal(resources, err)
	}
}

func TestPrivateRESTDeploymentFreezesIntegratedMethodsAndStages(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := store.CreateAPI(ctx, "us-east-1", "frozen-deployment", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateDeployment(ctx, "us-east-1", api.ID, ""); iamCode(err) != "BadRequestException" {
		t.Fatal("empty deployment accepted", err)
	}
	if _, err = store.PutMethod(ctx, "us-east-1", api.ID, api.RootResourceID, "ANY", nil); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateDeployment(ctx, "us-east-1", api.ID, ""); iamCode(err) != "BadRequestException" {
		t.Fatal("unintegrated method deployed", err)
	}
	prefix := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/"
	uri := prefix + "arn:aws:lambda:us-east-1:000000000000:function:first/invocations"
	for _, bad := range []string{"https://example.invalid/invoke", prefix + "arn:aws:lambda:us-west-2:000000000000:function:first/invocations", prefix + "arn:aws:lambda:us-east-1:111111111111:function:first/invocations", prefix + "arn:aws:lambda:us-east-1:000000000000:function:first:alias/invocations"} {
		if _, err = store.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "ANY", bad); iamCode(err) != "BadRequestException" {
			t.Fatal("external/qualified integration accepted", bad, err)
		}
	}
	if _, err = store.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "ANY", uri); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateDeployment(ctx, "us-east-1", api.ID, "first version")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutIntegration(ctx, "us-east-1", api.ID, api.RootResourceID, "ANY", prefix+"arn:aws:lambda:us-east-1:000000000000:function:second/invocations"); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateDeployment(ctx, "us-east-1", api.ID, "second version")
	if err != nil {
		t.Fatal(err)
	}
	_, frozen, err := store.Deployment(ctx, "us-east-1", api.ID, first.ID)
	if err != nil || len(frozen) != 1 || frozen[0].ResourceMethods["ANY"].MethodIntegration.URI != uri {
		t.Fatal("deployment changed with mutable configuration", frozen, err)
	}
	for _, variables := range []map[string]string{{"bad-name": "dummy"}, {"mode": "space is invalid"}, {"mode": ""}} {
		if _, err = store.CreateStage(ctx, "us-east-1", api.ID, "invalid", first.ID, variables); iamCode(err) != "BadRequestException" {
			t.Fatal("invalid stage variable accepted", variables, err)
		}
	}
	stage, err := store.CreateStage(ctx, "us-east-1", api.ID, "prod", first.ID, map[string]string{"mode": "dummy"})
	if err != nil || stage.DeploymentID != first.ID {
		t.Fatal(stage, err)
	}
	if _, err = store.CreateStage(ctx, "us-east-1", api.ID, "prod", second.ID, nil); iamCode(err) != "ConflictException" {
		t.Fatal("stage overwritten", err)
	}
	if _, err = store.CreateStage(ctx, "us-east-1", api.ID, "missing", "absent", nil); iamCode(err) != "NotFoundException" {
		t.Fatal("dangling stage accepted", err)
	}
	if _, err = store.CreateStage(ctx, "us-west-2", api.ID, "cross", first.ID, nil); iamCode(err) != "NotFoundException" {
		t.Fatal("cross-region deployment bound", err)
	}
	persisted, err := store.Stage(ctx, "us-east-1", api.ID, "prod")
	if err != nil || persisted.DeploymentID != first.ID || persisted.Variables["mode"] != "dummy" {
		t.Fatal(persisted, err)
	}
	if err = store.DeleteStage(ctx, "us-east-1", api.ID, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Stage(ctx, "us-east-1", api.ID, "prod"); iamCode(err) != "NotFoundException" {
		t.Fatal(err)
	}
	if err = store.DeleteAPI(ctx, "us-east-1", api.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Deployment(ctx, "us-east-1", api.ID, first.ID); iamCode(err) != "NotFoundException" {
		t.Fatal("orphan deployment", err)
	}
}

func TestPrivateRESTStageRoutingUsesFrozenSpecificResourceBeforeMethod(t *testing.T) {
	ctx := context.Background()
	db, _ := s3TestState(t)
	store, err := NewAPIGatewayStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	api, err := store.CreateAPI(ctx, "us-east-1", "routing", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := store.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "{proxy+}")
	if err != nil {
		t.Fatal(err)
	}
	literal, err := store.CreateResource(ctx, "us-east-1", api.ID, api.RootResourceID, "settings")
	if err != nil {
		t.Fatal(err)
	}
	uri := "arn:aws:apigateway:us-east-1:lambda:path/2015-03-31/functions/arn:aws:lambda:us-east-1:000000000000:function:handler/invocations"
	for _, item := range []struct{ id, method string }{{api.RootResourceID, "GET"}, {proxy.ID, "ANY"}, {literal.ID, "GET"}} {
		if _, err = store.PutMethod(ctx, "us-east-1", api.ID, item.id, item.method, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = store.PutIntegration(ctx, "us-east-1", api.ID, item.id, item.method, uri); err != nil {
			t.Fatal(err)
		}
	}
	deployment, err := store.CreateDeployment(ctx, "us-east-1", api.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.CreateStage(ctx, "us-east-1", api.ID, "prod", deployment.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ path, method, id, capture string }{{"/", "GET", api.RootResourceID, ""}, {"/settings", "GET", literal.ID, ""}, {"/wp-json/batch/v1", "POST", proxy.ID, "wp-json/batch/v1"}} {
		route, e := store.ResolveStage(ctx, "us-east-1", api.ID, "prod", item.path, item.method)
		if e != nil || route.Resource.ID != item.id || route.PathParameters["proxy"] != item.capture {
			t.Fatal(route, e)
		}
	}
	if _, err = store.ResolveStage(ctx, "us-east-1", api.ID, "prod", "/settings", "POST"); iamCode(err) != "MissingAuthenticationTokenException" {
		t.Fatal("literal missing method fell through to wildcard ANY", err)
	}
	if err = store.DeleteResource(ctx, "us-east-1", api.ID, literal.ID); err != nil {
		t.Fatal(err)
	}
	route, err := store.ResolveStage(ctx, "us-east-1", api.ID, "prod", "/settings", "GET")
	if err != nil || route.Resource.ID != literal.ID {
		t.Fatal("mutable resource deletion changed deployed route", route, err)
	}
	if _, err = store.ResolveStage(ctx, "us-west-2", api.ID, "prod", "/", "GET"); iamCode(err) != "NotFoundException" {
		t.Fatal("cross-region route", err)
	}
}
