package cognitoidp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const TargetPrefix = "AWSCognitoIdentityProviderService."
const JSONMediaType = "application/x-amz-json-1.1"
const RequestLimit = 4096

// Authorize must evaluate only the trusted local caller, not a body value, a
// legacy dummy SDK signing hint, or an upstream identity-provider credential.
type Authorize func(context.Context, string, string) bool

// RejectAuthentication uses the local SDK error envelope without exposing
// whether a credential exists, which scope is configured, or any pool metadata.
func RejectAuthentication(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, "UnrecognizedClientException", "Verified local Cognito SDK credentials are required.")
}

// NewHTTPHandler implements a metadata-only AWS JSON 1.1 user-pool surface.
// The composition layer owns native SDK authentication and provider dispatch.
func NewHTTPHandler(store *Store, scope Scope, actor Actor, authorize Authorize) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil || store.db == nil || !scope.valid() || !actor.valid() || authorize == nil {
			RejectAuthentication(w)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/" || r.URL.RawPath != "" || r.URL.RawQuery != "" ||
			r.URL.ForceQuery || len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("X-Amz-Target")) != 1 ||
			r.Body == nil {
			operationError(w, errInvalid)
			return
		}
		media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != JSONMediaType {
			operationError(w, errInvalid)
			return
		}
		op, matches := strings.CutPrefix(r.Header.Get("X-Amz-Target"), TargetPrefix)
		if !matches || !supportedOperation(op) {
			writeError(w, http.StatusBadRequest, "UnknownOperationException", "This service only implements local pool metadata operations.")
			return
		}
		ctx := r.Context()
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, RequestLimit))
		if err != nil {
			operationError(w, errInvalid)
			return
		}
		fields := requestFields(raw)
		argument, continuation := "", ""
		max := 0
		resource := "*"
		switch op {
		case "CreateUserPool":
			argument = clientString(fields["PoolName"])
			protection, included := fields["DeletionProtection"]
			if !only(fields, "PoolName", "DeletionProtection") || !validPoolName(argument) ||
				included && clientString(protection) != "INACTIVE" {
				operationError(w, errInvalid)
				return
			}
		case "ListUserPools":
			max, err = strconv.Atoi(string(fields["MaxResults"]))
			next, included := fields["NextToken"]
			continuation = clientString(next)
			if !only(fields, "MaxResults", "NextToken") || err != nil || max < 1 || max > maxPage ||
				included && (continuation == "" || len(continuation) > 1024) {
				operationError(w, errInvalid)
				return
			}
		default: // DescribeUserPool, DeleteUserPool
			argument = clientString(fields["UserPoolId"])
			if !only(fields, "UserPoolId") || !scope.validPoolID(argument) {
				operationError(w, errInvalid)
				return
			}
			resource = scope.arn(argument)
		}
		// In particular, every continuation is reauthorized before storage or
		// token resolution. Cursor possession is not a role grant.
		if ctx.Err() != nil {
			operationError(w, ctx.Err())
			return
		}
		if !authorize(ctx, "cognito-idp:"+op, resource) {
			writeError(w, http.StatusForbidden, "AccessDeniedException", "Local IAM policy does not allow this user pool operation.")
			return
		}
		switch op {
		case "CreateUserPool", "DescribeUserPool":
			var p pool
			if op == "CreateUserPool" {
				p, err = store.create(ctx, scope, argument)
			} else {
				p, err = store.describe(ctx, scope, argument)
			}
			if err != nil {
				operationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"UserPool": poolDetail{p.summary(), scope.arn(p.ID), "INACTIVE"}})
		case "ListUserPools":
			page, next, err := store.list(ctx, scope, actor, max, continuation)
			if err != nil {
				operationError(w, err)
				return
			}
			summaries := make([]poolSummary, 0, len(page))
			for _, p := range page {
				summaries = append(summaries, p.summary())
			}
			writeJSON(w, http.StatusOK, struct {
				Pools []poolSummary `json:"UserPools"`
				Token string        `json:"NextToken,omitempty"`
			}{summaries, next})
		case "DeleteUserPool":
			if err := store.delete(ctx, scope, argument); err != nil {
				operationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, nil) // Official empty successful response.
		}
	})
}

func supportedOperation(op string) bool {
	switch op {
	case "CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool":
		return true
	default:
		return false
	}
}

type poolSummary struct {
	ID                             string `json:"Id"`
	Name                           string
	CreationDate, LastModifiedDate float64
}

type poolDetail struct {
	poolSummary
	ARN                string `json:"Arn"`
	DeletionProtection string
}

func (p pool) summary() poolSummary {
	timestamp := float64(p.CreatedMS) / 1000
	return poolSummary{p.ID, p.Name, timestamp, timestamp}
}

func requestFields(raw []byte) map[string]json.RawMessage {
	if !utf8.Valid(raw) {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if first, err := d.Token(); err != nil || first != json.Delim('{') {
		return nil
	}
	out := map[string]json.RawMessage{}
	for d.More() {
		if len(out) >= 2 {
			return nil
		}
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || out[name] != nil {
			return nil
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil
		}
		out[name] = value
	}
	if close, err := d.Token(); err != nil || close != json.Delim('}') {
		return nil
	}
	if _, err := d.Token(); err != io.EOF {
		return nil
	}
	return out
}
func only(fields map[string]json.RawMessage, a ...string) bool {
	if fields == nil {
		return false
	}
	for key := range fields {
		match := false
		for _, allowed := range a {
			match = match || key == allowed
		}
		if !match {
			return false
		}
	}
	return true
}
func clientString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func operationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errInvalid):
		writeError(w, http.StatusBadRequest, "InvalidParameterException", "Invalid or unsupported user pool input.")
	case errors.Is(err, errMissing):
		writeError(w, http.StatusBadRequest, "ResourceNotFoundException", "User pool not found in the local scope.")
	case errors.Is(err, errQuota):
		writeError(w, http.StatusBadRequest, "LimitExceededException", "Local user pool limit reached.")
	default:
		writeError(w, http.StatusInternalServerError, "InternalErrorException", "Local user pool state operation failed.")
	}
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("X-Amzn-Errortype", code)
	writeJSON(w, status, map[string]string{"__type": code, "message": message})
}
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", JSONMediaType)
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
