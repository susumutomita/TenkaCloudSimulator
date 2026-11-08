package catalog

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func networkHarness(t *testing.T) *harness {
	return newHarness(t, appMesh, appSync, cloudMap, deviceFarm, globalAccelerator, location, networkManager, recoveryControl, rtbFabric, vpcLattice)
}

// newJSONRequest builds a signed JSON-protocol request in another region.
func newJSONRequest(h *harness, model, op string, data []byte, region string) *http.Request {
	b := h.binding(model)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(data))
	req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+"."+op)
	sign(req, b.signingName(), region)
	return req
}

func TestNetworkCheckHooksApplyDocumentedRules(t *testing.T) {
	fixed := func(code int64) map[string]any {
		return map[string]any{"fixedResponse": map[string]any{"statusCode": code}}
	}
	cases := []struct {
		name  string
		check func(map[string]any) error
		input map[string]any
		code  string
	}{
		{"service without provider", appMeshVirtualService, map[string]any{"spec": map[string]any{}}, ""},
		{"service without spec", appMeshVirtualService, map[string]any{}, ""},
		{"service provider", appMeshVirtualService, map[string]any{"spec": map[string]any{"provider": map[string]any{"virtualNode": map[string]any{"virtualNodeName": "web"}}}}, "BadRequestException"},
		{"default enabled accelerator", acceleratorCreate, map[string]any{"Name": "a"}, "InvalidArgumentException"},
		{"enabled accelerator", acceleratorCreate, map[string]any{"Enabled": true}, "InvalidArgumentException"},
		{"disabled accelerator", acceleratorCreate, map[string]any{"Enabled": false}, ""},
		{"byoip accelerator", acceleratorCreate, map[string]any{"Enabled": false, "IpAddresses": []any{"198.51.100.1"}}, "InvalidArgumentException"},
		{"enable accelerator", acceleratorUpdate, map[string]any{"Enabled": true}, "InvalidArgumentException"},
		{"rename accelerator", acceleratorUpdate, map[string]any{"Name": "b"}, ""},
		{"tls passthrough", latticeListener, map[string]any{"protocol": "TLS_PASSTHROUGH", "port": int64(443), "defaultAction": fixed(404)}, "ValidationException"},
		{"https listener", latticeListener, map[string]any{"protocol": "HTTPS", "defaultAction": fixed(500)}, ""},
		{"forward action", latticeAction, map[string]any{"defaultAction": map[string]any{"forward": map[string]any{}}}, "ValidationException"},
		{"fixed 200", latticeAction, map[string]any{"defaultAction": fixed(200)}, "ValidationException"},
		{"fixed 404", latticeAction, map[string]any{"defaultAction": fixed(404)}, ""},
		{"lambda target group", latticeTargetGroup, map[string]any{"type": "LAMBDA", "config": map[string]any{"lambdaEventStructureVersion": "V2"}}, ""},
		{"lambda without config", latticeTargetGroup, map[string]any{"type": "LAMBDA"}, ""},
		{"lambda vpc", latticeTargetGroup, map[string]any{"type": "LAMBDA", "config": map[string]any{"vpcIdentifier": "vpc-1"}}, "ValidationException"},
		{"alb health check", latticeTargetGroup, map[string]any{"type": "ALB", "config": map[string]any{"vpcIdentifier": "vpc-1", "healthCheck": map[string]any{}}}, "ValidationException"},
		{"instance ipv6", latticeTargetGroup, map[string]any{"type": "INSTANCE", "config": map[string]any{"vpcIdentifier": "vpc-1", "ipAddressType": "IPV6"}}, "ValidationException"},
		{"ip without vpc", latticeTargetGroup, map[string]any{"type": "IP", "config": map[string]any{"port": int64(80)}}, "ValidationException"},
		{"ip target group", latticeTargetGroup, map[string]any{"type": "IP", "config": map[string]any{"vpcIdentifier": "vpc-1", "ipAddressType": "IPV6", "healthCheck": map[string]any{}}}, ""},
		{"device at site", networkDevice, map[string]any{"SiteId": "site-1"}, "ValidationException"},
		{"device", networkDevice, map[string]any{"Model": "r1"}, ""},
		{"iam api", appSyncAuthorization, map[string]any{"authenticationType": "AWS_IAM"}, ""},
		{"cognito without pool", appSyncAuthorization, map[string]any{"authenticationType": "AMAZON_COGNITO_USER_POOLS"}, "BadRequestException"},
		{"cognito", appSyncAuthorization, map[string]any{"authenticationType": "AMAZON_COGNITO_USER_POOLS", "userPoolConfig": map[string]any{}}, ""},
		{"additional oidc", appSyncAuthorization, map[string]any{"authenticationType": "API_KEY",
			"additionalAuthenticationProviders": []any{map[string]any{"authenticationType": "OPENID_CONNECT"}}}, "BadRequestException"},
		{"additional lambda", appSyncAuthorization, map[string]any{"authenticationType": "API_KEY",
			"additionalAuthenticationProviders": []any{map[string]any{"authenticationType": "AWS_LAMBDA", "lambdaAuthorizerConfig": map[string]any{}}}}, ""},
		{"merged without role", appSyncAuthorization, map[string]any{"authenticationType": "AWS_IAM", "apiType": "MERGED"}, "BadRequestException"},
		{"merged", appSyncAuthorization, map[string]any{"authenticationType": "AWS_IAM", "apiType": "MERGED", "mergedApiExecutionRoleArn": "arn:aws:iam::000000000000:role/m"}, ""},
		{"none source", appSyncDataSource, map[string]any{"type": "NONE"}, ""},
		{"none source with config", appSyncDataSource, map[string]any{"type": "NONE", "httpConfig": map[string]any{}}, "BadRequestException"},
		{"bedrock source", appSyncDataSource, map[string]any{"type": "AMAZON_BEDROCK_RUNTIME"}, ""},
		{"lambda source without config", appSyncDataSource, map[string]any{"type": "AWS_LAMBDA"}, "BadRequestException"},
		{"dynamodb source", appSyncDataSource, map[string]any{"type": "AMAZON_DYNAMODB", "dynamodbConfig": map[string]any{}}, ""},
		{"dynamodb source with lambda", appSyncDataSource, map[string]any{"type": "AMAZON_DYNAMODB", "dynamodbConfig": map[string]any{}, "lambdaConfig": map[string]any{}}, "BadRequestException"},
		{"unknown style", locationMapStyle, map[string]any{"Configuration": map[string]any{"Style": "Watercolor"}}, "ValidationException"},
		{"esri place index", locationProvider, map[string]any{"DataSource": "Esri"}, ""},
		{"unknown provider", locationProvider, map[string]any{"DataSource": "Atlas"}, "ValidationException"},
		{"asset pricing map", locationMapStyle, map[string]any{"PricingPlan": "MobileAssetTracking", "Configuration": map[string]any{"Style": "VectorEsriStreets"}}, "ValidationException"},
		{"asset pricing index", locationProvider, map[string]any{"PricingPlan": "MobileAssetManagement", "DataSource": "Esri"}, "ValidationException"},
		{"request pricing", locationPricing, map[string]any{"PricingPlan": "RequestBasedUsage"}, ""},
	}
	for _, c := range cases {
		if got := faultCode(t, c.check(c.input)); got != c.code {
			t.Errorf("%s: want %q, got %q", c.name, c.code, got)
		}
	}
	for style, provider := range map[string]string{"VectorEsriStreets": "Esri", "VectorHereExplore": "Here", "VectorGrabStandardLight": "Grab", "VectorOpenDataStandardDark": "OpenData"} {
		input := map[string]any{"Configuration": map[string]any{"Style": style}}
		if err := locationMapStyle(input); err != nil || input["DataSource"] != provider {
			t.Errorf("style %s: data source %v, %v", style, input["DataSource"], err)
		}
	}
	for protocol, port := range map[string]int64{"HTTP": 80, "HTTPS": 443} {
		input := map[string]any{"protocol": protocol, "defaultAction": fixed(404)}
		if err := latticeListener(input); err != nil || input["port"] != port {
			t.Errorf("%s default port %v, %v", protocol, input["port"], err)
		}
	}
	kept := map[string]any{"protocol": "HTTP", "port": int64(8080), "defaultAction": fixed(404)}
	if err := latticeListener(kept); err != nil || kept["port"] != int64(8080) {
		t.Errorf("explicit port %v, %v", kept["port"], err)
	}
}

// Mesh children use the mesh-scoped official ARNs, list references carry
// the same metadata, and a mesh with children cannot be deleted.
func TestAppMeshChildrenMetadataAndDeletion(t *testing.T) {
	h := networkHarness(t)
	expect(t, h.rest("appmesh", "PUT", "/v20190125/meshes", map[string]any{"meshName": "m"}), 200, "")
	described := h.rest("appmesh", "GET", "/v20190125/meshes/m", nil)
	mesh := described.body // The official output payload is the mesh itself.
	if mesh["spec"].(map[string]any)["egressFilter"].(map[string]any)["type"] != "DROP_ALL" || mesh["metadata"].(map[string]any)["uid"] == nil {
		t.Fatalf("mesh defaults %v", mesh)
	}
	node := h.rest("appmesh", "PUT", "/v20190125/meshes/m/virtualNodes", map[string]any{"virtualNodeName": "web", "spec": map[string]any{}})
	expect(t, node, 200, "")
	arn := node.body["metadata"].(map[string]any)["arn"]
	if arn != "arn:aws:appmesh:us-east-1:000000000000:mesh/m/virtualNode/web" {
		t.Fatalf("virtual node ARN %v", arn)
	}
	listed := h.rest("appmesh", "GET", "/v20190125/meshes/m/virtualNodes", nil).body["virtualNodes"].([]any)
	if ref := listed[0].(map[string]any); ref["arn"] != arn || ref["meshName"] != "m" || asText(ref["version"]) != "1" {
		t.Fatalf("virtual node reference %v", ref)
	}
	expect(t, h.rest("appmesh", "GET", "/v20190125/meshes/m/virtualNodes/web?meshOwner=111122223333", nil), 400, "BadRequestException")
	expect(t, h.rest("appmesh", "DELETE", "/v20190125/meshes/m", nil), 409, "ResourceInUseException")
	expect(t, h.rest("appmesh", "GET", "/v20190125/tags?resourceArn=arn:aws:appmesh:us-east-1:000000000000:mesh/m&limit=5", nil), 400, "BadRequestException")
	expect(t, h.rest("appmesh", "PUT", "/v20190125/meshes/ghost/virtualNodes", map[string]any{"virtualNodeName": "x", "spec": map[string]any{}}), 404, "NotFoundException")
	expect(t, h.rest("appmesh", "DELETE", "/v20190125/meshes/m/virtualNodes/web", nil), 200, "")
	deleted := h.rest("appmesh", "DELETE", "/v20190125/meshes/m", nil)
	if deleted.body["status"].(map[string]any)["status"] != "DELETED" {
		t.Fatalf("deleted mesh %v", deleted.body)
	}
}

// Namespace operations return fresh operation identifiers and replay the
// original one for a repeated creator request.
func TestCloudMapNamespaceOperations(t *testing.T) {
	h := networkHarness(t)
	input := map[string]any{"Name": "apps", "CreatorRequestId": "r-1"}
	first := h.json1("servicediscovery", "CreateHttpNamespace", input)
	expect(t, first, 200, "")
	expect(t, h.json1("servicediscovery", "CreateHttpNamespace", input), 200, "")
	if again := h.json1("servicediscovery", "CreateHttpNamespace", input); again.body["OperationId"] != first.body["OperationId"] {
		t.Fatalf("replayed operation %v, %v", again.body, first.body)
	}
	namespaces := h.json1("servicediscovery", "ListNamespaces", map[string]any{}).body["Namespaces"].([]any)
	id := str(namespaces[0].(map[string]any)["Id"])
	if !strings.HasPrefix(id, "ns-") || len(namespaces) != 1 {
		t.Fatalf("namespaces %v", namespaces)
	}
	updated := h.json1("servicediscovery", "UpdateHttpNamespace", map[string]any{"Id": id, "Namespace": map[string]any{"Description": "d"}})
	if updated.body["OperationId"] == first.body["OperationId"] || updated.body["OperationId"] == nil {
		t.Fatalf("update operation %v", updated.body)
	}
	expect(t, h.json1("servicediscovery", "ListNamespaces", map[string]any{"Filters": []any{map[string]any{"Name": "TYPE", "Values": []any{"HTTP"}}}}), 400, "")
	expect(t, h.json1("servicediscovery", "CreateService", map[string]any{"Name": "web"}), 501, "UnknownOperationException")
	expect(t, h.json1("servicediscovery", "DeleteNamespace", map[string]any{"Id": id}), 200, "")
	expect(t, h.json1("servicediscovery", "GetNamespace", map[string]any{"Id": id}), 400, "NamespaceNotFound")
}

// Global services keep region-less ARNs in one partition-wide scope.
func TestGlobalNetworkServicesShareOneScope(t *testing.T) {
	h := networkHarness(t)
	created := h.json1("globalaccelerator", "CreateAccelerator", map[string]any{"Name": "a", "Enabled": false, "IdempotencyToken": "t-1"})
	expect(t, created, 200, "")
	arn := str(created.body["Accelerator"].(map[string]any)["AcceleratorArn"])
	if !strings.HasPrefix(arn, "arn:aws:globalaccelerator::000000000000:accelerator/") {
		t.Fatalf("accelerator ARN %s", arn)
	}
	data := []byte(`{"AcceleratorArn":"` + arn + `"}`)
	req := newJSONRequest(h, "globalaccelerator", "DescribeAccelerator", data, "ap-northeast-1")
	expect(t, h.do(req), 200, "")
	expect(t, h.json1("globalaccelerator", "DescribeCustomRoutingAccelerator", map[string]any{"AcceleratorArn": arn}), 400, "AcceleratorNotFoundException")
	cluster := h.rest("route53-recovery-control-config", "POST", "/cluster", map[string]any{"ClusterName": "c"})
	expect(t, cluster, 200, "")
	if clusterArn := str(cluster.body["Cluster"].(map[string]any)["ClusterArn"]); !strings.HasPrefix(clusterArn, "arn:aws:route53-recovery-control::000000000000:cluster/") {
		t.Fatalf("cluster ARN %s", clusterArn)
	}
	network := h.rest("networkmanager", "POST", "/global-networks", map[string]any{})
	expect(t, network, 200, "")
	gn := network.body["GlobalNetwork"].(map[string]any)
	if id := str(gn["GlobalNetworkId"]); !strings.HasPrefix(id, "global-network-") || gn["GlobalNetworkArn"] != "arn:aws:networkmanager::000000000000:global-network/"+id {
		t.Fatalf("global network %v", gn)
	}
	expect(t, h.rest("networkmanager", "GET", "/global-networks?globalNetworkIds=global-network-00000000000000000", nil), 404, "ResourceNotFoundException")
}

// Lattice listeners take the documented default ports and are deleted with
// their service.
func TestLatticeListenersFollowTheirService(t *testing.T) {
	h := networkHarness(t)
	service := h.rest("vpc-lattice", "POST", "/services", map[string]any{"name": "web"})
	expect(t, service, 201, "")
	id := str(service.body["id"])
	listener := h.rest("vpc-lattice", "POST", "/services/"+id+"/listeners", map[string]any{"name": "tls", "protocol": "HTTPS", "defaultAction": map[string]any{"fixedResponse": map[string]any{"statusCode": 404}}})
	expect(t, listener, 201, "")
	if asText(listener.body["port"]) != "443" || !strings.HasPrefix(str(listener.body["arn"]), "arn:aws:vpc-lattice:us-east-1:000000000000:service/"+id+"/listener/listener-") {
		t.Fatalf("listener %v", listener.body)
	}
	expect(t, h.rest("vpc-lattice", "GET", "/targetgroups?vpcIdentifier=vpc-12345678", nil), 400, "ValidationException")
	expect(t, h.rest("vpc-lattice", "DELETE", "/services/"+id, nil), 200, "")
	var rows int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service='vpc-lattice'").Scan(&rows)
	if rows != 0 {
		t.Fatalf("service deletion left %d rows", rows)
	}
}

// AppSync APIs delete their children and expose no endpoints.
func TestAppSyncApisCascadeAndServeNoEndpoints(t *testing.T) {
	h := networkHarness(t)
	api := h.rest("appsync", "POST", "/v1/apis", map[string]any{"name": "shop", "authenticationType": "API_KEY"})
	expect(t, api, 200, "")
	graphql := api.body["graphqlApi"].(map[string]any)
	if graphql["uris"] != nil || len(str(graphql["apiId"])) != 26 {
		t.Fatalf("graphql API %v", graphql)
	}
	id := str(graphql["apiId"])
	expect(t, h.rest("appsync", "POST", "/v1/apis/"+id+"/datasources", map[string]any{"name": "none", "type": "NONE"}), 200, "")
	expect(t, h.rest("appsync", "GET", "/v1/apis?owner=CURRENT_ACCOUNT", nil), 400, "BadRequestException")
	expect(t, h.rest("appsync", "DELETE", "/v1/apis/"+id, nil), 200, "")
	expect(t, h.rest("appsync", "GET", "/v1/apis/"+id+"/datasources/none", nil), 404, "NotFoundException")
	expect(t, h.rest("appsync", "POST", "/v1/apis/"+id+"/apikeys", map[string]any{}), 501, "UnknownOperationException")
}

// Map data sources are derived from the style; unsupported updates and
// device farm list members are refused.
func TestLocationAndDeviceFarmBounds(t *testing.T) {
	h := networkHarness(t)
	created := h.rest("location", "POST", "/maps/v0/maps", map[string]any{"MapName": "m", "Configuration": map[string]any{"Style": "VectorHereExplore"}})
	expect(t, created, 200, "")
	if got := h.rest("location", "GET", "/maps/v0/maps/m", nil); got.body["DataSource"] != "Here" || got.body["Description"] != "" {
		t.Fatalf("map %v", got.body)
	}
	expect(t, h.rest("location", "PATCH", "/maps/v0/maps/m", map[string]any{"ConfigurationUpdate": map[string]any{"PoliticalView": "JPN"}}), 400, "ValidationException")
	expect(t, h.rest("location", "POST", "/metadata/v0/keys", map[string]any{"KeyName": "k", "Restrictions": map[string]any{"AllowActions": []any{"geo:GetMap*"}, "AllowResources": []any{"*"}}}), 501, "UnknownOperationException")
	project := h.json1("devicefarm", "CreateProject", map[string]any{"name": "p"})
	expect(t, project, 200, "")
	if p := project.body["project"].(map[string]any); asText(p["defaultJobTimeoutMinutes"]) != "150" || !strings.HasPrefix(str(p["arn"]), "arn:aws:devicefarm:us-east-1:000000000000:project:") {
		t.Fatalf("project %v", p)
	}
	expect(t, h.json1("devicefarm", "ListTestGridProjects", map[string]any{"maxResult": 5}), 400, "")
	expect(t, h.json1("devicefarm", "ScheduleRun", map[string]any{}), 501, "UnknownOperationException")
}
