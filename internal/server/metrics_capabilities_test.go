package server

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCloudWatchMetricsCapabilityContractKeepsOtherServicesAndLimitsItsClaims(t *testing.T) {
	logs := map[string]any{"metrics": false, "alarms": false, "operations": []string{"CreateLogGroup"}}
	base := map[string]any{"services": map[string]any{"logs": logs, "kms": map[string]any{"operations": []string{"CreateKey"}}}, "cloudServices": map[string]any{"gcp": "preserved"}}
	before, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	result := addMetricsCapabilities(base)
	services := result["services"].(map[string]any)
	metric := services["cloudwatch"].(map[string]any)
	if !reflect.DeepEqual(metric["operations"], []string{"PutMetricData", "ListMetrics", "GetMetricStatistics"}) {
		t.Fatal("custom Metrics must report exactly its three supported APIs")
	}
	for key, want := range map[string]any{"available": true, "protocol": "Query", "apiVersion": "2010-08-01", "serviceSelector": "monitoring", "endpoint": "/", "providerEndpoint": "/_simulator/providers/aws/", "account": "000000000000", "sdkVerified": false, "console": true, "apiReference": false, "rawValuesOnly": true, "storageResolution": 60, "retentionDays": 15, "listVisibilityDays": 14} {
		if !reflect.DeepEqual(metric[key], want) {
			t.Fatalf("unexpected bounded Metrics capability %s", key)
		}
	}
	if !reflect.DeepEqual(metric["statistics"], []string{"SampleCount", "Sum", "Minimum", "Maximum", "Average"}) {
		t.Fatal("statistic capability must use the literal five supported names")
	}
	limits := metric["limits"].(map[string]any)
	wantLimits := map[string]any{"requestWireBytes": 1048576, "requestDecodedBytes": 1048576, "responseBytes": 2097152, "metricDataPerPut": 1000, "dimensionsPerMetric": 30, "series": 1000, "physicalSamples": 100000, "logicalStoredBytes": 67108864, "regions": 64, "listPageSize": 500, "cursorTTLSeconds": 3600, "statisticDatapoints": 1440}
	if !reflect.DeepEqual(limits, wantLimits) {
		t.Fatal("local Metrics limits changed")
	}
	delete(services, "cloudwatch")
	after, err := json.Marshal(result)
	if err != nil || string(before) != string(after) {
		t.Fatal("Metrics capability changed an existing provider/service claim")
	}
}
