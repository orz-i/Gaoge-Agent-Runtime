package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var outputContract = &model.OutputContract{
	Name:   "final_answer",
	Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
}

func TestStrictJSONSchemaWireAndToolContinuation(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Model string `json:"model"`
			Store bool   `json:"store"`
			Text  struct {
				Format struct {
					Type   string          `json:"type"`
					Name   string          `json:"name"`
					Schema json.RawMessage `json:"schema"`
					Strict bool            `json:"strict"`
				} `json:"format"`
			} `json:"text"`
			Include []string `json:"include"`
			Tools   []any    `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "gpt-4.1" || body.Store || body.Text.Format.Type != "json_schema" || body.Text.Format.Name != "final_answer" ||
			!body.Text.Format.Strict || string(body.Text.Format.Schema) != string(outputContract.Schema) {
			t.Errorf("native strict output not sent: %+v", body)
		}
		if len(body.Tools) != 1 || len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" {
			t.Errorf("tool+strict JSON contract missing replay requirements: %+v", body)
		}
		if calls == 1 {
			_, _ = fmt.Fprint(w, `{"id":"resp-tool","status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"resp-json","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"answer\":\"from tool\"}"}]}],"usage":{"input_tokens":4,"output_tokens":3}}`)
		}
	})
	request := model.Request{Model: "gpt-4.1", OutputContract: model.CloneOutputContract(outputContract),
		Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}, Tools: []tools.Definition{{Key: "lookup:v1", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	first, err := client.Generate(t.Context(), request)
	if err != nil || len(first.ToolCalls) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	request.Messages = append(request.Messages, model.Message{Role: model.RoleAssistant, ToolCalls: first.ToolCalls},
		model.Message{Role: model.RoleTool, ToolCallID: "c1", Content: "{}"})
	second, err := client.Generate(t.Context(), request)
	if err != nil || second.Content != `{"answer":"from tool"}` || calls != 2 {
		t.Fatalf("second=%+v err=%v calls=%d", second, err, calls)
	}
}
func TestStructuredOutputCapabilityFailsBeforeHTTPAndSupportsHostVerifiedOverride(t *testing.T) {
	calls := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{}"}]}]}`))
	})
	req := model.Request{Model: "unverified-gateway-model", Messages: []model.Message{{Role: model.RoleUser, Content: "answer"}}, OutputContract: outputContract}
	_, err := client.Generate(context.Background(), req)
	if !errors.Is(err, model.ErrCapabilityUnavailable) || calls != 0 {
		t.Fatalf("unknown model dispatched: %v, calls=%d", err, calls)
	}
	capabilities, err := client.Capabilities(t.Context(), "gpt-4.1")
	if err != nil || !capabilities.StructuredJSONSchema {
		t.Fatalf("known capability missing: %+v err=%v", capabilities, err)
	}
	older, err := client.Capabilities(t.Context(), "gpt-4o-2024-05-13")
	if err != nil || older.StructuredJSONSchema {
		t.Fatalf("early model falsely admitted: %+v err=%v", older, err)
	}
}
func TestStructuredOutputRefusalAndStreaming(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintln(w, "event: response.completed")
			_, _ = fmt.Fprintln(w, `data: {"type":"response.completed","response":{"id":"resp-sse","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"answer\":\"streamed\"}"}]}],"usage":{"input_tokens":2,"output_tokens":3}}}`)
			_, _ = fmt.Fprintln(w)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"resp-refusal","status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply."}]}],"usage":{"input_tokens":1,"output_tokens":2}}`)
		}
	})
	req := model.Request{Model: "gpt-4.1", OutputContract: outputContract, Messages: []model.Message{{Role: model.RoleUser, Content: "answer"}}}
	refused, err := client.Generate(t.Context(), req)
	if err != nil || !refused.Refused || refused.Content != "Cannot comply." {
		t.Fatalf("refusal lost: %+v err=%v", refused, err)
	}
	streamed, err := client.GenerateStream(t.Context(), req, nil)
	if err != nil || streamed.Refused || streamed.Content != `{"answer":"streamed"}` {
		t.Fatalf("stream lost native structured output: %+v err=%v", streamed, err)
	}
}

var _ model.CapabilityProvider = (*openai.Client)(nil)
