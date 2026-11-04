package openai_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A message's output_text.text must be a physical JSON string before
// sibling local Tools can become executable. Existing null/omitted legacy
// phase behavior remains unchanged.
func TestCompletedMessagePhysicalPhaseAndTextEvidence(t *testing.T) {
	const tool = `{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{}"}`
	const validMessage = `{"type":"message","content":[{"type":"output_text","text":"Working"}]}`
	for _, tc := range []struct {
		name, output string
		required     bool
		valid        bool
	}{
		{"empty_content_with_tool", `{"type":"message","content":[]},` + tool, false, true},
		{"null_content_with_tool", `{"type":"message","content":null},` + tool, true, false},
		{"missing_content_with_tool", `{"type":"message"},` + tool, false, false},
		{"null_phase_legacy_valid_tool", `{"type":"message","phase":null,"content":[{"type":"output_text","text":"Working"}]},` + tool, false, true},
		{"null_text_optional_tool_sibling", `{"type":"message","content":[{"type":"output_text","text":null},{"type":"output_text","text":"Working"}]},` + tool, false, false},
		{"null_text_required_tool_sibling", `{"type":"message","content":[{"type":"output_text","text":null},{"type":"output_text","text":"Working"}]},` + tool, true, false},
		{"missing_text_optional_tool_sibling", `{"type":"message","content":[{"type":"output_text"},{"type":"output_text","text":"Working"}]},` + tool, false, false},
		{"missing_text_required_tool_sibling", `{"type":"message","content":[{"type":"output_text"},{"type":"output_text","text":"Working"}]},` + tool, true, false},
		{"omitted_phase_valid_tool", validMessage + `,` + tool, false, true},
		{"explicit_commentary_valid_tool", `{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"Working"}]},` + tool, false, true},
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
						if sent.Stream != streaming || (sent.ToolChoice == "required") != tc.required {
							t.Errorf("incorrect request: %+v", sent)
						}
						body := `{"object":"response","id":"physical-evidence","status":"completed","output":[` + tc.output + `],"usage":{"input_tokens":17,"output_tokens":5}}`
						if streaming {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
						} else {
							_, _ = fmt.Fprint(w, body)
						}
					})
					request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: tc.required}
					terminal := 0
					var result model.Response
					var err error
					if streaming {
						result, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					if tc.valid {
						if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "call-1" || (streaming && terminal != 1) {
							t.Fatalf("legitimate legacy/phase Tool lost: result=%+v err=%v terminal=%d", result, err, terminal)
						}
						return
					}
					if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
						result.ResponseID != "physical-evidence" || result.Usage == nil || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 5 ||
						result.Content != "" || result.Refused || len(result.ToolCalls) != 0 || result.ProviderReplay != nil || terminal != 0 {
						t.Fatalf("untrusted message text promoted as completed Tool: result=%+v err=%v terminal=%d", result, err, terminal)
					}
				})
			}
		})
	}
}
