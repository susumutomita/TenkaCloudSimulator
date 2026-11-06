package server

// addMetricsCapabilities describes the bounded native custom-metric APIs. It
// does not enable automatic Logs/Lambda emission or claim general AWS SDK parity.
// Console=true means the source-wired view is available; it does not certify
// browser acceptance. SDK and served-reference verification remain separate.
func addMetricsCapabilities(base map[string]any) map[string]any {
	services, ok := base["services"].(map[string]any)
	if !ok {
		services = map[string]any{}
		base["services"] = services
	}
	services["cloudwatch"] = map[string]any{
		"operations": []string{"PutMetricData", "ListMetrics", "GetMetricStatistics"},
		"available":  true, "protocol": "Query", "apiVersion": "2010-08-01", "responseFormat": "XML", "serviceSelector": "monitoring",
		"endpoint": "/", "providerEndpoint": "/_simulator/providers/aws/", "account": "000000000000",
		"authorization": "Local authenticated exercise identity; current caller-transaction identity policy; not AWS SigV4 authentication",
		"resource":      "*", "namespaces": "Custom only; AWS/ is a local unsupported namespace restriction",
		"rawValuesOnly": true, "storageResolution": 60, "retentionDays": 15, "listVisibilityDays": 14,
		"retention":   "Local raw samples only; accepted authorized Put may collect expired samples in its own region and namespace; reads do not mutate storage",
		"statistics":  []string{"SampleCount", "Sum", "Minimum", "Maximum", "Average"},
		"omittedUnit": "Put records None; Get returns each recorded unit as a separate stream",
		"sdkVerified": false, "console": true, "apiReference": false,
		"limits": map[string]any{"requestWireBytes": 1 << 20, "requestDecodedBytes": 1 << 20, "responseBytes": 2 << 20,
			"metricDataPerPut": 1000, "dimensionsPerMetric": 30, "series": 1000, "physicalSamples": 100000,
			"logicalStoredBytes": 64 << 20, "regions": 64, "listPageSize": 500, "cursorTTLSeconds": 3600, "statisticDatapoints": 1440},
		"compression": "Optional single gzip member; independent wire and decoded local limits; malformed/trailing members rejected",
		"paging":      "Opaque persisted HMAC cursor bound to database, region, options, revision and captured visibility time; accepted regional Put invalidates old cursors",
		"unsupported": []string{"JSON protocol", "StatisticValues/Values/Counts", "high-resolution metrics", "percentiles/extended statistics",
			"entities", "GetMetricData", "alarms", "Insights", "automatic service emission", "CloudFormation metric resources", "rollups beyond the local raw window"},
	}
	return base
}
