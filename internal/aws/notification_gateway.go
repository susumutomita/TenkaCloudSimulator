package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/codestarnotifications"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const notificationService = "codestar-notifications"

var notificationFields = map[string][]string{
	"CreateNotificationRule":   {"Name", "EventTypeIds", "Resource", "Targets", "DetailType", "ClientRequestToken", "Status"},
	"DescribeNotificationRule": {"Arn"},
	"ListNotificationRules":    {"Filters", "NextToken", "MaxResults"},
	"UpdateNotificationRule":   {"Arn", "Name", "Status", "EventTypeIds", "Targets", "DetailType"},
	"DeleteNotificationRule":   {"Arn"},
}

func notificationsOwn(r *http.Request) bool {
	return r.Header.Get("X-Simulator-Service") == notificationService || r53SigningService(r.Header.Get("Authorization")) == notificationService
}

func notificationFailure(code, message string) error {
	return codestarnotifications.Fault{Code: code, Message: message}
}

func writeNotificationError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServerException", "The local notification operation could not be completed.", http.StatusInternalServerError
	var f codestarnotifications.Fault
	if errors.As(err, &f) {
		code, message, status = f.Code, f.Message, http.StatusBadRequest
		switch code {
		case "AccessDeniedException":
			status = 403
		case "ResourceNotFoundException":
			status = 404
		case "ResourceAlreadyExistsException":
			status = 409
		case "UnknownOperationException":
			status = 501
		}
	}
	w.Header().Set("X-Amzn-Errortype", code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"__type": code, "Message": message})
}

func decodeNotificationInput(b []byte, fields []string) (codestarnotifications.Input, error) {
	var in codestarnotifications.Input
	bad := notificationFailure("ValidationException", "Use the exact modeled JSON request fields and types.")
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if !kmsUnicode(b) || kmsJSONValue(d, 0) != nil {
		return in, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return in, bad
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(b, &object) != nil || object == nil {
		return in, bad
	}
	for k, value := range object {
		if !slices.Contains(fields, k) {
			return in, bad
		}
		switch k {
		case "Targets", "Filters":
			var nested []map[string]json.RawMessage
			if json.Unmarshal(value, &nested) != nil {
				return in, bad
			}
			for _, entry := range nested {
				if len(entry) != 2 {
					return in, bad
				}
				if k == "Targets" && (entry["TargetType"] == nil || entry["TargetAddress"] == nil) {
					return in, bad
				}
				if k == "Filters" && (entry["Name"] == nil || entry["Value"] == nil) {
					return in, bad
				}
			}
		case "ClientRequestToken", "NextToken":
			if string(value) == `""` {
				return in, bad
			}
		}
	}
	if json.Unmarshal(b, &in) != nil {
		return in, bad
	}
	return in, nil
}

func notificationRegion(r *http.Request) (string, error) {
	region := r.Header.Get("X-Simulator-Region")
	if auth := r.Header.Get("Authorization"); auth != "" {
		_, creds, ok := strings.Cut(auth, "Credential=")
		creds, _, _ = strings.Cut(creds, ",")
		scope := strings.Split(creds, "/")
		if !ok || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") || len(scope) != 5 || !strings.HasPrefix(scope[0], "TCSIM") || scope[1] == "" || scope[3] != notificationService || scope[4] != "aws4_request" || (region != "" && region != scope[2]) {
			return "", notificationFailure("AccessDeniedException", "Invalid local notification signing scope.")
		}
		region = scope[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	if !slices.Contains(Regions, region) {
		return "", notificationFailure("ValidationException", "Unsupported notification region.")
	}
	return region, nil
}

func (g *Gateway) serveNotifications(w http.ResponseWriter, r *http.Request) {
	actor, ok := provider.ActorFromContext(r.Context())
	_, session := SDKIdentityFromContext(r.Context())
	_, instance := r.Context().Value(consoleInstanceBoundaryKey{}).(consoleInstanceBoundary)
	_, parameter := r.Context().Value(consoleParameterBoundaryKey{}).(consoleParameterBoundary)
	if !ok || actor.ExerciseIdentity == "" || g.Identities == nil || session || instance || parameter || r.Header.Get("X-Amz-Security-Token") != "" {
		writeNotificationError(w, notificationFailure("AccessDeniedException", "An ordinary authenticated local exercise identity is required."))
		return
	}
	for _, k := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "Content-Type", "X-Amz-Security-Token", "X-Simulator-Principal"} {
		if len(r.Header.Values(k)) > 1 {
			writeNotificationError(w, notificationFailure("ValidationException", "Ambiguous request headers."))
			return
		}
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	hint := r.Header.Get("X-Simulator-Service")
	if err != nil || media != "application/json" || r.Method != http.MethodPost || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Path != r.URL.EscapedPath() || r.Header.Get("X-Amz-Target") != "" || (hint != "" && hint != notificationService) {
		writeNotificationError(w, notificationFailure("ValidationException", "Use the local notification REST JSON endpoint."))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	op := ""
	for candidate := range notificationFields {
		if strings.ToLower(candidate[:1])+candidate[1:] == path {
			op = candidate
			break
		}
	}
	if op == "" {
		writeNotificationError(w, notificationFailure("UnknownOperationException", "This local notification operation is unsupported."))
		return
	}
	region, err := notificationRegion(r)
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeNotificationError(w, notificationFailure("ValidationException", "The local request body limit is 64 KiB."))
		return
	}
	in, err := decodeNotificationInput(b, notificationFields[op])
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	if g.Notifications == nil {
		writeNotificationError(w, errors.New("notification rules are not configured"))
		return
	}
	decide, err := g.snsAuthorizationSnapshot(r.Context(), actor.ExerciseIdentity)
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	out, err := g.Notifications.Run(r.Context(), op, region, actor.ExerciseIdentity, in, func(a, arn string) string {
		decision := decide(a, arn)
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		return decision
	})
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
