package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/legal"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

//go:embed web/* apidocs/*
var assets embed.FS

func New(path string) (http.Handler, func() error, error) {
	store, err := core.New(path)
	if err != nil {
		return nil, nil, err
	}
	s3, err := aws.NewS3Store(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	identities := core.NewIdentities(store)
	parameters, err := aws.NewParameterStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	queues, err := aws.NewSQSStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	secrets, err := aws.NewSecretStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	stacks, err := aws.NewStackStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	lambdas, err := aws.NewLambdaStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	roles, err := aws.NewIAMRoleStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	apiGateway, err := aws.NewAPIGatewayStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	lambdaPermissions, err := aws.NewLambdaPermissionStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	gateway := &aws.Gateway{APIGateway: apiGateway, LambdaPermissions: lambdaPermissions, Store: store, S3: s3, Identities: identities, Parameters: parameters, Queues: queues, Secrets: secrets, Stacks: stacks, Lambdas: lambdas, Roles: roles}
	registry := provider.New("aws")
	if err := registry.Register("aws", gateway); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/_simulator/licenses/", http.StripPrefix("/_simulator/licenses/", legal.Handler()))
	mux.Handle(provider.Prefix, registry)
	docs, _ := fs.Sub(assets, "apidocs")
	mux.Handle("/_simulator/docs/", http.StripPrefix("/_simulator/docs/", http.FileServer(http.FS(docs))))
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/_simulator/console/", http.StripPrefix("/_simulator/console/", http.FileServer(http.FS(web))))
	mux.HandleFunc("/_simulator/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","simulator":true}`))
	})
	mux.HandleFunc("/_simulator/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "2026-11-03", "principal": r.Header.Get("X-Simulator-Principal"), "identitySelection": r.Header.Get("X-Simulator-Access-Role") != "participant", "simulatorVersion": "0.1.0", "providers": registry.Names(), "persistence": func() string {
			if path == "" || path == ":memory:" {
				return "memory"
			}
			return "file"
		}(), "provider": "aws", "service": "s3", "services": map[string]any{"s3": map[string]any{"operations": []string{"ListBuckets", "CreateBucket", "HeadBucket", "GetBucketLocation", "DeleteBucket", "ListObjectsV2", "PutObject", "GetObject", "HeadObject", "DeleteObject", "GetBucketVersioning", "PutBucketVersioning", "ListObjectVersions", "GetPublicAccessBlock", "PutPublicAccessBlock", "DeletePublicAccessBlock", "GetBucketTagging", "PutBucketTagging", "DeleteBucketTagging"}}, "iam": map[string]any{"operations": []string{"CreateRole", "GetRole", "ListRoles", "DeleteRole", "PutRolePolicy", "GetRolePolicy", "DeleteRolePolicy", "ListRolePolicies", "UpdateAssumeRolePolicy"}, "scope": "global", "rolePath": "/", "roleNamePattern": "[a-zA-Z0-9_-]{1,64}", "roleClaimsLimit": 1000, "inlinePoliciesPerRole": 10, "trustPolicyBytes": 32 << 10, "aggregatePolicyBytes": 32 << 10, "servicePrincipalTrust": true, "sts": false, "managedPolicies": false}, "ssm": map[string]any{"operations": []string{"PutParameter", "GetParameter", "GetParameters", "GetParametersByPath", "DescribeParameters", "DeleteParameter"}, "types": []string{"String", "StringList"}, "tier": "Standard", "parameterLimit": 1000}, "secretsmanager": map[string]any{"operations": []string{"CreateSecret", "DescribeSecret", "ListSecrets", "PutSecretValue", "GetSecretValue", "UpdateSecretVersionStage", "DeleteSecret"}, "protocol": "JSON 1.1 (X-Amz-Target secretsmanager.*)", "encryption": "none; dummy plaintext in private SQLite, not AWS KMS", "secretLimit": 100, "versionsPerSecret": 100, "valueBytes": 65536, "storedValueBytes": 64 << 20, "retiredARNLimit": 10000, "versionStages": []string{"AWSCURRENT", "AWSPREVIOUS"}, "deletion": "full ARN and ForceDeleteWithoutRecovery=true; immediate local removal", "unsupported": []string{"KMS encryption", "SecretBinary", "custom labels", "tags", "resource policies", "rotation", "replication", "recovery windows", "CloudFormation"}}, "sqs": map[string]any{"operations": []string{"CreateQueue", "ListQueues", "GetQueueUrl", "DeleteQueue", "SendMessage", "ReceiveMessage", "DeleteMessage", "ChangeMessageVisibility", "GetQueueAttributes"}, "protocol": "JSON 1.0 (X-Amz-Target AmazonSQS.*)", "queueTypes": []string{"Standard"}, "queueLimit": 1000, "messageBytes": 262144, "defaultVisibilityTimeoutSeconds": 30, "unsupported": []string{"FIFO queues", "Query protocol", "dead-letter redrive policies", "SSE/KMS settings", "batch operations (SendMessageBatch/DeleteMessageBatch/ChangeMessageVisibilityBatch)"}}, "lambda": map[string]any{"operations": []string{"CreateFunction", "ListFunctions", "GetFunctionConfiguration", "UpdateFunctionCode", "Invoke", "DeleteFunction", "AddPermission", "GetPolicy", "RemovePermission"}, "runtime": "python3.12", "interpreter": "CPython 3.12.16/WASI", "memoryMiB": 128, "maxTimeoutSeconds": 30, "functionLimit": 100, "codeBytes": 256 << 10, "expandedCodeBytes": 1 << 20, "eventBytes": 256 << 10, "concurrentSandboxes": 2, "executionRoleSDK": map[string]any{"ssm": []string{"GetParameter", "GetParameters"}, "s3": []string{"GetObject", "PutObject", "DeleteObject"}, "region": "function region", "authority": "saved execution role", "arbitraryHTTP": false, "objectBodyBytes": 32 << 10, "nativeResponseBytes": 48 << 10, "messageBytes": 64 << 10, "maxCalls": 128, "aggregateBytes": 1 << 20}}, "apigateway": map[string]any{"operations": []string{"CreateRestApi", "GetRestApis", "GetRestApi", "DeleteRestApi", "GetResources", "CreateResource", "GetResource", "DeleteResource", "PutMethod", "GetMethod", "PutIntegration", "CreateDeployment", "GetDeployment", "CreateStage", "GetStage", "DeleteStage"}, "endpointTypes": []string{"REGIONAL"}, "authorizationTypes": []string{"NONE"}, "integrationTypes": []string{"AWS_PROXY"}, "apiLimit": 100, "resourcesPerAPI": 100, "deploymentsPerAPI": 100, "stagesPerAPI": 10, "integrationDeadlineSeconds": 29, "proxyEventBytes": 256 << 10, "browserAppHosting": false, "responseMode": "non-executing attachment", "cloudFormation": true}, "cloudformation": map[string]any{"operations": []string{"CreateStack", "DescribeStacks", "DeleteStack"}, "resourceTypes": []string{"AWS::SSM::Parameter", "AWS::IAM::Role", "AWS::S3::Bucket", "AWS::Lambda::Function", "AWS::ApiGateway::RestApi", "AWS::ApiGateway::Resource", "AWS::ApiGateway::Method", "AWS::ApiGateway::Deployment", "AWS::ApiGateway::Stage", "AWS::Lambda::Permission"}, "stackLimit": 100, "resourceLimitPerStack": 100}}, "operations": []string{"ListBuckets", "CreateBucket", "HeadBucket", "GetBucketLocation", "DeleteBucket", "ListObjectsV2", "PutObject", "GetObject", "HeadObject", "DeleteObject", "GetBucketVersioning", "PutBucketVersioning", "ListObjectVersions", "GetPublicAccessBlock", "PutPublicAccessBlock", "DeletePublicAccessBlock", "GetBucketTagging", "PutBucketTagging", "DeleteBucketTagging"}, "regions": aws.Regions, "limits": map[string]int{"objectBytes": core.MaxObjectBytes, "storedBytes": core.MaxStoredBytes, "buckets": core.MaxBuckets, "objects": core.MaxObjects}, "state": "SQLite; ephemeral by default, persist with --state; one database per environment", "authentication": "CLI requires opaque local access tokens by default; participant tokens bind an exercise identity. SDK wire uses dummy TCSIM credentials, without signature verification. server.New is an unauthenticated composition API; hosts must wrap NewAccess. One process/database/token authority per team.", "unsupported": []string{"general resource policies, ACLs, conditions, boundaries, SCPs, sessions (only bounded API Gateway Lambda SourceArn resource grants are supported)", "multipart uploads", "presigned URLs", "virtual-host addressing", "services outside the declared S3/SSM/SQS/Secrets Manager/Lambda/CloudFormation/IAM/API Gateway subset", "STS, AWS federation, managed-policy permissions, advanced trust policies", "legacy TenkaCloud world/deploy/grading protocol; grading stays in the host"}})
	})
	mux.Handle("/_simulator/v1/iam/", identityAPI(identities, roles))
	mux.Handle("/", registry)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-TenkaCloud-Simulator", "true")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; font-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; sandbox allow-scripts allow-same-origin allow-downloads allow-forms")
		if strings.HasPrefix(r.URL.Path, "/_simulator/docs/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "Cross-origin requests are not allowed", 403)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(w, "Cross-site requests are not allowed", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_simulator/v1/") && r.URL.Path != "/_simulator/v1/capabilities" && !strings.HasPrefix(r.URL.Path, "/_simulator/v1/iam/") {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/_simulator/") || strings.HasPrefix(r.URL.Path, provider.Prefix) {
			registry.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return handler, func() error {
		runtimeErr := lambdas.Close()
		storeErr := store.Close()
		if runtimeErr != nil {
			return runtimeErr
		}
		return storeErr
	}, nil
}
