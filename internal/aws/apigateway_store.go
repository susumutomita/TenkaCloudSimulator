// Copyright (c) 2025 sivchari
// SPDX-License-Identifier: MIT
// Selected REST wire fields adapted from Kumo ce37a8fb406ecb96b5619204f645c26302b651a5,
// internal/service/apigateway/types.go. Original notice: third_party/KUMO-LICENSE.txt.
// SQLite storage, quotas, ownership and deployment snapshots are local adapters.
package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Provider-owned models hold the bounded local REST state. Explicit projections
// below define the public and persisted JSON representation from the AWS REST
// resource contracts; storage and admission do not depend on reflection tags.
type APIGatewayStore struct{ db *sql.DB }
type RESTAPI struct {
	ID                    string
	Name                  string
	Description           string
	CreatedDate           float64
	RootResourceID        string
	Tags                  map[string]string
	EndpointConfiguration RESTEndpoint
}
type RESTEndpoint struct {
	Types []string
}
type RESTResource struct {
	ID              string
	ParentID        string
	PathPart        string
	Path            string
	ResourceMethods map[string]RESTMethod
}
type RESTMethod struct {
	RequestParameters map[string]bool
	HTTPMethod        string
	AuthorizationType string
	APIKeyRequired    bool
	MethodIntegration *RESTIntegration
}
type RESTIntegration struct {
	Type            string
	HTTPMethod      string
	URI             string
	TimeoutInMillis int
}
type RESTDeployment struct {
	ID          string
	CreatedDate float64
	Description string
}
type RESTStage struct {
	StageName       string
	DeploymentID    string
	CreatedDate     float64
	LastUpdatedDate float64
	Variables       map[string]string
}

// All supported spellings also fold to the provider field names, so the standard
// JSON decoder still reads existing SQLite documents without migration. It keeps
// unknown-field handling under the caller's control, including strict requests.
// https://docs.aws.amazon.com/apigateway/latest/api/API_GetRestApi.html
func (api RESTAPI) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"id", api.ID, false}, awsOwnedWireField{"name", api.Name, false},
		awsOwnedWireField{"description", api.Description, api.Description == ""},
		awsOwnedWireField{"createdDate", api.CreatedDate, false},
		awsOwnedWireField{"rootResourceId", api.RootResourceID, false},
		awsOwnedWireField{"tags", api.Tags, len(api.Tags) == 0},
		awsOwnedWireField{"endpointConfiguration", api.EndpointConfiguration, false})
}
func (endpoint RESTEndpoint) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(awsOwnedWireField{"types", endpoint.Types, false})
}

// https://docs.aws.amazon.com/apigateway/latest/api/API_GetResource.html
func (resource RESTResource) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"id", resource.ID, false},
		awsOwnedWireField{"parentId", resource.ParentID, resource.ParentID == ""},
		awsOwnedWireField{"pathPart", resource.PathPart, resource.PathPart == ""},
		awsOwnedWireField{"path", resource.Path, false},
		awsOwnedWireField{"resourceMethods", resource.ResourceMethods, len(resource.ResourceMethods) == 0})
}

// https://docs.aws.amazon.com/apigateway/latest/api/API_GetMethod.html
func (method RESTMethod) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"requestParameters", method.RequestParameters, len(method.RequestParameters) == 0},
		awsOwnedWireField{"httpMethod", method.HTTPMethod, false},
		awsOwnedWireField{"authorizationType", method.AuthorizationType, false},
		awsOwnedWireField{"apiKeyRequired", method.APIKeyRequired, false},
		awsOwnedWireField{"methodIntegration", method.MethodIntegration, method.MethodIntegration == nil})
}
func (integration RESTIntegration) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"type", integration.Type, false},
		awsOwnedWireField{"httpMethod", integration.HTTPMethod, false},
		awsOwnedWireField{"uri", integration.URI, false},
		awsOwnedWireField{"timeoutInMillis", integration.TimeoutInMillis, false})
}

// https://docs.aws.amazon.com/apigateway/latest/api/API_GetDeployment.html
func (deployment RESTDeployment) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"id", deployment.ID, false},
		awsOwnedWireField{"createdDate", deployment.CreatedDate, false},
		awsOwnedWireField{"description", deployment.Description, deployment.Description == ""})
}

// https://docs.aws.amazon.com/apigateway/latest/api/API_GetStage.html
func (stage RESTStage) MarshalJSON() ([]byte, error) {
	return marshalAWSOwnedWireObject(
		awsOwnedWireField{"stageName", stage.StageName, false},
		awsOwnedWireField{"deploymentId", stage.DeploymentID, false},
		awsOwnedWireField{"createdDate", stage.CreatedDate, false},
		awsOwnedWireField{"lastUpdatedDate", stage.LastUpdatedDate, false},
		awsOwnedWireField{"variables", stage.Variables, len(stage.Variables) == 0})
}

func NewAPIGatewayStore(db *sql.DB) (*APIGatewayStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_apigateway_apis (
 region TEXT NOT NULL,id TEXT NOT NULL,document TEXT NOT NULL,PRIMARY KEY(region,id));
 CREATE TABLE IF NOT EXISTS aws_apigateway_resources (
 region TEXT NOT NULL,api_id TEXT NOT NULL,id TEXT NOT NULL,parent_id TEXT,path TEXT NOT NULL,document TEXT NOT NULL,
 PRIMARY KEY(region,api_id,id),UNIQUE(region,api_id,path),
 FOREIGN KEY(region,api_id) REFERENCES aws_apigateway_apis(region,id) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS aws_apigateway_deployments (
 region TEXT NOT NULL,api_id TEXT NOT NULL,id TEXT NOT NULL,document TEXT NOT NULL,snapshot TEXT NOT NULL,
 PRIMARY KEY(region,api_id,id),FOREIGN KEY(region,api_id) REFERENCES aws_apigateway_apis(region,id) ON DELETE CASCADE);
 CREATE TABLE IF NOT EXISTS aws_apigateway_stages (
 region TEXT NOT NULL,api_id TEXT NOT NULL,name TEXT NOT NULL,deployment_id TEXT NOT NULL,document TEXT NOT NULL,
 PRIMARY KEY(region,api_id,name),FOREIGN KEY(region,api_id,deployment_id) REFERENCES aws_apigateway_deployments(region,api_id,id));`)
	return &APIGatewayStore{db: db}, err
}
func apiFailure(code, message string) error { return ssmFailure{code, message} }
func apiRead(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, region, id string) (RESTAPI, error) {
	var data string
	err := query.QueryRowContext(ctx, "SELECT document FROM aws_apigateway_apis WHERE region=? AND id=?", region, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return RESTAPI{}, apiFailure("NotFoundException", "REST API does not exist in this region.")
	}
	if err != nil {
		return RESTAPI{}, err
	}
	var api RESTAPI
	if err = json.Unmarshal([]byte(data), &api); err != nil {
		return RESTAPI{}, err
	}
	if api.ID != id || api.RootResourceID == "" || !slices.Equal(api.EndpointConfiguration.Types, []string{"REGIONAL"}) {
		return RESTAPI{}, fmt.Errorf("invalid persisted REST API metadata")
	}
	return api, nil
}
func (s *APIGatewayStore) API(ctx context.Context, region, id string) (RESTAPI, error) {
	return apiRead(ctx, s.db, region, id)
}
func (s *APIGatewayStore) CreateAPI(ctx context.Context, region, name, description string, tags map[string]string) (RESTAPI, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTAPI{}, err
	}
	defer tx.Rollback()
	result, err := createAPITx(ctx, tx, region, name, description, tags, "", "")
	if err != nil {
		return RESTAPI{}, err
	}
	return result, tx.Commit()
}
func createAPITx(ctx context.Context, tx *sql.Tx, region, name, description string, tags map[string]string, idOverride, rootOverride string) (RESTAPI, error) {
	if !slices.Contains(Regions, region) || strings.TrimSpace(name) == "" || len(name) > 128 || !utf8.ValidString(name) || len(description) > 1024 || !utf8.ValidString(description) || len(tags) > 50 {
		return RESTAPI{}, apiFailure("BadRequestException", "Use the documented regional REST API name, description and tag limits.")
	}
	for key, value := range tags {
		if key == "" || len(key) > 128 || len(value) > 256 || !utf8.ValidString(key) || !utf8.ValidString(value) || strings.HasPrefix(strings.ToLower(key), "aws:") {
			return RESTAPI{}, apiFailure("BadRequestException", "Invalid or reserved API tag.")
		}
	}
	id, err := s3NewID()
	if err != nil {
		return RESTAPI{}, err
	}
	root, err := s3NewID()
	if err != nil {
		return RESTAPI{}, err
	}
	if idOverride != "" {
		if !apiLocalID.MatchString(idOverride) {
			return RESTAPI{}, apiFailure("BadRequestException", "Invalid provisioned API identifier.")
		}
		id = idOverride
	}
	if rootOverride != "" {
		if !apiLocalID.MatchString(rootOverride) {
			return RESTAPI{}, apiFailure("BadRequestException", "Invalid provisioned root identifier.")
		}
		root = rootOverride
	}
	api := RESTAPI{ID: id[:10], Name: name, Description: description, RootResourceID: root[:10], CreatedDate: float64(time.Now().UnixMilli()) / 1000, Tags: tags, EndpointConfiguration: RESTEndpoint{Types: []string{"REGIONAL"}}}
	document, _ := json.Marshal(api)
	resource, _ := json.Marshal(RESTResource{ID: api.RootResourceID, Path: "/"})
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_apigateway_apis").Scan(&count); err != nil {
		return RESTAPI{}, err
	}
	if count >= 100 {
		return RESTAPI{}, apiFailure("LimitExceededException", "The local REST API limit is100.")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_apigateway_apis(region,id,document) VALUES(?,?,?)", region, api.ID, string(document)); err != nil {
		return RESTAPI{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_apigateway_resources(region,api_id,id,parent_id,path,document) VALUES(?,?,?,NULL,'/',?)", region, api.ID, api.RootResourceID, string(resource)); err != nil {
		return RESTAPI{}, err
	}
	return api, nil
}
func (s *APIGatewayStore) APIs(ctx context.Context, region, after string, limit int) ([]RESTAPI, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT document FROM aws_apigateway_apis WHERE region=? AND id>? ORDER BY id LIMIT ?", region, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RESTAPI{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var api RESTAPI
		if err = json.Unmarshal([]byte(data), &api); err != nil {
			return nil, err
		}
		result = append(result, api)
	}
	return result, rows.Err()
}
func (s *APIGatewayStore) DeleteAPI(ctx context.Context, region, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deleteAPITx(ctx, tx, region, id); err != nil {
		return err
	}
	return tx.Commit()
}
func deleteAPITx(ctx context.Context, tx *sql.Tx, region, id string) error {
	var err error
	if _, err = apiRead(ctx, tx, region, id); err != nil {
		return err
	}
	// Stages are explicitly removed first; their immutable snapshot FK is strict.
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_apigateway_stages WHERE region=? AND api_id=?", region, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_apigateway_apis WHERE region=? AND id=?", region, id); err != nil {
		return err
	}
	return nil
}
