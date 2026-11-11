package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicPausedSearchRetainsNativeBlocksAndStopReason(t *testing.T) {
	for _, protocol := range []string{AdapterMiniMaxAnthropic, AdapterAnthropicMessages} {
		t.Run(protocol, func(t *testing.T) {
			stream := strings.Join([]string{
				`data: {"type":"message_start","message":{"id":"paused-1"}}`,
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"searching evidence"}}`,
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"native-signature"}}`,
				`data: {"type":"content_block_stop","index":0}`,
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"search-1","name":"web_search","input":{"query":"current evidence"}}}`,
				`data: {"type":"content_block_stop","index":1}`,
				`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":12}}`,
				`data: {"type":"message_stop"}`, "",
			}, "\n\n")
			output := &GenerateOutput{}
			err := consumeAnthropicStreamWithReplay(strings.NewReader(stream), output, nil,
				newAnthropicToolClassifier(nil, nil), newAnthropicReplay(protocol))
			require.NoError(t, err)
			compactAnthropicStreamToolCalls(output)
			require.Equal(t, "pause_turn", output.StopReason)
			require.Len(t, output.ServerToolCalls, 1)
			require.Equal(t, valueInProgressFCF26CA0, output.ServerToolCalls[0].Status, "without a result, execution has no terminal receipt")
			blocks := decodeProviderReplayItems(output.ReplayStateJSON, protocol)
			require.Len(t, blocks, 2)
			require.Equal(t, "native-signature", blocks[0]["signature"])
			input := GenerateInput{Messages: []Message{
				{Role: "user", Content: "research"},
				{Role: valueAssistantE3AFA514, ReplayStateJSON: output.ReplayStateJSON},
			}}
			payload, err := buildAnthropicRequestBody("model", input, false)
			require.NoError(t, err)
			applyAnthropicReplayRequest(protocol, payload, input)
			messages, ok := payload["messages"].([]map[string]interface{})
			require.True(t, ok)
			require.Len(t, messages, 2)
			replayed, err := json.Marshal(messages[1]["content"])
			require.NoError(t, err)
			original, err := json.Marshal(blocks)
			require.NoError(t, err)
			require.JSONEq(t, string(original), string(replayed))
		})
	}
}

func TestAnthropicPausedSearchResultUsesPendingReplayIdentity(t *testing.T) {
	const previous = `{"protocol":"minimax_anthropic_messages","items":[{"type":"server_tool_use","id":"pending-search","name":"web_search","input":{"query":"evidence"}}]}`
	const current = `{"protocol":"minimax_anthropic_messages","items":[{"type":"web_search_tool_result","tool_use_id":"pending-search","content":[{"type":"web_search_result","url":"https://example.test/evidence"}]}]}`
	input := GenerateInput{Messages: []Message{{Role: valueAssistantE3AFA514, ReplayStateJSON: previous}}}
	output := &GenerateOutput{ReplayStateJSON: current}
	reconcileAnthropicPendingResults(AdapterMiniMaxAnthropic, input, output)
	require.Len(t, output.ServerToolCalls, 1)
	require.Equal(t, "pending-search", output.ServerToolCalls[0].ToolCallID)
	require.Equal(t, valueCompletedFC6E652A, output.ServerToolCalls[0].Status)
	require.Contains(t, output.ServerToolCalls[0].OutputJSON, "https://example.test/evidence")
	reconcileAnthropicPendingResults(AdapterMiniMaxAnthropic, input, output)
	require.Len(t, output.ServerToolCalls, 1, "receipt must not be appended twice")
	require.JSONEq(t, current, output.ReplayStateJSON, "native blocks must stay intact")
	output.ServerToolCalls[0].Status = valueInProgressFCF26CA0
	output.ServerToolCalls[0].OutputJSON = ""
	reconcileAnthropicPendingResults(AdapterMiniMaxAnthropic, input, output)
	require.Equal(t, valueCompletedFC6E652A, output.ServerToolCalls[0].Status, "a repeated identity must still reconcile its authoritative result")
	require.Contains(t, output.ServerToolCalls[0].OutputJSON, "https://example.test/evidence")
}
