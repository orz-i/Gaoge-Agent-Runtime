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

// Independent audit: a refusal in commentary is important provider safety
// evidence, but MUST NOT turn an unrelated *non-refusal final answer* into
// an approved replacement for the mandatory Tool call on this request.
func TestIndependentForcedToolCommentaryRefusalCannotAuthorizeTextFinal(t *testing.T) {
	for _, mode := range []string{"unary", "stream"} {
		t.Run(mode, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ToolChoice string `json:"tool_choice"`
					Stream     bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.ToolChoice != "required" {
					t.Errorf("forced tool not sent: %q", req.ToolChoice)
				}
				if req.Stream != (mode == "stream") {
					t.Fatal("wrong stream mode")
				}
				body := `{"id":"refusal-in-commentary-only","status":"completed","output":[` +
					`{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"refusal","refusal":"Cannot comply."}]},` +
					`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"ordinary final answer WITHOUT Tool"}]}],` +
					`"usage":{"input_tokens":17,"output_tokens":5}}`
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n")
				} else {
					_, _ = fmt.Fprint(w, body)
				}
			})
			request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			terminal := 0
			var response model.Response
			var err error
			if mode == "stream" {
				response, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
					if event.ResponseID != "" {
						terminal++
					}
					return nil
				})
			} else {
				response, err = client.Generate(t.Context(), request)
			}
			// The rejected completion retains only billing evidence; even a
			// commentary refusal cannot authorize any persisted final text.
			if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
				response.Content != "" || response.Refused || response.ProviderReplay != nil ||
				response.ResponseID != "refusal-in-commentary-only" || response.Usage == nil ||
				response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
				len(response.ToolCalls) != 0 || terminal != 0 {
				t.Fatalf("NON-REFUSAL FINAL BYPASSED FORCE OR LOST REJECTED RECEIPT: result=%+v err=%v terminalReceipts=%d", response, err, terminal)
			}
		})
	}
}

func TestIndependentForcedToolFinalRefusalRemainsAllowed(t *testing.T) {
	for _, mode := range []string{"unary", "stream"} {
		t.Run(mode, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					ToolChoice string `json:"tool_choice"`
					Stream     bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if req.ToolChoice != "required" {
					t.Errorf("forced tool not sent: %q", req.ToolChoice)
				}
				if req.Stream != (mode == "stream") {
					t.Fatal("wrong stream mode")
				}
				body := `{"id":"refusal-in-commentary-only","status":"completed","output":[` +
					`{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Working"}]},` +
					`{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"refusal","refusal":"Cannot comply."}]}],` +
					`"usage":{"input_tokens":17,"output_tokens":5}}`
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n")
				} else {
					_, _ = fmt.Fprint(w, body)
				}
			})
			request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			terminal := 0
			var response model.Response
			var err error
			if mode == "stream" {
				response, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
					if event.ResponseID != "" {
						terminal++
					}
					return nil
				})
			} else {
				response, err = client.Generate(t.Context(), request)
			}
			// An actual terminal refusal remains an allowed safety outcome.
			if err != nil || !response.Refused || response.Content != "Cannot comply." ||
				len(response.ToolCalls) != 0 || (mode == "stream" && terminal != 1) {
				t.Fatalf("TERMINAL REFUSAL REGRESSION: response=%+v err=%v terminal=%d", response, err, terminal)
			}
		})
	}
}

// The refusal exception belongs to the terminal message, not the cumulative
// Response.Refused flag. Mixed ordinary text and refusal in the same final
// message, or a legacy refusal followed by an ordinary last message, do not
// authorize a missing required Tool call.
func TestIndependentForcedToolRequiresRefusalOnlySelectedTerminalMessage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs []string
		allowed bool
	}{
		{
			name: "phased_final_mixed_refusal_and_text",
			outputs: []string{
				`{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"Working."}]}`,
				`{"type":"message","phase":"final_answer","content":[{"type":"refusal","refusal":"Cannot comply."},{"type":"output_text","text":"ordinary answer"}]}`,
			},
		},
		{
			name: "legacy_refusal_then_ordinary_answer",
			outputs: []string{
				`{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply."}]}`,
				`{"type":"message","content":[{"type":"output_text","text":"ordinary last answer"}]}`,
			},
		},
		{
			name: "legacy_answer_then_terminal_refusal",
			outputs: []string{
				`{"type":"message","content":[{"type":"output_text","text":"intermediate text"}]}`,
				`{"type":"message","content":[{"type":"refusal","refusal":"Cannot comply."}]}`,
			}, allowed: true,
		},
		{
			name: "phased_commentary_refusal_and_final_refusal",
			outputs: []string{
				`{"type":"message","phase":"commentary","content":[{"type":"refusal","refusal":"Cannot comply."}]}`,
				`{"type":"message","phase":"final_answer","content":[{"type":"refusal","refusal":"Cannot comply."}]}`,
			}, allowed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "unary"
				if stream {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						writePhaseResponse(w, phaseResponse(tc.outputs...), stream)
					})
					request := model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must invoke lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					}
					var response model.Response
					var err error
					terminal := 0
					if stream {
						response, err = client.GenerateStream(t.Context(), request, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), request)
					}
					if tc.allowed {
						if err != nil || !response.Refused || len(response.ToolCalls) != 0 ||
							response.Usage == nil || (stream && terminal != 1) {
							t.Fatalf("valid terminal refusal denied: response=%+v err=%v final=%d", response, err, terminal)
						}
						return
					}
					if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
						response.Refused || response.Content != "" || response.ProviderReplay != nil || response.Usage == nil || response.ResponseID != "response-phase" ||
						len(response.ToolCalls) != 0 || terminal != 0 {
						t.Fatalf("non-refusal terminal escaped forced Tool check: response=%+v err=%v final=%d", response, err, terminal)
					}
				})
			}
		})
	}
}

// Permanent regression from independent audit: an unphased response message explicitly
// attributed to user/system/tool cannot act as an assistant's safety refusal
// that waives the mandatory local Tool call. The existing unphased encoder
// allows omitted role, but an explicit foreign role is not an assistant.
func TestIndependentForcedToolNonAssistantRoleCannotGrantSafetyRefusal(t *testing.T) {
	for _, tc := range []struct {
		role    string
		raw     string
		allowed bool
	}{
		{role: "user"}, {role: "system"}, {role: "tool"},
		{role: "explicit_null", raw: "null"},
		{role: "explicit_empty", raw: `""`},
		{role: "assistant", allowed: true}, {role: "", allowed: true},
	} {
		name := tc.role
		if name == "" {
			name = "role_omitted"
		}
		t.Run(name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				transport := "unary"
				if stream {
					transport = "stream"
				}
				t.Run(transport, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var req struct {
							ToolChoice string `json:"tool_choice"`
							Stream     bool   `json:"stream"`
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
						}
						if req.ToolChoice != "required" || req.Stream != stream {
							t.Errorf("outbound required tool mismatch: %+v", req)
						}
						role := ""
						if tc.raw != "" {
							role = `,"role":` + tc.raw
						} else if tc.role != "" {
							role = `,"role":` + fmt.Sprintf("%q", tc.role)
						}
						body := `{"id":"output-role-rejected","status":"completed","output":[{"type":"message"` + role + `,"content":[{"type":"refusal","refusal":"Cannot comply."}]}],"usage":{"input_tokens":17,"output_tokens":5}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n")
						} else {
							_, _ = fmt.Fprint(w, body)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("invoke local lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
					terminal := 0
					var response model.Response
					var err error
					if stream {
						response, err = client.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if tc.allowed {
						if err != nil || !response.Refused || (stream && terminal != 1) {
							t.Fatalf("legitimate assistant final refusal denied: response=%+v err=%v terminal=%d", response, err, terminal)
						}
						return
					}
					if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) || terminal != 0 ||
						response.Usage == nil || response.ResponseID != "output-role-rejected" || len(response.ToolCalls) != 0 {
						t.Fatalf("FOREIGN ROLE AUTHORISED REQUIRED TOOL BYPASS: role=%q response=%+v err=%v terminal=%d", tc.role, response, err, terminal)
					}
				})
			}
		})
	}
}

// A response.completed envelope does not make an explicitly in-progress
// *message item* a final assistant refusal. A gateway cannot use an
// intermediate refusal item to waive a mandatory Tool selection.
func TestIndependentRequiredToolCannotUseIncompleteRefusalItemAsTerminal(t *testing.T) {
	for _, caseStatus := range []struct{ name, status, raw string }{
		{name: "in_progress", status: "in_progress"}, {name: "incomplete", status: "incomplete"},
		{name: "explicit_null", raw: "null"}, {name: "explicit_empty", raw: `""`},
		{name: "completed", status: "completed"}, {name: "status_omitted"},
	} {
		t.Run(caseStatus.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "unary"
				if stream {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						data := `{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"refusal","refusal":"Cannot comply."}]`
						if caseStatus.raw != "" {
							data += `,"status":` + caseStatus.raw
						} else if caseStatus.status != "" {
							data += `,"status":` + fmt.Sprintf("%q", caseStatus.status)
						}
						data += `}`
						body := `{"id":"incomplete-item-refusal","status":"completed","output":[` + data + `],"usage":{"input_tokens":13,"output_tokens":2}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n")
						} else {
							_, _ = fmt.Fprint(w, body)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("run mandatory Tool")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
					receipts := 0
					var resp model.Response
					var err error
					if stream {
						resp, err = client.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								receipts++
							}
							return nil
						})
					} else {
						resp, err = client.Generate(t.Context(), req)
					}
					if caseStatus.raw == "" && (caseStatus.status == "" || caseStatus.status == "completed") {
						if err != nil || !resp.Refused || (stream && receipts != 1) {
							t.Fatalf("valid terminal refusal rejected: resp=%+v err=%v receipts=%d", resp, err, receipts)
						}
						return
					}
					if err == nil || receipts != 0 {
						t.Fatalf("UNCOMPLETED ITEM BYPASSED MANDATORY TOOL: status=%q response=%+v err=%v receipts=%d", caseStatus.status, resp, err, receipts)
					}
				})
			}
		})
	}
}
