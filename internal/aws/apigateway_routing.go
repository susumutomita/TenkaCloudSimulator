package aws

import (
	"context"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
)

type RESTRoute struct {
	API            RESTAPI
	Stage          RESTStage
	Resource       RESTResource
	Method         RESTMethod
	PathParameters map[string]string
}

// ResolveStage uses only the stage's immutable deployment. Resource selection
// precedes method selection: a missing method on a literal resource cannot
// silently fall through to a less-specific wildcard route.
func (s *APIGatewayStore) ResolveStage(ctx context.Context, region, api, stage, path, method string) (RESTRoute, error) {
	result := RESTRoute{}
	metadata, err := s.API(ctx, region, api)
	if err != nil {
		return result, err
	}
	selected, err := s.Stage(ctx, region, api, stage)
	if err != nil {
		return result, err
	}
	_, snapshot, err := s.Deployment(ctx, region, api, selected.DeploymentID)
	if err != nil {
		return result, err
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\x00") || len(path) > 1024 {
		return result, apiFailure("BadRequestException", "Invalid local REST invocation path.")
	}
	segments := apirouting.SplitPath(path)
	rank := ""
	found := false
	for _, resource := range snapshot {
		params, _, matches := apirouting.MatchPath(resource.Path, segments)
		if !matches {
			continue
		}
		candidate := ""
		for _, part := range apirouting.SplitPath(resource.Path) {
			switch {
			case strings.HasSuffix(part, "+}"):
				candidate += "0"
			case strings.HasPrefix(part, "{"):
				candidate += "1"
			default:
				candidate += "2"
			}
		}
		if found && candidate <= rank {
			continue
		}
		found = true
		rank = candidate
		result = RESTRoute{API: metadata, Stage: selected, Resource: resource, PathParameters: params}
	}
	if !found {
		return RESTRoute{}, apiFailure("MissingAuthenticationTokenException", "No deployed resource matches this path.")
	}
	definition, ok := result.Resource.ResourceMethods[method]
	if !ok {
		definition, ok = result.Resource.ResourceMethods["ANY"]
	}
	if !ok || definition.MethodIntegration == nil {
		return RESTRoute{}, apiFailure("MissingAuthenticationTokenException", "No deployed method matches this request.")
	}
	result.Method = definition
	return result, nil
}
