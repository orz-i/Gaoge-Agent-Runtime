package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxAnthropicStreamReplaysCompleteOrderedContent(t *testing.T) {
	response := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg-1","usage":{"input_tokens":12}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"","provider_extension":"preserve"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"carefully"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed"}}`,
		valueDataTypeContentBlockStopIndex0908D309E,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"checking"}}`,
		miniMaxFixtureContentBlockStopOne,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool-1","name":"lookup","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"id\":"}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"9007199254740993}"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		valueDataTypeMessageStop66811ABD, "",
	}, "\n\n")
	server, requests := miniMaxFixtureServer(t, "/anthropic/v1/messages", response)
	route := RouteConfig{Protocol: AdapterMiniMaxAnthropic, BaseURL: server.URL + "/anthropic/v1", APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3}
	output, err := newTestProviderClient().GenerateStream(t.Context(), route, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: testInteractionToolName}}}, nil)
	require.NoError(t, err)
	assert.Equal(t, true, (<-requests)[valueStream1E52954F])
	blocks := decodeProviderReplayItems(output.ReplayStateJSON, route.Protocol)
	require.Len(t, blocks, 3)
	assert.Equal(t, "think carefully", blocks[0][valueThinking0F938ED2])
	assert.Equal(t, "signed", blocks[0][interactionKeySignature])
	assert.Equal(t, "preserve", blocks[0]["provider_extension"])
	assert.Equal(t, "checking", blocks[1][valueText53ED4E15])
	assert.Equal(t, json.Number("9007199254740993"), asMap(blocks[2][valueInputF8378501])["id"])

	server2, requests2 := miniMaxFixtureServer(t, "/anthropic/v1/messages", `{"id":"msg-2","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`)
	route.BaseURL = server2.URL
	_, err = newTestProviderClient().Generate(t.Context(), route, GenerateInput{Messages: []Message{
		{Role: valueSystem730C6E5F, Content: "use tools"},
		{Role: valueUser72795C11, Content: testInteractionToolName},
		{Role: valueAssistantE3AFA514, Content: "reconstructed content must not replace blocks", ReplayStateJSON: output.ReplayStateJSON, ToolCalls: output.ToolCalls},
		{Role: valueTool12ABE035, ToolResults: []ToolResult{{ToolCallID: "tool-1", OutputJSON: `{"result":"found"}`}}},
	}})
	require.NoError(t, err)
	payload := <-requests2
	messages := asSlice(payload[valueMessages8CBF9CA9])
	require.Len(t, messages, 3)
	replayed, err := json.Marshal(asMap(messages[1])[valueContent3EAD52FD])
	require.NoError(t, err)
	original, err := json.Marshal(blocks)
	require.NoError(t, err)
	assert.JSONEq(t, string(original), string(replayed))
	assert.Equal(t, "tool_result", asMap(asSlice(asMap(messages[2])[valueContent3EAD52FD])[0])[valueTypeF2B37301])
}

func TestMiniMaxAnthropicReplayIsScopedToItsProtocol(t *testing.T) {
	input := GenerateInput{Messages: []Message{{Role: valueAssistantE3AFA514, Content: "fallback", ReplayStateJSON: encodeProviderReplayItems(AdapterMiniMaxAnthropic, []interface{}{map[string]interface{}{valueTypeF2B37301: valueThinking0F938ED2, valueThinking0F938ED2: "opaque"}})}}}
	payload, err := buildAnthropicRequestBody("claude", input, false)
	require.NoError(t, err)
	applyAnthropicReplayRequest(AdapterAnthropicMessages, payload, input)
	messages, ok := payload[valueMessages8CBF9CA9].([]map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "fallback", messages[0][valueContent3EAD52FD])
}
