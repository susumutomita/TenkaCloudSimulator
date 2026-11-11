package aws

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func isSSMSessionOperation(r *http.Request) bool {
	switch r.Header.Get("X-Amz-Target") {
	case "AmazonSSM.StartSession", "AmazonSSM.ResumeSession", "AmazonSSM.TerminateSession":
		return true
	}
	return false
}

// ServeNative handles only signed participant session operations. It is not a
// generic authorization bridge for terminal cookies or parameter handoffs.
func (s *SSMSessionStore) ServeNative(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { ssmError(w, ssmFailure{"ValidationException", message}) }
	if _, err := bindSSMSessionCaller(r); err != nil {
		ssmError(w, ssmFailure{"AccessDeniedException", "A verified participant session is required."})
		return
	}
	if _, cookieBound := r.Context().Value(terminalBoundaryKey{}).(terminalBoundary); cookieBound {
		ssmError(w, ssmFailure{"AccessDeniedException", "Terminal bindings cannot call native AWS APIs."})
		return
	}
	if s == nil {
		ssmError(w, ssmFailure{"UnknownOperationException", "A qualified interactive guest backend is not configured."})
		return
	}
	if len(r.Header.Values("Content-Type")) != 1 || len(r.Header.Values("X-Amz-Target")) != 1 || r.Method != "POST" || r.URL.Path != "/" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.Header.Get("Content-Type") != "application/x-amz-json-1.1" || !isSSMSessionOperation(r) {
		invalid("Use the native regional SSM root POST endpoint.")
		return
	}
	signature, err := parseSDKSignature(r)
	if err != nil || signature.service != "ssm" {
		ssmError(w, ssmFailure{"AccessDeniedException", "Signed SSM scope required."})
		return
	}
	region := signature.region
	if supplied := r.Header.Get("X-Simulator-Region"); supplied != "" && supplied != region {
		invalid("Region header and signed scope disagree.")
		return
	}
	operation := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
	fields := map[string]bool{"SessionId": true}
	if operation == "StartSession" {
		fields = map[string]bool{"Target": true, "DocumentName": true}
	}
	input, err := decodeSessionObject(http.MaxBytesReader(w, r.Body, 16<<10), fields)
	if err != nil {
		invalid("Expected bounded unique string fields for this session operation.")
		return
	}
	if operation == "StartSession" {
		if input["Target"] == "" || len(input["Target"]) > 64 {
			invalid("A target instance is required.")
			return
		}
		response, err := s.Start(r, region, input["Target"], input["DocumentName"])
		if err != nil {
			ssmError(w, err)
			return
		}
		ssmJSON(w, 200, response)
		return
	}
	id := input["SessionId"]
	if id == "" || len(id) > 96 {
		invalid("A bounded SessionId is required.")
		return
	}
	if operation == "ResumeSession" {
		response, err := s.Resume(r, region, id)
		if err != nil {
			ssmError(w, err)
			return
		}
		ssmJSON(w, 200, response)
		return
	}
	if err = s.Terminate(r, region, id); err != nil {
		ssmError(w, err)
		return
	}
	ssmJSON(w, 200, map[string]string{"SessionId": id})
}
func decodeSessionObject(reader io.Reader, allowed map[string]bool) (map[string]string, error) {
	dec := json.NewDecoder(reader)
	opening, err := dec.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, fmt.Errorf("object required")
	}
	result := map[string]string{}
	for dec.More() {
		key, err := dec.Token()
		name, ok := key.(string)
		if err != nil || !ok || !allowed[name] {
			return nil, fmt.Errorf("unsupported field")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate field")
		}
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 || raw[0] != '"' {
			return nil, fmt.Errorf("string required")
		}
		var value string
		if err = json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		result[name] = value
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	var trailing any
	if err = dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing data")
	}
	return result, nil
}
