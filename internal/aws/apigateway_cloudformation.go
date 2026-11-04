package aws

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

func stackAPIKind(kind string) bool {
	return strings.HasPrefix(kind, "AWS::ApiGateway::") || kind == "AWS::Lambda::Permission"
}
func stackGeneratedID(stack, logical string) string {
	sum := sha256.Sum256([]byte(stack + "\x00" + logical))
	return hex.EncodeToString(sum[:5])
}
func compileStackAPI(region, stackID, logical, kind string, p map[string]any, attrs map[string]string) (string, error) {
	fields := map[string][]string{
		"AWS::ApiGateway::RestApi":    {"Name", "Description", "EndpointConfiguration", "Tags"},
		"AWS::ApiGateway::Resource":   {"RestApiId", "ParentId", "PathPart"},
		"AWS::ApiGateway::Method":     {"RestApiId", "ResourceId", "HttpMethod", "AuthorizationType", "ApiKeyRequired", "RequestParameters", "Integration"},
		"AWS::ApiGateway::Deployment": {"RestApiId", "Description"},
		"AWS::ApiGateway::Stage":      {"RestApiId", "DeploymentId", "StageName", "Variables"},
		"AWS::Lambda::Permission":     {"FunctionName", "Action", "Principal", "SourceArn"},
	}
	allowed, ok := fields[kind]
	if !ok {
		return "", fmt.Errorf("unsupported resource type %s", kind)
	}
	for key := range p {
		found := false
		for _, v := range allowed {
			if key == v {
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("unsupported %s property %s", kind, key)
		}
	}
	id := stackGeneratedID(stackID, logical)
	switch kind {
	case "AWS::ApiGateway::RestApi":
		endpoint := object(p["EndpointConfiguration"])
		types, _ := endpoint["Types"].([]any)
		if len(endpoint) != 1 || len(types) != 1 || stringValue(types[0]) != "REGIONAL" {
			return "", fmt.Errorf("only explicit REGIONAL REST APIs are supported")
		}
		attrs[logical+".RootResourceId"] = stackGeneratedID(stackID, logical+"/root")
	case "AWS::ApiGateway::Resource":
		if stringValue(p["RestApiId"]) == "" || stringValue(p["ParentId"]) == "" || !apiPathPart.MatchString(stringValue(p["PathPart"])) {
			return "", fmt.Errorf("invalid API resource properties")
		}
	case "AWS::ApiGateway::Method":
		if stringValue(p["AuthorizationType"]) != "NONE" || !apiHTTPMethod(stringValue(p["HttpMethod"])) {
			return "", fmt.Errorf("only NONE API methods are supported")
		}
		if v, exists := p["ApiKeyRequired"]; exists && v != false && v != "false" {
			return "", fmt.Errorf("API keys unsupported")
		}
		integration := object(p["Integration"])
		for key := range integration {
			if key != "Type" && key != "IntegrationHttpMethod" && key != "Uri" && key != "RequestParameters" {
				return "", fmt.Errorf("unsupported integration property %s", key)
			}
		}
		if raw, exists := integration["RequestParameters"]; exists {
			bindings := object(raw)
			if len(bindings) != 1 || stringValue(bindings["integration.request.path.proxy"]) != "method.request.path.proxy" {
				return "", fmt.Errorf("unsupported integration request binding")
			}
			declared := object(p["RequestParameters"])
			if declared["method.request.path.proxy"] != true && declared["method.request.path.proxy"] != "true" {
				return "", fmt.Errorf("integration proxy binding requires declared method parameter")
			}
		}
		if stringValue(integration["Type"]) != "AWS_PROXY" || stringValue(integration["IntegrationHttpMethod"]) != "POST" {
			return "", fmt.Errorf("only POST AWS_PROXY integration supported")
		}
		id = stringValue(p["RestApiId"]) + "/" + stringValue(p["ResourceId"]) + "/" + stringValue(p["HttpMethod"])
	case "AWS::ApiGateway::Stage":
		id = stringValue(p["RestApiId"]) + "/" + stringValue(p["StageName"])
		if !apiStageName.MatchString(stringValue(p["StageName"])) {
			return "", fmt.Errorf("invalid stage name")
		}
	case "AWS::Lambda::Permission":
		name := stackPermissionFunction(region, stringValue(p["FunctionName"]))
		if !lambdaName.MatchString(name) || stringValue(p["Action"]) != "lambda:InvokeFunction" || stringValue(p["Principal"]) != "apigateway.amazonaws.com" || !localAPISource(region, stringValue(p["SourceArn"])) {
			return "", fmt.Errorf("unsupported Lambda permission")
		}
	}
	return id, nil
}
func stackPermissionFunction(region, name string) string {
	return strings.TrimPrefix(name, "arn:aws:lambda:"+region+":000000000000:function:")
}
func stackStringMap(v any) (map[string]string, error) {
	result := map[string]string{}
	if v == nil {
		return result, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected string map")
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expected string value")
		}
		result[k] = s
	}
	return result, nil
}
func stackAPITags(v any) (map[string]string, error) {
	result := map[string]string{}
	if v == nil {
		return result, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("Tags must be a list")
	}
	for _, raw := range list {
		m := object(raw)
		k := stringValue(m["Key"])
		if len(m) != 2 || k == "" {
			return nil, fmt.Errorf("invalid tag")
		}
		if _, exists := result[k]; exists {
			return nil, fmt.Errorf("duplicate tag")
		}
		value, ok := m["Value"].(string)
		if !ok {
			return nil, fmt.Errorf("tag values must be strings")
		}
		result[k] = value
	}
	return result, nil
}
func createStackAPI(ctx context.Context, tx *sql.Tx, record StackRecord, r StackResource) error {
	p := r.Properties
	api := stringValue(p["RestApiId"])

	switch r.Type {
	case "AWS::ApiGateway::RestApi":
		tags, err := stackAPITags(p["Tags"])
		if err != nil {
			return err
		}
		_, err = createAPITx(ctx, tx, record.Region, stringValue(p["Name"]), stringValue(p["Description"]), tags, r.PhysicalID, stackGeneratedID(record.ID, r.LogicalID+"/root"))
		return err
	case "AWS::ApiGateway::Resource":
		_, err := createResourceTx(ctx, tx, record.Region, api, stringValue(p["ParentId"]), stringValue(p["PathPart"]), r.PhysicalID)
		return err
	case "AWS::ApiGateway::Method":
		required := map[string]bool{}
		if raw, exists := p["RequestParameters"]; exists {
			m, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("RequestParameters must be a map")
			}
			for k, v := range m {
				if v != true && v != "true" {
					return fmt.Errorf("unsupported request binding")
				}
				required[k] = true
			}
		}
		_, err := putMethodTx(ctx, tx, record.Region, api, stringValue(p["ResourceId"]), stringValue(p["HttpMethod"]), required)
		if err != nil {
			return err
		}
		_, err = putIntegrationTx(ctx, tx, record.Region, api, stringValue(p["ResourceId"]), stringValue(p["HttpMethod"]), stringValue(object(p["Integration"])["Uri"]))
		return err
	case "AWS::ApiGateway::Deployment":
		_, err := createDeploymentTx(ctx, tx, record.Region, api, stringValue(p["Description"]), r.PhysicalID)
		return err
	case "AWS::ApiGateway::Stage":
		variables, err := stackStringMap(p["Variables"])
		if err != nil {
			return err
		}
		_, err = createStageTx(ctx, tx, record.Region, api, stringValue(p["StageName"]), stringValue(p["DeploymentId"]), variables)
		return err
	case "AWS::Lambda::Permission":
		_, err := addTx(ctx, tx, record.Region, stackPermissionFunction(record.Region, stringValue(p["FunctionName"])), r.PhysicalID, stringValue(p["Principal"]), stringValue(p["Action"]), stringValue(p["SourceArn"]), "")
		return err
	}
	return fmt.Errorf("unsupported API resource")
}
func deleteStackAPI(ctx context.Context, tx *sql.Tx, region string, r StackResource) error {
	p := r.Properties
	api := stringValue(p["RestApiId"])
	if r.Type != "AWS::Lambda::Permission" {
		parent := api
		if r.Type == "AWS::ApiGateway::RestApi" {
			parent = r.PhysicalID
		}
		if _, err := apiRead(ctx, tx, region, parent); serviceMissing(err) {
			return nil
		} else if err != nil {
			return err
		}
	}

	switch r.Type {
	case "AWS::ApiGateway::RestApi":
		return deleteAPITx(ctx, tx, region, r.PhysicalID)
	case "AWS::ApiGateway::Stage":
		_, err := tx.ExecContext(ctx, "DELETE FROM aws_apigateway_stages WHERE region=? AND api_id=? AND name=?", region, api, stringValue(p["StageName"]))
		return err
	case "AWS::ApiGateway::Deployment":
		_, err := tx.ExecContext(ctx, "DELETE FROM aws_apigateway_deployments WHERE region=? AND api_id=? AND id=?", region, api, r.PhysicalID)
		return err
	case "AWS::ApiGateway::Resource":
		_, err := tx.ExecContext(ctx, "DELETE FROM aws_apigateway_resources WHERE region=? AND api_id=? AND id=?", region, api, r.PhysicalID)
		return err
	case "AWS::ApiGateway::Method":
		resource, err := apiResourceRead(ctx, tx, region, api, stringValue(p["ResourceId"]))
		if serviceMissing(err) {
			return nil
		}
		if err != nil {
			return err
		}
		delete(resource.ResourceMethods, stringValue(p["HttpMethod"]))
		data, _ := json.Marshal(resource)
		_, err = tx.ExecContext(ctx, "UPDATE aws_apigateway_resources SET document=? WHERE region=? AND api_id=? AND id=?", string(data), region, api, resource.ID)
		return err
	case "AWS::Lambda::Permission":
		err := removeTx(ctx, tx, region, stackPermissionFunction(region, stringValue(p["FunctionName"])), r.PhysicalID, "")
		if serviceMissing(err) {
			return nil
		}
		return err
	}
	return fmt.Errorf("unsupported API resource")
}

func claimStackAPI(tx *sql.Tx, record StackRecord, r StackResource) error {
	if r.Type != "AWS::ApiGateway::RestApi" && r.Type != "AWS::Lambda::Permission" {
		found := false
		for _, parent := range record.Resources {
			if parent.Type == "AWS::ApiGateway::RestApi" && parent.PhysicalID == stringValue(r.Properties["RestApiId"]) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("CFN API children must belong to an API created by the same stack")
		}
	}
	_, err := tx.Exec("INSERT INTO aws_cfn_ownership VALUES(?,?,?,?)", stackResourceNamespace(record.Region, r.Type), r.Type, r.PhysicalID, record.ID)
	return err
}
func stackAPIPermissionARN(region string, r StackResource, deleting bool) string {
	path := "/restapis"
	api := stringValue(r.Properties["RestApiId"])
	switch r.Type {
	case "AWS::ApiGateway::RestApi":
		if deleting {
			path += "/" + r.PhysicalID
		}
	case "AWS::ApiGateway::Resource":
		path += "/" + api + "/resources/" + stringValue(r.Properties["ParentId"])
		if deleting {
			path = "/restapis/" + api + "/resources/" + r.PhysicalID
		}
	case "AWS::ApiGateway::Method":
		path += "/" + api + "/resources/" + stringValue(r.Properties["ResourceId"]) + "/methods/" + stringValue(r.Properties["HttpMethod"])
	case "AWS::ApiGateway::Deployment":
		path += "/" + api + "/deployments"
		if deleting {
			path += "/" + r.PhysicalID
		}
	case "AWS::ApiGateway::Stage":
		path += "/" + api + "/stages"
		if deleting {
			path += "/" + stringValue(r.Properties["StageName"])
		}
	}
	return "arn:aws:apigateway:" + region + "::" + path
}

// Fingerprint all API rows, including native children, before cleanup. A changed
// method, new stage or foreign child must never be silently cascaded away.
func stackAPIHash(tx *sql.Tx, region string, r StackResource) (string, error) {
	h := sha256.New()
	if r.Type == "AWS::ApiGateway::RestApi" {
		parts, err := stackAPIRowFingerprints(tx, region, r.PhysicalID)
		if err != nil {
			return "", err
		}
		keys := []string{}
		for key := range parts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(h, "%s:%s", key, parts[key])
		}

	} else if r.Type == "AWS::Lambda::Permission" {
		policy, _, err := lambdaPolicyRead(context.Background(), tx, region, stackPermissionFunction(region, stringValue(r.Properties["FunctionName"])))
		if serviceMissing(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		found := false
		for _, statement := range policy.Statement {
			if statement.Sid == r.PhysicalID {
				data, _ := json.Marshal(statement)
				h.Write(data)
				found = true
			}
		}
		if !found {
			return "", nil
		}
	} else {
		return "", nil
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func saveStackAPIFingerprints(tx *sql.Tx, record *StackRecord) error {
	for i := range record.Resources {
		hash, err := stackAPIHash(tx, record.Region, record.Resources[i])
		if err != nil {
			return err
		}
		record.Resources[i].StateHash = hash
		if record.Resources[i].Type == "AWS::ApiGateway::RestApi" {
			parts, err := stackAPIRowFingerprints(tx, record.Region, record.Resources[i].PhysicalID)
			if err != nil {
				return err
			}
			record.Resources[i].StateParts = parts
		}
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE aws_cfn_stacks SET document=? WHERE id=?", string(data), record.ID)
	return err
}
func checkStackAPIFingerprints(tx *sql.Tx, record StackRecord) error {
	for _, r := range record.Resources {
		if r.Type == "AWS::ApiGateway::RestApi" && r.StateParts != nil {
			current, err := stackAPIRowFingerprints(tx, record.Region, r.PhysicalID)
			if err != nil {
				return err
			}
			for key, value := range current {
				if r.StateParts[key] != value {
					return ssmFailure{"ValidationError", "Stack API state changed or includes unowned rows; deletion refused."}
				}
			}
			continue
		}
		if r.StateHash == "" {
			continue
		}
		hash, err := stackAPIHash(tx, record.Region, r)
		if err != nil {
			return err
		}
		if hash != "" && hash != r.StateHash {
			return ssmFailure{"ValidationError", "Stack API state drifted; deletion refused to protect changed or unowned resources."}
		}
	}
	return nil
}

func serviceMissing(err error) bool {
	var failure ssmFailure
	return errors.As(err, &failure) && (failure.code == "NotFoundException" || failure.code == "ResourceNotFoundException")
}
func stackAPIRowFingerprints(tx *sql.Tx, region, id string) (map[string]string, error) {
	parts := map[string]string{}
	for index, query := range []string{
		"SELECT id,document FROM aws_apigateway_apis WHERE region=? AND id=? ORDER BY id",
		"SELECT id,document FROM aws_apigateway_resources WHERE region=? AND api_id=? ORDER BY id",
		"SELECT id,document || snapshot FROM aws_apigateway_deployments WHERE region=? AND api_id=? ORDER BY id",
		"SELECT name,document FROM aws_apigateway_stages WHERE region=? AND api_id=? ORDER BY name",
	} {
		rows, err := tx.Query(query, region, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var key, data string
			if err = rows.Scan(&key, &data); err != nil {
				rows.Close()
				return nil, err
			}
			sum := sha256.Sum256([]byte(data))
			parts[fmt.Sprintf("%d/%s", index, key)] = hex.EncodeToString(sum[:])
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if _, found := parts["0/"+id]; !found && len(parts) > 0 {
		return nil, fmt.Errorf("orphaned API state refuses cleanup")
	}
	return parts, nil
}
