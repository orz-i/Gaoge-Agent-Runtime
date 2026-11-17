package http

import (
	"encoding/json"
	"testing"
)

func TestPublicDiagnosticProjectionRetainsLegitimateBusinessFailurePayload(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"result":{"error":{"code":"OUT_OF_STOCK","message":"No matching item"}},"items":[{"failure":{"reason":"invalid part"}}],"privateDebug":{"errorDetail":"Bearer sk-RAW-DIAGNOSTIC"}}`)
	public := PublicDiagnosticPayload(input)
	var object map[string]any
	if err := json.Unmarshal(public, &object); err != nil {
		t.Fatal(err)
	}
	result, ok := object["result"].(map[string]any)
	if !ok {
		t.Fatalf("normal result lost: %s", public)
	}
	failure, ok := result["error"].(map[string]any)
	if !ok || failure["code"] != "OUT_OF_STOCK" {
		t.Fatalf("business error lost: %s", public)
	}
	items, ok := object["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("business list lost: %s", public)
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["failure"] == nil {
		t.Fatalf("business failure lost: %s", public)
	}
	if _, exists := object["privateDebug"]; exists {
		t.Fatalf("private diagnostic container must not be exposed: %s", public)
	}
}
