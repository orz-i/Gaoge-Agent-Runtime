package openai_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A stored ProviderReplay cannot launder a missing/null output_text.text into
// a trusted Agent-visible message, even if the Tool ID/arguments still match.
// Fail before a continuation HTTP dispatch; no second paid request is allowed.
func TestStoredOpenAIReplayRequiresPhysicalOutputText(t *testing.T) {
	for _, tc := range []struct {
		name, textMember string
		valid            bool
	}{
		{name: "missing_text"},
		{name: "null_text", textMember: `,"text":null`},
		{name: "valid_text", textMember: `,"text":""`, valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests++
				_, _ = w.Write([]byte(`{"object":"response","id":"continuation","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":4,"output_tokens":2}}`))
			})
			replay := `[{"type":"message","phase":"commentary","content":[{"type":"output_text"` + tc.textMember + `},{"type":"output_text","text":"Working"}]},` +
				`{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}]`
			req := model.Request{Messages: []model.Message{
				{Role: model.RoleUser, Content: model.TextParts("call lookup")},
				{Role: model.RoleAssistant, Content: model.TextParts("Working"),
					ToolCalls:      []tools.Call{{ID: "call-1", Name: "lookup", ToolKey: definition.Key, Arguments: json.RawMessage(`{}`)}},
					ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(replay)}},
				{Role: model.RoleTool, ToolCallID: "call-1", Content: model.TextParts(`{"ok":true}`)},
			}, Tools: []tools.Definition{definition}}
			response, err := client.Generate(t.Context(), req)
			if tc.valid {
				if err != nil || requests != 1 || response.Content != "done" {
					t.Fatalf("valid persisted phased replay rejected: response=%+v err=%v requests=%d", response, err, requests)
				}
				return
			}
			if !errors.Is(err, openai.ErrUnsupported) || requests != 0 || response.ResponseID != "" || response.Usage != nil {
				t.Fatalf("invalid replay triggered upstream continuation: response=%+v err=%v requests=%d", response, err, requests)
			}
		})
	}
}

func TestStoredOpenAIReplayRejectsMissingMessageContent(t *testing.T) {
	for _, message := range []string{`{"type":"message","phase":"commentary"}`, `{"type":"message","phase":"commentary","content":null}`} {
		requests := 0
		client := newClient(t, func(w http.ResponseWriter, _ *http.Request) { requests++; _, _ = w.Write([]byte(`{}`)) })
		replay := `[` + message + `,{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}]`
		req := model.Request{Messages: []model.Message{
			{Role: model.RoleUser, Content: model.TextParts("lookup")},
			{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call-1", Name: "lookup", ToolKey: definition.Key, Arguments: json.RawMessage(`{}`)}}, ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "responses.output.v1", Data: json.RawMessage(replay)}},
			{Role: model.RoleTool, ToolCallID: "call-1", Content: model.TextParts(`{}`)},
		}, Tools: []tools.Definition{definition}}
		response, err := client.Generate(t.Context(), req)
		if !errors.Is(err, openai.ErrUnsupported) || requests != 0 || response.ResponseID != "" {
			t.Fatalf("invalid replay dispatched: %+v %v requests=%d", response, err, requests)
		}
	}
}
