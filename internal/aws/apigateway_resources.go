package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

var apiPathPart = regexp.MustCompile(`^(?:[a-zA-Z0-9._-]+|\{[a-zA-Z0-9_]+\+?\})$`)

func apiResourceRead(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, region, api, id string) (RESTResource, error) {
	var data string
	err := query.QueryRowContext(ctx, "SELECT document FROM aws_apigateway_resources WHERE region=? AND api_id=? AND id=?", region, api, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return RESTResource{}, apiFailure("NotFoundException", "API resource does not exist.")
	}
	if err != nil {
		return RESTResource{}, err
	}
	var resource RESTResource
	if err = json.Unmarshal([]byte(data), &resource); err != nil {
		return resource, err
	}
	return resource, nil
}
func (s *APIGatewayStore) Resource(ctx context.Context, region, api, id string) (RESTResource, error) {
	return apiResourceRead(ctx, s.db, region, api, id)
}
func (s *APIGatewayStore) Resources(ctx context.Context, region, api, after string, limit int) ([]RESTResource, error) {
	if _, err := s.API(ctx, region, api); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT document FROM aws_apigateway_resources WHERE region=? AND api_id=? AND id>? ORDER BY id LIMIT ?", region, api, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RESTResource{}
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var resource RESTResource
		if err = json.Unmarshal([]byte(data), &resource); err != nil {
			return nil, err
		}
		result = append(result, resource)
	}
	return result, rows.Err()
}
func (s *APIGatewayStore) CreateResource(ctx context.Context, region, api, parent, part string) (RESTResource, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTResource{}, err
	}
	defer tx.Rollback()
	result, err := createResourceTx(ctx, tx, region, api, parent, part, "")
	if err != nil {
		return RESTResource{}, err
	}
	return result, tx.Commit()
}
func createResourceTx(ctx context.Context, tx *sql.Tx, region, api, parent, part string, idOverride string) (RESTResource, error) {
	if !apiPathPart.MatchString(part) || len(part) > 128 || part == "." || part == ".." {
		return RESTResource{}, apiFailure("BadRequestException", "Unsupported API resource path part.")
	}
	id, err := s3NewID()
	if err != nil {
		return RESTResource{}, err
	}
	if _, err = apiRead(ctx, tx, region, api); err != nil {
		return RESTResource{}, err
	}
	owner, err := apiResourceRead(ctx, tx, region, api, parent)
	if err != nil {
		return RESTResource{}, err
	}
	if strings.HasSuffix(owner.PathPart, "+}") {
		return RESTResource{}, apiFailure("BadRequestException", "A greedy resource cannot have descendants.")
	}
	path := strings.TrimSuffix(owner.Path, "/") + "/" + part
	if len(path) > 1024 {
		return RESTResource{}, apiFailure("BadRequestException", "Local resource paths are limited to1024bytes.")
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_apigateway_resources WHERE region=? AND api_id=?", region, api).Scan(&count); err != nil {
		return RESTResource{}, err
	}
	if count >= 100 {
		return RESTResource{}, apiFailure("LimitExceededException", "The local resource limit is100perAPI.")
	}
	rows, err := tx.QueryContext(ctx, "SELECT document FROM aws_apigateway_resources WHERE region=? AND api_id=? AND parent_id=?", region, api, parent)
	if err != nil {
		return RESTResource{}, err
	}
	conflict := false
	for rows.Next() {
		var data string
		if err = rows.Scan(&data); err != nil {
			break
		}
		var child RESTResource
		if err = json.Unmarshal([]byte(data), &child); err != nil {
			break
		}
		if child.PathPart == part || strings.HasPrefix(child.PathPart, "{") && strings.HasPrefix(part, "{") {
			conflict = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return RESTResource{}, err
	}
	if conflict {
		return RESTResource{}, apiFailure("ConflictException", "A matching or variable sibling resource already exists.")
	}
	if idOverride != "" {
		if !apiLocalID.MatchString(idOverride) {
			return RESTResource{}, apiFailure("BadRequestException", "Invalid provisioned API identifier.")
		}
		id = idOverride
	}
	resource := RESTResource{ID: id[:10], ParentID: parent, PathPart: part, Path: path}
	data, _ := json.Marshal(resource)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_apigateway_resources(region,api_id,id,parent_id,path,document) VALUES(?,?,?,?,?,?)", region, api, resource.ID, parent, path, string(data)); err != nil {
		return RESTResource{}, err
	}
	return resource, nil
}
func (s *APIGatewayStore) DeleteResource(ctx context.Context, region, api, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deleteResourceTx(ctx, tx, region, api, id); err != nil {
		return err
	}
	return tx.Commit()
}
func deleteResourceTx(ctx context.Context, tx *sql.Tx, region, api, id string) error {
	var err error
	resource, err := apiResourceRead(ctx, tx, region, api, id)
	if err != nil {
		return err
	}
	if resource.ParentID == "" {
		return apiFailure("BadRequestException", "The API root resource cannot be deleted.")
	}
	// Bound descendants by stored hierarchy, not LIKE text (where '_' is a wildcard).
	_, err = tx.ExecContext(ctx, `WITH RECURSIVE subtree(id) AS (
 SELECT id FROM aws_apigateway_resources WHERE region=? AND api_id=? AND id=?
 UNION ALL SELECT child.id FROM aws_apigateway_resources child JOIN subtree parent ON child.parent_id=parent.id WHERE child.region=? AND child.api_id=?)
 DELETE FROM aws_apigateway_resources WHERE region=? AND api_id=? AND id IN (SELECT id FROM subtree)`, region, api, id, region, api, region, api)
	if err != nil {
		return err
	}
	return nil
}
func apiHTTPMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "ANY":
		return true
	}
	return false
}
func (s *APIGatewayStore) PutMethod(ctx context.Context, region, api, id, method string, required map[string]bool) (RESTMethod, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RESTMethod{}, err
	}
	defer tx.Rollback()
	result, err := putMethodTx(ctx, tx, region, api, id, method, required)
	if err != nil {
		return RESTMethod{}, err
	}
	return result, tx.Commit()
}
func putMethodTx(ctx context.Context, tx *sql.Tx, region, api, id, method string, required map[string]bool) (RESTMethod, error) {
	var err error
	if !apiHTTPMethod(method) {
		return RESTMethod{}, apiFailure("BadRequestException", "Unsupported HTTP method.")
	}
	resource, err := apiResourceRead(ctx, tx, region, api, id)
	if err != nil {
		return RESTMethod{}, err
	}
	for name, value := range required {
		if name != "method.request.path.proxy" || !value || resource.PathPart != "{proxy+}" {
			return RESTMethod{}, apiFailure("BadRequestException", "Only the declared greedy path parameter binding is supported.")
		}
	}
	if resource.ResourceMethods == nil {
		resource.ResourceMethods = map[string]RESTMethod{}
	}
	if _, exists := resource.ResourceMethods[method]; exists {
		return RESTMethod{}, apiFailure("ConflictException", "Method already exists.")
	}
	result := RESTMethod{HTTPMethod: method, AuthorizationType: "NONE", RequestParameters: required}
	resource.ResourceMethods[method] = result
	data, _ := json.Marshal(resource)
	if _, err = tx.ExecContext(ctx, "UPDATE aws_apigateway_resources SET document=? WHERE region=? AND api_id=? AND id=?", string(data), region, api, id); err != nil {
		return RESTMethod{}, err
	}
	return result, nil
}
