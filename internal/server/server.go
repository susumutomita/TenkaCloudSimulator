package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/legal"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
	"github.com/susumutomita/TenkaCloudSimulator/internal/sakura"
)

//go:embed web/* apidocs/*
var assets embed.FS

func New(path string) (http.Handler, func() error, error) {
	return NewWithCloudStorage(path, DefaultCloudStorageConfig())
}

// NewWithCloudStorage composes bounded providers; hosts must wrap NewAccess.
func NewWithCloudStorage(path string, cloudConfig CloudStorageConfig) (http.Handler, func() error, error) {
	return NewWithProviders(path, ProviderConfig{CloudStorage: cloudConfig})
}

// NewWithProviders is trusted composition; callers must still wrap NewAccess.
func NewWithProviders(path string, options ProviderConfig) (http.Handler, func() error, error) {
	sakuraScope, err := validatedSakuraScope(options.SakuraDisks)
	if err != nil {
		return nil, nil, err
	}
	cloudConfig := options.CloudStorage
	store, err := core.New(path)
	if err != nil {
		return nil, nil, err
	}
	var sakuraHandler http.Handler
	if sakuraScope != nil {
		sakuraHandler, err = sakura.NewProvider(store.Database(), *sakuraScope)
		if err != nil {
			_ = store.Close()
			return nil, nil, err
		}
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
	documents, err := aws.NewSSMDocumentStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	snsTopics, err := aws.NewSNSStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	queues, err := aws.NewSQSStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	keys, err := aws.NewKMSStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	secrets, err := aws.NewSecretStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	dynamodb, err := aws.NewDynamoDBStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	stacks, err := aws.NewStackStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	if options.configureInstances != nil {
		if err = options.configureInstances(store.Database(), stacks); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
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
	waf, err := aws.NewWAFStore(store.Database())
	if err != nil {
		_ = lambdas.Close()
		_ = store.Close()
		return nil, nil, err
	}
	lambdaPermissions, err := aws.NewLambdaPermissionStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	cloudWatchLogs, err := aws.NewCloudWatchLogsStore(store.Database())
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	route53, err := aws.NewRoute53Store(store.Database())
	if err != nil {
		_ = lambdas.Close()
		_ = store.Close()
		return nil, nil, err
	}
	catalogEngine, err := catalog.New(store.Database(), catalog.Config{Regions: aws.Regions, Account: "000000000000", DefaultPrincipal: core.Administrator, Authorize: identities.DecisionContext}, catalog.Services())
	if err != nil {
		_ = lambdas.Close()
		_ = store.Close()
		return nil, nil, err
	}
	gateway := &aws.Gateway{Route53: route53, WAF: waf, SNS: snsTopics, Catalog: catalogEngine, CatalogOwns: catalogEngine.Owns, CloudWatchLogs: cloudWatchLogs, Secrets: secrets, DynamoDB: dynamodb, KMS: keys, APIGateway: apiGateway, LambdaPermissions: lambdaPermissions, Store: store, S3: s3, Identities: identities, Parameters: parameters, Documents: documents, Queues: queues, Stacks: stacks, Lambdas: lambdas, Roles: roles}
	if options.commandAPI != nil {
		gateway.Commands = options.commandAPI()
	}
	if terminal, ok := options.terminalSessions.(*terminalHandler); ok {
		gateway.Sessions = terminal.store
	}
	registry := provider.New("aws")
	if err := registry.Register("aws", gateway); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	if sakuraHandler != nil {
		if err = registry.Register(sakura.ProviderName, sakuraHandler); err != nil {
			_ = lambdas.Close()
			_ = store.Close()
			return nil, nil, err
		}
	}
	gcpCatalog, err := registerCloudStorage(store.Database(), registry, cloudConfig)
	if err != nil {
		_ = lambdas.Close()
		_ = store.Close()
		return nil, nil, err
	}
	azureCatalog, err := registerAzureARM(store.Database(), registry, cloudConfig)
	if err != nil {
		_ = lambdas.Close()
		_ = store.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	if options.battleCapabilities != nil {
		mux.Handle("/_simulator/v1/battle-capabilities/", options.battleCapabilities)
	}
	if options.terminalSessions != nil {
		mux.Handle("/_simulator/v1/terminal-control", options.terminalSessions)
		mux.Handle("/_simulator/v1/terminal-channel/", options.terminalSessions)
	}
	mux.HandleFunc("/_simulator/", serveLanding)
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
		if gateway.Commands != nil {
			w.Header().Set("X-Simulator-Command-Protocol", "tcs-command-v1")
		}
		if options.terminalSessions != nil {
			w.Header().Set("X-Simulator-Terminal-Protocol", "tcs1-websocket-v1")
		}
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(addConsoleHandoffCapabilities(w, r, addSakuraCapabilities(map[string]any{"protocolVersion": "2026-11-03", "principal": r.Header.Get("X-Simulator-Principal"), "identitySelection": r.Header.Get("X-Simulator-Access-Role") != "participant", "simulatorVersion": "0.1.0", "providers": registry.Names(), "cloudStorage": cloudStorageCapabilities(cloudConfig), "gcpCatalog": gcpCatalogCapabilities(gcpCatalog, cloudConfig), "azureCatalog": azureCatalogCapabilities(azureCatalog), "persistence": func() string {
			if path == "" || path == ":memory:" {
				return "memory"
			}
			return "file"
		}(), "provider": "aws", "service": "s3", "services": map[string]any{"route53": map[string]any{"operations": []string{"CreateHostedZone", "GetHostedZone", "ListHostedZones", "DeleteHostedZone", "ChangeResourceRecordSets", "ListResourceRecordSets", "GetChange", "TestDNSAnswer"}, "protocol": "REST/XML 2013-04-01", "publicZonesOnly": true, "zoneLimit": 100, "recordSetsPerZone": 1000, "changesPerBatch": 100, "retainedChanges": 10000, "answers": "local saved state only; no external DNS", "privateZones": false, "console": false, "cloudFormation": false}, "sns": map[string]any{"operations": []string{"CreateTopic", "ListTopics", "DeleteTopic", "GetTopicAttributes", "Subscribe", "Unsubscribe", "ListSubscriptions", "ListSubscriptionsByTopic", "Publish"}, "protocol": "Query/XML 2010-03-31", "topics": 1000, "subscriptions": 10000, "storedMessages": 10000, "storedMessageBytes": 64 << 20, "deliveryRows": 100000, "delivery": "atomic local SQS fanout and audit records; no outbound delivery", "sqsFanout": true, "sqsFanoutAuthorization": "caller sqs:SendMessage on every destination; resource policies unsupported", "console": false, "fifoPublication": false, "deletion": "atomic topic/subscription/message/delivery cleanup"}, "catalog": catalogCapabilities(catalogEngine), "dynamodb": map[string]any{"operations": []string{"CreateTable", "ListTables", "DescribeTable", "DeleteTable", "PutItem", "GetItem", "DeleteItem"}, "itemBytes": 400 << 10, "tableLimit": 100, "itemsPerTable": 10000, "indexes": false, "conditions": "attribute_exists/attribute_not_exists of the partition key only"}, "secretsmanager": map[string]any{"operations": []string{"CreateSecret", "DescribeSecret", "UpdateSecret", "ListSecrets", "ListSecretVersionIds", "PutSecretValue", "GetSecretValue", "UpdateSecretVersionStage", "DeleteSecret"}, "protocol": "JSON 1.1 (X-Amz-Target secretsmanager.*)", "updateSecret": "Description only; empty clears; no value, KMS key, stage or rotation changes", "encryption": "none; dummy plaintext in private SQLite, not AWS KMS", "secretLimit": 100, "versionsPerSecret": 100, "valueBytes": 65536, "storedValueBytes": 64 << 20, "retiredARNLimit": 10000, "versionStages": []string{"AWSCURRENT", "AWSPREVIOUS"}, "deletion": "full ARN and ForceDeleteWithoutRecovery=true; immediate local removal", "unsupported": []string{"KMS encryption", "SecretBinary", "custom labels", "tags", "resource policies", "rotation", "replication", "recovery windows", "CloudFormation"}}, "s3": map[string]any{"operations": []string{"ListBuckets", "CreateBucket", "HeadBucket", "GetBucketLocation", "DeleteBucket", "ListObjectsV2", "PutObject", "GetObject", "HeadObject", "DeleteObject", "GetBucketVersioning", "PutBucketVersioning", "ListObjectVersions", "GetPublicAccessBlock", "PutPublicAccessBlock", "DeletePublicAccessBlock", "GetBucketTagging", "PutBucketTagging", "DeleteBucketTagging", "CreateMultipartUpload", "UploadPart", "ListParts", "ListMultipartUploads", "CompleteMultipartUpload", "AbortMultipartUpload"}}, "iam": map[string]any{"operations": []string{"CreateRole", "GetRole", "ListRoles", "DeleteRole", "PutRolePolicy", "GetRolePolicy", "DeleteRolePolicy", "ListRolePolicies", "UpdateAssumeRolePolicy", "CreatePolicy", "GetPolicy", "ListPolicies", "DeletePolicy", "CreatePolicyVersion", "GetPolicyVersion", "ListPolicyVersions", "SetDefaultPolicyVersion", "DeletePolicyVersion", "AttachRolePolicy", "DetachRolePolicy", "ListAttachedRolePolicies", "CreateInstanceProfile", "GetInstanceProfile", "ListInstanceProfiles", "DeleteInstanceProfile", "AddRoleToInstanceProfile", "RemoveRoleFromInstanceProfile", "ListInstanceProfilesForRole"}, "instanceProfiles": true, "instanceProfilePath": "/", "instanceProfileClaimsLimit": 1000, "rolesPerInstanceProfile": 1, "instanceProfileCredentials": false, "instanceProfileCloudFormation": true, "instanceProfileConsole": false, "scope": "global", "rolePath": "/", "roleNamePattern": "[a-zA-Z0-9_-]{1,64}", "roleClaimsLimit": 1000, "inlinePoliciesPerRole": 10, "trustPolicyBytes": 32 << 10, "aggregatePolicyBytes": 32 << 10, "servicePrincipalTrust": true, "sts": false, "managedPolicies": true, "managedPolicyScope": "customer policies and one pinned read-only AWS builtin; ListPolicies requires explicit Scope=Local and excludes the builtin", "managedPolicyPath": "/", "managedPolicyClaimsLimit": 1000, "managedPoliciesPerRole": 20, "managedVersionsPerPolicy": 5, "managedDocumentBytes": 32 << 10, "managedCompactCharacters": 6144, "managedDefaultUpdate": "atomic pointer and all attached roles; inline and default managed union; explicit Deny wins", "awsManagedPolicies": true, "awsManagedPolicyCatalog": false, "awsManagedPolicyBuiltins": []map[string]any{{"arn": "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore", "defaultVersionId": "v2", "readOnly": true}}, "awsManagedPolicyRuntime": "permission documents only; no EC2, SSM agent, commands or sessions"}, "ssm": map[string]any{"runCommand": nativeCommandCapabilities(gateway.Commands != nil), "interactiveSession": nativeSessionCapabilities(gateway.Sessions != nil), "operations": []string{"PutParameter", "GetParameter", "GetParameters", "GetParametersByPath", "DescribeParameters", "DeleteParameter", "DeleteParameters", "CreateDocument", "GetDocument", "DescribeDocument", "ListDocuments", "UpdateDocument", "DeleteDocument"}, "types": []string{"String", "StringList"}, "tier": "Standard", "parameterLimit": 1000, "documentManagement": map[string]any{"operations": []string{"CreateDocument", "GetDocument", "DescribeDocument", "ListDocuments", "UpdateDocument", "DeleteDocument"}, "definitionOnly": true, "execution": false, "documentType": "Command", "documentFormat": "JSON", "schemaVersion": "2.2", "documentLimit": 100, "versionsPerDocument": 100, "storedContentBytes": 8388608, "contentBytes": 65536, "requestBytes": 524288, "defaultVersion": "1; not editable", "unsupported": []string{"execution of stored document definitions", "inventory", "sessions through document management", "YAML", "TEXT", "attachments", "tags", "sharing", "associations", "parameters/interpolation", "CloudFormation", "console"}}}, "logs": map[string]any{"operations": []string{"CreateLogGroup", "DescribeLogGroups", "DeleteLogGroup", "CreateLogStream", "DeleteLogStream", "DescribeLogStreams", "PutLogEvents", "GetLogEvents"}, "protocol": "JSON1.1 Logs_20140328", "logGroupClass": "STANDARD", "sequenceTokenIgnored": true, "groupLimit": 100, "streamLimit": 1000, "eventLimit": 100000, "storedMessageBytes": 64 << 20, "batchBytes": 1048576, "batchEvents": 10000, "cursorTTLHours": 24, "automaticLambdaDelivery": false, "cloudFormation": false, "metrics": false, "alarms": false, "unsupported": []string{"retention settings", "tags", "KMS/encryption", "non-Standard classes", "resource policies/cross-account", "data protection/unmask", "Logs Insights", "automatic Lambda delivery", "CloudFormation Logs resources"}}, "sqs": map[string]any{"operations": []string{"CreateQueue", "ListQueues", "GetQueueUrl", "DeleteQueue", "SendMessage", "ReceiveMessage", "DeleteMessage", "DeleteMessageBatch", "ChangeMessageVisibility", "GetQueueAttributes"}, "protocol": "JSON 1.0 (X-Amz-Target AmazonSQS.*)", "queueTypes": []string{"Standard"}, "queueLimit": 1000, "messageBytes": 262144, "defaultVisibilityTimeoutSeconds": 30, "deleteMessageBatchEntries": 10, "deleteMessageBatchIAMAction": "sqs:DeleteMessage", "deleteMessageBatchFailureMode": "per-entry receipt failures; atomic rollback on infrastructure failure", "unsupported": []string{"FIFO queues", "Query protocol", "dead-letter redrive policies", "SSE/KMS settings", "SendMessageBatch", "ChangeMessageVisibilityBatch"}}, "kms": map[string]any{"operations": []string{"CreateKey", "DescribeKey", "ListKeys", "EnableKey", "DisableKey", "GetKeyPolicy", "Encrypt", "Decrypt", "CreateAlias", "ListAliases", "UpdateAlias", "DeleteAlias"}, "protocol": "JSON 1.1 (X-Amz-Target TrentService.*)", "keySpecs": []string{"SYMMETRIC_DEFAULT"}, "keyStates": []string{"Enabled", "Disabled"}, "keyLimit": 1000, "requestBytes": 64 << 10, "plaintextBytes": 4096, "ciphertextBytes": 6144, "encryptionContextEntries": 100, "encryptionContextBytes": 8192, "descriptionCharacters": 8192, "ciphertextFormat": "local TCSIMKMS1; incompatible with AWS KMS", "keyMaterialStorage": "raw provider SQLite; not an HSM or at-rest encryption guarantee", "policy": "immutable default delegation through local identity policies", "aliasTypes": []string{"customer"}, "aliasLimit": 1000, "aliasesPerKey": 50, "aliasNameCharacters": 256, "aliasListDefault": 50, "aliasListMaximum": 100, "aliasCryptoIdentifiers": []string{"Encrypt", "Decrypt"}, "aliasTargets": "same-region/account concrete key UUID or ARN; Enabled/Disabled", "aliasPaging": "opaque persisted HMAC marker bound to region/filter/limit/revision; stale markers fail", "aliasConsistency": "immediate local SQLite; no key deletion/recreation lifecycle", "unsupported": []string{"AWS-managed/predefined aliases", "alias-based policy conditions", "alias identifiers for DescribeKey/EnableKey/DisableKey/GetKeyPolicy", "grants", "scheduled deletion", "rotation", "custom key policies", "asymmetric/HMAC/data keys", "CloudFormation and other-service encryption integration"}}, "lambda": map[string]any{"operations": []string{"CreateFunction", "ListFunctions", "GetFunctionConfiguration", "UpdateFunctionCode", "Invoke", "DeleteFunction", "AddPermission", "GetPolicy", "RemovePermission"}, "runtime": "python3.12", "interpreter": "CPython 3.12.16/WASI", "memoryMiB": 128, "maxTimeoutSeconds": 30, "functionLimit": 100, "codeBytes": 256 << 10, "expandedCodeBytes": 1 << 20, "eventBytes": 256 << 10, "concurrentSandboxes": 2, "invokeLogTail": map[string]any{"request": "explicit synchronous LogType=Tail only", "maxBytes": 4096, "encoding": "base64 of raw bytes; UTF-8 may be split", "ordering": "stdout then stderr, not interleaved", "internalFrames": false, "cloudWatchDelivery": false, "persistence": false}, "executionRoleSDK": map[string]any{"ssm": []string{"GetParameter", "GetParameters"}, "s3": []string{"GetObject", "PutObject", "DeleteObject"}, "region": "function region", "authority": "saved execution role", "arbitraryHTTP": false, "objectBodyBytes": 32 << 10, "nativeResponseBytes": 48 << 10, "messageBytes": 64 << 10, "maxCalls": 128, "aggregateBytes": 1 << 20}}, "apigateway": map[string]any{"operations": []string{"CreateRestApi", "GetRestApis", "GetRestApi", "DeleteRestApi", "GetResources", "CreateResource", "GetResource", "DeleteResource", "PutMethod", "GetMethod", "PutIntegration", "CreateDeployment", "GetDeployment", "CreateStage", "GetStage", "DeleteStage"}, "endpointTypes": []string{"REGIONAL"}, "authorizationTypes": []string{"NONE"}, "integrationTypes": []string{"AWS_PROXY"}, "apiLimit": 100, "resourcesPerAPI": 100, "deploymentsPerAPI": 100, "stagesPerAPI": 10, "integrationDeadlineSeconds": 29, "proxyEventBytes": 256 << 10, "browserAppHosting": false, "responseMode": "non-executing attachment", "cloudFormation": true}, "wafv2": map[string]any{"operations": []string{"CreateWebACL", "GetWebACL", "UpdateWebACL", "DeleteWebACL", "ListWebACLs", "AssociateWebACL", "DisassociateWebACL", "GetWebACLForResource", "ListResourcesForWebACL", "ListTagsForResource"}, "scope": "REGIONAL", "protectedResourceTypes": []string{"API_GATEWAY"}, "statements": []string{"AndStatement", "ByteMatchStatement", "SqliMatchStatement"}, "textTransformations": []string{"NONE", "URL_DECODE"}, "actions": []string{"Allow", "Block", "Count"}, "bodyInspectionBytes": 16384, "sqlInjectionDetector": "libinjection-go v0.3.5; not AWS detection parity", "sensitivityLevels": []string{"LOW"}, "tagMutations": false, "cloudWatchDelivery": false, "sampledRequests": false, "cloudFormation": false}, "cloudformation": map[string]any{"operations": []string{"CreateStack", "DescribeStacks", "DeleteStack"}, "resourceTypes": []string{"AWS::SSM::Parameter", "AWS::IAM::Role", "AWS::IAM::InstanceProfile", "AWS::S3::Bucket", "AWS::Lambda::Function", "AWS::ApiGateway::RestApi", "AWS::ApiGateway::Resource", "AWS::ApiGateway::Method", "AWS::ApiGateway::Deployment", "AWS::ApiGateway::Stage", "AWS::Lambda::Permission"}, "stackLimit": 100, "resourceLimitPerStack": 100}}, "operations": []string{"ListBuckets", "CreateBucket", "HeadBucket", "GetBucketLocation", "DeleteBucket", "ListObjectsV2", "PutObject", "GetObject", "HeadObject", "DeleteObject", "GetBucketVersioning", "PutBucketVersioning", "ListObjectVersions", "GetPublicAccessBlock", "PutPublicAccessBlock", "DeletePublicAccessBlock", "GetBucketTagging", "PutBucketTagging", "DeleteBucketTagging", "CreateMultipartUpload", "UploadPart", "ListParts", "ListMultipartUploads", "CompleteMultipartUpload", "AbortMultipartUpload"}, "regions": aws.Regions, "limits": map[string]int{"objectBytes": core.MaxObjectBytes, "storedBytes": core.MaxStoredBytes, "buckets": core.MaxBuckets, "objects": core.MaxObjects}, "state": "SQLite; ephemeral by default, persist with --state; one database per environment", "authentication": "CLI requires opaque local access tokens by default; participant tokens bind an exercise identity. Default exercise SDK wire uses dummy TCSIM credentials; native temporary assumed-role credentials require signature verification. server.New is an unauthenticated composition API; hosts must wrap NewAccess. One process/database/token authority per team.", "unsupported": []string{"general resource policies, ACLs, advanced conditions, boundaries, SCPs, general sessions (bounded native role and opt-in interactive session subsets are separate)", "presigned URLs", "virtual-host addressing", "AWS services outside the declared S3/SSM/SQS/Logs/Secrets Manager/DynamoDB/KMS/Lambda/CloudFormation/IAM/API Gateway subset and the reviewed services.catalog operations", "general STS and AWS federation; only the documented local AssumeRole subset is supported; arbitrary AWS-managed policies, user/group managed attachments, permission boundaries, advanced trust policies", "legacy TenkaCloud world/deploy/grading protocol; grading stays in the host"}}, sakuraScope)))
	})
	mux.HandleFunc(catalogDescriptorsPath, catalogDescriptors(catalogEngine))
	mux.HandleFunc(gcpCatalogDescriptorsPath, gcpCatalogDescriptors(gcpCatalog))
	mux.HandleFunc(azureCatalogDescriptorsPath, azureCatalogDescriptors(azureCatalog))
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
		if strings.HasPrefix(r.URL.Path, "/_simulator/v1/") && r.URL.Path != "/_simulator/v1/capabilities" && r.URL.Path != catalogDescriptorsPath && r.URL.Path != gcpCatalogDescriptorsPath && r.URL.Path != azureCatalogDescriptorsPath && !strings.HasPrefix(r.URL.Path, "/_simulator/v1/iam/") && !(options.battleCapabilities != nil && strings.HasPrefix(r.URL.Path, "/_simulator/v1/battle-capabilities/")) && !(options.terminalSessions != nil && (r.URL.Path == "/_simulator/v1/terminal-control" || strings.HasPrefix(r.URL.Path, "/_simulator/v1/terminal-channel/"))) {
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
		if options.closeInstances != nil {
			if err := options.closeInstances(); err != nil {
				return err
			}
		}
		runtimeErr := lambdas.Close()
		storeErr := store.Close()
		if runtimeErr != nil {
			return runtimeErr
		}
		return storeErr
	}, nil
}
