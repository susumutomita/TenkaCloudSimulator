package server

import "github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"

// catalogCapabilities advertises the reviewed model-driven AWS services.
// Every listed operation is implemented; every other official operation of
// these services fails explicitly with UnknownOperationException.
func catalogCapabilities(engine *catalog.Engine) map[string]any {
	return map[string]any{
		"services":     engine.Describe(),
		"protocols":    []string{"JSON 1.0", "JSON 1.1", "REST-JSON"},
		"routing":      "X-Amz-Target prefix for JSON protocols; SigV4 credential-scope service for REST-JSON. Signatures are not verified; use dummy TCSIM credentials.",
		"state":        "configuration metadata in provider-owned SQLite; no workload, data plane, network, OAuth or real AWS call",
		"reference":    "/_simulator/docs/catalog.html",
		"model":        "documentation-free subsets of the pinned official botocore 1.41.6 service models (Apache-2.0)",
		"requestBytes": 256 << 10, "documentBytes": 64 << 10, "storedBytes": 32 << 20, "tagsPerResource": 50,
		"idempotencyHours": 24, "unsupported": "official operations not listed per service return HTTP 501 UnknownOperationException; unsupported members return the operation's validation exception",
	}
}
