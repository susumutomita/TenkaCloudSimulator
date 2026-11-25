package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAzureQueueReferenceUsesLocalOwnerAndAccount(t *testing.T) {
	cfg := DefaultCloudStorageConfig()
	cfg.AzureAccount = "referenceaccount"
	cfg.AzureOwner = "referenceowner"
	h, cleanup, err := NewWithCloudStorage(":memory:", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	})
	token := strings.Repeat("a", 64)
	secured, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []string{cfg.AzureOwner, "another"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1/_simulator/v1/capabilities", nil)
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("X-Simulator-Principal", principal)
		w := httptest.NewRecorder()
		secured.ServeHTTP(w, r)
		var caps struct {
			Queue *struct {
				Console bool
				Account string
			} `json:"azureQueue"`
		}
		if w.Code != 200 {
			t.Fatalf("capabilities status %d: %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
			t.Fatal(err)
		}
		if principal == cfg.AzureOwner && (caps.Queue == nil || !caps.Queue.Console || caps.Queue.Account != cfg.AzureAccount) {
			t.Fatal("configured owner did not discover the native console")
		}
		if principal != cfg.AzureOwner && caps.Queue != nil {
			t.Fatal("peer unexpectedly discovered owner console")
		}
	}

	for _, tc := range []struct {
		name, method, principal, path, origin string
		auth                                  bool
		status                                int
	}{
		{"anonymous", "GET", "", azureQueueReferencePath, "", false, 303},
		{"wrong owner", "GET", "another", azureQueueReferencePath, "", true, 403},
		{"cross origin", "GET", cfg.AzureOwner, azureQueueReferencePath, "https://example.invalid", true, 403},
		{"read only", "POST", cfg.AzureOwner, azureQueueReferencePath, "", true, 405},
		{"local owner", "GET", cfg.AzureOwner, azureQueueReferencePath, "", true, 200},
		{"static page", "GET", cfg.AzureOwner, "/_simulator/docs/azure-queue.html", "", true, 200},
		{"static script", "GET", cfg.AzureOwner, "/_simulator/docs/azure-queue-init.js", "", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://127.0.0.1"+tc.path, nil)
			if tc.auth {
				r.Header.Set("X-Simulator-Token", token)
				r.Header.Set("X-Simulator-Principal", tc.principal)
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			secured.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.path != azureQueueReferencePath || tc.status != 200 {
				return
			}
			var spec struct {
				Paths map[string]struct {
					Put struct {
						Parameters []struct {
							Name   string
							Schema struct{ Default string }
						}
					}
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
				t.Fatal(err)
			}
			for _, p := range spec.Paths["/{account}/{queue}"].Put.Parameters {
				if p.Name == "account" && p.Schema.Default == cfg.AzureAccount {
					return
				}
			}
			t.Fatal("configured account missing")
		})
	}
}
