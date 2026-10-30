package anthropic_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostVerifiedGatewayStrictOutputCapability(t *testing.T) {
	client, err := anthropic.New(anthropic.Config{APIKey: "test", StructuredOutputModels: []string{"private-model-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := client.Capabilities(t.Context(), "private-model-v1")
	if err != nil || !caps.StructuredJSONSchema {
		t.Fatalf("explicit capability missing: %+v err=%v", caps, err)
	}
	for _, id := range []string{"other-model", "claude-sonnet-5-5-20private", "claude-opus-4-5-unknown"} {
		unknown, err := client.Capabilities(t.Context(), id)
		if err != nil || unknown.StructuredJSONSchema {
			t.Fatalf("unverified model %q capability leaked: %+v err=%v", id, unknown, err)
		}
	}
}
func TestStructuredOutputTruncationDistinctFromRefusal(t *testing.T) {
	// Existing TestTruncatedAndThinkingResponsesRejected tests the 200-status
	// truncated message response. This directly tests the typed sentinel.
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "truncated", "stop_reason": "max_tokens",
			"content": []any{map[string]any{"type": "text", "text": "{"}}})
	})
	req := model.Request{Model: "claude-sonnet-5-5", OutputContract: outputContract, Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("answer")}}}
	_, err := client.Generate(t.Context(), req)
	if !errors.Is(err, anthropic.ErrIncomplete) {
		t.Fatalf("truncated JSON was not classified: %v", err)
	}
}
