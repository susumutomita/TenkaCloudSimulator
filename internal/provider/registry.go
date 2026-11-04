// Package provider routes independently implemented cloud protocols in one process.
// The registry shares no authorization rules or resource types between providers.
package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

const Prefix = "/_simulator/providers/"

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Registry struct {
	handlers    map[string]http.Handler
	defaultName string
}

func New(defaultName string) *Registry {
	return &Registry{handlers: make(map[string]http.Handler), defaultName: defaultName}
}

// Register is called during startup, before serving concurrent requests.
func (r *Registry) Register(name string, handler http.Handler) error {
	if !validName.MatchString(name) || handler == nil {
		return fmt.Errorf("invalid provider registration")
	}
	if _, exists := r.handlers[name]; exists {
		return fmt.Errorf("provider %s already registered", name)
	}
	r.handlers[name] = handler
	return nil
}
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
func (r *Registry) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	name := r.defaultName
	if strings.HasPrefix(request.URL.Path, Prefix) {
		rest := strings.TrimPrefix(request.URL.Path, Prefix)
		var path string
		var found bool
		name, path, found = strings.Cut(rest, "/")
		if !found {
			path = ""
		}
		// Provider identifiers are literal routing syntax. Reject an encoded
		// identifier or delimiter instead of letting URL.Path's decoding turn
		// one segment into a different provider boundary.
		prefix := Prefix + name
		escaped := request.URL.EscapedPath()
		if !strings.HasPrefix(escaped, prefix) || (len(escaped) > len(prefix) && escaped[len(prefix)] != '/') {
			http.Error(w, "Provider not implemented", http.StatusNotFound)
			return
		}
		tail := strings.TrimPrefix(escaped, prefix)
		if tail == "" {
			tail = "/"
		}
		clone := request.Clone(request.Context())
		u := *request.URL
		clone.URL = &u
		clone.URL.Path = "/" + path
		// Keep the escaped tail paired with the decoded Path. Leaf protocols
		// must still distinguish an encoded separator from a literal separator.
		// Never clean, redirect, or decode the provider-owned tail again.
		clone.URL.RawPath = tail
		request = clone
	}
	handler, ok := r.handlers[name]
	if !ok {
		http.Error(w, "Provider not implemented", 404)
		return
	}
	handler.ServeHTTP(w, request)
}
