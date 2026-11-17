package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	eventResponseOutputItemAdded = "event: response.output_item.added"
	valueMemoryList83D7D805      = "memory.list"
	valuePrintHi8277249E         = "print(\"hi\")"
	valueTypeD3C0BBD2            = "type"
	valueItems8677E4B3           = "{\"items\":[]}"
)

const (
	valueCustom3ED53885   = "custom"
	valueFunctionE887F145 = "function"
	valueText0A5F412C     = "text"
	valueToolDCEAC0D9     = "tool"
	valueUser07DDCD27     = "user"
)

const (
	valuePOSTB8F6D005                             = "POST"
	valueSearchTheWeb6BE1DC96                     = "Search the web"
	valueRedactedD1837277                         = "[redacted]"
	valueAdvisor20260301911E238E                  = "advisor_20260301"
	valueArgumentsC1235E64                        = "arguments"
	valueAssistant64D9B36F                        = "assistant"
	valueAuto06CC62F4                             = "auto"
	valueBingSearch648A9EFA                       = "bing_search"
	valueCall114A660D5                            = "call_1"
	valueCall123952DF1BF                          = "call_123"
	valueChoicesA69B7203                          = "choices"
	valueCodeExec6F7763BB                         = "code_exec"
	valueCodeExecutionEC3F88D5                    = "code_execution"
	valueCodeExecution2026012039DFFC07            = "code_execution_20260120"
	valueCodeInterpreterDCAD241D                  = "code_interpreter"
	valueCompleted5BC9F232                        = "completed"
	valueContent94830337                          = "content"
	valueDataDONE26000752                         = "data: [DONE]"
	valueDataTypeContentBlockStopIndex0908D309E   = "data: {\"type\":\"content_block_stop\",\"index\":0}"
	valueDataTypeMessageStartMessageIdMsgD09F9B26 = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":10}}}"
	valueDataTypeMessageStop66811ABD              = "data: {\"type\":\"message_stop\"}"
	valueDeltaA3D0A864                            = "delta"
	valueError4D50CFD6                            = "error"
	valueEventContentBlockDeltaA91AB1F9           = "event: content_block_delta"
	valueEventContentBlockStart415B336C           = "event: content_block_start"
	valueEventContentBlockStop71FF8CB0            = "event: content_block_stop"
	valueEventMessageStartDF12B601                = "event: message_start"
	valueEventMessageStopE2A0F595                 = "event: message_stop"
	valueEventResponseOutputItemDone3451AA40      = "event: response.output_item.done"
	valueFunctionCall93479F70                     = "function_call"
	valueFunctionCallOutputEA3E9427               = "function_call_output"
	valueHiddenFB9EF0F5                           = "hidden"
	valueHttpsExampleComA59C4793C                 = "https://example.com/a"
	valueImageGenerationA59A06EF                  = "image_generation"
	valueInProgress995727F7                       = "in_progress"
	valueIndex41F7A6B4                            = "index"
	valueInputE44CA1C0                            = "input"
	valueMemorySaveE2BC22BD                       = "memory.save"
	valueMessage33C0A304                          = "message"
	valueNameC49CCF6F                             = "name"
	valueObject9C36618F                           = "object"
	valueProviderLookupE67943A2                   = "provider_lookup"
	valueReasoningBA742EA3                        = "reasoning"
	valueRequestedEF0BAA99                        = "requested"
	valueSearch6C0C7288                           = "search"
	valueSearchWeb7EF22CDC                        = "search_web"
	valueSearching96F7C642                        = "searching"
	valueShellAD011B52                            = "shell"
	valueSuccess1E495DA3                          = "success"
	valueSummarize2FA92B6D                        = "summarize"
	valueThoughtSignature1AFD0D94D                = "thought-signature-1"
	valueToolCallsD36A9436                        = "tool_calls"
	valueToolSearchToolBm2520251119E75004D0       = "tool_search_tool_bm25_20251119"
	valueToolSearchToolRegex2025111932891D75      = "tool_search_tool_regex_20251119"
	valueToolsE04282FD                            = "tools"
	valueToolu1DA612952                           = "toolu_1"
	valueUrlContext879B99C3                       = "url_context"
	valueVisibleD660055F                          = "visible"
	valueWebFetch202602091A5FB47C                 = "web_fetch_20260209"
	valueWebSearch6C1E23E9                        = "web_search"
	valueWebSearch2025030564E3468B                = "web_search_20250305"
	valueWebSearch2026020902F6160F                = "web_search_20260209"
	valueWsc10518242B                             = "wsc_1"
	valueXKeywordSearchD14FC105                   = "x_keyword_search"
	valueXSearchFB43F46C                          = "x_search"
)

func mustBuildRequestBody(t *testing.T, protocol string, model string, endpoint string, input GenerateInput, stream bool) map[string]interface{} {
	t.Helper()
	payload, err := buildOpenAIRequestBody(protocol, model, endpoint, input, stream)
	if err != nil {
		t.Fatalf("build request body: %v", err)
	}
	return payload
}

func TestBuildDeepSeekV4ResponsesOmitsRequiredToolChoice(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "deepseek-v4-flash", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
	}, false)

	if _, ok := payload["tool_choice"]; ok {
		t.Fatalf("expected DeepSeek V4 Responses thinking mode to omit required tool_choice, got %#v", payload["tool_choice"])
	}
	tools := requireMapSlice(t, payload[valueToolsE04282FD], valueToolsE04282FD)
	if len(tools) != 1 {
		t.Fatalf("expected DeepSeek V4 Responses tools to remain available, got %#v", tools)
	}
}

func TestBuildOpenAIResponsesKeepsRequiredToolChoice(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5.6", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
	}, false)

	if payload["tool_choice"] != string(ToolChoiceRequired) {
		t.Fatalf("expected OpenAI Responses required tool_choice to remain unchanged, got %#v", payload["tool_choice"])
	}
}

func mustBuildAnthropicRequestBody(t *testing.T, model string, input GenerateInput, stream bool) map[string]interface{} {
	t.Helper()
	payload, err := buildAnthropicRequestBody(model, input, stream)
	if err != nil {
		t.Fatalf("build anthropic request body: %v", err)
	}
	return payload
}

func requireMapSlice(t *testing.T, value interface{}, label string) []map[string]interface{} {
	t.Helper()
	items, ok := value.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected %s as []map[string]interface{}, got %#v", label, value)
	}
	return items
}

func requireMapValue(t *testing.T, value interface{}, label string) map[string]interface{} {
	t.Helper()
	item, ok := value.(map[string]interface{})
	if !ok {
		t.Fatalf("expected %s as map[string]interface{}, got %#v", label, value)
	}
	return item
}

func TestBuildChatCompletionsToolMessages(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{
			{
				Role:             valueAssistant64D9B36F,
				ReasoningContent: "need memory",
				ToolCalls:        []ToolCall{{ToolCallID: valueCall114A660D5, ToolType: valueFunctionE887F145, ToolName: valueMemoryList83D7D805, ArgumentsJSON: `{"scope":"user"}`}},
			},
			{Role: valueToolDCEAC0D9, ToolResults: []ToolResult{{ToolCallID: valueCall114A660D5, ToolName: valueMemoryList83D7D805, OutputJSON: valueItems8677E4B3, Status: valueSuccess1E495DA3}}},
		},
	}, false)

	messages, ok := payload["messages"].([]map[string]interface{})
	if !ok || len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %#v", messages)
	}
	if messages[0]["role"] != valueAssistant64D9B36F {
		t.Fatalf("expected assistant tool call message, got %#v", messages[0])
	}
	if messages[0]["reasoning_content"] != "need memory" {
		t.Fatalf("expected reasoning_content passback, got %#v", messages[0])
	}
	toolCalls, ok := messages[0][valueToolCallsD36A9436].([]map[string]interface{})
	if !ok || len(toolCalls) == 0 {
		t.Fatalf("expected tool calls, got %#v", messages[0])
	}
	if toolCalls[0]["id"] != valueCall114A660D5 {
		t.Fatalf("expected tool call id, got %#v", toolCalls[0])
	}
	if messages[1]["role"] != valueToolDCEAC0D9 || messages[1]["tool_call_id"] != valueCall114A660D5 {
		t.Fatalf("expected tool result message, got %#v", messages[1])
	}
}

func TestBuildOpenRouterChatCompletionsToolMessagesUsesReasoningField(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenRouterChat, "openai/gpt-oss-120b:free", EndpointChatCompletions, GenerateInput{
		Messages: []Message{
			{
				Role:             valueAssistant64D9B36F,
				ReasoningContent: "need live news",
				ToolCalls:        []ToolCall{{ToolCallID: valueCall114A660D5, ToolType: valueFunctionE887F145, ToolName: valueSearchWeb7EF22CDC, ArgumentsJSON: `{"query":"today news China"}`}},
			},
			{Role: valueToolDCEAC0D9, ToolResults: []ToolResult{{ToolCallID: valueCall114A660D5, ToolName: valueSearchWeb7EF22CDC, OutputJSON: valueItems8677E4B3, Status: valueSuccess1E495DA3}}},
		},
	}, false)

	messages := requireMapSlice(t, payload["messages"], "messages")
	assistant := messages[0]
	if assistant[valueReasoningBA742EA3] != "need live news" {
		t.Fatalf("expected OpenRouter reasoning passback, got %#v", assistant)
	}
	if _, ok := assistant["reasoning_content"]; ok {
		t.Fatalf("expected no DeepSeek reasoning_content alias for OpenRouter, got %#v", assistant)
	}
	if payload["stream"] != false {
		t.Fatalf("expected chat completions request body, got %#v", payload)
	}
}

func TestParseChatCompletionsOutputSeparatesReasoningContentParts(t *testing.T) {
	result := &GenerateOutput{}
	parseChatCompletionsOutput(AdapterOpenAIChatCompletions, map[string]interface{}{
		valueChoicesA69B7203: []interface{}{
			map[string]interface{}{
				valueMessage33C0A304: map[string]interface{}{
					valueContent94830337: []interface{}{
						map[string]interface{}{valueTypeD3C0BBD2: valueReasoningBA742EA3, valueText0A5F412C: "hidden reasoning"},
						map[string]interface{}{valueTypeD3C0BBD2: valueText0A5F412C, valueText0A5F412C: "visible answer"},
					},
					valueToolCallsD36A9436: []interface{}{
						map[string]interface{}{
							"id":              valueCall114A660D5,
							valueTypeD3C0BBD2: valueFunctionE887F145,
							valueFunctionE887F145: map[string]interface{}{
								valueNameC49CCF6F:      valueMemoryList83D7D805,
								valueArgumentsC1235E64: "{}",
							},
						},
					},
				},
			},
		},
	}, result, false)

	if result.Text != "visible answer" {
		t.Fatalf("expected only visible content, got %q", result.Text)
	}
	if result.Reasoning == nil || result.Reasoning.Text != "hidden reasoning" {
		t.Fatalf("expected reasoning content to be separated, got %#v", result.Reasoning)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolName != valueMemoryList83D7D805 {
		t.Fatalf("expected tool call to be preserved, got %#v", result.ToolCalls)
	}
}

func TestApplyChatStreamEventSeparatesReasoningContentParts(t *testing.T) {
	result := &GenerateOutput{}
	var visible string
	var reasoning string
	err := applyChatStreamEvent(AdapterOpenAIChatCompletions, map[string]interface{}{
		valueChoicesA69B7203: []interface{}{
			map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueContent94830337: []interface{}{
						map[string]interface{}{valueTypeD3C0BBD2: valueReasoningBA742EA3, valueText0A5F412C: valueHiddenFB9EF0F5},
						map[string]interface{}{valueTypeD3C0BBD2: valueText0A5F412C, valueText0A5F412C: valueVisibleD660055F},
					},
				},
			},
		},
	}, result, func(event GenerateStreamEvent) error {
		visible += event.Delta
		if event.Reasoning != nil {
			reasoning += event.Reasoning.Text
		}
		return nil
	}, false)
	if err != nil {
		t.Fatalf("apply chat stream event: %v", err)
	}
	if visible != valueVisibleD660055F || result.Text != valueVisibleD660055F {
		t.Fatalf("expected only visible stream content, visible=%q result=%q", visible, result.Text)
	}
	if reasoning != valueHiddenFB9EF0F5 {
		t.Fatalf("expected reasoning stream content, got %q", reasoning)
	}
	if result.Reasoning == nil || result.Reasoning.Text != valueHiddenFB9EF0F5 {
		t.Fatalf("expected stream reasoning to be stored for passback, got %#v", result.Reasoning)
	}
}

func TestBuildChatCompletionsCustomToolMessages(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{
			{Role: valueAssistant64D9B36F, ToolCalls: []ToolCall{{ToolCallID: "call_custom", ToolType: valueCustom3ED53885, ToolName: valueCodeExec6F7763BB, ArgumentsJSON: valuePrintHi8277249E}}},
		},
	}, false)

	messages := requireMapSlice(t, payload["messages"], "messages")
	toolCalls := requireMapSlice(t, messages[0][valueToolCallsD36A9436], valueToolCallsD36A9436)
	if toolCalls[0][valueTypeD3C0BBD2] != valueCustom3ED53885 {
		t.Fatalf("expected custom tool call, got %#v", toolCalls[0])
	}
	custom := requireMapValue(t, toolCalls[0][valueCustom3ED53885], "custom tool payload")
	if custom[valueNameC49CCF6F] != valueCodeExec6F7763BB || custom[valueInputE44CA1C0] != valuePrintHi8277249E {
		t.Fatalf("expected custom tool payload, got %#v", custom)
	}
}

func TestBuildResponsesToolInputItems(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{
			{Role: valueToolDCEAC0D9, ToolResults: []ToolResult{{ToolCallID: valueCall114A660D5, ToolName: valueMemoryList83D7D805, OutputJSON: valueItems8677E4B3, Status: valueSuccess1E495DA3}}},
		},
	}, false)

	items := requireMapSlice(t, payload[valueInputE44CA1C0], valueInputE44CA1C0)
	if len(items) != 1 || items[0][valueTypeD3C0BBD2] != valueFunctionCallOutputEA3E9427 || items[0]["call_id"] != valueCall114A660D5 {
		t.Fatalf("expected function_call_output item, got %#v", items)
	}
}

func TestBuildOpenRouterResponsesToolHistoryAddsItemIDs(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenRouterResponses, "openai/o4-mini", EndpointResponses, GenerateInput{
		Messages: []Message{
			{Role: valueAssistant64D9B36F, ToolCalls: []ToolCall{{ToolCallID: valueCall123952DF1BF, ToolName: "get_weather", ArgumentsJSON: `{"location":"Boston, MA"}`}}},
			{Role: valueToolDCEAC0D9, ToolResults: []ToolResult{{ToolCallID: valueCall123952DF1BF, ToolName: "get_weather", OutputJSON: `{"temperature":"72F"}`, Status: valueSuccess1E495DA3}}},
		},
	}, false)

	items := requireMapSlice(t, payload[valueInputE44CA1C0], valueInputE44CA1C0)
	if len(items) != 2 {
		t.Fatalf("expected two tool history items, got %#v", items)
	}
	if items[0][valueTypeD3C0BBD2] != valueFunctionCall93479F70 || items[0]["id"] == "" || items[0]["call_id"] != valueCall123952DF1BF {
		t.Fatalf("expected function_call item id and call_id, got %#v", items[0])
	}
	if items[1][valueTypeD3C0BBD2] != valueFunctionCallOutputEA3E9427 || items[1]["id"] == "" || items[1]["call_id"] != valueCall123952DF1BF {
		t.Fatalf("expected function_call_output item id and call_id, got %#v", items[1])
	}
}

func TestBuildOpenAIToolsKeepsInputSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"},"count":{"type":"number"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
	}, false)

	tools := requireMapSlice(t, payload[valueToolsE04282FD], valueToolsE04282FD)
	fn := requireMapValue(t, tools[0][valueFunctionE887F145], valueFunctionE887F145)
	parameters := requireMapValue(t, fn["parameters"], "parameters")
	properties := requireMapValue(t, parameters["properties"], "properties")
	query := requireMapValue(t, properties["query"], "query")
	count := requireMapValue(t, properties["count"], "count")
	if fn[valueNameC49CCF6F] != valueBingSearch648A9EFA || query[valueTypeD3C0BBD2] != testJSONSchemaTypeString || count[valueTypeD3C0BBD2] != "number" {
		t.Fatalf("expected OpenAI tool schema to be preserved, got %#v", tools[0])
	}
}

func TestBuildOpenAIToolsMergesProviderAndMCPTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{
					valueTypeD3C0BBD2: valueWebSearch6C1E23E9,
				},
			},
		},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
	}, false)

	tools := requireMapSlice(t, payload[valueToolsE04282FD], valueToolsE04282FD)
	if len(tools) != 2 {
		t.Fatalf("expected provider tool and MCP tool, got %#v", tools)
	}
	if tools[0][valueTypeD3C0BBD2] != valueWebSearch6C1E23E9 {
		t.Fatalf("expected provider tool first, got %#v", tools[0])
	}
	fn := requireMapValue(t, tools[1][valueFunctionE887F145], valueFunctionE887F145)
	if fn[valueNameC49CCF6F] != valueBingSearch648A9EFA {
		t.Fatalf("expected MCP tool second, got %#v", tools[1])
	}
}

func TestBuildOpenAIResponsesNativeToolOptions(t *testing.T) {
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5.5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: "search and run code"}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch6C1E23E9},
				map[string]interface{}{
					valueTypeD3C0BBD2: valueShellAD011B52,
					"environment": map[string]interface{}{
						valueTypeD3C0BBD2: "container_auto",
					},
				},
				map[string]interface{}{valueTypeD3C0BBD2: valueImageGenerationA59A06EF},
				map[string]interface{}{
					valueTypeD3C0BBD2: valueCodeInterpreterDCAD241D,
					"container": map[string]interface{}{
						valueTypeD3C0BBD2: valueAuto06CC62F4,
					},
				},
			},
		},
	}, true)
	assertOpenAIResponsesNativeTools(t, payload)
}

func assertOpenAIResponsesNativeTools(t *testing.T, payload map[string]interface{}) {
	t.Helper()
	tools, ok := payload[valueToolsE04282FD].([]map[string]interface{})
	requireCondition(t, ok && len(tools) == 4, "expected four native OpenAI tools, got %#v", payload[valueToolsE04282FD])
	requireCondition(t, tools[0][valueTypeD3C0BBD2] == valueWebSearch6C1E23E9 && tools[1][valueTypeD3C0BBD2] == valueShellAD011B52 && tools[2][valueTypeD3C0BBD2] == valueImageGenerationA59A06EF && tools[3][valueTypeD3C0BBD2] == valueCodeInterpreterDCAD241D, "expected OpenAI native tool order to be preserved, got %#v", tools)
	environment, ok := tools[1]["environment"].(map[string]interface{})
	requireCondition(t, ok && environment[valueTypeD3C0BBD2] == "container_auto", "expected shell container_auto environment, got %#v", tools[1])
	container, ok := tools[3]["container"].(map[string]interface{})
	requireCondition(t, ok && container[valueTypeD3C0BBD2] == valueAuto06CC62F4, "expected code_interpreter auto container, got %#v", tools[3])
}

func TestBuildRequestBodyDisableToolsRemovesProviderAndMCPTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSummarize2FA92B6D}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch6C1E23E9}},
		},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
		DisableTools: true,
	}, false)

	if _, ok := payload[valueToolsE04282FD]; ok {
		t.Fatalf("expected tools to be omitted when disabled, got %#v", payload[valueToolsE04282FD])
	}
}

func TestBuildResponsesDisableToolsRemovesWebSearchAndMCPTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildRequestBody(t, AdapterOpenAIResponses, "gpt-5", EndpointResponses, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSummarize2FA92B6D}},
		Options: map[string]interface{}{
			valueWebSearch6C1E23E9: true,
			valueToolsE04282FD:     []interface{}{map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch6C1E23E9}},
		},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
		DisableTools: true,
	}, false)

	if _, ok := payload[valueToolsE04282FD]; ok {
		t.Fatalf("expected tools to be omitted when disabled, got %#v", payload[valueToolsE04282FD])
	}
}

func TestBuildAnthropicToolsMergesProviderAndMCPTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Options: map[string]interface{}{
			valueWebSearch6C1E23E9: true,
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{
					valueTypeD3C0BBD2: "custom_tool",
					valueNameC49CCF6F: valueProviderLookupE67943A2,
				},
			},
		},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
	}, false)

	tools := requireMapSlice(t, payload[valueToolsE04282FD], valueToolsE04282FD)
	if len(tools) != 3 {
		t.Fatalf("expected provider, web search, and MCP tools, got %#v", tools)
	}
	if tools[0][valueNameC49CCF6F] != valueProviderLookupE67943A2 {
		t.Fatalf("expected provider tool first, got %#v", tools[0])
	}
	if tools[1][valueTypeD3C0BBD2] != valueWebSearch2025030564E3468B {
		t.Fatalf("expected Anthropic web search second, got %#v", tools[1])
	}
	if tools[1][valueNameC49CCF6F] != valueWebSearch6C1E23E9 {
		t.Fatalf("expected Anthropic native web search name, got %#v", tools[1])
	}
	if _, ok := tools[1]["allowed_callers"]; ok {
		t.Fatalf("expected legacy Anthropic web search to avoid allowed_callers, got %#v", tools[1])
	}
	if tools[2][valueNameC49CCF6F] != valueBingSearch648A9EFA {
		t.Fatalf("expected MCP tool third, got %#v", tools[2])
	}
}

func TestBuildAnthropicNativeToolsAddsRequiredNames(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-haiku-4-5", GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: "今日天气？"}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch2026020902F6160F},
				map[string]interface{}{valueTypeD3C0BBD2: valueWebFetch202602091A5FB47C},
				map[string]interface{}{valueTypeD3C0BBD2: valueCodeExecution2026012039DFFC07},
				map[string]interface{}{valueTypeD3C0BBD2: valueAdvisor20260301911E238E},
				map[string]interface{}{valueTypeD3C0BBD2: valueToolSearchToolRegex2025111932891D75},
				map[string]interface{}{valueTypeD3C0BBD2: valueToolSearchToolBm2520251119E75004D0},
			},
		},
	}, true)

	tools, ok := payload[valueToolsE04282FD].([]map[string]interface{})
	if !ok || len(tools) != 6 {
		t.Fatalf("expected normalized Anthropic native tools, got %#v", payload[valueToolsE04282FD])
	}
	expectedNames := []string{valueWebSearch6C1E23E9, "web_fetch", valueCodeExecutionEC3F88D5, "advisor", "tool_search_tool_regex", "tool_search_tool_bm25"}
	for index, expected := range expectedNames {
		if tools[index][valueNameC49CCF6F] != expected {
			t.Fatalf("expected tool %d name %q, got %#v", index, expected, tools[index])
		}
	}
	for _, index := range []int{0, 1} {
		callers, ok := tools[index]["allowed_callers"].([]string)
		if !ok || len(callers) != 1 || callers[0] != "direct" {
			t.Fatalf("expected tool %d to use direct callers, got %#v", index, tools[index])
		}
	}
	if _, ok := tools[2]["allowed_callers"]; ok {
		t.Fatalf("expected code execution tool to avoid allowed_callers, got %#v", tools[2])
	}
}

func TestBuildAnthropicRequestBodyDisableToolsRemovesProviderAndMCPTools(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4", GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSummarize2FA92B6D}},
		Options: map[string]interface{}{
			valueWebSearch6C1E23E9: true,
			valueToolsE04282FD: []interface{}{map[string]interface{}{
				valueTypeD3C0BBD2: "custom_tool",
				valueNameC49CCF6F: valueProviderLookupE67943A2,
			}},
		},
		Tools: []ToolDefinition{{
			Name:        valueBingSearch648A9EFA,
			Description: valueSearchTheWeb6BE1DC96,
			InputSchema: schema,
		}},
		DisableTools: true,
	}, false)

	if _, ok := payload[valueToolsE04282FD]; ok {
		t.Fatalf("expected tools to be omitted when disabled, got %#v", payload[valueToolsE04282FD])
	}
}

func TestApplyAnthropicBetaHeadersForNativeTools(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("anthropic-beta", "existing-beta")

	applyAnthropicBetaHeaders(req, map[string]interface{}{
		valueToolsE04282FD: []map[string]interface{}{
			{valueTypeD3C0BBD2: valueWebSearch2026020902F6160F},
			{valueTypeD3C0BBD2: valueWebFetch202602091A5FB47C},
			{valueTypeD3C0BBD2: valueCodeExecution2026012039DFFC07},
			{valueTypeD3C0BBD2: valueAdvisor20260301911E238E},
			{valueTypeD3C0BBD2: "mcp_toolset_20251119"},
		},
	}, nil)

	header := req.Header.Get("anthropic-beta")
	if strings.Contains(header, "web-fetch") || strings.Contains(header, "code-execution") {
		t.Fatalf("expected GA native tools to avoid beta headers, got %q", header)
	}
	if !strings.Contains(header, "advisor-2026-03-01") {
		t.Fatalf("expected advisor beta header, got %q", header)
	}
	if !strings.Contains(header, "mcp-client-2025-11-20") {
		t.Fatalf("expected mcp client beta header, got %q", header)
	}
	if !strings.Contains(header, "existing-beta") {
		t.Fatalf("expected existing beta header to be preserved, got %q", header)
	}
}

func TestBuildRequestBodyRejectsInvalidProviderToolsOption(t *testing.T) {
	_, err := buildOpenAIRequestBody(AdapterOpenAIChatCompletions, "gpt-5", EndpointChatCompletions, GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Options: map[string]interface{}{
			valueToolsE04282FD: map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch6C1E23E9},
		},
	}, false)
	if err == nil || err.Error() != "model option tools must be an array" {
		t.Fatalf("expected invalid tools error, got %v", err)
	}
}

func TestChatStreamToolCallArgumentsAreConcatenatedWithoutDefaultPrefix(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	chunks := []map[string]interface{}{
		{
			"id": "chatcmpl_1",
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						"id":               valueCall114A660D5,
						valueTypeD3C0BBD2:  valueFunctionE887F145,
						valueFunctionE887F145: map[string]interface{}{
							valueNameC49CCF6F: valueBingSearch648A9EFA,
						},
					}},
				},
			}},
		},
		{
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						valueFunctionE887F145: map[string]interface{}{
							valueArgumentsC1235E64: "{\"query\":\"IDC",
						},
					}},
				},
			}},
		},
		{
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						valueFunctionE887F145: map[string]interface{}{
							valueArgumentsC1235E64: " Flare\"}",
						},
					}},
				},
			}},
		},
	}

	for _, chunk := range chunks {
		if err := applyChatStreamEvent(AdapterOpenAIChatCompletions, chunk, result, nil, false); err != nil {
			t.Fatalf("apply stream event: %v", err)
		}
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].ArgumentsJSON != `{"query":"IDC Flare"}` {
		t.Fatalf("unexpected arguments JSON: %q", result.ToolCalls[0].ArgumentsJSON)
	}
}

func TestChatStreamCustomToolCallInputIsConcatenated(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	chunks := []map[string]interface{}{
		{
			"id": "chatcmpl_1",
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						"id":               "call_custom",
						valueTypeD3C0BBD2:  valueCustom3ED53885,
						valueCustom3ED53885: map[string]interface{}{
							valueNameC49CCF6F: valueCodeExec6F7763BB,
						},
					}},
				},
			}},
		},
		{
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						valueCustom3ED53885: map[string]interface{}{
							valueInputE44CA1C0: "print(",
						},
					}},
				},
			}},
		},
		{
			valueChoicesA69B7203: []interface{}{map[string]interface{}{
				valueDeltaA3D0A864: map[string]interface{}{
					valueToolCallsD36A9436: []interface{}{map[string]interface{}{
						valueIndex41F7A6B4: float64(0),
						valueCustom3ED53885: map[string]interface{}{
							valueInputE44CA1C0: `"hi")`,
						},
					}},
				},
			}},
		},
	}

	for _, chunk := range chunks {
		if err := applyChatStreamEvent(AdapterOpenAIChatCompletions, chunk, result, nil, false); err != nil {
			t.Fatalf("apply stream event: %v", err)
		}
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one custom tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].ToolType != valueCustom3ED53885 || result.ToolCalls[0].ToolName != valueCodeExec6F7763BB || result.ToolCalls[0].ArgumentsJSON != valuePrintHi8277249E {
		t.Fatalf("unexpected custom tool call: %#v", result.ToolCalls[0])
	}
}

func TestParseChatCompletionsCustomToolCall(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "chatcmpl_1",
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "call_custom",
					"type": "custom",
					"custom": {"name": "code_exec", "input": "print(\"hi\")"}
				}]
			}
		}]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointChatCompletions, payload)
	require.NoError(t, err)
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolType != valueCustom3ED53885 || result.ToolCalls[0].ToolName != valueCodeExec6F7763BB || result.ToolCalls[0].ArgumentsJSON != valuePrintHi8277249E {
		t.Fatalf("unexpected custom tool call: %#v", result.ToolCalls)
	}
}

func TestParseChatCompletionsDSMLToolCalls(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "chatcmpl_1",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"searchGitHub\">\n<｜DSML｜parameter name=\"query\" string=\"true\">默认启用MCP Gaoge</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜tool_calls>"
			}
		}]
	}`)

	result, err := buildGenerateOutputFromParsedForAdapter(EndpointChatCompletions, AdapterOpenAIChatCompletions, payload, true)
	require.NoError(t, err)
	if result.Text != "" {
		t.Fatalf("expected DSML envelope to be removed from visible text, got %q", result.Text)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one parsed DSML tool call, got %#v", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.ToolCallID != "dsml_call_1" || call.ToolType != valueFunctionE887F145 || call.ToolName != "searchGitHub" || call.Status != valueRequestedEF0BAA99 {
		t.Fatalf("unexpected DSML tool call: %#v", call)
	}
	if call.ArgumentsJSON != `{"query":"默认启用MCP Gaoge"}` {
		t.Fatalf("unexpected DSML arguments: %q", call.ArgumentsJSON)
	}
}

func TestParseChatCompletionsDSMLToolCallsDecodesJSONParameters(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "chatcmpl_1",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"searchGitHub\">\n<｜DSML｜parameter name=\"query\" string=\"true\">Gaoge</｜DSML｜parameter>\n<｜DSML｜parameter name=\"limit\" string=\"false\">3</｜DSML｜parameter>\n<｜DSML｜parameter name=\"filters\" string=\"false\">{\"language\":\"Go\"}</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜tool_calls>"
			}
		}]
	}`)

	result, err := buildGenerateOutputFromParsedForAdapter(EndpointChatCompletions, AdapterOpenAIChatCompletions, payload, true)
	require.NoError(t, err)
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one parsed DSML tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].ArgumentsJSON != `{"filters":{"language":"Go"},"limit":3,"query":"Gaoge"}` {
		t.Fatalf("unexpected DSML arguments: %q", result.ToolCalls[0].ArgumentsJSON)
	}
}

func TestParseChatCompletionsDSMLToolCallsDisabledByDefault(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "chatcmpl_1",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "<｜DSML｜tool_calls><｜DSML｜invoke name=\"searchGitHub\"><｜DSML｜parameter name=\"query\" string=\"true\">Gaoge</｜DSML｜parameter></｜DSML｜invoke></｜DSML｜tool_calls>"
			}
		}]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointChatCompletions, payload)
	require.NoError(t, err)
	if !strings.Contains(result.Text, "DSML") {
		t.Fatalf("expected default chat completions path to keep DSML as text, got %q", result.Text)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected default chat completions path not to parse DSML tool calls, got %#v", result.ToolCalls)
	}
}

func TestTextEncodedToolCallsOnlyEnabledForDeepSeekChatCompletions(t *testing.T) {
	if !deepSeekTextEncodedToolCallsEnabled(RouteConfig{
		Protocol:      AdapterOpenAIChatCompletions,
		UpstreamModel: "deepseek-v4-flash",
	}) {
		t.Fatalf("expected DeepSeek chat completions route to enable text-encoded tool calls")
	}
	if deepSeekTextEncodedToolCallsEnabled(RouteConfig{
		Protocol:      AdapterOpenAIChatCompletions,
		UpstreamModel: "gpt-5.4",
	}) {
		t.Fatalf("expected non-DeepSeek chat completions route to keep text-encoded tool calls disabled")
	}
	if deepSeekTextEncodedToolCallsEnabled(RouteConfig{
		Protocol:      AdapterOpenAIResponses,
		UpstreamModel: "deepseek-v4-flash",
	}) {
		t.Fatalf("expected non-chat-completions route to keep text-encoded tool calls disabled")
	}
}

func TestConsumeChatStreamDSMLToolCallsAreNotEmittedAsText(t *testing.T) {
	rawStream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"searchGitHub\">\n"}}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"<｜DSML｜parameter name=\"query\" string=\"true\">Gaoge MCP</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜tool_calls>"}}]}`,
		valueDataDONE26000752,
		``,
	}, "\n\n")
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	var deltas []string

	err := consumeOpenAIGenerateStream(EndpointChatCompletions, AdapterOpenAIChatCompletions, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.Delta != "" {
			deltas = append(deltas, event.Delta)
		}
		return nil
	}, true)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if len(deltas) != 0 || result.Text != "" {
		t.Fatalf("expected DSML stream to stay out of visible text, deltas=%#v text=%q", deltas, result.Text)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolName != "searchGitHub" || result.ToolCalls[0].ArgumentsJSON != `{"query":"Gaoge MCP"}` {
		t.Fatalf("unexpected DSML stream tool calls: %#v", result.ToolCalls)
	}
}

func TestConsumeChatStreamIncompleteDSMLToolCallsReturnsError(t *testing.T) {
	rawStream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"searchGitHub\">\n"}}]}`,
		valueDataDONE26000752,
		``,
	}, "\n\n")
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}

	err := consumeOpenAIGenerateStream(EndpointChatCompletions, AdapterOpenAIChatCompletions, strings.NewReader(rawStream), result, nil, true)
	if !errors.Is(err, errDeepSeekDSMLToolCallsIncomplete) {
		t.Fatalf("expected incomplete DSML error, got %v", err)
	}
	if result.Text != "" || len(result.ToolCalls) != 0 {
		t.Fatalf("expected incomplete DSML to stay out of output, text=%q toolCalls=%#v", result.Text, result.ToolCalls)
	}
}

func TestParseOpenAIGenerateOutputIncompleteDSMLToolCallsReturnsError(t *testing.T) {
	body := []byte(`{
		"id": "chatcmpl_1",
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "<｜DSML｜tool_calls>\n<｜DSML｜invoke name=\"searchGitHub\">"
			}
		}]
	}`)

	_, err := parseOpenAIGenerateOutput(EndpointChatCompletions, AdapterOpenAIChatCompletions, body, true)
	if !errors.Is(err, errDeepSeekDSMLToolCallsIncomplete) {
		t.Fatalf("expected incomplete DSML error, got %v", err)
	}
}

func TestConsumeChatStreamAngleBracketTextStillEmits(t *testing.T) {
	rawStream := strings.Join([]string{
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"<"}}]}`,
		`data: {"id":"chatcmpl_1","choices":[{"delta":{"content":"not-dsml> ok"}}]}`,
		valueDataDONE26000752,
		``,
	}, "\n\n")
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	var deltas []string

	err := consumeOpenAIGenerateStream(EndpointChatCompletions, AdapterOpenAIChatCompletions, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.Delta != "" {
			deltas = append(deltas, event.Delta)
		}
		return nil
	}, true)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "<not-dsml> ok" || result.Text != got {
		t.Fatalf("expected ordinary angle bracket text to stream, deltas=%#v text=%q", deltas, result.Text)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected no tool calls, got %#v", result.ToolCalls)
	}
}

func TestConsumeChatStreamErrorPayloadReturnsUpstreamError(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	stream := bytes.NewBufferString("data: {\"error\":{\"message\":\"Param Incorrect\",\"code\":400}}\n\n")

	err := consumeOpenAIGenerateStream(EndpointChatCompletions, AdapterOpenAIChatCompletions, stream, result, nil, false)
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected upstream error, got %T %v", err, err)
	}
	if upstreamErr.StatusCode != http.StatusBadRequest || upstreamErr.Message != "Param Incorrect" {
		t.Fatalf("unexpected upstream error: %#v", upstreamErr)
	}
	if upstreamErr.Body != `{"error":{"message":"Param Incorrect","code":400}}` {
		t.Fatalf("expected raw upstream error body, got %q", upstreamErr.Body)
	}
}

func TestProviderSpecificErrorsPreserveDebugSnapshots(t *testing.T) {
	debug := &UpstreamDebugSnapshot{
		Request: UpstreamDebugRequest{
			Method:  valuePOSTB8F6D005,
			Path:    "/v1/messages",
			Headers: map[string]string{"x-api-key": valueRedactedD1837277},
			Body:    `{"model":"claude-sonnet-4"}`,
		},
		Response: UpstreamDebugResponse{
			StatusCode: 400,
			Body:       `{"error":{"message":"invalid request"}}`,
		},
	}

	anthropicErr := parseAnthropicError(400, []byte(`{"error":{"message":"invalid request"}}`), debug)
	var upstreamErr *UpstreamError
	if !errors.As(anthropicErr, &upstreamErr) || upstreamErr.Debug == nil || upstreamErr.Debug.Request.Path != "/v1/messages" {
		t.Fatalf("expected anthropic debug snapshot, got %#v", anthropicErr)
	}

	geminiDebug := &UpstreamDebugSnapshot{
		Request: UpstreamDebugRequest{
			Method:  valuePOSTB8F6D005,
			Path:    "/v1beta/models/gemini-2.0-flash:generateContent",
			Headers: map[string]string{"x-goog-api-key": valueRedactedD1837277},
			Body:    `{"contents":[]}`,
		},
		Response: UpstreamDebugResponse{
			StatusCode: 400,
			Body:       `{"error":{"message":"bad request"}}`,
		},
	}
	geminiErr := parseGeminiError(400, []byte(`{"error":{"message":"bad request"}}`), geminiDebug)
	upstreamErr = nil
	if !errors.As(geminiErr, &upstreamErr) || upstreamErr.Debug == nil || upstreamErr.Debug.Request.Path != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Fatalf("expected gemini debug snapshot, got %#v", geminiErr)
	}
}

func TestAttachUpstreamDebugWrapsStreamErrors(t *testing.T) {
	debug := &UpstreamDebugSnapshot{
		Request: UpstreamDebugRequest{
			Method:  valuePOSTB8F6D005,
			Path:    testResponsesEndpointPath,
			Headers: map[string]string{"Authorization": valueRedactedD1837277},
			Body:    `{"model":"grok-4.3","input":[{"role":"user","content":[{"type":"input_text","text":"武汉天气"}]}],"tools":[{"type":"x_search"}],"stream":true}`,
		},
		Response: UpstreamDebugResponse{
			StatusCode: 400,
			Headers:    map[string]string{"Content-Type": "text/event-stream"},
			Body:       `{"type":"response.error","error":{"message":"Argument not supported: metadata","code":"bad_response_status_code"}}`,
		},
	}

	err := attachUpstreamDebug(errCategory696D869F02, debug)
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) {
		t.Fatalf("expected upstream error, got %T %v", err, err)
	}
	if upstreamErr.Debug == nil || upstreamErr.Debug.Request.Path != testResponsesEndpointPath {
		t.Fatalf("expected upstream request snapshot, got %#v", upstreamErr.Debug)
	}
	if upstreamErr.Debug.Request.Body == "" || upstreamErr.Debug.Response.Body == "" {
		t.Fatalf("expected complete request and response bodies, got %#v", upstreamErr.Debug)
	}
	if upstreamErr.StatusCode != http.StatusBadRequest || upstreamErr.Body == "" {
		t.Fatalf("expected response status/body to be preserved, got %#v", upstreamErr)
	}
}

func TestStreamDebugSnapshotPreservesRawSSEBody(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	rawStream := "event: response.error\ndata: {\"type\":\"response.error\",\"error\":{\"message\":\"Argument not supported: metadata\"}}\n\n"
	recorder := newUpstreamBodyRecorder(bytes.NewBufferString(rawStream))

	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterXAIResponses, recorder, result, nil, false)
	req, reqErr := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://api.x.ai/v1/responses", strings.NewReader(`{"model":"grok-4.3"}`))
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	debug := upstreamDebugSnapshot(
		req,
		[]byte(`{"model":"grok-4.3"}`),
		&http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}},
		streamErrorBody(recorder, err),
	)
	if debug.Response.Body != rawStream {
		t.Fatalf("expected raw SSE response body, got %q", debug.Response.Body)
	}
}

func TestResponsesStreamReasoningSummaryDeltaIsEmittedAndStored(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"先梳理问题"}`,
		``,
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"，再给出答案。"}`,
		``,
	}, "\n")

	reasoningText := ""
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.Reasoning != nil {
			reasoningText += event.Reasoning.Text
		}
		return nil
	}, false)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if reasoningText != "先梳理问题，再给出答案。" {
		t.Fatalf("expected reasoning deltas to be emitted, got %q", reasoningText)
	}
	if result.Reasoning == nil || result.Reasoning.Summary != reasoningText {
		t.Fatalf("expected stream reasoning summary to be stored, got %#v", result.Reasoning)
	}
}

func TestResponsesCompletedReasoningSummaryIsEmittedWhenNoDeltaArrived(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"id":"rs_1","type":"reasoning","status":"completed","summary":[{"type":"summary_text","text":"这是完整思考摘要。"}]},{"type":"message","content":[{"type":"output_text","text":"最终答案"}]}],"usage":{"input_tokens":1,"output_tokens":2}}}`,
		``,
	}, "\n")

	reasoningText := ""
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.Reasoning != nil {
			reasoningText += event.Reasoning.Text
		}
		return nil
	}, false)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if reasoningText != "这是完整思考摘要。" {
		t.Fatalf("expected completed reasoning summary to be emitted, got %q", reasoningText)
	}
	if result.Text != "最终答案" {
		t.Fatalf("expected completed response text, got %q", result.Text)
	}
	if result.Reasoning == nil || result.Reasoning.Summary != reasoningText {
		t.Fatalf("expected completed reasoning summary to be stored, got %#v", result.Reasoning)
	}
}

func TestResponsesStreamDoneEventsAreMergedWithoutDuplicateText(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"legacy-message","type":"message","content":[]}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		``,
		`event: response.output_text.done`,
		`data: {"type":"response.output_text.done","text":"Hello"}`,
		``,
		`event: response.reasoning_summary_text.done`,
		`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","text":"简要思考"}`,
		``,
	}, "\n")

	if err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, nil, false); err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if result.Text != "Hello" {
		t.Fatalf("expected output_text.done not to duplicate text, got %q", result.Text)
	}
	if result.Reasoning == nil || result.Reasoning.Summary != "简要思考" {
		t.Fatalf("expected reasoning done text to be stored, got %#v", result.Reasoning)
	}
}

func TestParseResponsesSeparatesFunctionCallsFromServerSideToolCalls(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"x_search_call","id":"xsc_1","status":"completed","action":{"query":"latest xAI news"},"results":[{"url":"https://x.ai/news","title":"xAI"}]},
			{"type":"function_call","call_id":"call_1","name":"memory.save","arguments":"{\"text\":\"remember\"}"},
			{"type":"message","content":[{"type":"output_text","text":"done"}]}
		],
		"usage": {
			"prompt_tokens": 11,
			"completion_tokens": 13,
			"prompt_tokens_details": {"cached_tokens": 5},
			"completion_tokens_details": {"reasoning_tokens": 7},
			"server_side_tool_usage": {"x_search": 1}
		}
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	assertResponsesFunctionAndServerToolSeparation(t, result)
}

func assertResponsesFunctionAndServerToolSeparation(t *testing.T, result *GenerateOutput) {
	t.Helper()
	requireCondition(t, len(result.ToolCalls) == 1 && result.ToolCalls[0].ToolName == valueMemorySaveE2BC22BD, "expected only function_call to require local execution, got %#v", result.ToolCalls)
	requireCondition(t, len(result.ServerToolCalls) == 1 && result.ServerToolCalls[0].ToolType == "x_search_call" && result.ServerToolCalls[0].ToolName == valueXSearchFB43F46C, "expected x_search_call as server-side tool trace, got %#v", result.ServerToolCalls)
	requireEqual(t, result.Text, "done", "expected text output, got %q", result.Text)
	requireCondition(t, result.Usage.InputTokens == 6 && result.Usage.OutputTokens == 6 && result.Usage.CacheReadTokens == 5 && result.Usage.ReasoningTokens == 7, "unexpected usage fallback parse: %+v", result.Usage)
	requireEqual(t, result.ServerSideToolUsage[valueXSearchFB43F46C], int64(1), "expected server side tool usage, got %#v", result.ServerSideToolUsage)
}

func TestParseResponsesCapturesServerSideToolCallOutputItems(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"x_search_call","id":"xsc_1","status":"completed","action":{"query":"latest xAI news","sources":[{"url":"https://x.ai/source"}]}},
			{"type":"x_search_call_output","call_id":"xsc_1","outputs":[{"url":"https://x.ai/news","title":"xAI News"}]}
		]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if len(result.ServerToolCalls) != 1 {
		t.Fatalf("expected merged server-side tool call, got %#v", result.ServerToolCalls)
	}
	call := result.ServerToolCalls[0]
	if call.ToolCallID != "xsc_1" || call.ToolType != "x_search_call_output" || call.ToolName != valueXSearchFB43F46C {
		t.Fatalf("unexpected server-side tool call identity: %#v", call)
	}
	if call.OutputJSON == "" || !strings.Contains(call.OutputJSON, "https://x.ai/news") {
		t.Fatalf("expected tool output item to be captured, got %#v", call.OutputJSON)
	}
	if len(result.Citations) != 2 || result.Citations[0] != "https://x.ai/source" || result.Citations[1] != "https://x.ai/news" {
		t.Fatalf("expected citations from action sources and output, got %#v", result.Citations)
	}
}

func TestParseResponsesCapturesSearchActionSourcesAsToolOutput(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"web_search_call","id":"wsc_1","status":"completed","action":{"type":"search","query":"today news","sources":[{"type":"url","url":"https://example.com/a"},{"type":"url","url":"https://example.com/b"}]}}
		]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if len(result.ServerToolCalls) != 1 {
		t.Fatalf("expected server-side web search call, got %#v", result.ServerToolCalls)
	}
	call := result.ServerToolCalls[0]
	if !strings.Contains(call.ArgumentsJSON, "today news") || strings.Contains(call.ArgumentsJSON, valueHttpsExampleComA59C4793C) {
		t.Fatalf("expected request action without sources, got %q", call.ArgumentsJSON)
	}
	if !strings.Contains(call.OutputJSON, valueHttpsExampleComA59C4793C) || !strings.Contains(call.OutputJSON, "today news") {
		t.Fatalf("expected action sources as tool output, got %q", call.OutputJSON)
	}
	if len(result.Citations) != 2 || result.Citations[0] != valueHttpsExampleComA59C4793C || result.Citations[1] != "https://example.com/b" {
		t.Fatalf("expected citations from action sources, got %#v", result.Citations)
	}
}

func TestParseResponsesCapturesTopLevelServerSideToolCalls(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"tool_calls": [
			{"type":"web_search_call","id":"wsc_1","status":"completed","action":{"query":"weather"},"results":[{"url":"https://example.com/weather"}]},
			{"type":"x_search_call","id":"xsc_1","status":"in_progress","action":{"query":"xAI"}},
			{"type":"code_interpreter_call","id":"cic_1","status":"completed","input":{"code":"print(1)"},"outputs":[{"type":"logs","logs":"1"}]}
		],
		"output": [
			{"type":"message","content":[{"type":"output_text","text":"done"}]}
		]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	assertTopLevelServerSideToolCalls(t, result)
}

func assertTopLevelServerSideToolCalls(t *testing.T, result *GenerateOutput) {
	t.Helper()
	requireCondition(t, len(result.ServerToolCalls) == 3, "expected top-level server-side tool calls, got %#v", result.ServerToolCalls)
	requireCondition(t, result.ServerToolCalls[0].ToolCallID == valueWsc10518242B && result.ServerToolCalls[0].OutputJSON != "", "expected web search call output to be captured, got %#v", result.ServerToolCalls[0])
	requireCondition(t, result.ServerToolCalls[1].ToolCallID == "xsc_1" && result.ServerToolCalls[1].Status == valueInProgress995727F7 && result.ServerToolCalls[1].ArgumentsJSON != "", "expected x search call status and action to be captured, got %#v", result.ServerToolCalls[1])
	requireCondition(t, result.ServerToolCalls[2].ToolCallID == "cic_1" && result.ServerToolCalls[2].OutputJSON != "", "expected code interpreter output to be captured, got %#v", result.ServerToolCalls[2])
	requireCondition(t, len(result.Citations) == 1 && result.Citations[0] == "https://example.com/weather", "expected citations from top-level server-side tool calls, got %#v", result.Citations)
}

func TestParseResponsesCapturesOpenAINativeShellAndImageTools(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"shell_call","id":"sh_1","status":"completed","input":{"cmd":"ls -la"},"output":{"stdout":"total 0"}},
			{"type":"image_generation_call","id":"img_1","status":"completed","output_format":"png","result":"aW1hZ2U="}
		],
		"usage": {
			"server_side_tool_usage_details": {"shell_calls":1,"image_generation_calls":1}
		}
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	assertOpenAINativeShellAndImageToolTrace(t, result)
}

func assertOpenAINativeShellAndImageToolTrace(t *testing.T, result *GenerateOutput) {
	t.Helper()
	requireCondition(t, len(result.ServerToolCalls) == 2, "expected shell and image generation traces, got %#v", result.ServerToolCalls)
	requireCondition(t, result.ServerToolCalls[0].ToolName == valueShellAD011B52 && strings.Contains(result.ServerToolCalls[0].ArgumentsJSON, "ls -la") && strings.Contains(result.ServerToolCalls[0].OutputJSON, "total 0"), "expected shell input/output to be captured, got %#v", result.ServerToolCalls[0])
	requireCondition(t, result.ServerToolCalls[1].ToolName == valueImageGenerationA59A06EF && strings.Contains(result.ServerToolCalls[1].OutputJSON, "aW1hZ2U="), "expected image generation output to be captured, got %#v", result.ServerToolCalls[1])
	requireCondition(t, result.ServerSideToolUsage[valueShellAD011B52] == 1 && result.ServerSideToolUsage[valueImageGenerationA59A06EF] == 1, "expected native tool usage to be normalized, got %#v", result.ServerSideToolUsage)
	requireCondition(t, len(result.GeneratedImages) == 1 && result.GeneratedImages[0].B64JSON == "aW1hZ2U=" && result.GeneratedImages[0].MIMEType == valueImagePng415383B9, "expected hosted image payload to be normalized outside text, got %#v", result.GeneratedImages)
}

func TestParseResponsesTreatsXSearchCustomCallsAsServerSide(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"custom_tool_call","id":"ctc_1","call_id":"xs_0","name":"x_keyword_search","status":"completed","input":"{\"query\":\"since:2026-05-11 news\",\"limit\":\"5\",\"mode\":\"Latest\"}"},
			{"type":"custom_tool_call","id":"ctc_2","call_id":"xs_1","name":"x_semantic_search","status":"completed","input":"{\"query\":\"today's news\"}"}
		],
		"usage": {
			"server_side_tool_usage_details": {"web_search_calls":0,"x_search_calls":2,"code_interpreter_calls":0}
		}
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected x_search native custom calls not to require local execution, got %#v", result.ToolCalls)
	}
	if len(result.ServerToolCalls) != 2 {
		t.Fatalf("expected x_search native custom calls as server-side tools, got %#v", result.ServerToolCalls)
	}
	if result.ServerToolCalls[0].ToolName != valueXKeywordSearchD14FC105 || !strings.Contains(result.ServerToolCalls[0].ArgumentsJSON, "since:2026-05-11") {
		t.Fatalf("expected x_keyword_search input, got %#v", result.ServerToolCalls[0])
	}
	if result.ServerSideToolUsage[valueXSearchFB43F46C] != 2 {
		t.Fatalf("expected normalized x_search usage details, got %#v", result.ServerSideToolUsage)
	}
}

func TestParseResponsesCapturesMixedWebAndXSearchToolShapes(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"today news","sources":[{"type":"url","url":"https://example.com/news"}]}},
			{"id":"ws_2","type":"web_search_call","status":"completed","action":{"type":"open_page","url":"https://example.com/news"}},
			{"id":"ctc_1","call_id":"xs_0","type":"custom_tool_call","name":"x_keyword_search","status":"completed","input":"{\"query\":\"today news\",\"limit\":\"5\",\"mode\":\"Latest\"}"}
		],
		"usage": {
			"server_side_tool_usage_details": {"web_search_calls":2,"x_search_calls":1}
		}
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	assertMixedWebAndXSearchToolShapes(t, result)
}

func assertMixedWebAndXSearchToolShapes(t *testing.T, result *GenerateOutput) {
	t.Helper()
	requireCondition(t, len(result.ToolCalls) == 0, "expected no local tool calls for native search tools, got %#v", result.ToolCalls)
	requireCondition(t, len(result.ServerToolCalls) == 3, "expected web search, open page, and x search traces, got %#v", result.ServerToolCalls)
	requireCondition(t, result.ServerToolCalls[0].ToolName == valueWebSearch6C1E23E9 && strings.Contains(result.ServerToolCalls[0].OutputJSON, "https://example.com/news"), "expected web search sources as output, got %#v", result.ServerToolCalls[0])
	requireCondition(t, result.ServerToolCalls[1].ToolName == valueWebSearch6C1E23E9 && strings.Contains(result.ServerToolCalls[1].ArgumentsJSON, "open_page"), "expected open_page action as web search request, got %#v", result.ServerToolCalls[1])
	requireCondition(t, result.ServerToolCalls[2].ToolName == valueXKeywordSearchD14FC105 && result.ServerToolCalls[2].ToolCallID == "ctc_1", "expected xAI x_search custom item id to be preserved, got %#v", result.ServerToolCalls[2])
	requireCondition(t, result.ServerSideToolUsage[valueWebSearch6C1E23E9] == 2 && result.ServerSideToolUsage[valueXSearchFB43F46C] == 1, "expected mixed native tool usage to be normalized, got %#v", result.ServerSideToolUsage)
}

func TestParseXAIResponsesNormalizesBillableNativeToolUsage(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"usage": {
			"server_side_tool_usage_details": {
				"web_search_calls": 1,
				"x_search_calls": 2,
				"code_interpreter_calls": 3,
				"file_attachment_search_calls": 4,
				"collection_search_calls": 5
			}
		}
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if result.ServerSideToolUsage[valueWebSearch6C1E23E9] != 1 ||
		result.ServerSideToolUsage[valueXSearchFB43F46C] != 2 ||
		result.ServerSideToolUsage[valueCodeInterpreterDCAD241D] != 3 ||
		result.ServerSideToolUsage["attachment_search"] != 4 ||
		result.ServerSideToolUsage["collections_search"] != 5 {
		t.Fatalf("expected xAI billable native tool usage to be normalized, got %#v", result.ServerSideToolUsage)
	}
}

func TestParseResponsesKeepsClientToolCallsGeneric(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"custom_tool_call","call_id":"ctc_1","name":"browser.open","input":{"url":"https://example.com"}},
			{"type":"project_tool_call","call_id":"ptc_1","name":"workspace.lookup","input":{"query":"readme"}}
		],
		"tool_calls": [
			{"type":"custom_tool_call","call_id":"ctc_2","name":"memory.save","input":{"text":"remember"}}
		]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if len(result.ServerToolCalls) != 0 {
		t.Fatalf("expected no server-side tool calls for client tools, got %#v", result.ServerToolCalls)
	}
	if len(result.ToolCalls) != 3 {
		t.Fatalf("expected generic client tool calls, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].ToolName != "browser.open" || result.ToolCalls[0].ArgumentsJSON == "" {
		t.Fatalf("expected custom tool call input to be captured, got %#v", result.ToolCalls[0])
	}
	if result.ToolCalls[1].ToolType != "project_tool_call" || result.ToolCalls[1].ToolName != "workspace.lookup" {
		t.Fatalf("expected unknown client tool call type to stay generic, got %#v", result.ToolCalls[1])
	}
	if result.ToolCalls[2].ToolCallID != "ctc_2" || result.ToolCalls[2].ToolName != valueMemorySaveE2BC22BD {
		t.Fatalf("expected top-level client tool call to be captured, got %#v", result.ToolCalls[2])
	}
}

func TestParseResponsesCitationsFromOutputAnnotations(t *testing.T) {
	payload := mustDecodeObject(t, `{
		"id": "resp_1",
		"output": [
			{"type":"message","content":[{"type":"output_text","text":"source","annotations":[{"type":"url_citation","url":"https://example.com/a"}]}]},
			{"type":"web_search_call","id":"wsc_1","status":"completed","results":[{"url":"https://example.com/b"}]}
		]
	}`)

	result, err := buildGenerateOutputFromParsed(EndpointResponses, payload)
	require.NoError(t, err)
	if len(result.Citations) != 2 || result.Citations[0] != valueHttpsExampleComA59C4793C || result.Citations[1] != "https://example.com/b" {
		t.Fatalf("expected citations from annotations and server tool results, got %#v", result.Citations)
	}
}

func TestResponsesOutputItemDoneCapturesServerSideToolCall(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"wsc_1","status":"completed","action":{"query":"weather"},"sources":[{"url":"https://example.com/weather"}]}}`,
		``,
	}, "\n")

	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, nil, false)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected no local tool calls, got %#v", result.ToolCalls)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].ToolType != "web_search_call" {
		t.Fatalf("expected server-side web search call, got %#v", result.ServerToolCalls)
	}
	if len(result.Citations) != 1 || result.Citations[0] != "https://example.com/weather" {
		t.Fatalf("expected citations from server-side tool, got %#v", result.Citations)
	}
}

func TestResponsesStreamEmitsServerSideToolStatusEvents(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.web_search_call.in_progress`,
		`data: {"type":"response.web_search_call.in_progress","item_id":"wsc_1"}`,
		``,
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"wsc_1","status":"completed","action":{"query":"weather"},"sources":[{"url":"https://example.com/weather"}]}}`,
		``,
	}, "\n")

	statuses := make([]string, 0)
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			statuses = append(statuses, event.ServerToolCall.Status)
		}
		return nil
	}, false)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != valueInProgress995727F7 || statuses[1] != valueCompleted5BC9F232 {
		t.Fatalf("expected streamed server tool statuses, got %#v", statuses)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].Status != valueCompleted5BC9F232 {
		t.Fatalf("expected final merged server tool call, got %#v", result.ServerToolCalls)
	}
}

func TestResponsesServerToolFinalItemReplacesStreamingPlaceholder(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.web_search_call.searching`,
		`data: {"type":"response.web_search_call.searching","action":{"type":"search","query":""}}`,
		``,
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","id":"wsc_1","status":"completed","action":{"type":"search","query":"今日新闻","sources":[{"url":"https://example.com/news"}]}}}`,
		``,
	}, "\n")

	if err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, nil, false); err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if len(result.ServerToolCalls) != 1 {
		t.Fatalf("expected final item to replace streaming placeholder, got %#v", result.ServerToolCalls)
	}
	call := result.ServerToolCalls[0]
	if call.ToolCallID != valueWsc10518242B || call.Status != valueCompleted5BC9F232 || !strings.Contains(call.ArgumentsJSON, "今日新闻") {
		t.Fatalf("unexpected merged server tool call: %#v", call)
	}
}

func TestResponsesStreamStatusEventCapturesNestedServerToolItem(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		`event: response.web_search_call.searching`,
		`data: {"type":"response.web_search_call.searching","item":{"id":"wsc_1","action":{"query":"weather"},"sources":[{"url":"https://example.com/source"}]}}`,
		``,
	}, "\n")

	var streamed *ToolCall
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			value := *event.ServerToolCall
			streamed = &value
		}
		return nil
	}, false)
	if err != nil {
		t.Fatalf("consume stream: %v", err)
	}
	if streamed == nil || streamed.ToolCallID != valueWsc10518242B || streamed.Status != valueSearching96F7C642 || streamed.ArgumentsJSON == "" || streamed.OutputJSON == "" {
		t.Fatalf("expected nested status event payload to be captured, got %#v", streamed)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].ToolCallID != valueWsc10518242B {
		t.Fatalf("expected merged server tool call, got %#v", result.ServerToolCalls)
	}
}

func TestResponsesStreamUsesOutputIndexForIdlessParallelServerToolIdentity(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		eventResponseOutputItemAdded,
		`data: {"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress","action":{"type":"search","query":"alpha"}},"output_index":1}`,
		``,
		eventResponseOutputItemAdded,
		`data: {"type":"response.output_item.added","item":{"type":"web_search_call","status":"in_progress","action":{"type":"search","query":"beta"}},"output_index":2}`,
		``,
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"alpha"}},"output_index":1}`,
		``,
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"beta"}},"output_index":2}`,
		``,
	}, "\n")

	events := make([]ToolCall, 0, 4)
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterOpenAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	}, false)
	requireNoError(t, err, "consume stream: %v")
	if len(result.ServerToolCalls) != 2 || len(events) != 4 {
		t.Fatalf("idless server Tool lifecycles collapsed: result=%#v events=%#v", result.ServerToolCalls, events)
	}
	firstID := "gaoge_server_tool:web_search_call:1"
	secondID := "gaoge_server_tool:web_search_call:2"
	if result.ServerToolCalls[0].ToolCallID != firstID || result.ServerToolCalls[1].ToolCallID != secondID ||
		events[0].ToolCallID != firstID || events[2].ToolCallID != firstID ||
		events[1].ToolCallID != secondID || events[3].ToolCallID != secondID {
		t.Fatalf("unstable output-index Tool identities: result=%#v events=%#v", result.ServerToolCalls, events)
	}
}

func TestResponsesStreamCapturesXSearchCustomToolInput(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		eventResponseOutputItemAdded,
		`data: {"type":"response.output_item.added","item":{"id":"ctc_1","call_id":"xs_0","input":"","name":"x_keyword_search","type":"custom_tool_call","status":"in_progress"},"output_index":1}`,
		``,
		`event: response.custom_tool_call_input.delta`,
		`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_1","delta":"{\"query\":\"news\""}`,
		``,
		`event: response.custom_tool_call_input.done`,
		`data: {"type":"response.custom_tool_call_input.done","item_id":"ctc_1","input":"{\"query\":\"news\",\"limit\":\"5\"}"}`,
		``,
		valueEventResponseOutputItemDone3451AA40,
		`data: {"type":"response.output_item.done","item":{"id":"ctc_1","call_id":"xs_0","input":"{\"query\":\"news\",\"limit\":\"5\"}","name":"x_keyword_search","type":"custom_tool_call","status":"completed"},"output_index":1}`,
		``,
	}, "\n")

	events := make([]ToolCall, 0)
	err := consumeOpenAIGenerateStream(EndpointResponses, AdapterXAIResponses, strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	}, false)
	requireNoError(t, err, "consume stream: %v")
	assertResponsesStreamXSearchCustomInput(t, result, events)
}

func TestResponsesStreamCapturesFunctionCallArgumentsWithoutFinalItem(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		eventResponseOutputItemAdded,
		`data: {"type":"response.output_item.added","item":{"id":"fc_1","call_id":"call_1","arguments":"","name":"fixture_publish_result","type":"function_call","status":"in_progress"},"output_index":1}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"title\":\"第3—4集\""}`,
		``,
		`event: response.function_call_arguments.done`,
		`data: {"type":"response.function_call_arguments.done","item_id":"fc_1","arguments":"{\"title\":\"第3—4集\",\"operations\":[]}"}`,
		``,
	}, "\n")

	err := consumeOpenAIGenerateStream(
		EndpointResponses,
		AdapterOpenAIResponses,
		strings.NewReader(rawStream),
		result,
		nil,
		false,
	)
	requireNoError(t, err, "consume stream: %v")
	requireCondition(t, len(result.ToolCalls) == 1, "expected one local tool call, got %#v", result.ToolCalls)
	requireCondition(t, result.ToolCalls[0].ToolCallID == "call_1", "expected provider call ID, got %#v", result.ToolCalls[0])
	requireCondition(t, json.Valid([]byte(result.ToolCalls[0].ArgumentsJSON)), "expected valid arguments, got %q", result.ToolCalls[0].ArgumentsJSON)
	requireCondition(t, strings.Contains(result.ToolCalls[0].ArgumentsJSON, `"operations":[]`), "expected final arguments, got %q", result.ToolCalls[0].ArgumentsJSON)
}

func TestResponsesStreamErrorTreatsSuccessStatusAsBadGateway(t *testing.T) {
	err := parseResponsesStreamErrorEvent(map[string]interface{}{
		valueType945D3026: "response.failed",
		valueResponse6042335E: map[string]interface{}{
			"error": map[string]interface{}{
				"status":  http.StatusOK,
				"message": "upstream unavailable",
			},
		},
	}, "")
	var upstream *UpstreamError
	requireCondition(t, errors.As(err, &upstream), "expected upstream error, got %v", err)
	requireCondition(t, upstream.StatusCode == http.StatusBadGateway, "status = %d, want %d", upstream.StatusCode, http.StatusBadGateway)
}

func assertResponsesStreamXSearchCustomInput(t *testing.T, result *GenerateOutput, events []ToolCall) {
	t.Helper()
	requireCondition(t, len(result.ToolCalls) == 0, "expected no local tool calls, got %#v", result.ToolCalls)
	requireCondition(t, len(result.ServerToolCalls) == 1 && result.ServerToolCalls[0].ToolName == valueXKeywordSearchD14FC105 && strings.Contains(result.ServerToolCalls[0].ArgumentsJSON, `"limit":"5"`), "expected streamed x_search custom input, got %#v", result.ServerToolCalls)
	requireCondition(t, len(events) >= 2, "expected streamed server tool events, got %#v", events)
	requireCondition(t, hasXSearchInputDoneEvent(events), "expected custom tool input done event to update the existing server tool call, got %#v", events)
}

func hasXSearchInputDoneEvent(events []ToolCall) bool {
	for _, event := range events {
		if event.ToolCallID == "ctc_1" && event.ToolName == valueXKeywordSearchD14FC105 && strings.Contains(event.ArgumentsJSON, `"limit":"5"`) {
			return true
		}
	}
	return false
}

func TestBuildAnthropicToolBlocks(t *testing.T) {
	content := buildAnthropicContent(Message{
		Role:        valueToolDCEAC0D9,
		Content:     "continue after the tool result",
		ToolResults: []ToolResult{{ToolCallID: valueToolu1DA612952, ToolName: valueMemoryList83D7D805, OutputJSON: valueItems8677E4B3, Status: valueSuccess1E495DA3}},
	})

	blocks := requireMapSlice(t, content, "anthropic content blocks")
	if len(blocks) != 2 || blocks[0][valueTypeD3C0BBD2] != "tool_result" ||
		blocks[0]["tool_use_id"] != valueToolu1DA612952 ||
		blocks[1][valueTypeD3C0BBD2] != valueText53ED4E15 {
		t.Fatalf("expected anthropic tool_result block, got %#v", blocks)
	}
}

func TestAnthropicStreamToolUseInputJSONDeltaIsCaptured(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"memory.save","input":{}}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"text\":\"hello"}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" world\"}"}}`,
		``,
		valueEventContentBlockStop71FF8CB0,
		miniMaxFixtureContentBlockStopOne,
		``,
		valueEventMessageStopE2A0F595,
		valueDataTypeMessageStop66811ABD,
		``,
	}, "\n")

	if err := consumeAnthropicStream(strings.NewReader(rawStream), result, nil, anthropicToolClassifier{}); err != nil {
		t.Fatalf("consume anthropic stream: %v", err)
	}
	compactAnthropicStreamToolCalls(result)
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %#v", result.ToolCalls)
	}
	if result.ToolCalls[0].ToolCallID != valueToolu1DA612952 || result.ToolCalls[0].ToolName != valueMemorySaveE2BC22BD || result.ToolCalls[0].ArgumentsJSON != `{"text":"hello world"}` {
		t.Fatalf("unexpected anthropic stream tool call: %#v", result.ToolCalls[0])
	}
}

func TestAnthropicStreamRejectsEOFBeforeMessageStop(t *testing.T) {
	result := &GenerateOutput{}
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"title\":\"unfinished"}}`,
		``,
	}, "\n")

	err := consumeAnthropicStream(strings.NewReader(rawStream), result, nil, anthropicToolClassifier{})
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) || !strings.Contains(upstreamErr.Message, "before message_stop") {
		t.Fatalf("expected incomplete stream error, got %T %v", err, err)
	}
}

func TestAnthropicStreamReportsMaxTokensTruncation(t *testing.T) {
	result := &GenerateOutput{}
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":8192}}`,
		``,
		valueEventMessageStopE2A0F595,
		valueDataTypeMessageStop66811ABD,
		``,
	}, "\n")

	err := consumeAnthropicStream(strings.NewReader(rawStream), result, nil, anthropicToolClassifier{})
	var upstreamErr *UpstreamError
	if !errors.As(err, &upstreamErr) || !strings.Contains(upstreamErr.Message, "stop_reason=max_tokens") {
		t.Fatalf("expected max_tokens truncation error, got %T %v", err, err)
	}
	if result.StopReason != valueMaxTokens8DDF2E0E || result.Usage.OutputTokens != 8192 {
		t.Fatalf("unexpected completion metadata: stop=%q usage=%+v", result.StopReason, result.Usage)
	}
}

func TestAnthropicStreamRejectsNativeToolUseReturnedAsClientTool(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4-6", GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: "北京今天天气"}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch2026020902F6160F},
			},
		},
	}, true)
	classifier := newAnthropicToolClassifier(payload, nil)
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	events := make([]ToolCall, 0)
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"web_search","input":{"query":"北京天气"}}}`,
		``,
	}, "\n")

	err := consumeAnthropicStream(strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	}, classifier)
	assertAnthropicNativeToolMismatch(t, err, result, events)
}

func assertAnthropicNativeToolMismatch(t *testing.T, err error, result *GenerateOutput, events []ToolCall) {
	t.Helper()
	requireCondition(t, err != nil, "expected native tool mismatch error")
	var upstreamErr *UpstreamError
	requireCondition(t, errors.As(err, &upstreamErr), "expected upstream error, got %T %v", err, err)
	requireCondition(t, strings.Contains(upstreamErr.Message, "client-side tool call"), "expected clear native tool error, got %q", upstreamErr.Message)
	requireCondition(t, len(result.ToolCalls) == 0, "expected no local tool calls, got %#v", result.ToolCalls)
	requireCondition(t, len(result.ServerToolCalls) == 1 && result.ServerToolCalls[0].Status == valueError4D50CFD6 && result.ServerToolCalls[0].ToolName == valueWebSearch6C1E23E9, "expected failed server-side tool trace, got %#v", result.ServerToolCalls)
	requireCondition(t, len(events) == 1 && events[0].Status == valueError4D50CFD6, "expected one error trace event, got %#v", events)
}

func TestAnthropicStreamKeepsDeclaredClientToolWithNativeNameLocal(t *testing.T) {
	payload := mustBuildAnthropicRequestBody(t, "claude-sonnet-4-6", GenerateInput{
		Messages: []Message{{Role: valueUser07DDCD27, Content: valueSearch6C0C7288}},
		Options: map[string]interface{}{
			valueToolsE04282FD: []interface{}{
				map[string]interface{}{valueTypeD3C0BBD2: valueWebSearch2026020902F6160F},
			},
		},
		Tools: []ToolDefinition{{Name: valueWebSearch6C1E23E9, Description: "Local search", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, true)
	classifier := newAnthropicToolClassifier(payload, []ToolDefinition{{Name: valueWebSearch6C1E23E9}})
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"web_search","input":{}}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"weather\"}"}}`,
		``,
		valueEventContentBlockStop71FF8CB0,
		valueDataTypeContentBlockStopIndex0908D309E,
		``,
		valueEventMessageStopE2A0F595,
		valueDataTypeMessageStop66811ABD,
		``,
	}, "\n")

	if err := consumeAnthropicStream(strings.NewReader(rawStream), result, nil, classifier); err != nil {
		t.Fatalf("consume anthropic stream: %v", err)
	}
	compactAnthropicStreamToolCalls(result)
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ToolName != valueWebSearch6C1E23E9 {
		t.Fatalf("expected declared client tool call, got %#v", result.ToolCalls)
	}
	if len(result.ServerToolCalls) != 0 {
		t.Fatalf("expected no server-side tool calls, got %#v", result.ServerToolCalls)
	}
}

func TestAnthropicStreamServerToolUseIsNotLocalToolCall(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"weather\"}"}}`,
		``,
		valueEventContentBlockStop71FF8CB0,
		valueDataTypeContentBlockStopIndex0908D309E,
		``,
		valueEventMessageStopE2A0F595,
		valueDataTypeMessageStop66811ABD,
		``,
	}, "\n")

	if err := consumeAnthropicStream(strings.NewReader(rawStream), result, nil, anthropicToolClassifier{}); err != nil {
		t.Fatalf("consume anthropic stream: %v", err)
	}
	compactAnthropicStreamToolCalls(result)
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected no local tool calls, got %#v", result.ToolCalls)
	}
	if len(result.ServerToolCalls) != 1 || result.ServerToolCalls[0].ToolName != valueWebSearch6C1E23E9 || result.ServerToolCalls[0].Status != valueInProgressFCF26CA0 {
		t.Fatalf("expected pending server-side tool call without a result, got %#v", result.ServerToolCalls)
	}
}

func TestAnthropicStreamMergesServerToolResultIntoToolCall(t *testing.T) {
	result := &GenerateOutput{ToolCalls: make([]ToolCall, 0), ServerToolCalls: make([]ToolCall, 0)}
	events := make([]ToolCall, 0)
	rawStream := strings.Join([]string{
		valueEventMessageStartDF12B601,
		valueDataTypeMessageStartMessageIdMsgD09F9B26,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}}`,
		``,
		valueEventContentBlockDeltaA91AB1F9,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"北京天气今天 2026\"}"}}`,
		``,
		valueEventContentBlockStop71FF8CB0,
		valueDataTypeContentBlockStopIndex0908D309E,
		``,
		valueEventContentBlockStart415B336C,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","title":"北京天气","url":"https://example.com/weather","page_age":"2026-05-24","encrypted_content":"redacted"}]}}`,
		``,
		valueEventContentBlockStop71FF8CB0,
		miniMaxFixtureContentBlockStopOne,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","usage":{"output_tokens":20,"server_tool_use":{"web_search_requests":1}}}`,
		``,
		valueEventMessageStopE2A0F595,
		valueDataTypeMessageStop66811ABD,
		``,
	}, "\n")

	if err := consumeAnthropicStream(strings.NewReader(rawStream), result, func(event GenerateStreamEvent) error {
		if event.ServerToolCall != nil {
			events = append(events, *event.ServerToolCall)
		}
		return nil
	}, anthropicToolClassifier{}); err != nil {
		t.Fatalf("consume anthropic stream: %v", err)
	}
	compactAnthropicStreamToolCalls(result)
	assertAnthropicServerToolResultMerge(t, result, events)
}

func assertAnthropicServerToolResultMerge(t *testing.T, result *GenerateOutput, events []ToolCall) {
	t.Helper()
	requireCondition(t, len(result.ToolCalls) == 0, "expected no local tool calls, got %#v", result.ToolCalls)
	requireCondition(t, len(result.ServerToolCalls) == 1, "expected one server-side tool call, got %#v", result.ServerToolCalls)
	call := result.ServerToolCalls[0]
	requireCondition(t, call.ToolName == valueWebSearch6C1E23E9 && call.Status == valueCompleted5BC9F232, "expected completed web search call, got %#v", call)
	requireCondition(t, strings.Contains(call.OutputJSON, "https://example.com/weather") && strings.Contains(call.OutputJSON, "北京天气"), "expected search result output to be captured, got %q", call.OutputJSON)
	requireCondition(t, !strings.Contains(call.OutputJSON, "encrypted_content") && !strings.Contains(call.OutputJSON, "redacted"), "expected opaque search result fields to be removed, got %q", call.OutputJSON)
	requireEqual(t, result.ServerSideToolUsage[valueWebSearch6C1E23E9], int64(1), "expected anthropic server-side tool usage, got %#v", result.ServerSideToolUsage)
	requireCondition(t, len(events) >= 2 && events[len(events)-1].OutputJSON != "", "expected streaming result event with output, got %#v", events)
}

var (
	errCategory696D869F02Cause = errors.New("argument not supported")
	errCategory696D869F02      = withMessage(errCategory696D869F02Cause, "Argument not supported: metadata")
)
