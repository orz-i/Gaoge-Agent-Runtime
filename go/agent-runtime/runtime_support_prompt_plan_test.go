package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	promptBudgetRequiredPolicy    = "required policy"
	promptBudgetLatestInstruction = "latest instruction"
	promptBudgetPublishTool       = "publish"
	promptBudgetLatestCallID      = "latest"
)

func TestEnforcePromptInputBudgetCountsToolsAndKeepsLatestTurn(t *testing.T) {
	messages := []Message{
		{Role: valueSystemE36EB5DA, Content: promptBudgetRequiredPolicy},
		{Role: valueUser90BA419D, Content: strings.Repeat("old-user ", 40)},
		{Role: valueAssistantB87088D6, Content: strings.Repeat("old-assistant ", 40)},
		{Role: valueUser90BA419D, Content: promptBudgetLatestInstruction},
	}
	tools := []ToolDefinition{{
		Name:        "large_tool",
		Description: strings.Repeat("schema guidance ", 20),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
	}}
	toolTokens := estimateToolDefinitionTokens(tools)
	got := enforcePromptInputBudget(messages, toolTokens, 200)

	if len(got) != 2 {
		t.Fatalf("trimmed messages = %#v, want required system and latest user", got)
	}
	if got[0].Role != valueSystemE36EB5DA || got[0].Content != promptBudgetRequiredPolicy {
		t.Fatalf("required system message was not preserved: %#v", got)
	}
	if got[1].Role != valueUser90BA419D || got[1].Content != promptBudgetLatestInstruction {
		t.Fatalf("latest user message was not preserved: %#v", got)
	}
	if total := estimatePromptTokens(got) + toolTokens; total > 200*promptTransportBudgetPercent/100 {
		t.Fatalf("prompt total = %d, exceeds transport-safe budget", total)
	}
}

func TestTrimOldestPromptTurnRemovesWholeUserAssistantTurn(t *testing.T) {
	messages := []Message{
		{Role: valueSystemE36EB5DA, Content: "policy"},
		{Role: valueUser90BA419D, Content: "old user"},
		{Role: valueAssistantB87088D6, Content: "old assistant"},
		{Role: valueUser90BA419D, Content: "latest user"},
		{Role: valueAssistantB87088D6, Content: "latest assistant"},
	}
	got, ok := trimOldestPromptTurn(messages)
	if !ok {
		t.Fatal("expected oldest turn to be trimmed")
	}
	if len(got) != 3 || got[1].Content != "latest user" || got[2].Content != "latest assistant" {
		t.Fatalf("trimmed messages = %#v", got)
	}
}

func TestEnforcePromptTransportByteBudgetCountsSerializedToolSchemas(t *testing.T) {
	messages := []Message{
		{Role: valueSystemE36EB5DA, Content: promptBudgetRequiredPolicy},
		{Role: valueUser90BA419D, Content: strings.Repeat("old payload ", 600)},
		{Role: valueAssistantB87088D6, Content: strings.Repeat("old result ", 600)},
		{Role: valueUser90BA419D, Content: promptBudgetLatestInstruction},
	}
	tools := []ToolDefinition{{
		Name:        "large_tool",
		Description: strings.Repeat("tool description ", 300),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`),
	}}
	got := enforcePromptTransportByteBudget(messages, tools, 4000)

	if len(got) != 2 || got[0].Role != valueSystemE36EB5DA || got[1].Content != promptBudgetLatestInstruction {
		t.Fatalf("transport trimming did not preserve only required context: %#v", got)
	}
	if size := estimatePromptTransportBytes(got, tools); size > 4000*5/2 {
		t.Fatalf("serialized prompt size = %d, exceeds byte budget", size)
	}
}

func TestPromptBudgetDropsOnlySupersededFailedToolAttempts(t *testing.T) {
	messages := []Message{
		{Role: valueSystemE36EB5DA, Content: "policy"},
		{Role: valueUser90BA419D, Content: "publish a change set"},
		{Role: valueAssistantB87088D6, ToolCalls: []ToolCall{{ToolCallID: "old", ToolName: promptBudgetPublishTool, ArgumentsJSON: strings.Repeat("x", 5000)}}},
		{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: "old", ToolName: promptBudgetPublishTool, Status: "failed", Error: "old validation error"}}},
		{Role: valueAssistantB87088D6, ToolCalls: []ToolCall{{ToolCallID: promptBudgetLatestCallID, ToolName: promptBudgetPublishTool, ArgumentsJSON: strings.Repeat("y", 5000)}}},
		{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: promptBudgetLatestCallID, ToolName: promptBudgetPublishTool, Status: valueSuccess4D886D19, OutputJSON: `{"published":true}`}}},
	}
	got := enforcePromptTransportByteBudget(messages, nil, 3000)

	if len(got) != 4 {
		t.Fatalf("trimmed messages = %#v, want system, user, and latest failed attempt", got)
	}
	if got[2].ToolCalls[0].ToolCallID != promptBudgetLatestCallID || got[3].ToolResults[0].Status != valueSuccess4D886D19 {
		t.Fatalf("successful replacement was not preserved: %#v", got)
	}
}

func TestPromptBudgetDoesNotDropFailureWithoutSuccessfulReplacement(t *testing.T) {
	messages := []Message{
		{Role: valueUser90BA419D, Content: "publish a change set"},
		{Role: valueAssistantB87088D6, ToolCalls: []ToolCall{{ToolCallID: "old", ToolName: promptBudgetPublishTool}}},
		{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: "old", ToolName: promptBudgetPublishTool, Status: "failed", Error: "old validation error"}}},
		{Role: valueAssistantB87088D6, ToolCalls: []ToolCall{{ToolCallID: "latest", ToolName: promptBudgetPublishTool}}},
		{Role: valueToolCCF14517, ToolResults: []ToolResult{{ToolCallID: "latest", ToolName: promptBudgetPublishTool, Status: "failed", Error: "latest validation error"}}},
	}
	if got, ok := trimSupersededFailedToolAttempt(messages); ok || len(got) != len(messages) {
		t.Fatalf("failure-only chain was trimmed: %#v", got)
	}
}
