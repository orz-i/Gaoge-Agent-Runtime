package transport

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func phaseFixture(phase any, text string) map[string]any {
	return map[string]any{"type": "message", "id": "item-" + text, "role": "assistant", "status": "completed", "phase": phase, "content": []any{map[string]any{"type": "output_text", "text": text}}, "fixture_opaque": map[string]any{"keep": true}}
}
func TestResponsesPhaseProjection(t *testing.T) {
	commentary := phaseFixture("commentary", "Working")
	final := phaseFixture("final_answer", `{"ok":true}`)
	for _, adapter := range []string{AdapterOpenAIResponses, AdapterOpenRouterResponses} {
		t.Run(adapter, func(t *testing.T) {
			result, err := buildGenerateOutputFromParsedForAdapter(EndpointResponses, adapter, map[string]any{"output": []any{commentary, final}, "output_text": "Working{\"ok\":true}"}, false)
			require.NoError(t, err)
			require.Equal(t, `{"ok":true}`, result.Text)
			require.False(t, result.Refused)
			if adapter == AdapterOpenAIResponses {
				require.Contains(t, result.ReplayStateJSON, `"phase":"commentary"`)
				require.Contains(t, result.ReplayStateJSON, "fixture_opaque")
			}
		})
	}
	refusal := phaseFixture("commentary", "")
	refusal["content"] = []any{map[string]any{"type": "refusal", "refusal": "Cannot comply."}}
	result, err := buildGenerateOutputFromParsed(EndpointResponses, map[string]any{"output": []any{refusal, final}})
	require.NoError(t, err)
	require.True(t, result.Refused)
	require.Equal(t, `{"ok":true}`, result.Text)
	for _, phase := range []any{nil, "legacy-absent"} {
		legacy := phaseFixture(phase, "Old answer")
		if phase == "legacy-absent" {
			delete(legacy, "phase")
		}
		result, err := buildGenerateOutputFromParsed(EndpointResponses, map[string]any{"output": []any{legacy}})
		require.NoError(t, err)
		require.Equal(t, "Old answer", result.Text)
	}
	call := map[string]any{"type": "function_call", "call_id": "c", "name": "lookup", "arguments": "{}"}
	for name, items := range map[string][]any{
		"unknown": {phaseFixture("future", "x")}, "empty": {phaseFixture("", "x")}, "nonstring": {phaseFixture(1, "x")},
		"commentary only": {commentary}, "late commentary": {final, commentary}, "duplicate final": {final, final},
		"mixed legacy": {phaseFixture(nil, "old"), final}, "final tool": {final, call}, "refusal tool": {refusal, call},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildGenerateOutputFromParsed(EndpointResponses, map[string]any{"output": items})
			require.ErrorIs(t, err, errInvalidResponsesPhase)
		})
	}
	result, err = buildGenerateOutputFromParsed(EndpointResponses, map[string]any{"output": []any{commentary, call}})
	require.NoError(t, err)
	require.Equal(t, "Working", result.Text)
	require.Len(t, result.ToolCalls, 1)
}
func TestResponsesPhaseStreaming(t *testing.T) {
	result := &GenerateOutput{}
	var visible strings.Builder
	callback := func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
	send := func(kind string, event map[string]any) {
		t.Helper()
		require.NoError(t, applyResponsesStreamEvent(AdapterOpenAIResponses, kind, event, "", result, callback))
	}
	commentary := phaseFixture("commentary", "Working")
	final := phaseFixture("final_answer", "answer")
	send("response.output_item.added", map[string]any{"output_index": 0, "item": commentary})
	send("response.output_text.delta", map[string]any{"output_index": 0, "item_id": commentary["id"], "delta": "Working"})
	send("response.output_text.done", map[string]any{"output_index": 0, "text": "Working"})
	send("response.output_item.added", map[string]any{"output_index": 1, "item": final})
	send("response.output_text.delta", map[string]any{"output_index": 1, "delta": "answer"})
	send("response.completed", map[string]any{"response": map[string]any{"output": []any{commentary, final}}})
	require.Equal(t, "answer", result.Text)
	require.Equal(t, "answer", visible.String())
	require.Contains(t, result.ReplayStateJSON, "commentary")
	require.ErrorIs(t, applyResponsesStreamEvent(AdapterOpenAIResponses, "response.output_item.added", map[string]any{"output_index": 2, "item": phaseFixture("unknown", "bad")}, "", result, nil), errInvalidResponsesPhase)
	require.ErrorIs(t, applyResponsesStreamEvent(AdapterOpenAIResponses, "response.completed", map[string]any{"response": map[string]any{"output": []any{commentary}}}, "", result, nil), errInvalidResponsesPhase)
}
func TestAnthropicMalformedStreamJSONFailsClosed(t *testing.T) {
	parser := &anthropicSSEParser{result: &GenerateOutput{}}
	require.Error(t, parser.dispatch(`{"type":"content_block_delta","index":`))
}

func TestResponsesUnassociatedStreamDeltasWaitForProjection(t *testing.T) {
	result := &GenerateOutput{}
	var visible strings.Builder
	callback := func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
	require.NoError(t, applyResponsesStreamEvent(AdapterOpenAIResponses, "response.output_text.delta", map[string]any{"item_id": "unseen", "output_index": 0, "delta": "Working"}, "", result, callback))
	require.Empty(t, visible.String())
	require.Empty(t, result.Text)
	require.NoError(t, rememberResponsesPhase(result, phaseFixture(nil, "legacy"), 0))
	require.NoError(t, applyResponsesTextDeltaEvent(map[string]any{"item_id": "other", "output_index": 0, "delta": "Working"}, result, callback))
	require.Empty(t, visible.String())
	final := phaseFixture("final_answer", "answer")
	require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", map[string]any{"response": map[string]any{"output": []any{phaseFixture("commentary", "Working"), final}}}, result, callback))
	require.Equal(t, "answer", visible.String())
	require.Equal(t, "answer", result.Text)
}

func TestResponsesCompletedEmitsMissingTextExactlyOnce(t *testing.T) {
	for _, partial := range []string{"", "ans"} {
		t.Run(partial, func(t *testing.T) {
			result := &GenerateOutput{}
			var visible strings.Builder
			callback := func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
			final := phaseFixture("final_answer", "answer")
			require.NoError(t, rememberResponsesPhase(result, final, 0))
			if partial != "" {
				require.NoError(t, applyResponsesTextDeltaEvent(map[string]any{"output_index": 0, "delta": partial}, result, callback))
			}
			require.NoError(t, applyResponsesTextDoneEvent(map[string]any{"output_index": 0, "text": "answer"}, result, "text"))
			event := map[string]any{"response": map[string]any{"output": []any{final}}}
			require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", event, result, callback))
			require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", event, result, callback))
			require.Equal(t, "answer", visible.String())
			require.Equal(t, "answer", result.Text)
		})
	}
}

func TestExplicitResponsesPhaseRequiresPhysicalOutput(t *testing.T) {
	for _, content := range []any{nil, "text", []any{}, []any{map[string]any{"type": "input_text", "text": "x"}}, []any{map[string]any{"type": "output_text"}}, []any{map[string]any{"type": "output_text", "text": nil}}, []any{map[string]any{"type": "refusal", "refusal": ""}}} {
		item := phaseFixture("final_answer", "")
		item["content"] = content
		_, err := buildGenerateOutputFromParsed(EndpointResponses, map[string]any{"output": []any{item}})
		require.ErrorIs(t, err, errInvalidResponsesPhase)
	}
}

func TestLegacyResponsesCompletionReconcilesText(t *testing.T) {
	for _, omitted := range []bool{false, true} {
		for _, partial := range []string{"", "ans"} {
			for _, noCallback := range []bool{false, true} {
				result := &GenerateOutput{}
				var visible strings.Builder
				var callback func(GenerateStreamEvent) error
				if !noCallback {
					callback = func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
				}
				item := phaseFixture(nil, "answer")
				if omitted {
					delete(item, "phase")
				}
				require.NoError(t, rememberResponsesPhase(result, item, 0))
				if partial != "" {
					require.NoError(t, applyResponsesTextDeltaEvent(map[string]any{"output_index": 0, "delta": partial}, result, callback))
				}
				require.NoError(t, applyResponsesTextDoneEvent(map[string]any{"output_index": 0, "text": "answer"}, result, "text"))
				event := map[string]any{"response": map[string]any{"output": []any{item}}}
				for i := 0; i < 2; i++ {
					require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", event, result, callback))
					require.Equal(t, "answer", result.Text)
				}
				if !noCallback {
					require.Equal(t, "answer", visible.String())
				}
			}
		}
	}
}
func TestLegacyResponsesEmptyCompletionPreservesConfirmedText(t *testing.T) {
	result := &GenerateOutput{Text: "answer"}
	require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", map[string]any{"response": map[string]any{"id": "r"}}, result, nil))
	require.Equal(t, "answer", result.Text)
}

func TestLegacyDoneReconcilesWithoutCompletionOutput(t *testing.T) {
	for _, omitted := range []bool{true, false} {
		for _, noCallback := range []bool{true, false} {
			result := &GenerateOutput{}
			item := phaseFixture(nil, "")
			if omitted {
				delete(item, "phase")
			}
			require.NoError(t, rememberResponsesPhase(result, item, 0))
			var visible strings.Builder
			var callback func(GenerateStreamEvent) error
			if !noCallback {
				callback = func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
			}
			require.NoError(t, applyResponsesTextDeltaEvent(map[string]any{"output_index": 0, "delta": "ans"}, result, callback))
			require.NoError(t, applyResponsesTextDoneEvent(map[string]any{"output_index": 0, "text": "answer"}, result, "text"))
			require.NoError(t, applyResponsesTextDoneEvent(map[string]any{"output_index": 0, "text": "answer"}, result, "text"))
			require.NoError(t, applyResponsesCompletedEvent(AdapterOpenAIResponses, "response.completed", map[string]any{"response": map[string]any{"id": "r"}}, result, callback))
			require.Equal(t, "answer", result.Text)
			if !noCallback {
				require.Equal(t, "answer", visible.String())
			}
			require.NoError(t, applyResponsesTextDoneEvent(map[string]any{"output_index": 0, "content_index": 1, "text": "answer"}, result, "text"))
			require.Equal(t, "answeranswer", result.Text, "distinct identical content blocks remain distinct")
		}
	}
}

func TestLegacySparseCompletionEventSequence(t *testing.T) {
	for _, omitted := range []bool{true, false} {
		for _, noCallback := range []bool{true, false} {
			result := &GenerateOutput{}
			var visible strings.Builder
			var callback func(GenerateStreamEvent) error
			if !noCallback {
				callback = func(event GenerateStreamEvent) error { visible.WriteString(event.Delta); return nil }
			}
			item := phaseFixture(nil, "")
			item["id"] = "m"
			if omitted {
				delete(item, "phase")
			}
			events := []map[string]any{
				{"type": "response.output_item.added", "output_index": 0, "item": item},
				{"type": "response.output_text.delta", "item_id": "m", "output_index": 0, "content_index": 0, "delta": "ans"},
				{"type": "response.output_text.done", "item_id": "m", "output_index": 0, "content_index": 0, "text": "answer"},
				{"type": "response.completed", "response": map[string]any{"id": "r"}},
			}
			for _, event := range events {
				require.NoError(t, applyResponsesStreamEvent(AdapterOpenAIResponses, "", event, "", result, callback))
			}
			require.Equal(t, "answer", result.Text)
			if !noCallback {
				require.Equal(t, "answer", visible.String())
			}
			require.ErrorIs(t, applyResponsesTextDeltaEvent(map[string]any{"item_id": "m", "output_index": 1, "delta": "x"}, result, callback), errInvalidResponsesPhase)
		}
	}
}
