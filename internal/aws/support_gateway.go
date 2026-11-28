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

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/supportcases"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

const supportTarget = "AWSSupport_20130415."

var supportFields = map[string][]string{
	"CreateCase":             {"subject", "communicationBody", "language", "issueType", "attachmentSetId"},
	"DescribeCases":          {"caseIdList", "displayId", "includeResolvedCases", "includeCommunications", "nextToken", "maxResults"},
	"DescribeCommunications": {"caseId", "beforeTime", "afterTime", "nextToken", "maxResults"},
	"AddCommunicationToCase": {"caseId", "communicationBody", "attachmentSetId"},
	"ResolveCase":            {"caseId"},
	"AddAttachmentsToSet":    {"attachmentSetId", "attachments"},
	"DescribeAttachment":     {"attachmentId"},
}

func SupportOperations() []string {
	return []string{"AddAttachmentsToSet", "AddCommunicationToCase", "CreateCase", "DescribeAttachment", "DescribeCases", "DescribeCommunications", "ResolveCase"}
}

func isSupportRequest(r *http.Request) bool {
	for _, target := range r.Header.Values("X-Amz-Target") {
		if strings.HasPrefix(target, supportTarget) {
			return true
		}
	}
	return r.Header.Get("X-Simulator-Service") == "support" || r53SigningService(r.Header.Get("Authorization")) == "support"
}

func supportWriteError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServerError", "The local support operation could not be completed.", 500
	var fault supportcases.Fault
	if errors.As(err, &fault) {
		code, message, status = fault.Code, fault.Message, 400
		switch code {
		case "AccessDeniedException":
			status = http.StatusForbidden
		case "UnknownOperationException":
			status = http.StatusNotImplemented
		}
	}
	w.Header().Set("X-Amzn-Errortype", code)
	aspJSON(w, status, map[string]string{"__type": code, "message": message})
}

func supportBad(message string) error {
	return supportcases.Fault{Code: "ValidationException", Message: message}
}

func supportInput(data []byte, fields []string) (supportcases.Input, error) {
	var in supportcases.Input
	// The existing AWS JSON structural check rejects duplicate keys, nulls,
	// malformed Unicode and excess depth before decoding can normalize them.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !kmsUnicode(data) || kmsJSONValue(decoder, 0) != nil {
		return in, supportBad("Expected a well-formed JSON object.")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return in, supportBad("Provide exactly one JSON object.")
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil || values == nil {
		return in, supportBad("Expected a JSON object.")
	}
	for key := range values {
		if !slices.Contains(fields, key) {
			return in, supportBad("The request contains an unsupported field.")
		}
	}
	// encoding/json matches struct members case-insensitively. Verify nested
	// attachment keys by their exact official spelling as well as the top level.
	if raw, ok := values["attachments"]; ok {
		var files []map[string]json.RawMessage
		if json.Unmarshal(raw, &files) != nil {
			return in, supportBad("attachments must be a list of files.")
		}
		for _, file := range files {
			if len(file) != 2 || file["fileName"] == nil || file["data"] == nil {
				return in, supportBad("Each attachment requires fileName and base64 data.")
			}
			var encoded string
			if json.Unmarshal(file["data"], &encoded) != nil || strings.ContainsAny(encoded, "\r\n") {
				return in, supportBad("Attachment data must be a base64 string.")
			}
		}
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return in, supportBad("A request field has an invalid value.")
	}
	for _, key := range []string{"caseId", "attachmentId", "attachmentSetId", "displayId", "nextToken", "afterTime", "beforeTime", "issueType", "language"} {
		if raw, present := values[key]; present && string(raw) == `""` {
			return in, supportBad("Provide a nonempty value for " + key + ".")
		}
	}
	if _, present := values["maxResults"]; present && (in.MaxResults < 10 || in.MaxResults > 100) {
		return in, supportBad("maxResults must be from 10 through 100.")
	}
	return in, nil
}

func (g *Gateway) serveSupport(w http.ResponseWriter, r *http.Request) {
	deny := supportcases.Fault{Code: "AccessDeniedException", Message: "An authenticated local caller is required."}
	actor, ok := provider.ActorFromContext(r.Context())
	if !ok || actor.ExerciseIdentity == "" || g.Identities == nil {
		supportWriteError(w, deny)
		return
	}
	for _, name := range []string{"Content-Type", "X-Amz-Target", "Authorization", "X-Amz-Security-Token", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal"} {
		if len(r.Header.Values(name)) > 1 {
			supportWriteError(w, supportBad("Ambiguous authority or protocol header."))
			return
		}
	}
	if _, temporary := SDKIdentityFromContext(r.Context()); temporary || r.Header.Get("X-Amz-Security-Token") != "" {
		supportWriteError(w, deny)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/x-amz-json-1.1" || r.Method != "POST" || r.URL.Path != "/" || r.URL.EscapedPath() != "/" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		supportWriteError(w, supportBad("Use JSON 1.1 POST / without a query."))
		return
	}
	if hint := r.Header.Get("X-Simulator-Service"); hint != "" && hint != "support" {
		supportWriteError(w, supportBad("Use the support service."))
		return
	}
	if region := r.Header.Get("X-Simulator-Region"); region != "" && region != "us-east-1" {
		supportWriteError(w, supportBad("Use the local us-east-1 support endpoint."))
		return
	}
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		_, credential, found := strings.Cut(authorization, "Credential=")
		credential, _, _ = strings.Cut(credential, ",")
		parts := strings.Split(credential, "/")
		if !found || !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[2] != "us-east-1" || parts[3] != "support" || parts[4] != "aws4_request" {
			supportWriteError(w, deny)
			return
		}
	}
	op, ok := strings.CutPrefix(r.Header.Get("X-Amz-Target"), supportTarget)
	fields, known := supportFields[op]
	if !ok || !known {
		supportWriteError(w, supportcases.Fault{Code: "UnknownOperationException", Message: "This AWS Support operation is unsupported."})
		return
	}
	decision, err := g.sdkDecision(r, actor.ExerciseIdentity, "support:"+op, "*")
	if err != nil {
		supportWriteError(w, err)
		return
	}
	w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		supportWriteError(w, deny)
		return
	}
	if g.Support == nil {
		supportWriteError(w, errors.New("unconfigured support store"))
		return
	}
	// Three base64-encoded 64 KiB files plus their names fit in one request.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 288<<10))
	if err != nil {
		supportWriteError(w, supportBad("The request exceeds the local body limit."))
		return
	}
	in, err := supportInput(data, fields)
	if err != nil {
		supportWriteError(w, err)
		return
	}
	if (op == "DescribeCommunications" && in.CaseID == "") || (op == "DescribeAttachment" && in.AttachmentID == "") {
		supportWriteError(w, supportBad("The requested operation requires its resource identifier."))
		return
	}
	result, err := g.Support.Run(r.Context(), op, actor.ExerciseIdentity, in)
	if err != nil {
		supportWriteError(w, err)
		return
	}
	aspJSON(w, 200, result)
}
