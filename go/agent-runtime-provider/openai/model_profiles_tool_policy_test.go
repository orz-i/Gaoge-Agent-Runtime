package openai_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestOpenAIImportedForcedToolPolicyIsExactAndPreDispatch(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if string(payload["tool_choice"]) != `"required"` {
			t.Errorf("allowed model lost forced Tool choice: %s", payload["tool_choice"])
		}
		_, _ = fmt.Fprint(w, `{"object":"response","id":"physical-allowed","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()

	profiles := model.StaticModelProfiles{
		"future-model":       {SupportsForcedToolChoice: false},
		"future-model-proxy": {SupportsForcedToolChoice: true},
	}
	client, err := openai.New(openai.Config{APIKey: "synthetic", Endpoint: server.URL, ModelProfiles: profiles})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		Model:    "future-model",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must use lookup")}},
		Tools:    []tools.Definition{definition}, RequireToolCall: true,
	}
	if _, err := client.Generate(t.Context(), request); err == nil {
		t.Fatal("blocked forced Tool reached Unary")
	}
	if _, err := client.GenerateStream(t.Context(), request, nil); err == nil {
		t.Fatal("blocked forced Tool reached SSE")
	}
	if physical.Load() != 0 {
		t.Fatalf("Host-forbidden mandatory Tool reached physical provider %d times", physical.Load())
	}
	request.Model = "future-model-proxy"
	result, err := client.Generate(t.Context(), request)
	if err != nil || result.ResponseID != "physical-allowed" || len(result.ToolCalls) != 1 || physical.Load() != 1 {
		t.Fatalf("unblocked exact model inherited the wrong restriction: %+v err=%v HTTP=%d", result, err, physical.Load())
	}
}
