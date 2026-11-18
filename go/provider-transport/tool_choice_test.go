package transport

import (
	"encoding/json"
	"testing"
)

const (
	testToolPublishResult = "fixture_publish_result"
	testToolMemorySave    = "memory_save"
)

func TestApplyNativeToolChoiceOpenAIChatRequired(t *testing.T) {
	payload := buildChatCompletionsRequestBody(
		AdapterOpenAIChatCompletions,
		"gpt-test",
		GenerateInput{
			ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
			Tools:      []ToolDefinition{{Name: testToolPublishResult, InputSchema: json.RawMessage(`{}`)}},
		},
		[]Message{{Role: valueUserF1E3DFF7, Content: "hi"}},
		nil,
		[]ToolDefinition{{Name: testToolPublishResult, InputSchema: json.RawMessage(`{}`)}},
		nil,
		false,
	)
	if payload["tool_choice"] != string(ToolChoiceRequired) {
		t.Fatalf("tool_choice = %#v", payload["tool_choice"])
	}
}

func TestApplyNativeToolChoiceXAIResponsesRequired(t *testing.T) {
	payload := buildResponsesRequestBody(
		AdapterXAIResponses,
		"grok-test",
		GenerateInput{
			ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
			Tools:      []ToolDefinition{{Name: testToolPublishResult, InputSchema: json.RawMessage(`{}`)}},
		},
		[]Message{{Role: valueUserF1E3DFF7, Content: "hi"}},
		nil,
		[]ToolDefinition{{Name: testToolPublishResult, InputSchema: json.RawMessage(`{}`)}},
		true,
		nil,
		false,
	)
	if payload["tool_choice"] != string(ToolChoiceRequired) {
		t.Fatalf("tool_choice = %#v", payload["tool_choice"])
	}
}

func TestApplyNativeToolChoiceAnthropicRequired(t *testing.T) {
	payload, err := buildAnthropicRequestBody("claude-test", GenerateInput{
		Messages:   []Message{{Role: valueUserF1E3DFF7, Content: "hi"}},
		Tools:      []ToolDefinition{{Name: testToolPublishResult, Description: "p", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
		Options:    map[string]interface{}{valueMaxTokens8DDF2E0E: 128},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	choice, ok := payload["tool_choice"].(map[string]interface{})
	if !ok || choice["type"] != anthropicToolChoiceAny {
		t.Fatalf("tool_choice = %#v", payload["tool_choice"])
	}
}

func TestApplyNativeToolChoiceDoesNotOverrideExplicitAnthropicNamedChoice(t *testing.T) {
	payload, err := buildAnthropicRequestBody("claude-test", GenerateInput{
		Messages:   []Message{{Role: valueUserF1E3DFF7, Content: "hi"}},
		Tools:      []ToolDefinition{{Name: testToolMemorySave, Description: "p", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: ToolChoice{Mode: ToolChoiceRequired},
		Options: map[string]interface{}{
			"max_tokens":  128,
			"tool_choice": map[string]interface{}{valueType945D3026: valueToolEBFF948B, "name": testToolMemorySave},
		},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	choice, ok := payload["tool_choice"].(map[string]interface{})
	if !ok || choice["type"] != valueToolEBFF948B || choice["name"] != testToolMemorySave {
		t.Fatalf("expected named tool_choice preserved, got %#v", payload["tool_choice"])
	}
}

func TestAnthropicFinalAnswerExplicitlyForbidsHistoricalTools(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit_none", true: valueDisabled98E034E3}[disable], func(t *testing.T) {
			input := GenerateInput{
				DisableTools: disable,
				ToolChoice:   ToolChoice{Mode: ToolChoiceNone},
				Messages: []Message{
					{Role: valueUserF1E3DFF7, Content: "Use existing evidence."},
					{Role: valueAssistantE3AFA514, ToolCalls: []ToolCall{{ToolCallID: "past-tool", ToolType: "function", ToolName: testToolMemorySave, ArgumentsJSON: `{}`}}},
					{Role: valueToolEBFF948B, ToolResults: []ToolResult{{ToolCallID: "past-tool", OutputJSON: `{"saved":true}`, Status: "completed"}}},
					{Role: valueUserF1E3DFF7, Content: "Finish with existing evidence."},
				},
				Options: map[string]interface{}{
					"max_tokens":  128,
					"tool_choice": map[string]interface{}{valueType945D3026: valueToolEBFF948B, "name": testToolMemorySave},
				},
			}
			if disable {
				input.ToolChoice = ToolChoice{Mode: ToolChoiceRequired}
				input.Tools = []ToolDefinition{{Name: testToolMemorySave, InputSchema: json.RawMessage(`{"type":"object"}`)}}
			}
			payload, err := buildAnthropicRequestBody("MiniMax-M3.1-Flash-Preview", input, false)
			if err != nil {
				t.Fatal(err)
			}
			choice, ok := payload["tool_choice"].(map[string]interface{})
			if !ok || choice[valueType945D3026] != string(ToolChoiceNone) {
				t.Fatalf("final generation can still call tools: %#v", payload["tool_choice"])
			}
			if _, exists := payload["tools"]; exists && disable {
				t.Fatalf("disabled declarations survived: %#v", payload["tools"])
			}
		})
	}
}
