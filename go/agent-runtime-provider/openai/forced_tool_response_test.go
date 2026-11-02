package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A proxy can ignore tool_choice=required and return completed plain text.
// A successfully transported response is not proof of the required Tool call.
func TestForcedToolResponseMustActuallyContainToolCall(t *testing.T) {
	for _, tt := range []struct {
		name, content string
		required      bool
		refusal       bool
		toolCall      bool
		reject        bool
	}{
		{name: "forced_plain_text_fails_closed", content: "no tool executed", required: true, reject: true},
		{name: "forced_real_tool_call_unchanged", required: true, toolCall: true},
		{name: "unforced_plain_text_unchanged", content: "normal answer"},
		{name: "explicit_safety_refusal_preserved", content: "Cannot comply.", required: true, refusal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				var sent struct {
					ToolChoice string `json:"tool_choice"`
					Stream     bool   `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					t.Error(err)
				}
				expectedChoice := ""
				if tt.required {
					expectedChoice = "required"
				}
				if sent.ToolChoice != expectedChoice {
					t.Errorf("tool choice not sent: got=%q want=%q", sent.ToolChoice, expectedChoice)
				}
				part := map[string]any{"type": "output_text", "text": tt.content}
				if tt.refusal {
					part = map[string]any{"type": "refusal", "refusal": tt.content}
				}
				output := []any{map[string]any{"type": "message", "content": []any{part}}}
				if tt.toolCall {
					output = []any{map[string]any{"type": "function_call", "call_id": "call-1", "name": "lookup", "arguments": `{"x":1}`}}
				}
				response, err := json.Marshal(map[string]any{
					"object": "response", "id": "response-missing-required-tool", "status": "completed",
					"output": output,
					"usage":  map[string]any{"input_tokens": 1, "output_tokens": 2},
				})
				if err != nil {
					t.Fatal(err)
				}
				if sent.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintln(w, "event: response.completed")
					_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":%s}\n\n", response)
				} else {
					_, _ = w.Write(response)
				}
			})
			request := model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("call lookup")}},
				Tools:    []tools.Definition{definition}, RequireToolCall: tt.required,
			}
			for _, mode := range []string{"unary", "stream"} {
				t.Run(mode, func(t *testing.T) {
					var result model.Response
					var err error
					receipts := 0
					if mode == "stream" {
						result, err = client.GenerateStream(context.Background(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								receipts++
							}
							return nil
						})
					} else {
						result, err = client.Generate(context.Background(), request)
					}
					if tt.reject {
						if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							result.ResponseID == "" || result.Usage == nil || len(result.ToolCalls) != 0 || receipts != 0 {
							t.Fatalf("tool-choice violation accepted or emitted terminal receipt: response=%+v err=%v receipts=%d", result, err, receipts)
						}
						return
					}
					if err != nil || result.Content != tt.content || result.Refused != tt.refusal ||
						(mode == "stream" && receipts != 1) ||
						(tt.toolCall && (len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolKey != definition.Key)) {
						t.Fatalf("allowed reply lost: response=%+v err=%v receipts=%d", result, err, receipts)
					}
				})
			}
			if requests != 2 {
				t.Fatalf("unexpected requests=%d", requests)
			}
		})
	}
}

// Permanent regression: the provider has FINISHED and supplied cumulative token usage.
// Failing a required-Tool semantic contract must retain this known completion
// and its consumption evidence rather than look like a lost/unknown Model call.
func TestIndependentRequiredToolRejectionRetainsKnownUsageReceipt(t *testing.T) {
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"response","id":"paid-openai-reply","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"no function call"}]}],"usage":{"input_tokens":17,"output_tokens":5}}`))
	})
	request := model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must call lookup")}},
		Tools:    []tools.Definition{definition}, RequireToolCall: true,
	}
	response, err := client.Generate(context.Background(), request)
	if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
		response.ResponseID != "paid-openai-reply" || response.Usage == nil ||
		response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 || len(response.ToolCalls) != 0 {
		t.Fatalf("KNOWN PROVIDER COMPLETION/USAGE LOST ON REJECTED TOOL: response=%+v err=%v rejected=%v", response, err, model.IsResponseRejectedError(err))
	}
}

// A completed provider reply without trustworthy cumulative Usage is not an
// accounting receipt. Even if the gateway ignored mandatory Tool choice,
// do not infer paid completion or emit a terminal SSE success event.
func TestForcedToolUnknownUsageNeverMintsCompletedReceipt(t *testing.T) {
	for _, tc := range []struct{ name, usage string }{
		{name: "missing"},
		{name: "partial", usage: `,"usage":{"input_tokens":17}`},
		{name: "negative", usage: `,"usage":{"input_tokens":-1,"output_tokens":5}`},
		{name: "negative_cache", usage: `,"usage":{"input_tokens":17,"output_tokens":5,"input_tokens_details":{"cached_tokens":-2}}`},
		{name: "overflow", usage: `,"usage":{"input_tokens":9223372036854775807,"output_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				body := `{"object":"response","id":"unknown-usage","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"no required Tool"}]}]` + tc.usage + `}`
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
				} else {
					_, _ = fmt.Fprint(w, body)
				}
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("must use lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
			for _, mode := range []string{"unary", "stream"} {
				t.Run(mode, func(t *testing.T) {
					var response model.Response
					var err error
					terminal := 0
					if mode == "stream" {
						response, err = client.GenerateStream(t.Context(), req, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if !errors.Is(err, openai.ErrUnsupported) || model.IsResponseRejectedError(err) ||
						response.ResponseID != "" || response.Usage != nil || len(response.ToolCalls) != 0 || terminal != 0 {
						t.Fatalf("unknown usage was promoted to trusted completion: response=%+v err=%v terminal=%d", response, err, terminal)
					}
				})
			}
		})
	}
}

// Independent audit: a complete Responses envelope cannot authorize dispatch
// of a tool item that explicitly says its own arguments/output are unfinished.
// This is distinct from the assistant final refusal status contract.
func TestIndependentForcedToolCannotExecuteUnfinishedFunctionCallItem(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		valid        bool
	}{
		{name: "in_progress", status: `"in_progress"`},
		{name: "incomplete", status: `"incomplete"`},
		{name: "explicit_null", status: `null`},
		{name: "explicit_empty", status: `""`},
		{name: "unknown", status: `"unknown"`},
		{name: "completed", status: `"completed"`, valid: true},
		{name: "omitted", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "unary"
				if stream {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var req struct {
							ToolChoice string `json:"tool_choice"`
							Stream     bool   `json:"stream"`
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
						}
						if req.Stream != stream || req.ToolChoice != "required" {
							t.Errorf("wrong outbound force: %+v", req)
						}
						status := ""
						if tc.status != "" {
							status = `,"status":` + tc.status
						}
						response := `{"object":"response","id":"unfinished-tool-item","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"` + status + `}],"usage":{"input_tokens":11,"output_tokens":3}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+response+"}\n\n")
						} else {
							_, _ = fmt.Fprint(w, response)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: true}
					receipts := 0
					var response model.Response
					var err error
					if stream {
						response, err = client.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								receipts++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if tc.valid {
						if err != nil || len(response.ToolCalls) != 1 ||
							response.ToolCalls[0].ToolKey != definition.Key ||
							response.Usage == nil || response.ResponseID != "unfinished-tool-item" ||
							(stream && receipts != 1) {
							t.Fatalf("legitimate completed local tool failed: response=%+v err=%v receipts=%d", response, err, receipts)
						}
					} else {
						if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
							response.ResponseID != "unfinished-tool-item" || response.Usage == nil ||
							response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 3 ||
							response.Content != "" || response.ProviderReplay != nil || response.Refused ||
							len(response.ToolCalls) != 0 || receipts != 0 {
							t.Fatalf("UNFINISHED TOOL CALL AUTHORIZED OR REJECTED COMPLETION LOST: status=%q result=%+v err=%v terminal=%d", tc.status, response, err, receipts)
						}
					}
				})
			}
		})
	}
}

func TestIndependentOptionalToolCannotExecuteUnfinishedFunctionCallItem(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		valid        bool
	}{
		{name: "in_progress", status: `"in_progress"`},
		{name: "incomplete", status: `"incomplete"`},
		{name: "explicit_null", status: `null`},
		{name: "explicit_empty", status: `""`},
		{name: "unknown", status: `"unknown"`},
		{name: "completed", status: `"completed"`, valid: true},
		{name: "omitted", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				mode := "unary"
				if stream {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var req struct {
							ToolChoice string `json:"tool_choice"`
							Stream     bool   `json:"stream"`
						}
						if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
							t.Error(err)
						}
						if req.Stream != stream || req.ToolChoice != "" {
							t.Errorf("wrong outbound force: %+v", req)
						}
						status := ""
						if tc.status != "" {
							status = `,"status":` + tc.status
						}
						response := `{"object":"response","id":"unfinished-tool-item","status":"completed","output":[{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"` + status + `}],"usage":{"input_tokens":11,"output_tokens":3}}`
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":"+response+"}\n\n")
						} else {
							_, _ = fmt.Fprint(w, response)
						}
					})
					req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}}, Tools: []tools.Definition{definition}, RequireToolCall: false}
					receipts := 0
					var response model.Response
					var err error
					if stream {
						response, err = client.GenerateStream(t.Context(), req, func(e model.StreamEvent) error {
							if e.ResponseID != "" {
								receipts++
							}
							return nil
						})
					} else {
						response, err = client.Generate(t.Context(), req)
					}
					if tc.valid {
						if err != nil || len(response.ToolCalls) != 1 ||
							response.ToolCalls[0].ToolKey != definition.Key ||
							response.Usage == nil || response.ResponseID != "unfinished-tool-item" ||
							(stream && receipts != 1) {
							t.Fatalf("legitimate completed local tool failed: response=%+v err=%v receipts=%d", response, err, receipts)
						}
					} else {
						if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
							model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
							response.ResponseID != "unfinished-tool-item" || response.Usage == nil ||
							response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 3 ||
							response.Content != "" || response.ProviderReplay != nil || response.Refused ||
							len(response.ToolCalls) != 0 || receipts != 0 {
							t.Fatalf("UNFINISHED TOOL CALL AUTHORIZED OR REJECTED COMPLETION LOST: status=%q result=%+v err=%v terminal=%d", tc.status, response, err, receipts)
						}
					}
				})
			}
		})
	}
}

// A function_call that is explicitly unfinished may contain JSON arguments
// that cannot yet be parsed. Check item status before parsing calls: a
// settled Provider response with verifiable Usage must still produce a
// tool-free, exactly-once rejected completion, not an unreceipted error.
func TestUnfinishedFunctionItemWithMalformedArgumentsNeverExecutesOrLosesReceipt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		validFirst bool
		completed  bool
		withUsage  bool
	}{
		{name: "partial_arguments_known_usage", withUsage: true},
		{name: "partial_arguments_missing_usage"},
		{name: "valid_first_then_partial_known_usage", validFirst: true, withUsage: true},
		{name: "completed_item_malformed_arguments", completed: true, withUsage: true},
		{name: "completed_item_malformed_arguments_without_usage", completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"unary", "stream"} {
				t.Run(mode, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
						var sent struct {
							Stream bool `json:"stream"`
						}
						if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
							t.Error(err)
						}
						if sent.Stream != (mode == "stream") {
							t.Error("wrong transport")
						}
						status := "in_progress"
						if tc.completed {
							status = "completed"
						}
						output := []any{}
						if tc.validFirst {
							output = append(output, map[string]any{"type": "function_call", "status": "completed", "call_id": "call-first", "name": "lookup", "arguments": "{}"})
						}
						output = append(output, map[string]any{"type": "function_call", "status": status,
							"call_id": "call-broken", "name": "lookup", "arguments": "{"})
						response := map[string]any{"object": "response", "id": "partially-encoded-response", "status": "completed", "output": output}
						if tc.withUsage {
							response["usage"] = map[string]any{"input_tokens": 11, "output_tokens": 3}
						}
						body, err := json.Marshal(response)
						if err != nil {
							t.Fatal(err)
						}
						if mode == "stream" {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
						} else {
							_, _ = w.Write(body)
						}
					})
					request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools: []tools.Definition{definition}}
					var result model.Response
					var err error
					terminal := 0
					if mode == "stream" {
						result, err = client.GenerateStream(t.Context(), request, func(event model.StreamEvent) error {
							if event.ResponseID != "" {
								terminal++
							}
							return nil
						})
					} else {
						result, err = client.Generate(t.Context(), request)
					}
					if !errors.Is(err, openai.ErrUnsupported) || len(result.ToolCalls) != 0 || terminal != 0 || result.ProviderReplay != nil {
						t.Fatalf("incomplete/malformed call authorized execution: result=%+v err=%v terminal=%d", result, err, terminal)
					}
					if !tc.withUsage {
						if model.IsResponseRejectedError(err) || result.ResponseID != "" || result.Usage != nil {
							t.Fatalf("uncertified completed output invented receipt: result=%+v err=%v", result, err)
						}
					} else if !model.IsResponseRejectedError(err) || model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
						result.ResponseID != "partially-encoded-response" || result.Usage == nil ||
						result.Usage.InputTokens != 11 || result.Usage.OutputTokens != 3 || result.Content != "" {
						t.Fatalf("completed invalid Tool lost its known billing receipt: result=%+v err=%v", result, err)
					}
				})
			}
		})
	}
}
