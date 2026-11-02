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

// A valid physical function_call does not make the entire output executable:
// contradictory or unsupported sibling output must reject the whole
// completed response while retaining only its trustworthy billing receipt.
func TestCompletedMixedToolOutputRetainsMinimalRejectionReceipt(t *testing.T) {
	const tool = `{"type":"function_call","status":"completed","call_id":"call-1","name":"lookup","arguments":"{\"x\":1}"}`
	const refusal = `{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot comply."}]}`
	const final = `{"type":"message","phase":"final_answer","role":"assistant","status":"completed","content":[{"type":"output_text","text":"invalid final"}]}`
	const unknown = `{"type":"not_a_supported_item","content":"opaque"}`
	const malformedType = `{"type":123,"content":"opaque"}`
	const invalidPart = `{"type":"message","role":"assistant","content":[{"type":"unsupported_part","text":"bad"}]}`
	const noReplay = `{"type":"reasoning","id":"reason-1","summary":[]}`

	cases := []struct {
		name       string
		output     string
		required   bool
		replayOnly bool
		noTool     bool
	}{
		{"tool_then_refusal", "[" + tool + "," + refusal + "]", true, false, false},
		{"refusal_then_tool", "[" + refusal + "," + tool + "]", true, false, false},
		{"tool_then_final", "[" + tool + "," + final + "]", true, false, false},
		{"tool_then_unknown", "[" + tool + "," + unknown + "]", false, false, false},
		{"unknown_then_tool", "[" + unknown + "," + tool + "]", true, false, false},
		{"tool_then_invalid_type", "[" + tool + "," + malformedType + "]", true, false, false},
		{"invalid_type_then_tool", "[" + malformedType + "," + tool + "]", false, false, false},
		{"tool_then_invalid_text_part", "[" + tool + "," + invalidPart + "]", true, false, false},
		{"no_tool_unsupported_unchanged", "[" + unknown + "]", false, false, true},
		{"missing_encrypted_replay_unchanged", "[" + noReplay + "," + tool + "]", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, usage := range []struct {
				name, value string
				known       bool
			}{
				{"known", `,"usage":{"input_tokens":17,"output_tokens":5}`, true},
				{"missing", "", false},
				{"negative", `,"usage":{"input_tokens":-1,"output_tokens":5}`, false},
				{"overflow", `,"usage":{"input_tokens":9223372036854775807,"output_tokens":1}`, false},
			} {
				t.Run(usage.name, func(t *testing.T) {
					for _, streaming := range []bool{false, true} {
						name := "unary"
						if streaming {
							name = "sse"
						}
						t.Run(name, func(t *testing.T) {
							client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
								var sent struct {
									Stream bool `json:"stream"`
								}
								if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
									t.Error(err)
								}
								if sent.Stream != streaming {
									t.Errorf("transport mismatch: %+v", sent)
								}
								body := `{"id":"paid-mixed","status":"completed","output":` + tc.output + usage.value + "}"
								if streaming {
									w.Header().Set("Content-Type", "text/event-stream")
									_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", body)
								} else {
									_, _ = fmt.Fprint(w, body)
								}
							})
							req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("use lookup")}},
								Tools: []tools.Definition{definition}, RequireToolCall: tc.required}
							terminal := 0
							var response model.Response
							var err error
							if streaming {
								response, err = client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
									if evt.ResponseID != "" {
										terminal++
									}
									return nil
								})
							} else {
								response, err = client.Generate(t.Context(), req)
							}
							if !errors.Is(err, openai.ErrUnsupported) || terminal != 0 ||
								response.Content != "" || response.Refused || len(response.ToolCalls) != 0 ||
								response.ProviderReplay != nil {
								t.Fatalf("semantically invalid output was accepted/exposed: response=%+v err=%v terminal=%d", response, err, terminal)
							}
							if tc.replayOnly {
								if !errors.Is(err, openai.ErrReplayUnavailable) || model.IsResponseRejectedError(err) ||
									response.ResponseID != "" || response.Usage != nil {
									t.Fatalf("encrypted reasoning failure classification changed: response=%+v err=%v", response, err)
								}
								return
							}
							if !tc.noTool && usage.known {
								if !model.IsResponseRejectedError(err) ||
									model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
									response.ResponseID != "paid-mixed" || response.Usage == nil ||
									response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 {
									t.Fatalf("paid rejected Tool response lost receipt: response=%+v err=%v", response, err)
								}
							} else if model.IsResponseRejectedError(err) || response.ResponseID != "" || response.Usage != nil {
								t.Fatalf("invented completion without required proof: response=%+v err=%v", response, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestCompletedReplaySizeFailureRetainsPaidToolReceipt(t *testing.T) {
	// An output with >1024 valid intermediate messages passes Tool/phase
	// projection but cannot be encoded into an authorized replay envelope.
	output := make([]map[string]any, 0, 1026)
	for i := 0; i < 1025; i++ {
		output = append(output, map[string]any{
			"type": "message", "role": "assistant", "phase": "commentary",
			"content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf("step %d", i)}},
		})
	}
	output = append(output, map[string]any{
		"type": "function_call", "status": "completed", "call_id": "call-1", "name": "lookup", "arguments": "{}",
	})
	for _, streaming := range []bool{false, true} {
		name := "unary"
		if streaming {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				payload, err := json.Marshal(map[string]any{
					"id": "paid-replay-limit", "status": "completed",
					"output": output, "usage": map[string]any{"input_tokens": 17, "output_tokens": 5},
				})
				if err != nil {
					t.Error(err)
				}
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", payload)
				} else {
					_, _ = w.Write(payload)
				}
			})
			req := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
				Tools: []tools.Definition{definition}}
			var response model.Response
			var err error
			terminal := 0
			if streaming {
				response, err = client.GenerateStream(t.Context(), req, func(evt model.StreamEvent) error {
					if evt.ResponseID != "" {
						terminal++
					}
					return nil
				})
			} else {
				response, err = client.Generate(t.Context(), req)
			}
			if !errors.Is(err, openai.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
				response.ResponseID != "paid-replay-limit" || response.Usage == nil ||
				response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
				response.Content != "" || len(response.ToolCalls) != 0 || response.ProviderReplay != nil || terminal != 0 {
				t.Fatalf("failed replay encoding lost paid Tool completion: response=%+v err=%v terminal=%d", response, err, terminal)
			}
		})
	}
}
