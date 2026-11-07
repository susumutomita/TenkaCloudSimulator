package catalog

import (
	"encoding/json"
	"strings"
)

// Reviewed bounded specifications for developer-tool and application services.
// Each binds only official operations whose effect is configuration metadata.

var codeConnections = &Service{
	Model: "codeconnections", Title: "AWS CodeConnections", IAMPrefix: "codeconnections", Issues: []int{297},
	Resources: []*Resource{
		{Kind: "connection", Shape: "Connection", ID: "uuid", ARN: "arn:{partition}:codeconnections:{region}:{account}:connection/{id}",
			ARNMember: "ConnectionArn", NameMember: "ConnectionName", Limit: 250,
			Generate: map[string]any{"ConnectionArn": "$arn", "OwnerAccountId": "$account", "ConnectionStatus": "PENDING"},
			Refs:     map[string]string{"HostArn": "host"}},
		{Kind: "host", Shape: "Host", ID: "hex:12", ARN: "arn:{partition}:codeconnections:{region}:{account}:host/{name}/{id}",
			ARNMember: "HostArn", NameMember: "Name", Unique: true, Limit: 100,
			Generate: map[string]any{"HostArn": "$arn", "Status": "PENDING"}},
	},
	Ops: []Op{
		{Name: "CreateConnection", Kind: Create, Resource: "connection", Check: connectionProvider},
		{Name: "GetConnection", Kind: Read, Resource: "connection", Lookup: "ConnectionArn"},
		{Name: "ListConnections", Kind: List, Resource: "connection", Filters: map[string]string{"ProviderTypeFilter": "ProviderType", "HostArnFilter": "HostArn"}},
		{Name: "DeleteConnection", Kind: Delete, Resource: "connection", Lookup: "ConnectionArn"},
		{Name: "CreateHost", Kind: Create, Resource: "host"},
		{Name: "GetHost", Kind: Read, Resource: "host", Lookup: "HostArn"},
		{Name: "ListHosts", Kind: List, Resource: "host"},
		{Name: "UpdateHost", Kind: Update, Resource: "host", Lookup: "HostArn", Fields: []string{"ProviderEndpoint", "VpcConfiguration"}},
		{Name: "DeleteHost", Kind: Delete, Resource: "host", Lookup: "HostArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "ResourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "ResourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "ResourceArn"},
	},
	Bounds: []string{
		"Connections and hosts stay PENDING: no OAuth handshake, provider app installation or repository access is performed.",
		"Connection resource links, repository sync configurations and CodeStar Connections legacy endpoints are not implemented.",
	},
}

// connectionProvider enforces the documented rule that a connection names
// either a provider type or an installed host, never neither.
func connectionProvider(input map[string]any) error {
	_, provider := input["ProviderType"]
	_, host := input["HostArn"]
	if provider == host {
		return Failure("ValidationException", "Specify exactly one of ProviderType or HostArn.")
	}
	return nil
}

var stepFunctions = &Service{
	Model: "stepfunctions", Title: "AWS Step Functions", IAMPrefix: "states", Issues: []int{83},
	Resources: []*Resource{
		{Kind: "stateMachine", Shape: "DescribeStateMachineOutput", Summary: "StateMachineListItem", ID: "name",
			ARN:       "arn:{partition}:states:{region}:{account}:stateMachine:{name}",
			ARNMember: "stateMachineArn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"stateMachineArn": "$arn", "status": "ACTIVE", "type": "STANDARD", "creationDate": "$now", "revisionId": "$uuid"}},
		{Kind: "activity", Shape: "DescribeActivityOutput", Summary: "ActivityListItem", ID: "name",
			ARN:       "arn:{partition}:states:{region}:{account}:activity:{name}",
			ARNMember: "activityArn", NameMember: "name", Unique: true, Limit: 100,
			Generate: map[string]any{"activityArn": "$arn", "creationDate": "$now"}},
	},
	Ops: []Op{
		{Name: "CreateStateMachine", Kind: Create, Resource: "stateMachine", Check: stateMachineDefinition},
		{Name: "DescribeStateMachine", Kind: Read, Resource: "stateMachine", Lookup: "stateMachineArn"},
		{Name: "ListStateMachines", Kind: List, Resource: "stateMachine"},
		{Name: "UpdateStateMachine", Kind: Update, Resource: "stateMachine", Lookup: "stateMachineArn",
			Fields: []string{"definition", "roleArn", "loggingConfiguration", "tracingConfiguration", "encryptionConfiguration"},
			Touch:  []string{"updateDate"}, Set: map[string]any{"revisionId": "$uuid"}, Check: stateMachineDefinition},
		{Name: "DeleteStateMachine", Kind: Delete, Resource: "stateMachine", Lookup: "stateMachineArn"},
		{Name: "CreateActivity", Kind: Create, Resource: "activity"},
		{Name: "DescribeActivity", Kind: Read, Resource: "activity", Lookup: "activityArn"},
		{Name: "ListActivities", Kind: List, Resource: "activity"},
		{Name: "DeleteActivity", Kind: Delete, Resource: "activity", Lookup: "activityArn"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"State machine definitions are stored after a structural Amazon States Language check; executions, versions, aliases and activity tasks are not run.",
		"StartExecution, StartSyncExecution and other execution APIs return UnknownOperationException with HTTP 501.",
	},
}

// stateMachineDefinition applies the structural ASL rules that a stored
// definition must satisfy: a JSON object whose StartAt names a state.
func stateMachineDefinition(input map[string]any) error {
	text, ok := input["definition"].(string)
	if !ok {
		return nil
	}
	var definition struct {
		StartAt string                     `json:"StartAt"`
		States  map[string]json.RawMessage `json:"States"`
	}
	if err := json.Unmarshal([]byte(text), &definition); err != nil || definition.StartAt == "" || len(definition.States) == 0 {
		return Failure("InvalidDefinition", "The definition must be an Amazon States Language object with StartAt and States.")
	}
	if _, ok := definition.States[definition.StartAt]; !ok {
		return Failure("InvalidDefinition", "StartAt must name a state in States.")
	}
	for name, raw := range definition.States {
		var state struct {
			Type string `json:"Type"`
		}
		if json.Unmarshal(raw, &state) != nil || !strings.Contains("|Task|Pass|Choice|Wait|Succeed|Fail|Parallel|Map|", "|"+state.Type+"|") || state.Type == "" {
			return Failure("InvalidDefinition", "State "+name+" has no valid Type.")
		}
	}
	return nil
}

var ecr = &Service{
	Model: "ecr", Title: "Amazon Elastic Container Registry", IAMPrefix: "ecr", Issues: []int{111},
	AccountMembers: []string{"registryId"},
	Resources: []*Resource{
		{Kind: "repository", Shape: "Repository", ID: "name", ARN: "arn:{partition}:ecr:{region}:{account}:repository/{name}",
			ARNMember: "repositoryArn", NameMember: "repositoryName", Unique: true, Limit: 100,
			Generate: map[string]any{"repositoryArn": "$arn", "registryId": "$account", "createdAt": "$now",
				"repositoryUri":              "$fmt:{account}.dkr.ecr.{region}.amazonaws.com/{name}",
				"imageTagMutability":         "MUTABLE",
				"imageScanningConfiguration": map[string]any{"scanOnPush": false},
				"encryptionConfiguration":    map[string]any{"encryptionType": "AES256"}}},
	},
	Ops: []Op{
		{Name: "CreateRepository", Kind: Create, Resource: "repository"},
		{Name: "DescribeRepositories", Kind: List, Resource: "repository", Names: "repositoryNames"},
		{Name: "PutImageTagMutability", Kind: Update, Resource: "repository", Lookup: "repositoryName", Fields: []string{"imageTagMutability", "imageTagMutabilityExclusionFilters"}},
		{Name: "PutImageScanningConfiguration", Kind: Update, Resource: "repository", Lookup: "repositoryName", Fields: []string{"imageScanningConfiguration"}},
		{Name: "DeleteRepository", Kind: Delete, Resource: "repository", Lookup: "repositoryName", Accept: []string{"force"}},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Repositories hold no images: image push/pull, layers, registry authorization tokens and scanning are not implemented; force therefore has no further effect.",
	},
}

var amplify = &Service{
	Model: "amplify", Title: "AWS Amplify", IAMPrefix: "amplify", Issues: []int{147},
	Resources: []*Resource{
		{Kind: "app", Shape: "App", ID: "alnum:13:d", ARN: "arn:{partition}:amplify:{region}:{account}:apps/{id}",
			ARNMember: "appArn", NameMember: "name", IDMember: "appId", Limit: 25,
			Generate: map[string]any{"appId": "$id", "appArn": "$arn", "createTime": "$now", "updateTime": "$now",
				"description": "", "repository": "", "platform": "WEB", "environmentVariables": map[string]any{},
				"defaultDomain": "$fmt:{id}.amplifyapp.com", "enableBranchAutoBuild": false, "enableBasicAuth": false}},
		{Kind: "branch", Shape: "Branch", Parent: "app", ParentMember: "appId", ID: "name",
			ARN:       "arn:{partition}:amplify:{region}:{account}:apps/{parent}/branches/{name}",
			ARNMember: "branchArn", NameMember: "branchName", Unique: true, Limit: 50,
			Generate: map[string]any{"branchArn": "$arn", "createTime": "$now", "updateTime": "$now", "description": "",
				"displayName": "$name", "enableNotification": false, "environmentVariables": map[string]any{},
				"enableAutoBuild": true, "customDomains": []any{}, "framework": "", "activeJobId": "", "totalNumberOfJobs": "0",
				"enableBasicAuth": false, "ttl": "5", "enablePullRequestPreview": false}},
	},
	Ops: []Op{
		{Name: "CreateApp", Kind: Create, Resource: "app", Check: noBasicAuth},
		{Name: "GetApp", Kind: Read, Resource: "app", Lookup: "appId"},
		{Name: "ListApps", Kind: List, Resource: "app"},
		{Name: "UpdateApp", Kind: Update, Resource: "app", Lookup: "appId", Touch: []string{"updateTime"},
			Fields: []string{"name", "description", "platform", "environmentVariables", "enableBranchAutoBuild", "enableBranchAutoDeletion", "customRules", "buildSpec", "customHeaders", "enableAutoBranchCreation", "autoBranchCreationPatterns"}},
		{Name: "DeleteApp", Kind: Delete, Resource: "app", Lookup: "appId"},
		{Name: "CreateBranch", Kind: Create, Resource: "branch", Parent: "appId", Check: noBasicAuth},
		{Name: "GetBranch", Kind: Read, Resource: "branch", Parent: "appId", Lookup: "branchName"},
		{Name: "ListBranches", Kind: List, Resource: "branch", Parent: "appId"},
		{Name: "UpdateBranch", Kind: Update, Resource: "branch", Parent: "appId", Lookup: "branchName", Touch: []string{"updateTime"},
			Fields: []string{"description", "framework", "stage", "enableNotification", "enableAutoBuild", "environmentVariables", "buildSpec", "ttl", "displayName", "enablePullRequestPreview"}},
		{Name: "DeleteBranch", Kind: Delete, Resource: "branch", Parent: "appId", Lookup: "branchName"},
		{Name: "TagResource", Kind: Tag, Lookup: "resourceArn"},
		{Name: "UntagResource", Kind: Untag, Lookup: "resourceArn"},
		{Name: "ListTagsForResource", Kind: ListTags, Lookup: "resourceArn"},
	},
	Bounds: []string{
		"Apps and branches are configuration metadata: no repository connection, OAuth or access token, build, deployment, hosting or domain is created. oauthToken, accessToken and basic-auth credentials are refused.",
		"Deleting an app with branches is refused until the branches are deleted.",
	},
}

// noBasicAuth refuses stored credentials: the simulator never keeps
// basic-auth secrets for hosting it does not provide.
func noBasicAuth(input map[string]any) error {
	if _, ok := input["basicAuthCredentials"]; ok {
		return Failure("BadRequestException", "Basic-auth credentials are not stored by the simulator.")
	}
	if enabled, _ := input["enableBasicAuth"].(bool); enabled {
		return Failure("BadRequestException", "Basic authentication requires hosting, which the simulator does not provide.")
	}
	// Auto-branch settings would copy the same credentials into new branches.
	if nested, ok := input["autoBranchCreationConfig"].(map[string]any); ok {
		return noBasicAuth(nested)
	}
	return nil
}
