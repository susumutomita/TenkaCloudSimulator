package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type routeActorKey struct{}
type routeObservation struct{ Path, Escaped, Query, Target, Actor, Header string }

// Actual HTTP, with probe leaves only: this does not claim a cloud implementation.
func TestRegistryEscapedTailActualHTTP(t *testing.T) {
	r := New("aws")
	leaf := http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		actor, _ := q.Context().Value(routeActorKey{}).(string)
		_ = json.NewEncoder(w).Encode(routeObservation{q.URL.Path, q.URL.EscapedPath(), q.URL.RawQuery, q.RequestURI, actor, q.Header.Get("X-Test-Identity")})
	})
	for _, name := range []string{"aws", "azure", "gcp"} {
		if err := r.Register(name, leaf); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.ServeHTTP(w, q.WithContext(context.WithValue(q.Context(), routeActorKey{}, "bound-participant")))
	}))
	defer server.Close()
	client := server.Client()
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return fmt.Errorf("unexpected redirect") }
	for _, tail := range []string{"/", "/bucket/a%2Fb", "/bucket/a%2fb", "/bucket/a%252Fb", "/bucket/%E6%97%A5%E6%9C%AC", "/bucket/../name", "/bucket/%2E%2E/name", "/bucket/a//b", "/bucket/%25", "/account/container%2Fother/blob"} {
		for _, prefix := range []string{"", Prefix + "aws", Prefix + "azure", Prefix + "gcp"} {
			target := prefix + tail + "?same=a%2Fb&same=a%252Fb"
			q, err := http.NewRequest(http.MethodGet, server.URL+target, nil)
			if err != nil {
				t.Fatal(err)
			}
			q.Header.Set("X-Test-Identity", "exercise-user")
			response, err := client.Do(q)
			if err != nil {
				t.Fatal(err)
			}
			var got routeObservation
			err = json.NewDecoder(response.Body).Decode(&got)
			response.Body.Close()
			decoded, _ := url.PathUnescape(tail)
			if err != nil || response.StatusCode != 200 || got.Path != decoded || got.Escaped != tail || got.Query != "same=a%2Fb&same=a%252Fb" || got.Target != target || got.Actor != "bound-participant" || got.Header != "exercise-user" {
				t.Fatalf("target %q: %#v, status%d err%v", target, got, response.StatusCode, err)
			}
		}
	}
	for _, target := range []string{Prefix + "aws", Prefix + "aws/"} {
		response, err := client.Get(server.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		var got routeObservation
		err = json.NewDecoder(response.Body).Decode(&got)
		response.Body.Close()
		if err != nil || got.Path != "/" || got.Escaped != "/" {
			t.Fatalf("root %q: %#v %v", target, got, err)
		}
	}
}

func TestRegistryEncodedProviderBoundaryAndUnknownRefused(t *testing.T) {
	r := New("aws")
	calls := 0
	if err := r.Register("aws", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(204) })); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{Prefix + "%61ws/bucket", Prefix + "aws%2Fbucket/key", Prefix + "aws%252Fbucket/key", Prefix + "missing/", Prefix + "%E6%97%A5/", Prefix + "/bucket"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		if w.Code != 404 {
			t.Fatalf("%q: %d", target, w.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("rejected route reached leaf %d times", calls)
	}
}

func TestRegistryCloneDoesNotMutateOriginalURL(t *testing.T) {
	r := New("aws")
	if err := r.Register("aws", http.HandlerFunc(func(_ http.ResponseWriter, q *http.Request) {
		if q.URL.EscapedPath() != "/a%2Fb" {
			t.Fatal(q.URL)
		}
	})); err != nil {
		t.Fatal(err)
	}
	q := httptest.NewRequest("GET", Prefix+"aws/a%2Fb?x=y", nil)
	before := *q.URL
	ptr := q.URL
	r.ServeHTTP(httptest.NewRecorder(), q)
	if q.URL != ptr || *q.URL != before {
		t.Fatal("original request URL mutated")
	}
}

func TestRegistryThirteenEscapedKeysCoexistOverHTTP(t *testing.T) {
	// Escaped-target identity is deliberate probe behavior, not S3 key semantics.
	keys := []string{"a/b", "a%2Fb", "a%2fb", "a%252Fb", "a%252fb", "a//b", "./a", "../a", "%2E/a", "%2E%2E/a", "%E6%97%A5", "%25E6%2597%25A5", "a%20b"}
	r := New("probe")
	state := map[string]string{}
	var mu sync.Mutex
	leaf := http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := q.URL.EscapedPath()
		if q.Method == http.MethodPut {
			body, err := io.ReadAll(io.LimitReader(q.Body, 64))
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			state[key] = string(body)
			w.WriteHeader(204)
			return
		}
		body, ok := state[key]
		if !ok {
			http.NotFound(w, q)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	if err := r.Register("probe", leaf); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(r)
	defer server.Close()
	client := server.Client()
	for i, key := range keys {
		q, err := http.NewRequest(http.MethodPut, server.URL+Prefix+"probe/bucket/"+key, strings.NewReader(fmt.Sprintf("value-%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(q)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 204 {
			t.Fatal(response.StatusCode)
		}
	}
	for i, key := range keys {
		response, err := client.Get(server.URL + Prefix + "probe/bucket/" + key)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != fmt.Sprintf("value-%d", i) {
			t.Fatalf("%q: %q status%d err%v", key, body, response.StatusCode, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(state) != 13 {
		t.Fatalf("collapsed keys: %d", len(state))
	}
}

func TestRegistryStrictLeafCanRejectEncodedContainerSeparator(t *testing.T) {
	r := New("strict")
	if err := r.Register("strict", http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		segments := strings.Split(strings.TrimPrefix(q.URL.EscapedPath(), "/"), "/")
		if len(segments) < 3 {
			http.Error(w, "bad shape", 400)
			return
		}
		account, err := url.PathUnescape(segments[0])
		if err != nil || strings.Contains(account, "/") {
			http.Error(w, "bad account", 400)
			return
		}
		container, err := url.PathUnescape(segments[1])
		if err != nil || strings.Contains(container, "/") {
			http.Error(w, "bad container", 400)
			return
		}
		w.WriteHeader(204)
	})); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		target string
		status int
	}{{"/account/container/blob%2Fkey", 204}, {"/account/container/blob%252Fkey", 204}, {"/account/container%2Fother/blob", 400}, {"/account%2Fother/container/blob", 400}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", Prefix+"strict"+tc.target, nil))
		if w.Code != tc.status {
			t.Fatalf("%q status%d", tc.target, w.Code)
		}
	}
}
