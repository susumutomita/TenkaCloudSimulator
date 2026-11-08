package server

import (
	"encoding/json"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

// catalogDescriptorsPath serves the full descriptors of every catalog service.
const catalogDescriptorsPath = "/_simulator/v1/catalog/aws"

// catalogDescriptors serves the full AWS catalog descriptors that first-party
// clients use to build native requests.
func catalogDescriptors(engine *catalog.Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"services": engine.Describe()})
	}
}

// catalogCapabilities advertises the reviewed model-driven AWS services.
// Every listed operation is implemented; every other official operation of
// these services fails explicitly with UnknownOperationException.
// The capability document carries a compact summary; full descriptors
// (operations, members, resources) are served by catalogDescriptorsPath so
// the capability document stays bounded as services are added.
func catalogCapabilities(engine *catalog.Engine) map[string]any {
	summaries := []map[string]any{}
	for _, d := range engine.Describe() {
		summaries = append(summaries, map[string]any{"model": d.Model, "title": d.Title, "serviceId": d.ServiceID, "protocol": d.Protocol,
			"issues": d.Issues, "operations": len(d.Operations), "officialOperations": d.Official})
	}
	return map[string]any{
		"services":     summaries,
		"descriptors":  catalogDescriptorsPath,
		"protocols":    []string{"JSON 1.0", "JSON 1.1", "REST-JSON"},
		"routing":      "X-Amz-Target prefix for JSON protocols; SigV4 credential-scope service for REST-JSON. Signatures are not verified; use dummy TCSIM credentials.",
		"state":        "configuration metadata in provider-owned SQLite; no workload, data plane, network, OAuth or real AWS call",
		"reference":    "/_simulator/docs/catalog.html",
		"model":        "documentation-free subsets of the pinned official botocore 1.41.6 service models (Apache-2.0)",
		"requestBytes": 256 << 10, "documentBytes": 64 << 10, "storedBytes": 32 << 20, "tagsPerResource": 50,
		"idempotencyHours": 24, "unsupported": "official operations not listed per service return HTTP 501 UnknownOperationException; unsupported members return the operation's validation exception",
	}
}
