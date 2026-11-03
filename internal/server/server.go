package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/legal"
	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

//go:embed web/* apidocs/*
var assets embed.FS

func New(path string) (http.Handler, func() error, error) {
	store, err := core.New(path)
	if err != nil {
		return nil, nil, err
	}
	identities := core.NewIdentities(store)
	gateway := &aws.Gateway{Store: store, Identities: identities}
	registry := provider.New("aws")
	if err := registry.Register("aws", gateway); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/_simulator/licenses/", http.StripPrefix("/_simulator/licenses/", legal.Handler()))
	mux.Handle(provider.Prefix, registry)
	docs, _ := fs.Sub(assets, "apidocs")
	mux.Handle("/_simulator/docs/", http.StripPrefix("/_simulator/docs/", http.FileServer(http.FS(docs))))
	web, _ := fs.Sub(assets, "web")
	mux.Handle("/_simulator/console/", http.StripPrefix("/_simulator/console/", http.FileServer(http.FS(web))))
	mux.HandleFunc("/_simulator/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","simulator":true}`))
	})
	mux.HandleFunc("/_simulator/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"protocolVersion": "2026-11-03", "simulatorVersion": "0.1.0", "providers": registry.Names(), "persistence": func() string {
			if path == "" || path == ":memory:" {
				return "memory"
			}
			return "file"
		}(), "provider": "aws", "service": "s3", "operations": []string{"ListBuckets", "CreateBucket", "HeadBucket", "GetBucketLocation", "DeleteBucket", "ListObjectsV2", "PutObject", "GetObject", "HeadObject", "DeleteObject"}, "regions": aws.Regions, "limits": map[string]int{"objectBytes": core.MaxObjectBytes, "storedBytes": core.MaxStoredBytes, "buckets": core.MaxBuckets, "objects": core.MaxObjects}, "state": "SQLite; ephemeral by default, persist with --state; one database per environment", "authentication": "unsigned or simulator TCSIM credentials; signatures not evaluated; identity policy subset enforced via x-simulator-principal", "unsupported": []string{"resource policies, ACLs, conditions, boundaries, SCPs, sessions", "versioning", "multipart uploads", "presigned URLs", "virtual-host addressing", "other cloud services", "legacy TenkaCloud world/deploy/grading protocol"}})
	})
	mux.Handle("/_simulator/v1/iam/", identityAPI(identities))
	mux.Handle("/", registry)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-TenkaCloud-Simulator", "true")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'; sandbox allow-scripts allow-same-origin allow-downloads allow-forms")
		if strings.HasPrefix(r.URL.Path, "/_simulator/docs/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; worker-src blob:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, e := url.Parse(origin)
			if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				http.Error(w, "Cross-origin requests are not allowed", 403)
				return
			}
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(w, "Cross-site requests are not allowed", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_simulator/v1/") && r.URL.Path != "/_simulator/v1/capabilities" && !strings.HasPrefix(r.URL.Path, "/_simulator/v1/iam/") {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/_simulator/") || strings.HasPrefix(r.URL.Path, provider.Prefix) {
			registry.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return handler, store.Close, nil
}
