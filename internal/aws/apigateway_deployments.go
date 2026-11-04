package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var apiStageName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var apiVariableName = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
var apiVariableValue = regexp.MustCompile(`^[A-Za-z0-9-._~:/?#&=,]+$`)

func (s *APIGatewayStore) PutIntegration(ctx context.Context, region, api, id, method, uri string) (RESTIntegration, error) {
	prefix := "arn:aws:apigateway:" + region + ":lambda:path/2015-03-31/functions/"
	suffix := "/invocations"
	if !strings.HasPrefix(uri, prefix) || !strings.HasSuffix(uri, suffix) {
		return RESTIntegration{}, apiFailure("BadRequestException", "Only local Lambda AWS_PROXY integration URIs are supported.")
	}
	target := strings.TrimSuffix(strings.TrimPrefix(uri, prefix), suffix)
	arnPrefix := "arn:aws:lambda:" + region + ":000000000000:function:"
	if !strings.HasPrefix(target, arnPrefix) || !lambdaName.MatchString(strings.TrimPrefix(target, arnPrefix)) {
		return RESTIntegration{}, apiFailure("BadRequestException", "Integration must target an unqualified local function in the same region.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTIntegration{}, err
	}
	defer tx.Rollback()
	resource, err := apiResourceRead(ctx, tx, region, api, id)
	if err != nil {
		return RESTIntegration{}, err
	}
	definition, ok := resource.ResourceMethods[method]
	if !ok {
		return RESTIntegration{}, apiFailure("NotFoundException", "Method does not exist.")
	}
	integration := RESTIntegration{Type: "AWS_PROXY", HTTPMethod: "POST", URI: uri, TimeoutInMillis: 29000}
	definition.MethodIntegration = &integration
	resource.ResourceMethods[method] = definition
	data, _ := json.Marshal(resource)
	if _, err = tx.ExecContext(ctx, "UPDATE aws_apigateway_resources SET document=? WHERE region=? AND api_id=? AND id=?", string(data), region, api, id); err != nil {
		return RESTIntegration{}, err
	}
	return integration, tx.Commit()
}
func (s *APIGatewayStore) CreateDeployment(ctx context.Context, region, api, description string) (RESTDeployment, error) {
	if len(description) > 1024 || !utf8.ValidString(description) {
		return RESTDeployment{}, apiFailure("BadRequestException", "Deployment description exceeds1024bytes.")
	}
	id, err := s3NewID()
	if err != nil {
		return RESTDeployment{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTDeployment{}, err
	}
	defer tx.Rollback()
	if _, err = apiRead(ctx, tx, region, api); err != nil {
		return RESTDeployment{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT document FROM aws_apigateway_resources WHERE region=? AND api_id=? ORDER BY path", region, api)
	if err != nil {
		return RESTDeployment{}, err
	}
	snapshot := []RESTResource{}
	methods := 0
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			break
		}
		var resource RESTResource
		if err = json.Unmarshal([]byte(data), &resource); err != nil {
			break
		}
		for _, method := range resource.ResourceMethods {
			methods++
			if method.MethodIntegration == nil {
				err = apiFailure("BadRequestException", "Every deployed method needs an integration.")
				break
			}
		}
		if err != nil {
			break
		}
		snapshot = append(snapshot, resource)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return RESTDeployment{}, err
	}
	if methods == 0 {
		return RESTDeployment{}, apiFailure("BadRequestException", "The API contains no methods.")
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_apigateway_deployments WHERE region=? AND api_id=?", region, api).Scan(&count); err != nil {
		return RESTDeployment{}, err
	}
	if count >= 100 {
		return RESTDeployment{}, apiFailure("LimitExceededException", "Local deployments are limited to100perAPI.")
	}
	deployment := RESTDeployment{ID: id[:10], CreatedDate: float64(time.Now().UnixMilli()) / 1000, Description: description}
	data, _ := json.Marshal(deployment)
	frozen, _ := json.Marshal(snapshot)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_apigateway_deployments(region,api_id,id,document,snapshot) VALUES(?,?,?,?,?)", region, api, deployment.ID, string(data), string(frozen)); err != nil {
		return RESTDeployment{}, err
	}
	return deployment, tx.Commit()
}
func apiDeploymentRead(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, region, api, id string) (RESTDeployment, []RESTResource, error) {
	var data, snapshot string
	err := query.QueryRowContext(ctx, "SELECT document,snapshot FROM aws_apigateway_deployments WHERE region=? AND api_id=? AND id=?", region, api, id).Scan(&data, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return RESTDeployment{}, nil, apiFailure("NotFoundException", "Deployment does not exist.")
	}
	if err != nil {
		return RESTDeployment{}, nil, err
	}
	var deployment RESTDeployment
	var resources []RESTResource
	if err = json.Unmarshal([]byte(data), &deployment); err != nil {
		return deployment, nil, err
	}
	err = json.Unmarshal([]byte(snapshot), &resources)
	return deployment, resources, err
}
func (s *APIGatewayStore) Deployment(ctx context.Context, region, api, id string) (RESTDeployment, []RESTResource, error) {
	return apiDeploymentRead(ctx, s.db, region, api, id)
}
func (s *APIGatewayStore) CreateStage(ctx context.Context, region, api, name, deployment string, variables map[string]string) (RESTStage, error) {
	if !apiStageName.MatchString(name) || len(variables) > 20 {
		return RESTStage{}, apiFailure("BadRequestException", "Use a local stage name and at most20variables.")
	}
	bytes := 0
	for key, value := range variables {
		if !apiVariableName.MatchString(key) || !apiVariableValue.MatchString(value) {
			return RESTStage{}, apiFailure("BadRequestException", "Invalid stage variable name.")
		}
		bytes += len(key) + len(value)
	}
	if bytes > 4096 {
		return RESTStage{}, apiFailure("BadRequestException", "Stage variables exceed4096bytes.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTStage{}, err
	}
	defer tx.Rollback()
	if _, _, err = apiDeploymentRead(ctx, tx, region, api, deployment); err != nil {
		return RESTStage{}, err
	}
	var count, exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),count(CASE WHEN name=? THEN 1 END) FROM aws_apigateway_stages WHERE region=? AND api_id=?", name, region, api).Scan(&count, &exists); err != nil {
		return RESTStage{}, err
	}
	if exists != 0 {
		return RESTStage{}, apiFailure("ConflictException", "Stage already exists.")
	}
	if count >= 10 {
		return RESTStage{}, apiFailure("LimitExceededException", "Local stages are limited to10perAPI.")
	}
	now := float64(time.Now().UnixMilli()) / 1000
	stage := RESTStage{StageName: name, DeploymentID: deployment, Variables: variables, CreatedDate: now, LastUpdatedDate: now}
	data, _ := json.Marshal(stage)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_apigateway_stages(region,api_id,name,deployment_id,document) VALUES(?,?,?,?,?)", region, api, name, deployment, string(data)); err != nil {
		return RESTStage{}, err
	}
	return stage, tx.Commit()
}
func (s *APIGatewayStore) Stage(ctx context.Context, region, api, name string) (RESTStage, error) {
	var data string
	err := s.db.QueryRowContext(ctx, "SELECT document FROM aws_apigateway_stages WHERE region=? AND api_id=? AND name=?", region, api, name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return RESTStage{}, apiFailure("NotFoundException", "Stage does not exist.")
	}
	if err != nil {
		return RESTStage{}, err
	}
	var stage RESTStage
	err = json.Unmarshal([]byte(data), &stage)
	return stage, err
}
func (s *APIGatewayStore) DeleteStage(ctx context.Context, region, api, name string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM aws_apigateway_stages WHERE region=? AND api_id=? AND name=?", region, api, name)
	if err != nil {
		return wafDeletionError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return apiFailure("NotFoundException", "Stage does not exist.")
	}
	return nil
}
