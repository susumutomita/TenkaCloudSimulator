package server

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

var principalName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func identityAPI(identities *core.Identities) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
		}
		principal := r.Header.Get("x-simulator-principal")
		if principal != "" && principal != core.Administrator {
			fail(403, "Only simulator-admin can manage identity policy state.")
			return
		}
		if r.URL.Path == "/_simulator/v1/iam/principals" {
			if r.Method != "GET" {
				fail(405, "Use GET to list principals.")
				return
			}
			names, err := identities.Names()
			if err != nil {
				fail(500, "Could not read identity state.")
				return
			}
			sort.Strings(names)
			_ = json.NewEncoder(w).Encode(map[string]any{"principals": names})
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/_simulator/v1/iam/principals/")
		name, suffix, ok := strings.Cut(rest, "/")
		if !ok || suffix != "policy" || !principalName.MatchString(name) || name == core.Administrator {
			fail(400, "Use /_simulator/v1/iam/principals/{name}/policy with a non-reserved 1–64 character name.")
			return
		}
		switch r.Method {
		case "GET":
			p, exists, err := identities.Policy(name)
			if err != nil {
				fail(500, "Could not read identity policy.")
				return
			}
			if !exists {
				fail(404, "Principal not found.")
				return
			}
			_ = json.NewEncoder(w).Encode(p)
		case "PUT":
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
			if err != nil {
				fail(413, "Policies are limited to 32 KiB.")
				return
			}
			p, err := core.ParsePolicy(data)
			if err != nil {
				fail(400, err.Error())
				return
			}
			if err = identities.Set(name, p); err != nil {
				fail(400, err.Error())
				return
			}
			_ = json.NewEncoder(w).Encode(p)
		case "DELETE":
			if err := identities.Delete(name); err != nil {
				fail(500, "Could not delete identity policy.")
				return
			}
			w.WriteHeader(204)
		default:
			fail(405, "Use GET, PUT or DELETE.")
		}
	})
}
