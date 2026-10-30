package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var outputContract = &model.OutputContract{
	Name:   "final_answer",
	Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
}

func TestStrictJSONSchemaNativeWireAndToolContinuation(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Format struct {
					Type   string          `json:"type"`
					Schema json.RawMessage `json:"schema"`
				} `json:"format"`
			} `json:"output_config"`
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "claude-sonnet-5-5" || body.OutputConfig.Format.Type != "json_schema" ||
			string(body.OutputConfig.Format.Schema) != string(outputContract.Schema) || len(body.Tools) != 1 {
			t.Errorf("native structured output missing: %+v", body)
		}
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg-tool", "stop_reason": "tool_use", "content": []any{
					map[string]any{"type": "tool_use", "id": "c1", "name": "lookup", "input": map[string]any{}},
				}, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2},
			})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg-json", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": `{"answer":"from tool"}`}},
				"usage": map[string]any{"input_tokens": 4, "output_tokens": 3},
			})
		}
	})
	req := model.Request{Model: "claude-sonnet-5-5", OutputContract: model.CloneOutputContract(outputContract),
		Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}},
		Tools:    []tools.Definition{{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	first, err := client.Generate(t.Context(), req)
	if err != nil || len(first.ToolCalls) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.Messages = append(req.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: first.ToolCalls},
		model.Message{Role: model.RoleTool, ToolCallID: "c1", Content: "{}"})
	second, err := client.Generate(t.Context(), req)
	if err != nil || second.Content != `{"answer":"from tool"}` || calls != 2 {
		t.Fatalf("second=%+v err=%v calls=%d", second, err, calls)
	}
}
func TestStructuredOutputKnownUnknownAndExplicitGatewayModel(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "m", "stop_reason": "end_turn", "content": []any{map[string]any{"type": "text", "text": `{"answer":"ok"}`}}})
	})
	known, err := client.Capabilities(t.Context(), "claude-sonnet-5-5")
	if err != nil || !known.StructuredJSONSchema {
		t.Fatalf("known capability absent: %+v %v", known, err)
	}
	old, err := client.Capabilities(t.Context(), "claude-sonnet-3-7")
	if err != nil || old.StructuredJSONSchema {
		t.Fatalf("old model falsely admitted: %+v %v", old, err)
	}
	req := model.Request{Model: "gateway/private-model", Messages: []model.Message{{Role: model.RoleUser, Content: "answer"}}, OutputContract: outputContract}
	_, err = client.Generate(context.Background(), req)
	if !errors.Is(err, model.ErrCapabilityUnavailable) || calls != 0 {
		t.Fatalf("unsupported model dispatched: %v calls=%d", err, calls)
	}
}
func TestStructuredOutputRefusalAndStreaming(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "refused", "stop_reason": "refusal", "content": []any{map[string]any{"type": "text", "text": "Cannot comply."}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(name string, event any) {
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintln(w, "event: "+name)
			_, _ = fmt.Fprintln(w, "data: "+string(raw))
			_, _ = fmt.Fprintln(w)
		}
		send("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": "sse-structured", "usage": map[string]any{"input_tokens": 2}}})
		send("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		send("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": `{"answer":"streamed"}`}})
		send("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		send("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 3}})
		send("message_stop", map[string]any{"type": "message_stop"})
	})
	req := model.Request{Model: "claude-sonnet-5-5", OutputContract: outputContract, Messages: []model.Message{{Role: model.RoleUser, Content: "answer"}}}
	refused, err := client.Generate(t.Context(), req)
	if err != nil || !refused.Refused {
		t.Fatalf("refusal not explicit: %+v err=%v", refused, err)
	}
	stream, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || stream.Refused || stream.Content != `{"answer":"streamed"}` {
		t.Fatalf("stream structured result: %+v err=%v", stream, err)
	}
}

var _ model.CapabilityProvider = (*anthropic.Client)(nil)
