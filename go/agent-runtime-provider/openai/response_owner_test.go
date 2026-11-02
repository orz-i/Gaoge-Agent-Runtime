package openai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Only a completed Responses resource (object=response) owns the output
// function_call items. Arbitrary gateway resources cannot manufacture local
// Tool dispatch authority merely by copying response-shaped fields.
func TestResponsesOuterResourceIdentityMustBeResponse(t *testing.T) {
	for _, tc := range []struct {
		name, object string
		allowed      bool
	}{
		{"valid_response", `"response"`, true},
		{"foreign_chat_completion", `"chat.completion"`, false},
		{"foreign_image", `"image"`, false},
		{"missing_object", "", false},
		{"null_object", "null", false},
		{"empty_object", `""`, false},
		{"nonstring_object", "42", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "unary"
				if streaming {
					mode = "sse"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var sent struct {
							Stream     bool   `json:"stream"`
							ToolChoice string `json:"tool_choice"`
						}
						if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
							t.Error(err)
						}
						if sent.Stream != streaming || sent.ToolChoice != "required" {
							t.Errorf("wrong outbound request: %+v", sent)
						}
						resource := map[string]any{"id": "resp-owner", "status": "completed", "usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
							"output": []any{map[string]any{"type": "function_call", "status": "completed", "call_id": "local-1", "name": "lookup", "arguments": `{}`}}}
						if tc.object != "" {
							var object any
							if err := json.Unmarshal([]byte(tc.object), &object); err != nil {
								t.Fatal(err)
							}
							resource["object"] = object
						}
						raw, err := json.Marshal(resource)
						if err != nil {
							t.Fatal(err)
						}
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", raw)
						} else {
							_, _ = w.Write(raw)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
						Tools: []tools.Definition{definition}, RequireToolCall: true}
					terminal := 0
					var resp model.Response
					var err error
					if streaming {
						resp, err = client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
							if evt.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						resp, err = client.Generate(t.Context(), req)
					}
					if tc.allowed {
						if err != nil || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ToolKey != definition.Key || resp.ResponseID != "resp-owner" || resp.Usage == nil || (streaming && terminal != 1) {
							t.Fatalf("valid Responses owner denied: response=%+v err=%v terminal=%d", resp, err, terminal)
						}
						return
					}
					if (!errors.Is(err, openai.ErrUnsupported) && !errors.Is(err, providerhttp.ErrProtocol)) || model.IsResponseRejectedError(err) ||
						len(resp.ToolCalls) != 0 || resp.ProviderReplay != nil || resp.ResponseID != "" || resp.Usage != nil || terminal != 0 {
						t.Fatalf("non-Responses resource authorized local Tool: response=%+v err=%v terminal=%d", resp, err, terminal)
					}
				})
			}
		})
	}
}
