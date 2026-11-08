package provider_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Combining the Context canonical serializer and the native OpenAI replay
// counter is critical: independent Adapter-only tests cannot prove this
// entire route works after a function_call + opaque reasoning transcript.
func TestContextCounterRoutedOpaqueReasoningAndToolReply(t *testing.T) {
	var countCalls, generationCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses/input_tokens" {
			generationCalls.Add(1)
			t.Errorf("generation leak path %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		countCalls.Add(1)
		var countBody map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&countBody); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var wireItems []map[string]any
		if err := json.Unmarshal(countBody["input"], &wireItems); err != nil {
			t.Error(err)
		}
		if len(wireItems) != 4 || wireItems[1]["type"] != "reasoning" || wireItems[2]["type"] != "function_call" || wireItems[3]["type"] != "function_call_output" {
			t.Errorf("opaque replay was not counted as true native input: items=%d", len(wireItems))
		}
		if string(countBody["tool_choice"]) != `"required"` || len(countBody["text"]) == 0 ||
			len(countBody["reasoning"]) == 0 || len(countBody["tools"]) == 0 {
			t.Error("counting omitted Host-certified mandatory tools, schema, effort or Tool declarations")
		}
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":367}`))
	}))
	defer srv.Close()
	profiles := model.StaticModelProfiles{"native-replay": {
		StructuredJSONSchema: true, EffortLevels: []string{"high"}, SupportsForcedToolChoice: true,
	}}
	client, err := openai.New(openai.Config{
		Endpoint: srv.URL + "/v1/responses", APIKey: "fixture", HTTPClient: srv.Client(),
		BindingScope: "ctx-counter-full-replay", ModelProfiles: profiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "host-binding", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"z":1,"a":2}`)
	call, err := json.Marshal(map[string]any{"type": "function_call", "id": "fc_1", "status": "completed", "name": "lookup", "call_id": "call_1", "arguments": string(args)})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := json.Marshal([]json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-frozen"}`), call})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{
		RunID: "run", Model: "openai/native-replay",
		ModelOptions:    json.RawMessage(`{"reasoning":{"effort":"high"}}`),
		RequireToolCall: true,
		OutputContract:  &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)},
		Messages: []model.Message{
			{Role: model.RoleUser, Content: model.TextParts("find 分页 evidence")},
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call_1", ToolKey: "lookup", Name: "lookup", Arguments: args}},
				ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: replay}},
			{Role: model.RoleTool, ToolCallID: "call_1", Content: model.TextParts(`{"result":"ok"}`)},
		},
		Tools: []tools.Definition{{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"z":{"type":"integer"}}}`)}},
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: provider.ContextTokenCounter{Client: router}})
	assessment, err := manager.AssessModelRequest(t.Context(), request, runtimecontext.ModelWindow{
		ContextTokens:     1050000,
		TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-replay"},
	}, runtimecontext.Policy{MaxInputTokens: 32768})
	if err != nil || assessment.TokenCountSource != runtimecontext.CountExact ||
		assessment.HardTokenEstimate != 367 || countCalls.Load() != 1 || generationCalls.Load() != 0 {
		t.Fatalf("routed opaque replay failed exact counting: assessment=%+v err=%v calls=%d generation=%d",
			assessment, err, countCalls.Load(), generationCalls.Load())
	}

	// Vary original argument spelling systematically. The native count
	// must preserve the same opaque Tool/reasoning replay as Generate even
	// when nested JSON contains Unicode, spaces, exponent notation and
	// map ordering that generic canonicalJSON would alter.
	for index, spelling := range []string{
		`{"a":2,"z":1}`,
		`{"z":1,"a":2}`,
		`{ "z": 1, "a": 2 }`,
		"{\n  \"z\": 1,\n  \"a\": 2\n}",
		`{"nested":{"z":"one","a":"two"},"z":1,"a":2}`,
		`{"unicode":"中文🌱","z":1,"a":2}`,
		`{"escaped":"\u0061","z":1,"a":2}`,
		`{"scientific":1e0004,"z":1,"a":2}`,
		`{"array":[{"z":1,"a":2}],"z":1,"a":2}`,
	} {
		t.Run(fmt.Sprintf("signed-json-variant-%02d", index), func(t *testing.T) {
			variant := model.CloneRequest(request)
			variant.Messages[1].ToolCalls[0].Arguments = json.RawMessage(spelling)
			candidateCall, marshalErr := json.Marshal(map[string]any{
				"type": "function_call", "id": "fc_1", "status": "completed",
				"name": "lookup", "call_id": "call_1", "arguments": spelling,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			variant.Messages[1].ProviderReplay.Data, marshalErr = json.Marshal([]json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque-frozen"}`),
				candidateCall,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			before := countCalls.Load()
			observed, countErr := manager.AssessModelRequest(t.Context(), variant, runtimecontext.ModelWindow{
				ContextTokens:     1050000,
				TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-replay"},
			}, runtimecontext.Policy{MaxInputTokens: 32768})
			if countErr != nil || observed.TokenCountSource != runtimecontext.CountExact ||
				observed.HardTokenEstimate != 367 || countCalls.Load() != before+1 || generationCalls.Load() != 0 {
				t.Fatalf("original signed variant failed exact counting: %+v err=%v nativeDelta=%d",
					observed, countErr, countCalls.Load()-before)
			}
		})
	}

	// This is the original defect's minimal counterexample: reordering
	// nested RawMessage JSON changes a signed Tool argument's bytes, even
	// though a generic map re-encoding says the JSON values are equivalent.
	typed, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err = json.Unmarshal(typed, &generic); err != nil {
		t.Fatal(err)
	}
	reordered, err := json.Marshal(generic)
	if err != nil || bytes.Equal(typed, reordered) {
		t.Fatalf("fixture did not retain the physically distinct Tool argument order: err=%v", err)
	}

	// A native count is NOT permission to count mutated signed replay. The
	// original error path must remain conservative and never contact HTTP.
	for name, mutate := range map[string]func(*model.Request){
		"changed-call-arguments": func(value *model.Request) {
			value.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"z":9,"a":2}`)
		},
		"same-values-different-key-order": func(value *model.Request) {
			value.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"a":2,"z":1}`)
		},
		"wrong-replay-provider": func(value *model.Request) {
			value.Messages[1].ProviderReplay.Provider = "other"
		},
		"duplicate-call-arguments": func(value *model.Request) {
			value.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"z":1,"z":2,"a":2}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := model.CloneRequest(request)
			mutate(&changed)
			before := countCalls.Load()
			fallback, err := manager.AssessModelRequest(t.Context(), changed, runtimecontext.ModelWindow{
				ContextTokens:     1050000,
				TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-replay"},
			}, runtimecontext.Policy{MaxInputTokens: 32768})
			if err != nil || fallback.TokenCountSource != runtimecontext.CountEstimated ||
				fallback.HardTokenEstimate < fallback.SerializedBytes ||
				countCalls.Load() != before || generationCalls.Load() != 0 {
				t.Fatalf("mutated signed replay bypassed the fail-closed byte budget: assessment=%+v err=%v nativeCalls=%d",
					fallback, err, countCalls.Load()-before)
			}
		})
	}

	// The byte fallback must never become smaller than the original
	// typed request merely because generic JSON normalization collapses
	// a long but valid decimal literal to a shorter float representation.
	long := model.CloneRequest(request)
	long.Messages[1].ToolCalls[0].Arguments = json.RawMessage(`{"n":1.` + strings.Repeat("0", 3000) + `}`)
	rawBytes, err := json.Marshal(long)
	if err != nil {
		t.Fatal(err)
	}
	before := countCalls.Load()
	fallback, err := manager.AssessModelRequest(t.Context(), long, runtimecontext.ModelWindow{
		ContextTokens:     1050000,
		TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai", Model: "native-replay"},
	}, runtimecontext.Policy{MaxInputTokens: 32768})
	if err != nil || fallback.TokenCountSource != runtimecontext.CountEstimated ||
		fallback.HardTokenEstimate < int64(len(rawBytes)) ||
		fallback.SerializedBytes < int64(len(rawBytes)) || countCalls.Load() != before {
		t.Fatalf("normalization reduced the fail-closed hard-byte bound: raw=%d assessment=%+v err=%v",
			len(rawBytes), fallback, err)
	}
}
