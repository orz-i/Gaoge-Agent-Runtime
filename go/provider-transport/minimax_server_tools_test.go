package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const miniMaxSearchResultURL = "https://example.test/minimax-search"

const miniMaxResponsesSearchFixture = `{"id":"search-response","object":"response","status":"completed","output":[{"id":"search-call","type":"web_search_call","status":"completed","action":{"type":"search","query":"latest MiniMax docs"}},{"id":"search-message","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Search answer","annotations":[{"type":"url_citation","url":"https://example.test/minimax-search","title":"MiniMax docs","start_index":0,"end_index":0}]}]}],"output_text":"Search answer","usage":{"input_tokens":12,"output_tokens":8}}`

const miniMaxMessagesSearchFixture = `{"id":"search-message","type":"message","role":"assistant","content":[{"type":"server_tool_use","id":"search-call","name":"web_search","input":{"query":"latest MiniMax docs"}},{"type":"web_search_tool_result","tool_use_id":"search-call","content":[{"type":"web_search_result","title":"MiniMax docs","url":"https://example.test/minimax-search","page_age":"2026-10-01","content":"Result excerpt"}]},{"type":"text","text":"Search answer"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":8}}`

func miniMaxSearchDeclaration(protocol string) map[string]interface{} {
	if protocol == AdapterOpenAIResponses {
		return map[string]interface{}{valueTypeF2B37301: "web_search"}
	}
	return map[string]interface{}{valueTypeF2B37301: "web_search_20250305", "name": "web_search"}
}

func TestMiniMaxHostedSearchHTTPDeclarationsAndResponses(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterMiniMaxAnthropic} {
		t.Run(protocol, func(t *testing.T) {
			path, response := testResponsesEndpointPath, miniMaxResponsesSearchFixture
			if protocol == AdapterMiniMaxAnthropic {
				path, response = miniMaxFixtureMessagesPath, miniMaxMessagesSearchFixture
			}
			server, requests := miniMaxFixtureServer(t, path, response)
			output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{
				Protocol: protocol, BaseURL: server.URL + "/v1", APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3,
			}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: "Search the latest MiniMax docs"}},
				Options: map[string]interface{}{valueTools45A87F6B: []map[string]interface{}{miniMaxSearchDeclaration(protocol)}},
			})
			require.NoError(t, err)
			tools := anthropicToolPayloads((<-requests)[valueTools45A87F6B])
			require.Len(t, tools, 1)
			assert.Equal(t, miniMaxSearchDeclaration(protocol), tools[0])
			assertMiniMaxHostedSearchOutput(t, protocol, output)
			if protocol == AdapterMiniMaxAnthropic {
				blocks := decodeProviderReplayItems(output.ReplayStateJSON, protocol)
				require.Len(t, blocks, 3)
				assert.Equal(t, "web_search_tool_result", asMap(blocks[1])[valueTypeF2B37301])
			}
		})
	}
}

func assertMiniMaxHostedSearchOutput(t *testing.T, protocol string, output *GenerateOutput) {
	t.Helper()
	assert.Equal(t, "Search answer", output.Text)
	assert.Empty(t, output.ToolCalls, "server-side search must not require local execution")
	require.Len(t, output.ServerToolCalls, 1)
	assert.Equal(t, "web_search", output.ServerToolCalls[0].ToolName)
	assert.Equal(t, "completed", output.ServerToolCalls[0].Status)
	assert.Contains(t, output.ServerToolCalls[0].ArgumentsJSON, "latest MiniMax docs")
	assert.NotEmpty(t, output.ReplayStateJSON)
	if protocol == AdapterOpenAIResponses {
		assert.Contains(t, output.Citations, miniMaxSearchResultURL)
	} else {
		assert.Contains(t, output.ServerToolCalls[0].OutputJSON, miniMaxSearchResultURL)
		assert.Contains(t, output.ServerToolCalls[0].OutputJSON, "Result excerpt")
	}
}

func TestMiniMaxHostedSearchSSEEmitsTraceAndRetainsReplay(t *testing.T) {
	responsesStream := strings.Join([]string{
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"search-call","type":"web_search_call","status":"in_progress"}}`,
		`data: {"type":"response.web_search_call.searching","item_id":"search-call","output_index":0}`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"search-call","type":"web_search_call","status":"completed","action":{"type":"search","query":"latest MiniMax docs"}}}`,
		`data: {"type":"response.output_text.delta","delta":"Search answer"}`,
		`data: {"type":"response.completed","response":` + miniMaxResponsesSearchFixture + `}`,
		"data: [DONE]", "",
	}, "\n\n")
	messagesStream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"search-message","usage":{"input_tokens":12}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"search-call","name":"web_search","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"latest MiniMax docs\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"search-call","content":[{"type":"web_search_result","title":"MiniMax docs","url":"https://example.test/minimax-search","content":"Result excerpt"}]}}`,
		miniMaxFixtureContentBlockStopOne,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Search answer"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
		`data: {"type":"message_stop"}`, "",
	}, "\n\n")
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterMiniMaxAnthropic} {
		t.Run(protocol, func(t *testing.T) {
			path, response := testResponsesEndpointPath, responsesStream
			if protocol == AdapterMiniMaxAnthropic {
				path, response = miniMaxFixtureMessagesPath, messagesStream
			}
			server, requests := miniMaxFixtureServer(t, path, response)
			var trace []ToolCall
			output, err := newTestProviderClient().GenerateStream(t.Context(), RouteConfig{
				Protocol: protocol, BaseURL: server.URL + "/v1", APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3,
			}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: "Search the latest MiniMax docs"}},
				Options: map[string]interface{}{valueTools45A87F6B: []map[string]interface{}{miniMaxSearchDeclaration(protocol)}},
			}, func(event GenerateStreamEvent) error {
				if event.ServerToolCall != nil {
					trace = append(trace, *event.ServerToolCall)
				}
				return nil
			})
			require.NoError(t, err)
			assert.Equal(t, true, (<-requests)["stream"])
			assert.NotEmpty(t, trace)
			assertMiniMaxHostedSearchOutput(t, protocol, output)
		})
	}
}

func TestMiniMaxHostedSearchDisableToolsOmitsDeclarations(t *testing.T) {
	for _, protocol := range []string{AdapterOpenAIResponses, AdapterMiniMaxAnthropic} {
		t.Run(protocol, func(t *testing.T) {
			path, response := testResponsesEndpointPath, `{"id":"no-search","status":"completed","output_text":"answer","output":[]}`
			if protocol == AdapterMiniMaxAnthropic {
				path, response = miniMaxFixtureMessagesPath, `{"id":"no-search","content":[{"type":"text","text":"answer"}],"stop_reason":"end_turn"}`
			}
			server, requests := miniMaxFixtureServer(t, path, response)
			_, err := newTestProviderClient().Generate(t.Context(), RouteConfig{
				Protocol: protocol, BaseURL: server.URL + "/v1", APIKey: miniMaxFixtureAPIKey, UpstreamModel: miniMaxModelM3,
			}, GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: "answer"}}, DisableTools: true,
				Options: map[string]interface{}{valueTools45A87F6B: []map[string]interface{}{miniMaxSearchDeclaration(protocol)}},
			})
			require.NoError(t, err)
			assert.NotContains(t, <-requests, valueTools45A87F6B)
		})
	}
}
