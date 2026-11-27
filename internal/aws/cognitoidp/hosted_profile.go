package cognitoidp

import "net/http"

// HostedBrowser handles only its fixed, uncredentialed local federation
// endpoints. A caller must never use its JWTs to authenticate simulator APIs.
type HostedBrowser interface {
	Owns(*http.Request) bool
	ServeHTTP(http.ResponseWriter, *http.Request)
}
