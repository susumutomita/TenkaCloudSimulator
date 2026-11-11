package apirouting

import "net/http"

// ApplicationHeaders applies the same closed authority exclusion as the REST
// proxy event to another trusted data-plane projection. Cookies, simulator
// authority, SigV4 Authorization and session tokens never reach guest code.
// Duplicate spellings keep the deterministic last-value projection.
func ApplicationHeaders(input http.Header) (map[string]string, error) {
	last, _, err := applicationHeaders(input)
	return last, err
}
