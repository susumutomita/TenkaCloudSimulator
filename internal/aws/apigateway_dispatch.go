package aws

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func isAPIGateway(r *http.Request) bool {
	service := r.Header.Get("X-Simulator-Service")
	if service != "" {
		return service == "apigateway" || service == "execute-api"
	}
	if strings.HasPrefix(r.URL.Path, "/_execute-api/") {
		return true
	}
	_, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	parts := strings.Split(scope, "/")
	return ok && len(parts) >= 5 && (parts[3] == "apigateway" || parts[3] == "execute-api")
}
func apiScopeRegion(r *http.Request, service, region string) (string, error) {
	if selected := r.Header.Get("X-Simulator-Region"); selected != "" {
		if region != "" && region != selected {
			return "", apiFailure("BadRequestException", "Region header and endpoint disagree.")
		}
		region = selected
	}
	for _, field := range []string{"X-Amz-Credential", "X-Amz-Algorithm", "X-Amz-Security-Token"} {
		if r.URL.Query().Has(field) {
			return "", apiFailure("BadRequestException", "Presigned requests are unsupported.")
		}
	}
	if selected := r.Header.Get("X-Simulator-Service"); selected != "" && selected != service {
		return "", apiFailure("BadRequestException", "Service selection and endpoint disagree.")
	}
	auth := r.Header.Get("Authorization")
	if auth != "" && !(service == "execute-api" && strings.HasPrefix(auth, "Bearer ")) {
		_, scope, ok := strings.Cut(auth, "Credential=")
		parts := strings.Split(scope, "/")
		if !ok || len(parts) < 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[3] != service {
			return "", apiFailure("AccessDeniedException", "Use dummy TCSIM credentials for this service.")
		}
		if region != "" && region != parts[2] {
			return "", apiFailure("BadRequestException", "Region and credential scope disagree.")
		}
		region = parts[2]
	}
	if r.Header.Get("X-Amz-Security-Token") != "" || r.Header.Get("X-Amz-Target") != "" {
		return "", apiFailure("BadRequestException", "Sessions and JSON target dispatch are unsupported.")
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		return "", apiFailure("BadRequestException", "Unsupported local region.")
	}
	return region, nil
}
func (g *Gateway) serveAPIGateway(w http.ResponseWriter, r *http.Request) {
	if g.APIGateway == nil {
		apiError(w, apiFailure("NotImplementedException", "API Gateway state is not configured."))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_execute-api/") {
		g.serveRESTInvocation(w, r)
		return
	}
	region, err := apiScopeRegion(r, "apigateway", r.Header.Get("X-Simulator-Region"))
	if err != nil {
		apiError(w, err)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	var policy core.Policy
	if principal != core.Administrator {
		if g.Identities == nil {
			apiError(w, apiFailure("AccessDeniedException", "No identity authority configured."))
			return
		}
		policy, _, err = g.Identities.Policy(principal)
		if err != nil {
			apiError(w, err)
			return
		}
	}
	g.APIGateway.serveControl(w, r, region, func(action, resource string) bool {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision == "allowed"
	})
}
func (g *Gateway) serveRESTInvocation(w http.ResponseWriter, r *http.Request) {
	if !apiHTTPMethod(r.Method) || r.Method == "ANY" {
		apiError(w, apiFailure("BadRequestException", "Unsupported local REST invocation method."))
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/_execute-api/"), "/", 4)
	if len(parts) < 3 || !apiLocalID.MatchString(parts[1]) || !apiStageName.MatchString(parts[2]) {
		apiError(w, apiFailure("BadRequestException", "Use a local regional API stage path."))
		return
	}
	region, err := apiScopeRegion(r, "execute-api", parts[0])
	if err != nil {
		apiError(w, err)
		return
	}
	path := "/"
	if len(parts) == 4 {
		path += parts[3]
	}
	route, err := g.APIGateway.ResolveStage(r.Context(), region, parts[1], parts[2], path, r.Method)
	if err != nil {
		apiError(w, err)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, apirouting.MaxProxyEventBytes))
	if err != nil {
		apiError(w, apiFailure("BadRequestException", "REST body exceeds256KiB."))
		return
	}
	event, err := apirouting.BuildProxyEvent(r, apirouting.ProxyRequest{APIID: route.API.ID, Stage: route.Stage.StageName, ResourcePath: route.Resource.Path, Path: path, RequestID: fmt.Sprintf("TCSIMREST%016x", sequence.Add(1)), PathParameters: route.PathParameters, StageVariables: route.Stage.Variables}, body)
	if err != nil {
		apiError(w, apiFailure("BadRequestException", "Invalid or oversized local REST request event."))
		return
	}
	response, err := g.invokeREST(r.Context(), region, route, path, r.Method, event)
	if err != nil {
		apiError(w, err)
		return
	}
	for name, values := range apirouting.DownloadHeaders(response) {
		w.Header().Del(name)
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.WriteString(w, response.Body)
}
