package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

type sdkAuthority = aws.STSAuthority

// SDK mode is opt-in through reserved local access-key names. Existing browser
// sessions and documented dummy SDK + local-header clients retain their paths.
func (a *Access) sdkAuthentication(w http.ResponseWriter, r *http.Request) (*http.Request, string, bool) {
	authorization := r.Header.Get("Authorization")
	if !strings.Contains(authorization, "Credential=TCSIMOPERATOR/") && !strings.Contains(authorization, "Credential=TCSIMASIA") {
		return r, "", false
	}
	if r.Header.Get("X-Simulator-Token") != "" {
		sdkAuthenticationError(w, r)
		return r, "", true
	}
	if _, err := r.Cookie(a.cookie); err == nil {
		sdkAuthenticationError(w, r)
		return r, "", true
	}
	request, identity, err := a.sdk.Authenticate(r)
	if err != nil {
		sdkAuthenticationError(w, r)
		return r, "", true
	}
	return request, identity.Principal, true
}

func sdkAuthenticationError(w http.ResponseWriter, r *http.Request) {
	// Do not expose whether a key, role or token exists. Preserve official wire
	// envelopes so SDK clients can display a typed error instead of XML parsing.
	const message = "The local AWS credentials or signature are invalid or expired."
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.") {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"__type": "UnrecognizedClientException", "message": message})
		return
	}
	if r.URL.Path == "/2015-03-31/functions" || strings.HasPrefix(r.URL.Path, "/2015-03-31/functions/") {
		// Lambda REST-JSON clients read the error type from this header.
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Amzn-ErrorType", "UnrecognizedClientException")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"Type": "User", "message": message})
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(http.StatusForbidden)
	_ = xml.NewEncoder(w).Encode(struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		Error   struct {
			Type, Code, Message string
		}
	}{Error: struct{ Type, Code, Message string }{"Sender", "InvalidClientTokenId", message}})
}

// Keep the authority type referenced here so access.go only needs the field and
// constructor hook; all SDK-specific behavior stays separate from UI sessions.
func newSDKAuthority(token string) *aws.STSAuthority { return aws.NewSTSAuthority(token) }
