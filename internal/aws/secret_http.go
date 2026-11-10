package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type secretRequest struct {
	Name                       string
	Description                string
	SecretId                   string
	ClientRequestToken         string
	SecretString               *string
	VersionId                  string
	VersionStage               string
	VersionStages              []string
	MoveToVersionId            string
	RemoveFromVersionId        string
	ForceDeleteWithoutRecovery *bool
	MaxResults                 *int
	NextToken                  string
	SortBy                     string
	SortOrder                  string
}

func secretJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Simulator-Encryption", "none")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func secretHTTPError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServiceError", "Simulator secret storage operation failed.", 500
	var failure secretFailure
	var legacy ssmFailure
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, 400
	} else if errors.As(err, &legacy) && legacy.code == "AccessDeniedException" {
		code, message, status = legacy.code, legacy.message, 403
	}
	if code == "AccessDeniedException" {
		status = 403
	}
	if code == "UnknownOperationException" {
		status = 501
	}
	w.Header().Set("X-Amzn-Errortype", code)
	secretJSON(w, status, map[string]string{"__type": code, "message": message})
}

// Reject unpaired UTF-16 escapes before encoding/json can replace them. Valid
// escaped pairs and literal UTF-8 must retain their exact decoded value bytes.
func secretUnicodeScalars(data []byte) bool {
	inside := false
	for i := 0; i < len(data); i++ {
		if !inside {
			if data[i] == '"' {
				inside = true
			}
			continue
		}
		if data[i] == '"' {
			inside = false
			continue
		}
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+10 >= len(data) || data[i+5] != '\\' || data[i+6] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+7:i+11]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 10
		} else {
			if value >= 0xdc00 && value <= 0xdfff {
				return false
			}
			i += 4
		}
	}
	return !inside
}

func secretUniqueJSON(dec *json.Decoder, depth int) error {
	if depth > 16 {
		return secretInvalid()
	}
	token, err := dec.Token()
	if err != nil {
		return secretInvalid()
	}
	if token == nil {
		return secretInvalid()
	}
	d, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if d == '{' {
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return secretInvalid()
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return secretInvalid()
			}
			seen[name] = true
			if err = secretUniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
	} else if d == '[' {
		for dec.More() {
			if err = secretUniqueJSON(dec, depth+1); err != nil {
				return err
			}
		}
	} else {
		return secretInvalid()
	}
	_, err = dec.Token()
	return err
}

// Multiple wire selectors are ambiguous even when their first values match.
// Do not let Header.Get choose authority/protocol on behalf of an intermediary.
func secretSingleSelectors(r *http.Request) bool {
	for _, name := range []string{"Content-Type", "X-Amz-Target", "X-Simulator-Service", "X-Simulator-Region", "Authorization", "X-Simulator-Principal", "X-Amz-Security-Token"} {
		if len(r.Header.Values(name)) > 1 {
			return false
		}
	}
	return true
}

// ServeHTTP is a native JSON protocol adapter, not tenant authentication. The
// Gateway must supply the region and a PURE, already captured policy callback;
// several store methods authorize inside their short single-connection SQL Tx.
func (s *SecretStore) ServeHTTP(w http.ResponseWriter, r *http.Request, region string, authorize SecretAuthorizer) {
	fail := func(err error) { secretHTTPError(w, err) }
	if !secretSingleSelectors(r) {
		fail(secretInvalid())
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-amz-json-1.1" || r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || !secretRegion(region) {
		fail(secretInvalid())
		return
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "secretsmanager" {
		fail(secretInvalid())
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail(secretInvalid())
		return
	}
	if credentials := r.Header.Get("Authorization"); credentials != "" {
		_, scope, ok := strings.Cut(credentials, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		parts := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(credentials, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[2] != region || parts[3] != "secretsmanager" || parts[4] != "aws4_request" {
			fail(secretFailure{"AccessDeniedException", "Use simulator-only credentials for this service and region."})
			return
		}
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "secretsmanager.")
	allowed := map[string][]string{
		"CreateSecret":             {"Name", "Description", "ClientRequestToken", "SecretString"},
		"DescribeSecret":           {"SecretId"},
		"UpdateSecret":             {"SecretId", "Description", "ClientRequestToken"},
		"ListSecrets":              {"MaxResults", "NextToken", "SortBy", "SortOrder"},
		"ListSecretVersionIds":     {"SecretId", "IncludeDeprecated", "MaxResults", "NextToken"},
		"PutSecretValue":           {"SecretId", "ClientRequestToken", "SecretString", "VersionStages"},
		"GetSecretValue":           {"SecretId", "VersionId", "VersionStage"},
		"UpdateSecretVersionStage": {"SecretId", "VersionStage", "MoveToVersionId", "RemoveFromVersionId"},
		"DeleteSecret":             {"SecretId", "ForceDeleteWithoutRecovery"},
	}
	fields, ok := allowed[op]
	if !ok || r.Header.Get("X-Amz-Target") != "secretsmanager."+op {
		fail(secretFailure{"UnknownOperationException", "This Secrets Manager operation is not implemented."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil || !utf8.Valid(data) || !secretUnicodeScalars(data) {
		fail(secretInvalid())
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = secretUniqueJSON(dec, 0); err != nil {
		fail(secretInvalid())
		return
	}
	if _, err = dec.Token(); err != io.EOF {
		fail(secretInvalid())
		return
	}
	var raw map[string]json.RawMessage
	var input secretRequest
	if json.Unmarshal(data, &raw) != nil || raw == nil || json.Unmarshal(data, &input) != nil {
		fail(secretInvalid())
		return
	}
	for field := range raw {
		if !slices.Contains(fields, field) {
			fail(secretInvalid())
			return
		}
	}
	if op == "CreateSecret" || op == "PutSecretValue" {
		if input.SecretString == nil {
			fail(secretInvalid())
			return
		}
	}
	if op == "UpdateSecret" {
		if _, present := raw["Description"]; !present {
			fail(secretInvalid())
			return
		}
		if _, present := raw["ClientRequestToken"]; present && !secretToken(input.ClientRequestToken) {
			fail(secretInvalid())
			return
		}
	}
	if op == "PutSecretValue" {
		if _, present := raw["VersionStages"]; present && !slices.Equal(input.VersionStages, []string{"AWSCURRENT"}) {
			fail(secretInvalid())
			return
		}
	}
	if op == "UpdateSecretVersionStage" {
		if input.VersionStage != "AWSCURRENT" {
			fail(secretInvalid())
			return
		}
		if _, present := raw["RemoveFromVersionId"]; present && !secretToken(input.RemoveFromVersionId) {
			fail(secretInvalid())
			return
		}
	}
	if op == "GetSecretValue" {
		if _, present := raw["VersionId"]; present && !secretToken(input.VersionId) {
			fail(secretInvalid())
			return
		}
		if _, present := raw["VersionStage"]; present && input.VersionStage != "AWSCURRENT" && input.VersionStage != "AWSPREVIOUS" {
			fail(secretInvalid())
			return
		}
	}
	if op == "ListSecrets" {
		if _, present := raw["NextToken"]; present && input.NextToken == "" {
			fail(secretInvalid())
			return
		}
		if _, present := raw["SortBy"]; present && input.SortBy == "" {
			fail(secretInvalid())
			return
		}
		if _, present := raw["SortOrder"]; present && input.SortOrder == "" {
			fail(secretInvalid())
			return
		}
	}
	if op == "DeleteSecret" && (input.ForceDeleteWithoutRecovery == nil || !*input.ForceDeleteWithoutRecovery) {
		fail(secretInvalid())
		return
	}
	ctx := r.Context()
	var output any
	switch op {
	case "CreateSecret":
		output, err = s.Create(ctx, region, input.Name, input.Description, input.ClientRequestToken, newSecretMaterial(*input.SecretString), authorize)
	case "UpdateSecret":
		output, err = s.UpdateDescription(ctx, region, input.SecretId, input.Description, authorize)
	case "DescribeSecret":
		output, err = s.Describe(ctx, region, input.SecretId, authorize)
	case "PutSecretValue":
		output, err = s.Put(ctx, region, input.SecretId, input.ClientRequestToken, newSecretMaterial(*input.SecretString), authorize)
	case "UpdateSecretVersionStage":
		output, err = s.Promote(ctx, region, input.SecretId, input.MoveToVersionId, input.RemoveFromVersionId, authorize)
	case "DeleteSecret":
		output, err = s.DeleteForce(ctx, region, input.SecretId, authorize)
	case "ListSecrets":
		limit := 100
		if input.MaxResults != nil {
			limit = *input.MaxResults
		}
		var entries []SecretListEntry
		var next string
		entries, next, err = s.List(ctx, region, SecretListQuery{MaxResults: limit, NextToken: input.NextToken, SortBy: input.SortBy, SortOrder: input.SortOrder}, authorize)
		result := map[string]any{"SecretList": entries}
		if next != "" {
			result["NextToken"] = next
		}
		output = result
	case "ListSecretVersionIds":
		var id string
		var query SecretVersionListQuery
		id, query, err = decodeSecretVersionListRequest(data)
		if err == nil {
			output, err = s.ListSecretVersionIds(ctx, region, id, query, authorize)
		}
	case "GetSecretValue":
		var result SecretValueResult
		result, err = s.Get(ctx, region, input.SecretId, input.VersionId, input.VersionStage, authorize)
		if err == nil {
			revealed := map[string]any{"ARN": result.ARN, "Name": result.Name, "VersionId": result.VersionId, "CreatedDate": result.CreatedDate, "SecretString": result.material.plaintext()}
			if len(result.VersionStages) > 0 {
				revealed["VersionStages"] = result.VersionStages
			}
			output = revealed
		}
	}
	if err != nil {
		fail(err)
		return
	}
	secretJSON(w, 200, output)
}
