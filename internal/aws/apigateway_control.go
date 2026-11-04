package aws

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

func apiJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func apiError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServerErrorException", "Local REST API operation failed.", 500
	var failure ssmFailure
	if errors.As(err, &failure) {
		code, message = failure.code, failure.message
		switch code {
		case "BadRequestException":
			status = 400
		case "NotFoundException":
			status = 404
		case "ConflictException":
			status = 409
		case "LimitExceededException":
			status = 429
		case "AccessDeniedException":
			status = 403
		case "BadGatewayException":
			status = 502
		case "MissingAuthenticationTokenException":
			status = 403
		case "NotImplementedException":
			status = 501
		}
	}
	w.Header().Set("x-amzn-ErrorType", code)
	apiJSON(w, status, map[string]string{"message": message})
}
func apiDecode(w http.ResponseWriter, r *http.Request, target any, allowed ...string) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		return apiFailure("BadRequestException", "REST API request exceeds128KiB.")
	}
	if !utf8.Valid(data) || !iamTrustFields(data, allowed...) || iamStrictJSON(data, target) != nil {
		return apiFailure("BadRequestException", "Use the documented case-sensitive local REST API request fields.")
	}
	return nil
}

// serveControl is not registered yet. Its caller must first enforce local
// connection authentication and validate the APIGateway SDK scope/region. IAM
// authorization is required independently for each native management ARN.
func (s *APIGatewayStore) serveControl(w http.ResponseWriter, r *http.Request, region string, authorize func(action, resource string) bool) {
	if authorize == nil {
		apiError(w, apiFailure("AccessDeniedException", "Missing management authorization."))
		return
	}
	if r.URL.Path != "/restapis" && !strings.HasPrefix(r.URL.Path, "/restapis/") {
		apiError(w, apiFailure("NotImplementedException", "Unsupported REST API management path."))
		return
	}
	if !authorize("apigateway:"+r.Method, "arn:aws:apigateway:"+region+"::"+r.URL.Path) {
		apiError(w, apiFailure("AccessDeniedException", "Management action is denied."))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/restapis"), "/")[1:]
	if r.URL.Path == "/restapis" {
		parts = nil
	}
	query, err := parseAPIQuery(r)
	if err != nil {
		apiError(w, err)
		return
	}
	limit, position := 25, ""
	for key, value := range query {
		switch key {
		case "limit":
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 500 {
				apiError(w, apiFailure("BadRequestException", "Use a limit between1and500."))
				return
			}
			limit = n
		case "position":
			position = value
		default:
			apiError(w, apiFailure("BadRequestException", "Unsupported REST API query field."))
			return
		}
	}
	list := (len(parts) == 0 || len(parts) == 2 && parts[1] == "resources") && r.Method == "GET"
	if len(query) > 0 && !list {
		apiError(w, apiFailure("BadRequestException", "Pagination is only valid on supported list operations."))
		return
	}
	var result any
	status := 200
	ctx := r.Context()
	notImplemented := func() {
		apiError(w, apiFailure("NotImplementedException", "This REST API operation is not implemented."))
	}
	switch {
	case len(parts) == 0 && r.Method == "POST":
		var input struct {
			Name                  string            `json:"name"`
			Description           string            `json:"description"`
			Tags                  map[string]string `json:"tags"`
			EndpointConfiguration RESTEndpoint      `json:"endpointConfiguration"`
		}
		err = apiDecode(w, r, &input, "name", "description", "tags", "endpointConfiguration")
		if err == nil {
			if len(input.EndpointConfiguration.Types) != 1 || input.EndpointConfiguration.Types[0] != "REGIONAL" {
				err = apiFailure("BadRequestException", "Only explicit REGIONAL APIs are supported.")
			} else {
				result, err = s.CreateAPI(ctx, region, input.Name, input.Description, input.Tags)
				status = 201
			}
		}
	case len(parts) == 0 && r.Method == "GET":
		var items []RESTAPI
		items, err = s.APIs(ctx, region, position, limit+1)
		value := map[string]any{"item": items}
		if len(items) > limit {
			value["item"] = items[:limit]
			value["position"] = items[limit-1].ID
		}
		result = value
	case len(parts) == 1 && r.Method == "GET":
		result, err = s.API(ctx, region, parts[0])
	case len(parts) == 1 && r.Method == "DELETE":
		err = s.DeleteAPI(ctx, region, parts[0])
		status = 202
	case len(parts) == 2 && parts[1] == "resources" && r.Method == "GET":
		var items []RESTResource
		items, err = s.Resources(ctx, region, parts[0], position, limit+1)
		value := map[string]any{"item": items}
		if len(items) > limit {
			value["item"] = items[:limit]
			value["position"] = items[limit-1].ID
		}
		result = value
	case len(parts) == 3 && parts[1] == "resources" && r.Method == "POST":
		var input struct {
			PathPart string `json:"pathPart"`
		}
		err = apiDecode(w, r, &input, "pathPart")
		if err == nil {
			result, err = s.CreateResource(ctx, region, parts[0], parts[2], input.PathPart)
			status = 201
		}
	case len(parts) == 3 && parts[1] == "resources" && r.Method == "GET":
		result, err = s.Resource(ctx, region, parts[0], parts[2])
	case len(parts) == 3 && parts[1] == "resources" && r.Method == "DELETE":
		err = s.DeleteResource(ctx, region, parts[0], parts[2])
		status = 202
	case len(parts) == 5 && parts[1] == "resources" && parts[3] == "methods" && r.Method == "PUT":
		var input struct {
			AuthorizationType string          `json:"authorizationType"`
			APIKeyRequired    bool            `json:"apiKeyRequired"`
			RequestParameters map[string]bool `json:"requestParameters"`
		}
		err = apiDecode(w, r, &input, "authorizationType", "apiKeyRequired", "requestParameters")
		if err == nil {
			if input.AuthorizationType != "NONE" || input.APIKeyRequired {
				err = apiFailure("BadRequestException", "Only NONE methods without API keys are supported.")
			} else {
				result, err = s.PutMethod(ctx, region, parts[0], parts[2], parts[4], input.RequestParameters)
				status = 201
			}
		}
	case len(parts) == 5 && parts[1] == "resources" && parts[3] == "methods" && r.Method == "GET":
		var resource RESTResource
		resource, err = s.Resource(ctx, region, parts[0], parts[2])
		if err == nil {
			value, ok := resource.ResourceMethods[parts[4]]
			if !ok {
				err = apiFailure("NotFoundException", "Method does not exist.")
			} else {
				result = value
			}
		}
	case len(parts) == 6 && parts[1] == "resources" && parts[3] == "methods" && parts[5] == "integration" && r.Method == "PUT":
		var input struct {
			Type       string `json:"type"`
			HTTPMethod string `json:"httpMethod"`
			URI        string `json:"uri"`
		}
		err = apiDecode(w, r, &input, "type", "httpMethod", "uri")
		if err == nil {
			if input.Type != "AWS_PROXY" || input.HTTPMethod != "POST" {
				err = apiFailure("BadRequestException", "Only Lambda AWS_PROXY POST integrations are supported.")
			} else {
				result, err = s.PutIntegration(ctx, region, parts[0], parts[2], parts[4], input.URI)
				status = 201
			}
		}
	case len(parts) == 2 && parts[1] == "deployments" && r.Method == "POST":
		var input struct {
			Description string `json:"description"`
		}
		err = apiDecode(w, r, &input, "description")
		if err == nil {
			result, err = s.CreateDeployment(ctx, region, parts[0], input.Description)
			status = 201
		}
	case len(parts) == 3 && parts[1] == "deployments" && r.Method == "GET":
		result, _, err = s.Deployment(ctx, region, parts[0], parts[2])
	case len(parts) == 2 && parts[1] == "stages" && r.Method == "POST":
		var input struct {
			StageName    string            `json:"stageName"`
			DeploymentID string            `json:"deploymentId"`
			Variables    map[string]string `json:"variables"`
		}
		err = apiDecode(w, r, &input, "stageName", "deploymentId", "variables")
		if err == nil {
			result, err = s.CreateStage(ctx, region, parts[0], input.StageName, input.DeploymentID, input.Variables)
			status = 201
		}
	case len(parts) == 3 && parts[1] == "stages" && r.Method == "GET":
		result, err = s.Stage(ctx, region, parts[0], parts[2])
	case len(parts) == 3 && parts[1] == "stages" && r.Method == "DELETE":
		err = s.DeleteStage(ctx, region, parts[0], parts[2])
		status = 202
	default:
		notImplemented()
		return
	}
	if err != nil {
		apiError(w, err)
		return
	}
	apiJSON(w, status, result)
}

func parseAPIQuery(r *http.Request) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, apiFailure("BadRequestException", "Malformed REST API query string.")
	}
	result := map[string]string{}
	for name, items := range values {
		if len(items) != 1 {
			return nil, apiFailure("BadRequestException", "Duplicate REST API query fields are unsupported.")
		}
		result[name] = items[0]
	}
	return result, nil
}
