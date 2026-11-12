package aws

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type ssmDocumentRequest struct {
	Name            string `json:"Name"`
	Content         string `json:"Content"`
	DocumentType    string `json:"DocumentType"`
	DocumentFormat  string `json:"DocumentFormat"`
	DocumentVersion string `json:"DocumentVersion"`
	MaxResults      int    `json:"MaxResults"`
	NextToken       string `json:"NextToken"`
}

var ssmDocumentFields = map[string][]string{
	"CreateDocument":   {"Name", "Content", "DocumentType", "DocumentFormat"},
	"UpdateDocument":   {"Name", "Content", "DocumentVersion", "DocumentFormat"},
	"GetDocument":      {"Name", "DocumentVersion", "DocumentFormat"},
	"DescribeDocument": {"Name", "DocumentVersion"},
	"DeleteDocument":   {"Name"},
	"ListDocuments":    {"MaxResults", "NextToken"},
}

func isSSMDocumentOperation(op string) bool { _, ok := ssmDocumentFields[op]; return ok }

// Integration calls this only after serveSSM's existing method/path/credential/
// region validation. Host token/session admission remains outside this provider.
// There is deliberately no connection to SSMCommandStore or guest execution.
func (g *Gateway) serveSSMDocuments(w http.ResponseWriter, r *http.Request, store *SSMDocumentStore, region, op string) {
	fail := func(code, message string) { ssmError(w, ssmFailure{code, message}) }
	// A parameter handoff is not general SSM authority, even with ssm:* IAM.
	if _, scoped := r.Context().Value(consoleParameterBoundaryKey{}).(consoleParameterBoundary); scoped {
		fail("AccessDeniedException", "Request outside console session scope.")
		return
	}
	fields, ok := ssmDocumentFields[op]
	if !ok || store == nil {
		fail("UnknownOperationException", "Document management is unavailable.")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		fail("ValidationException", "Document request exceeds 512 KiB.")
		return
	}
	var input ssmDocumentRequest
	if err = strictDocumentJSON(raw, &input); err != nil {
		fail("ValidationException", err.Error())
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		fail("ValidationException", "Expected a JSON object.")
		return
	}
	for field := range object {
		found := false
		for _, allowed := range fields {
			if field == allowed {
				found = true
			}
		}
		if !found {
			fail("ValidationException", "Unsupported field: "+field)
			return
		}
	}
	if !validSSMDocumentRegion(region) || (op != "ListDocuments" && !validSSMDocumentAddress(region, input.Name)) {
		fail("ValidationException", "Invalid local document address.")
		return
	}
	if input.DocumentFormat != "" && input.DocumentFormat != "JSON" {
		fail("ValidationException", "Only JSON documents are supported.")
		return
	}
	if input.DocumentType != "" && input.DocumentType != "Command" {
		fail("ValidationException", "Only Command documents are supported.")
		return
	}
	if op == "UpdateDocument" && input.DocumentVersion == "" {
		fail("InvalidDocumentVersion", "Supply the latest version or $LATEST.")
		return
	}
	if op == "ListDocuments" {
		if _, present := object["MaxResults"]; !present {
			input.MaxResults = 50
		}
		if input.MaxResults < 1 || input.MaxResults > 50 {
			fail("ValidationException", "MaxResults must be 1 to 50.")
			return
		}
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	// Evaluate all required actions against one immutable policy. A document does
	// not supply a role or tags, so PassRole/AddTagsToResource are never requested.
	policy, hasPolicy := SDKPolicyFromContext(r.Context())
	if !hasPolicy && principal != core.Administrator {
		policy, _, err = g.Identities.PolicyContext(r.Context(), principal)
		if err != nil {
			ssmError(w, err)
			return
		}
		hasPolicy = true
	}
	resource := fmt.Sprintf("arn:aws:ssm:%s:000000000000:document/%s", region, input.Name)
	if op == "ListDocuments" {
		resource = "*"
	}
	actions := []string{"ssm:" + op}
	if op == "UpdateDocument" {
		actions = append(actions, "ssm:GetDocument")
	}
	for _, action := range actions {
		decision := "allowed"
		if hasPolicy {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			fail("AccessDeniedException", "Document operation denied.")
			return
		}
	}
	var result any
	var doc SSMDocument
	switch op {
	case "CreateDocument", "UpdateDocument":
		doc, err = store.Save(r.Context(), region, input.Name, input.Content, input.DocumentVersion)
		result = map[string]any{"DocumentDescription": ssmDocumentDescription(doc)}
	case "GetDocument", "DescribeDocument":
		doc, err = store.Get(r.Context(), region, input.Name, input.DocumentVersion)
		if op == "DescribeDocument" {
			result = map[string]any{"Document": ssmDocumentDescription(doc)}
		} else {
			result = map[string]any{"Name": doc.Name, "Content": doc.Content, "DocumentVersion": doc.DocumentVersion, "DocumentType": "Command", "DocumentFormat": "JSON", "Status": "Active", "CreatedDate": doc.CreatedDate}
		}
	case "DeleteDocument":
		err = store.Delete(r.Context(), region, input.Name)
	case "ListDocuments":
		result, err = store.documentPage(r, region, principal, input.MaxResults, input.NextToken)
	}
	if err != nil {
		ssmError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	if op == "DeleteDocument" {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
func ssmDocumentDescription(d SSMDocument) map[string]any {
	return map[string]any{"Name": d.Name, "DocumentVersion": d.DocumentVersion, "DefaultVersion": d.DefaultVersion, "LatestVersion": d.LatestVersion, "CreatedDate": d.CreatedDate, "Hash": d.Hash, "HashType": "Sha256", "Owner": "000000000000", "Status": "Active", "DocumentType": "Command", "DocumentFormat": "JSON", "SchemaVersion": "2.2", "PlatformTypes": []string{"Linux"}}
}

type ssmDocumentCursor struct {
	Region, Principal, Fingerprint string
	Size, Offset                   int
}

func (s *SSMDocumentStore) documentPage(r *http.Request, region, principal string, size int, token string) (any, error) {
	docs, err := s.List(r.Context(), region)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(docs)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	offset := 0
	if token != "" {
		bad := documentFailure("InvalidNextToken", "Token does not match this request or the current document snapshot.")
		if len(token) > 2048 {
			return nil, bad
		}
		raw, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil {
			return nil, bad
		}
		var cursor ssmDocumentCursor
		if strictDocumentJSON(raw, &cursor) != nil || cursor.Region != region || cursor.Principal != principal || cursor.Size != size || cursor.Fingerprint != fingerprint || cursor.Offset < 1 || cursor.Offset >= len(docs) || cursor.Offset%size != 0 {
			return nil, bad
		}
		offset = cursor.Offset
	}
	end := offset + size
	if end > len(docs) {
		end = len(docs)
	}
	items := []any{}
	for _, d := range docs[offset:end] {
		items = append(items, map[string]any{"Name": d.Name, "DocumentVersion": d.DocumentVersion, "DocumentType": "Command", "DocumentFormat": "JSON", "SchemaVersion": "2.2", "Owner": "000000000000", "PlatformTypes": []string{"Linux"}})
	}
	result := map[string]any{"DocumentIdentifiers": items}
	if end < len(docs) {
		raw, _ := json.Marshal(ssmDocumentCursor{region, principal, fingerprint, size, end})
		result["NextToken"] = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}
