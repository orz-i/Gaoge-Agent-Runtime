package http

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPublicDiagnosticPayloadPreservesJSONNumbersAndRedaction(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{
		"eventHead":9007199254740993,
		"input":{"id":18446744073709551615,"providerReplay":{"secret":"private"}},
		"items":[{"result":{"amount":0.123456789012345678901,"error":{"code":"ORDER_CONFLICT"},"debug":"private"}}],
		"values":[-9007199254740993,1e400],
		"errorDetail":"private",
		"failure":{"code":"agent.tool_failed","detail":"private"}
	}`)
	original := bytes.Clone(payload)
	projected := PublicDiagnosticPayload(payload)
	const want = `{"eventHead":9007199254740993,"failure":{"code":"agent.tool_failed"},"input":{"id":18446744073709551615},"items":[{"result":{"amount":0.123456789012345678901,"error":{"code":"ORDER_CONFLICT"}}}],"values":[-9007199254740993,1e400]}`
	if string(projected) != want {
		t.Fatalf("public payload lost numeric or redaction semantics:\ngot  %s\nwant %s", projected, want)
	}
	if !bytes.Equal(payload, original) {
		t.Fatal("public projection changed its authoritative payload")
	}
}

func TestPublicDiagnosticPayloadRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{"", " \t\n", `{`, `{"id":9007199254740993} {"debug":"private"}`, `{"id":1}garbage`, `"value" true`} {
		t.Run(payload, func(t *testing.T) {
			if projected := PublicDiagnosticPayload(json.RawMessage(payload)); projected != nil {
				t.Fatalf("invalid payload %q was projected: %s", payload, projected)
			}
		})
	}
}
