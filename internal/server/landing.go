package server

import (
	"bytes"
	"embed"
	"net/http"
	"path"
	"time"
)

//go:embed landing/*
var landingAssets embed.FS

// These exact, read-only resources contain product information and dummy
// architecture data. They never confer authority to a simulator service.
var landingPaths = map[string]string{
	"/_simulator/":                            "landing/index.html",
	"/_simulator/landing/landing.css":         "landing/landing.css",
	"/_simulator/landing/landing.mjs":         "landing/landing.mjs",
	"/_simulator/landing/topology-data.mjs":   "landing/topology-data.mjs",
	"/_simulator/landing/topology-canvas.mjs": "landing/topology-canvas.mjs",
}

func isLandingPath(requestPath string) bool {
	_, ok := landingPaths[requestPath]
	return ok
}

func serveLanding(w http.ResponseWriter, r *http.Request) {
	name, ok := landingPaths[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	content, err := landingAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "Landing resource unavailable", http.StatusInternalServerError)
		return
	}
	if path.Ext(name) == ".mjs" {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	http.ServeContent(w, r, path.Base(name), time.Time{}, bytes.NewReader(content))
}
