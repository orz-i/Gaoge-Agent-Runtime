package anthropic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestImportedForcedToolPolicyIsExactAndPreDispatchForUnaryAndSSE(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body["tool_choice"]) == 0 {
			t.Error("supported forced tool choice omitted")
		}
		_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"physical-allowed","stop_reason":"tool_use","content":[{"type":"tool_use","id":"tool-id","name":"lookup","input":{}}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()

	catalog := model.StaticModelProfiles{
		"new-future-model":       {StructuredJSONSchema: true, DisallowForcedToolChoice: true},
		"new-future-model-proxy": {StructuredJSONSchema: true},
	}
	client, err := anthropic.New(anthropic.Config{APIKey: "synthetic", Endpoint: server.URL, ModelProfiles: catalog})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		Model:    "new-future-model",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must use lookup")}},
		Tools:    []tools.Definition{definition}, RequireToolCall: true,
	}
	if _, err := client.Generate(t.Context(), request); err == nil {
		t.Fatal("Host forbidden forced Tool choice accepted in Unary")
	}
	if _, err := client.GenerateStream(t.Context(), request, nil); err == nil {
		t.Fatal("Host forbidden forced Tool choice accepted in SSE")
	}
	if physical.Load() != 0 {
		t.Fatalf("forbidden Host Tool authority caused %d HTTP requests", physical.Load())
	}

	request.Model = "new-future-model-proxy" // exact ID, no suffix inference
	result, err := client.Generate(t.Context(), request)
	if err != nil || result.ResponseID != "physical-allowed" || len(result.ToolCalls) != 1 ||
		physical.Load() != 1 {
		t.Fatalf("allowed exact model lost forced Tool protocol: response=%+v err=%v calls=%d", result, err, physical.Load())
	}
}
