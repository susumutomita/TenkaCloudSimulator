package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIndependentProvidersCanCoexist(t *testing.T) {
	r := New("first")
	for _, name := range []string{"first", "second"} {
		label := name
		if err := r.Register(name, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Provider", label)
			_, _ = w.Write([]byte(request.URL.Path))
		})); err != nil {
			t.Fatal(err)
		}
	}
	if r.Register("first", http.NotFoundHandler()) == nil {
		t.Fatal("duplicate provider accepted")
	}
	for _, tc := range []struct {
		path, provider, body string
		status               int
	}{{"/native/path", "first", "/native/path", 200}, {Prefix + "second/native/path", "second", "/native/path", 200}, {Prefix + "second", "second", "/", 200}, {Prefix + "missing/", "", "", 404}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status || w.Header().Get("Provider") != tc.provider || (tc.status == 200 && w.Body.String() != tc.body) {
			t.Fatal(w)
		}
	}
}
