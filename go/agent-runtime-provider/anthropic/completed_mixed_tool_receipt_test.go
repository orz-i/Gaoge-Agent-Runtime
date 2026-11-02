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
						_, _ = fmt.Fprintf(w, `{"id":"paid-anthropic-mixed","stop_reason":"tool_use","content":[%s],%s}`, tc.content, usage.json)
					})
					response, err := client.Generate(t.Context(), model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					})
					assertMixedAnthropicRejection(t, response, err, tc.tool && usage.known, false)
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
		{"invalid_thinking_without_tool", []string{mixedThinking}, "end_turn", false},
		{"truncated_outer_with_tool", []string{mixedThinking, mixedValidTool}, "max_tokens", true},
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
							"message": map[string]any{"id": "paid-anthropic-mixed", "usage": map[string]any{"input_tokens": 17, "output_tokens": 0}}})
						for i, block := range tc.blocks {
							send("content_block_start", map[string]any{"type": "content_block_start",
								"index": i, "content_block": json.RawMessage(block)})
							send("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
						}
						var final map[string]any
						if err := json.Unmarshal([]byte("{"+usage.final+"}"), &final); err != nil {
							t.Error(err)
						}
						final["type"] = "message_delta"
						final["delta"] = map[string]any{"stop_reason": tc.stop}
						send("message_delta", final)
						send("message_stop", map[string]any{"type": "message_stop"})
					})
					var terminal int
					response, err := client.GenerateStream(t.Context(), model.Request{
						Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("lookup")}},
						Tools:    []tools.Definition{definition}, RequireToolCall: true,
					}, func(event model.StreamEvent) error {
						if event.ResponseID != "" {
							terminal++
						}
						return nil
					})
					assertMixedAnthropicRejection(t, response, err, tc.hasTool && tc.stop == "tool_use" && usage.known, terminal != 0)
				})
			}
		})
	}
}

func assertMixedAnthropicRejection(t *testing.T, response model.Response, err error, knownTool, emittedTerminal bool) {
	t.Helper()
	if err == nil || response.Content != "" || len(response.ToolCalls) != 0 ||
		response.ProviderReplay != nil || response.Refused || emittedTerminal {
		t.Fatalf("invalid Tool-bearing completion leaked content, execution or terminal success: response=%+v err=%v terminal=%v", response, err, emittedTerminal)
	}
	if !knownTool {
		if model.IsResponseRejectedError(err) || response.ResponseID != "" || response.Usage != nil {
			t.Fatalf("invented receipt for unknown/invalid or Tool-free completion: response=%+v err=%v", response, err)
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
