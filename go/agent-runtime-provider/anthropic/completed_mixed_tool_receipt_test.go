package anthropic_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	mixedValidTool = `{"type":"tool_use","id":"toolu-1","name":"lookup","input":{}}`
	mixedRichText  = `{"type":"text","text":"unrepresentable","citations":[{"type":"web_search_result_location"}]}`
	mixedThinking  = `{"type":"thinking","thinking":"opaque","signature":""}`
	mixedRedacted  = `{"type":"redacted_thinking","data":""}`
	mixedUnknown   = `{"type":"unsupported_block","text":"opaque"}`
)

// A valid physical Tool elsewhere in a completed response does not authorize
// malformed sibling content. Nor may a sibling parse failure erase an already
// paid, trustworthy final ID/Usage receipt, regardless of output order.
func TestCompletedMixedAnthropicToolUnaryKeepsPaidReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		tool          bool
	}{
		{"tool_then_rich_text", mixedValidTool + "," + mixedRichText, true},
		{"rich_text_then_tool", mixedRichText + "," + mixedValidTool, true},
		{"tool_then_thinking_without_signature", mixedValidTool + "," + mixedThinking, true},
		{"thinking_without_signature_then_tool", mixedThinking + "," + mixedValidTool, true},
		{"tool_then_redacted_without_data", mixedValidTool + "," + mixedRedacted, true},
		{"tool_then_unknown_block", mixedValidTool + "," + mixedUnknown, true},
		{"unknown_block_then_tool", mixedUnknown + "," + mixedValidTool, true},
		{"rich_text_without_tool", mixedRichText, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, usage := range []struct {
				name, json string
				known      bool
			}{
				{"known", `"usage":{"input_tokens":17,"output_tokens":5}`, true},
				{"missing", `"ignored":null`, false},
				{"negative", `"usage":{"input_tokens":-1,"output_tokens":5}`, false},
				{"overflow", `"usage":{"input_tokens":9223372036854775807,"output_tokens":1,"cache_read_input_tokens":1}`, false},
			} {
				t.Run(usage.name, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
						_, _ = fmt.Fprintf(w, `{"type":"message","role":"assistant","id":"paid-anthropic-mixed","stop_reason":"tool_use","content":[%s],%s}`, tc.content, usage.json)
					})
					response, err := client.Generate(t.Context(), model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					})
					assertMixedAnthropicRejection(t, response, err, usage.known, false)
				})
			}
		})
	}
}

func TestCompletedMixedAnthropicToolStreamKeepsPaidReceipt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blocks  []string
		stop    string
		hasTool bool
	}{
		{"invalid_thinking_then_tool", []string{mixedThinking, mixedValidTool}, "tool_use", true},
		{"tool_then_invalid_thinking", []string{mixedValidTool, mixedThinking}, "tool_use", true},
		{"tool_then_invalid_redacted", []string{mixedValidTool, mixedRedacted}, "tool_use", true},
		// These fail at content_block_start, before final cumulative Usage.
		{"rich_citations_then_tool", []string{mixedRichText, mixedValidTool}, "tool_use", true},
		{"tool_then_rich_citations", []string{mixedValidTool, mixedRichText}, "tool_use", true},
		{"unknown_block_then_tool", []string{mixedUnknown, mixedValidTool}, "tool_use", true},
		{"tool_then_unknown_block", []string{mixedValidTool, mixedUnknown}, "tool_use", true},
		{"rich_text_without_tool", []string{mixedRichText}, "end_turn", false},
		{"invalid_thinking_without_tool", []string{mixedThinking}, "end_turn", false},
		{"truncated_outer_with_tool", []string{mixedThinking, mixedValidTool}, "max_tokens", true},
		{"truncated_rich_text_with_tool", []string{mixedRichText, mixedValidTool}, "max_tokens", true},
		{"missing_message_stop_with_tool", []string{mixedRichText, mixedValidTool}, "tool_use", true},
		{"malformed_frame_after_rich_text", []string{mixedValidTool, mixedRichText}, "tool_use", true},
		{"noncontiguous_indices_after_rich_text", []string{mixedRichText, mixedValidTool}, "tool_use", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, usage := range []struct {
				name  string
				final string
				known bool
			}{
				{"known", `"usage":{"output_tokens":5}`, true},
				{"unknown_final", `"ignored":null`, false},
				{"negative_final", `"usage":{"output_tokens":-1}`, false},
			} {
				t.Run(usage.name, func(t *testing.T) {
					client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						send := func(typ string, data any) {
							buf, err := json.Marshal(data)
							if err != nil {
								t.Error(err)
								return
							}
							_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, buf)
						}
						send("message_start", map[string]any{"type": "message_start",
							"message": map[string]any{"type": "message", "role": "assistant", "id": "paid-anthropic-mixed", "usage": map[string]any{"input_tokens": 17, "output_tokens": 0}}})
						for i, block := range tc.blocks {
							index := i
							if tc.name == "noncontiguous_indices_after_rich_text" && i == 1 {
								index = 2 // The missing index 1 is invalid SSE framing.
							}
							send("content_block_start", map[string]any{"type": "content_block_start",
								"index": index, "content_block": json.RawMessage(block)})
							if block == mixedRichText {
								send("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
									"delta": map[string]any{"type": "text_delta", "text": "untrusted delta"}})
							}
							send("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
						}
						if tc.name == "malformed_frame_after_rich_text" {
							// Event/type mismatch is a protocol failure, even after
							// a recognized semantic rejection and physical Tool.
							send("content_block_stop", map[string]any{"type": "message_stop"})
							return
						}
						var final map[string]any
						if err := json.Unmarshal([]byte("{"+usage.final+"}"), &final); err != nil {
							t.Error(err)
						}
						final["type"] = "message_delta"
						final["delta"] = map[string]any{"stop_reason": tc.stop}
						send("message_delta", final)
						if tc.name != "missing_message_stop_with_tool" {
							send("message_stop", map[string]any{"type": "message_stop"})
						}
					})
					var terminal, rejectedDelta int
					response, err := client.GenerateStream(t.Context(), model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					}, func(event model.StreamEvent) error {
						if event.ResponseID != "" {
							terminal++
						}
						if event.Delta == "untrusted delta" {
							rejectedDelta++
						}
						return nil
					})
					if rejectedDelta != 0 {
						t.Fatalf("semantically rejected content leaked via SSE: count=%d", rejectedDelta)
					}
					assertMixedAnthropicRejection(t, response, err, tc.stop != "max_tokens" &&
						tc.name != "missing_message_stop_with_tool" && tc.name != "malformed_frame_after_rich_text" &&
						tc.name != "noncontiguous_indices_after_rich_text" &&
						usage.known, terminal != 0)
				})
			}
		})
	}
}

func assertMixedAnthropicRejection(t *testing.T, response model.Response, err error, knownCompletion, emittedTerminal bool) {
	t.Helper()
	if err == nil || response.Content != "" || len(response.ToolCalls) != 0 ||
		response.ProviderReplay != nil || response.Refused || emittedTerminal {
		t.Fatalf("invalid Tool-bearing completion leaked content, execution or terminal success: response=%+v err=%v terminal=%v", response, err, emittedTerminal)
	}
	if !knownCompletion {
		if model.IsResponseRejectedError(err) || response.ResponseID != "" || response.Usage != nil {
			t.Fatalf("invented receipt for unknown/invalid completion: response=%+v err=%v", response, err)
		}
		return
	}
	if !errors.Is(err, anthropic.ErrUnsupported) || !model.IsResponseRejectedError(err) ||
		model.ClassifiedErrorCode(err) != "model.post_generation_failed" ||
		response.ResponseID != "paid-anthropic-mixed" || response.Usage == nil ||
		response.Usage.InputTokens != 17 || response.Usage.OutputTokens != 5 ||
		strings.TrimSpace(response.Content) != "" {
		t.Fatalf("known completed Anthropic Tool response lost its sanitized billing receipt: response=%+v err=%v", response, err)
	}
}
