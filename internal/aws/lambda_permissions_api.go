package aws

import (
	"encoding/json"
	"io"
	"net/http"
)

func (g *Gateway) serveLambdaPolicy(w http.ResponseWriter, r *http.Request, region, name string, parts []string, authorize func(action, resource string) bool) {
	if g.LambdaPermissions == nil {
		lambdaError(w, apiFailure("NotImplementedException", "Lambda resource policies are not configured."))
		return
	}
	resource := lambdaARN(region, name)
	if len(parts) == 2 && r.Method == "GET" {
		if !authorize("lambda:GetPolicy", resource) {
			return
		}
		policy, revision, err := g.LambdaPermissions.Policy(r.Context(), region, name)
		if err != nil {
			lambdaError(w, err)
			return
		}
		if len(policy.Statement) == 0 {
			lambdaError(w, apiFailure("ResourceNotFoundException", "Function resource policy not found."))
			return
		}
		data, _ := json.Marshal(policy)
		lambdaJSON(w, 200, map[string]string{"Policy": string(data), "RevisionId": revision})
		return
	}
	if len(parts) == 2 && r.Method == "POST" {
		if !authorize("lambda:AddPermission", resource) {
			return
		}
		var input struct {
			StatementID string `json:"StatementId"`
			Principal   string
			Action      string
			SourceARN   string `json:"SourceArn"`
			RevisionID  string `json:"RevisionId"`
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
		if err != nil || !iamTrustFields(data, "StatementId", "Principal", "Action", "SourceArn", "RevisionId") || iamStrictJSON(data, &input) != nil {
			lambdaError(w, apiFailure("InvalidParameterValueException", "Use the documented bounded service permission fields."))
			return
		}
		statement, err := g.LambdaPermissions.Add(r.Context(), region, name, input.StatementID, input.Principal, input.Action, input.SourceARN, input.RevisionID)
		if err != nil {
			lambdaError(w, err)
			return
		}
		encoded, _ := json.Marshal(statement)
		lambdaJSON(w, 201, map[string]string{"Statement": string(encoded)})
		return
	}
	if len(parts) == 3 && r.Method == "DELETE" {
		if !authorize("lambda:RemovePermission", resource) {
			return
		}
		if err := g.LambdaPermissions.Remove(r.Context(), region, name, parts[2], r.URL.Query().Get("RevisionId")); err != nil {
			lambdaError(w, err)
			return
		}
		lambdaJSON(w, 204, nil)
		return
	}
	lambdaError(w, apiFailure("NotImplementedException", "This Lambda resource policy operation is not implemented."))
}
