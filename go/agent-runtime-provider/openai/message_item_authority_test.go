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

// A completed outer Responses resource cannot promote an incomplete or
// foreign-owned message alongside a valid physical function_call into a
// locally executable Tool. This fence is independent of tool_choice.
func TestCompletedResponseMessageMustBeAssistantAndCompletedBeforeToolDispatch(t *testing.T) {
	type authorityCase struct {
		name, phase                string
		role, status               any
		rolePresent, statusPresent bool
		valid                      bool
	}
	cases := []authorityCase{
		{name: "omitted_legacy_fields", valid: true},
		{name: "explicit_assistant_complete", rolePresent: true, role: "assistant", statusPresent: true, status: "completed", valid: true},
		{name: "phased_commentary_assistant_complete", phase: "commentary", rolePresent: true, role: "assistant", statusPresent: true, status: "completed", valid: true},
		{name: "foreign_user", rolePresent: true, role: "user"},
		{name: "foreign_system", rolePresent: true, role: "system"},
		{name: "foreign_tool", rolePresent: true, role: "tool"},
		{name: "explicit_null_role", rolePresent: true},
		{name: "explicit_empty_role", rolePresent: true, role: ""},
		{name: "nonstring_role", rolePresent: true, role: 42},
		{name: "status_in_progress", statusPresent: true, status: "in_progress"},
		{name: "status_incomplete", statusPresent: true, status: "incomplete"},
		{name: "status_unknown", statusPresent: true, status: "cancelled"},
		{name: "explicit_null_status", statusPresent: true},
		{name: "explicit_empty_status", statusPresent: true, status: ""},
		{name: "nonstring_status", statusPresent: true, status: 42},
		{name: "phased_foreign_role", phase: "commentary", rolePresent: true, role: "user"},
		{name: "phased_incomplete_status", phase: "commentary", statusPresent: true, status: "in_progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, forced := range []bool{false, true} {
				forceName := "optional"
				if forced {
					forceName = "mandatory"
				}
				t.Run(forceName, func(t *testing.T) {
					for _, stream := range []bool{false, true} {
						mode := "unary"
						if stream {
							mode = "sse"
						}
						t.Run(mode, func(t *testing.T) {
							for _, reverse := range []bool{false, true} {
								order := "message_before_tool"
								if reverse {
									order = "tool_before_message"
								}
								t.Run(order, func(t *testing.T) {
									client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
										var sent struct {
											Stream     bool   `json:"stream"`
											ToolChoice string `json:"tool_choice"`
										}
										if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
											t.Error(err)
										}
										wantChoice := ""
										if forced {
											wantChoice = "required"
										}
										if sent.Stream != stream || sent.ToolChoice != wantChoice {
											t.Errorf("wrong request: %+v", sent)
										}
										message := map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "untrusted sibling"}}}
										if tc.phase != "" {
											message["phase"] = tc.phase
										}
										if tc.rolePresent {
											message["role"] = tc.role
										}
										if tc.statusPresent {
											message["status"] = tc.status
										}
										tool := map[string]any{"type": "function_call", "status": "completed", "call_id": "local-1", "name": "lookup", "arguments": `{}`}
										output := []any{message, tool}
										if reverse {
											output = []any{tool, message}
										}
										body, err := json.Marshal(map[string]any{
											"object": "response", "id": "paid-message-authority", "status": "completed", "output": output,
											"usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
										})
										if err != nil {
											t.Fatal(err)
										}
										if stream {
											w.Header().Set("Content-Type", "text/event-stream")
											_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
										} else {
											_, _ = w.Write(body)
										}
									})
									req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("use lookup")}},
										Tools: []tools.Definition{definition}, RequireToolCall: forced}
									term := 0
									var response model.Response
									var err error
									if stream {
										response, err = client.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
											if e.ResponseID != "" {
												term++
											}
											return nil
										})
									} else {
										response, err = client.Generate(t.Context(), req)
									}
									if tc.valid {
										if err != nil || len(response.ToolCalls) != 1 || response.ToolCalls[0].ToolKey != definition.Key ||
											response.ResponseID != "paid-message-authority" || response.Usage == nil || (stream && term != 1) {
											t.Fatalf("legitimate legacy or completed assistant Tool broken: response=%+v err=%v terminal=%d", response, err, term)
										}
										return
									}
									if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
										model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
										response.ResponseID != "paid-message-authority" || response.Usage == nil ||
										response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
										response.Content != "" || response.Refused || response.ProviderReplay != nil ||
										len(response.ToolCalls) != 0 || term != 0 {
										t.Fatalf("untrusted message authorized Tool or lost known Usage: response=%+v err=%v terminal=%d", response, err, term)
									}
								})
							}
						})
					}
				})
			}
		})
	}
}
