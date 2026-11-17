package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testInteractionToolName = "lookup"
	testValueMedium         = "medium"
)

func TestBuildGeminiInteractionsRequestBodyUsesNativeV1Shape(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	payload, err := buildGeminiInteractionsRequestBody("models/gemini-3.7-flash", GenerateInput{
		Instructions:       "Follow the system contract.",
		PreviousResponseID: "it_previous",
		Messages: []Message{
			{Role: valueSystemE9E56F73, Content: "Prefer concise answers."},
			{Role: valueUser72795C11, Content: "Search for the answer."},
		},
		Tools:      []ToolDefinition{{Name: testInteractionToolName, Description: "Look up data", InputSchema: schema}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired, AllowedToolNames: []string{testInteractionToolName}},
		Options: map[string]interface{}{
			interactionKeyGenerationConfig: map[string]interface{}{
				"max_output_tokens": 4096,
				"thinking_level":    testValueMedium,
			},
			interactionKeyTools: []interface{}{map[string]interface{}{interactionKeyType: interactionTypeGoogleSearch}},
		},
	}, true)
	require.NoError(t, err)
	require.Equal(t, "gemini-3.7-flash", payload[interactionKeyModel])
	require.Equal(t, true, payload[interactionKeyStream])
	require.Equal(t, false, payload[interactionKeyStore])
	require.NotContains(t, payload, interactionKeyPreviousInteractionID)
	require.Equal(t, "Follow the system contract.\n\nPrefer concise answers.", payload[interactionKeySystemInstruction])
	input := interactionMapSlice(payload[interactionKeyInput])
	require.Len(t, input, 1)
	require.Equal(t, interactionTypeUserInput, getString(asMap(input[0])[interactionKeyType]))
	generationConfig := asMap(payload[interactionKeyGenerationConfig])
	require.Equal(t, 4096, generationConfig["max_output_tokens"])
	require.Equal(t, testValueMedium, generationConfig["thinking_level"])
	choice := asMap(generationConfig[interactionKeyToolChoice])
	allowed := asMap(choice[interactionKeyAllowedTools])
	require.Equal(t, interactionToolChoiceAny, allowed[interactionKeyMode])
	tools := interactionMapSlice(payload[interactionKeyTools])
	require.Len(t, tools, 2)
	require.Equal(t, interactionTypeGoogleSearch, getString(asMap(tools[0])[interactionKeyType]))
	require.Equal(t, interactionTypeFunction, getString(asMap(tools[1])[interactionKeyType]))
	function := asMap(tools[1])
	require.Equal(t, testInteractionToolName, function[interactionKeyName])
	require.Equal(t, "object", asMap(function[interactionKeyParameters])[interactionKeyType])
}

func TestBuildGeminiInteractionsHistoryPreservesFunctionCallSignature(t *testing.T) {
	payload, err := buildGeminiInteractionsRequestBody("gemini-3.7-flash", GenerateInput{
		Messages: []Message{{
			Role: valueAssistant6B7057DB,
			ToolCalls: []ToolCall{{
				ToolCallID: "fc_history", ToolName: testInteractionToolName, ArgumentsJSON: `{"query":"x"}`, ThoughtSignature: "sig_history",
			}},
		}},
	}, false)
	require.NoError(t, err)
	steps := interactionMapSlice(payload[interactionKeyInput])
	require.Len(t, steps, 1)
	step := asMap(steps[0])
	require.Equal(t, interactionTypeFunctionCall, step[interactionKeyType])
	require.Equal(t, "sig_history", step[interactionKeySignature])
}

func TestBuildGeminiInteractionsURLNormalizesVersionedBases(t *testing.T) {
	t.Parallel()
	for _, base := range []string{
		"https://generativelanguage.googleapis.com",
		"https://generativelanguage.googleapis.com/v1",
		"https://generativelanguage.googleapis.com/v1beta",
		"https://generativelanguage.googleapis.com/v1beta2/",
	} {
		if got := buildGeminiInteractionsURL(base); got != "https://generativelanguage.googleapis.com/v1/interactions" {
			t.Fatalf("buildGeminiInteractionsURL(%q) = %q", base, got)
		}
	}
}

func interactionMapSlice(raw interface{}) []interface{} {
	switch typed := raw.(type) {
	case []interface{}:
		return typed
	case []map[string]interface{}:
		result := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func TestParseGeminiInteractionPreservesReasoningToolsCitationsAndUsage(t *testing.T) {
	body := []byte(`{
      "id":"it_123",
      "status":"completed",
      "steps":[
        {"type":"model_output","content":[{"type":"text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.com/source"}]}]},
        {"type":"thought","signature":"sig_1","summary":[{"type":"text","text":"reasoning"}]},
        {"type":"function_call","id":"fc_1","name":"lookup","arguments":{"query":"x"},"signature":"sig_fc","status":"completed"},
        {"type":"google_search_call","id":"gs_1","arguments":{"queries":["current weather"]},"signature":"sig_search"},
        {"type":"google_search_result","call_id":"gs_1","result":[{"search_suggestions":"weather result"}],"signature":"sig_search"}
      ],
      "usage":{"total_input_tokens":10,"total_output_tokens":20,"total_cached_tokens":3,"total_thought_tokens":4}
    }`)
	output, err := parseGeminiInteraction(body)
	require.NoError(t, err)
	require.Equal(t, "it_123", output.ResponseID)
	require.Equal(t, "answer", output.Text)
	require.Equal(t, valueCompleted08D3ABC9, output.StopReason)
	require.NotNil(t, output.Reasoning)
	require.Equal(t, "reasoning", output.Reasoning.Text)
	require.Equal(t, "sig_1", output.Reasoning.Signature)
	require.Len(t, output.ToolCalls, 1)
	require.Equal(t, testInteractionToolName, output.ToolCalls[0].ToolName)
	require.Contains(t, output.ToolCalls[0].ArgumentsJSON, `"query":"x"`)
	require.Equal(t, "sig_fc", output.ToolCalls[0].ThoughtSignature)
	require.Len(t, output.ServerToolCalls, 1)
	require.Equal(t, interactionTypeGoogleSearch, output.ServerToolCalls[0].ToolName)
	require.Equal(t, valueCompleted9E9778C8, output.ServerToolCalls[0].Status)
	require.JSONEq(t, `{"queries":["current weather"]}`, output.ServerToolCalls[0].ArgumentsJSON)
	require.JSONEq(t, `[{"search_suggestions":"weather result"}]`, output.ServerToolCalls[0].OutputJSON)
	require.Equal(t, int64(1), output.ServerSideToolUsage[interactionUsageGoogleSearch])
	require.Equal(t, []string{"https://example.com/source"}, output.Citations)
	require.Equal(t, int64(10), output.Usage.InputTokens)
	require.Equal(t, int64(20), output.Usage.OutputTokens)
	require.Equal(t, int64(3), output.Usage.CacheReadTokens)
	require.Equal(t, int64(4), output.Usage.ReasoningTokens)
	replay := decodeProviderReplayItems(output.ReplayStateJSON, AdapterGoogleInteractions)
	require.Len(t, replay, 5)
	require.Equal(t, "sig_1", getString(replay[1][interactionKeySignature]))
	require.Equal(t, "sig_fc", getString(replay[2][interactionKeySignature]))
	replayed, err := buildGeminiInteractionsRequestBody("gemini-3.7-flash", GenerateInput{
		Messages: []Message{{Role: valueAssistant6B7057DB, ReplayStateJSON: output.ReplayStateJSON}},
	}, false)
	require.NoError(t, err)
	require.Equal(t, false, replayed[interactionKeyStore])
	require.Equal(t, replay, interactionMapSliceAsMaps(replayed[interactionKeyInput]))
}

func TestConsumeGeminiInteractionsStreamHandlesTextThinkingAndFunctionArguments(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"event_type":"interaction.created","interaction":{"id":"it_stream"}}`,
		"",
		`data: {"event_type":"step.start","index":0,"step":{"type":"thought","signature":"","summary":[{"type":"text","text":"initial "}]}}`,
		"",
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_summary","content":{"type":"text","text":"thinking"}}}`,
		"",
		`data: {"event_type":"step.delta","index":0,"delta":{"type":"thought_signature","signature":"sig_thought"}}`,
		"",
		`data: {"event_type":"step.stop","index":0}`,
		"",
		`data: {"event_type":"step.start","index":1,"step":{"type":"function_call","id":"fc_stream","name":"lookup","signature":"sig_fc_stream"}}`,
		"",
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"{\"query\":"}}`,
		"",
		`data: {"event_type":"step.delta","index":1,"delta":{"type":"arguments_delta","arguments":"\"x\"}"}}`,
		"",
		`data: {"event_type":"step.stop","index":1}`,
		"",
		`data: {"event_type":"step.start","index":2,"step":{"type":"google_search_call","id":"gs_stream"}}`,
		"",
		`data: {"event_type":"step.delta","index":2,"delta":{"type":"google_search_call","arguments":{"queries":["current weather"]},"signature":"sig_search"}}`,
		"",
		`data: {"event_type":"step.stop","index":2}`,
		"",
		`data: {"event_type":"step.start","index":3,"step":{"type":"google_search_result","call_id":"gs_stream"}}`,
		"",
		`data: {"event_type":"step.delta","index":3,"delta":{"type":"google_search_result","result":[{"search_suggestions":"weather result"}],"signature":"sig_search"}}`,
		"",
		`data: {"event_type":"step.stop","index":3}`,
		"",
		`data: {"event_type":"step.start","index":4,"step":{"type":"model_output"}}`,
		"",
		`data: {"event_type":"step.delta","index":4,"delta":{"type":"text","text":"hello"}}`,
		"",
		`data: {"event_type":"step.stop","index":4}`,
		"",
		`data: {"event_type":"interaction.completed","interaction":{"id":"it_stream","status":"completed","usage":{"total_input_tokens":5,"total_output_tokens":2}}}`,
		"",
	}, "\n")
	result := &GenerateOutput{ServerSideToolUsage: map[string]int64{}}
	var textDelta string
	var reasoningDelta string
	serverToolEvents := make([]ToolCall, 0)
	err := consumeGeminiInteractionsStream(strings.NewReader(stream), result, func(event GenerateStreamEvent) error {
		textDelta += event.Delta
		reasoningDelta += interactionReasoningDeltaText(event.Reasoning)
		if event.ServerToolCall != nil {
			serverToolEvents = append(serverToolEvents, *event.ServerToolCall)
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "it_stream", result.ResponseID)
	require.Equal(t, "hello", result.Text)
	require.Equal(t, "hello", textDelta)
	require.Equal(t, valueCompleted08D3ABC9, result.StopReason)
	require.NotNil(t, result.Reasoning)
	require.Equal(t, "initial thinking", result.Reasoning.Text)
	require.Equal(t, "sig_thought", result.Reasoning.Signature)
	require.Equal(t, "initial thinking", reasoningDelta)
	require.Len(t, result.ToolCalls, 1)
	require.JSONEq(t, `{"query":"x"}`, result.ToolCalls[0].ArgumentsJSON)
	require.Equal(t, valueCompleted08D3ABC9, result.ToolCalls[0].Status)
	require.Equal(t, "sig_fc_stream", result.ToolCalls[0].ThoughtSignature)
	require.Len(t, result.ServerToolCalls, 1)
	require.Equal(t, "gs_stream", result.ServerToolCalls[0].ToolCallID)
	require.Equal(t, interactionTypeGoogleSearch, result.ServerToolCalls[0].ToolName)
	require.Equal(t, valueCompleted9E9778C8, result.ServerToolCalls[0].Status)
	require.JSONEq(t, `{"queries":["current weather"]}`, result.ServerToolCalls[0].ArgumentsJSON)
	require.JSONEq(t, `[{"search_suggestions":"weather result"}]`, result.ServerToolCalls[0].OutputJSON)
	require.Equal(t, int64(1), result.ServerSideToolUsage[interactionUsageGoogleSearch])
	require.NotEmpty(t, serverToolEvents)
	require.Equal(t, valueCompleted9E9778C8, serverToolEvents[len(serverToolEvents)-1].Status)
	require.Equal(t, int64(5), result.Usage.InputTokens)
	require.Equal(t, int64(2), result.Usage.OutputTokens)
	replay := decodeProviderReplayItems(result.ReplayStateJSON, AdapterGoogleInteractions)
	require.Len(t, replay, 5)
	require.Equal(t, "sig_thought", getString(replay[0][interactionKeySignature]))
	require.Equal(t, "sig_fc_stream", getString(replay[1][interactionKeySignature]))
	require.Equal(t, "x", getString(asMap(replay[1][interactionKeyArguments])["query"]))
	require.Equal(t, "hello", getString(asMap(asSlice(replay[4][interactionKeyContent])[0])[interactionKeyText]))
}

func interactionMapSliceAsMaps(raw interface{}) []map[string]interface{} {
	items := interactionMapSlice(raw)
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		result = append(result, asMap(item))
	}
	return result
}

func interactionReasoningDeltaText(delta *ReasoningDelta) string {
	if delta == nil {
		return ""
	}
	return delta.Text
}
