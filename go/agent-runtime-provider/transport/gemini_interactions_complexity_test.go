package transport

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiAssistantInteractionsPreserveContentAndToolCallOrder(t *testing.T) {
	steps := geminiInteractionMessageSteps(Message{
		Role:    valueAssistant6B7057DB,
		Content: "original model response",
		ToolCalls: []ToolCall{
			{ToolCallID: "first", ToolName: "search", ArgumentsJSON: "{\"query\":\"a\"}", ThoughtSignature: "sig"},
			{ToolCallID: "second", ToolName: "lookup", ArgumentsJSON: "{\"query\":\"b\"}"},
		},
	})
	require.Len(t, steps, 3)
	require.Equal(t, interactionTypeModelOutput, steps[0][interactionKeyType])
	require.Equal(t, interactionTypeFunctionCall, steps[1][interactionKeyType])
	require.Equal(t, "first", steps[1][interactionKeyID])
	require.Equal(t, "sig", steps[1][interactionKeySignature])
	require.Equal(t, "search", steps[1][interactionKeyName])
	require.Equal(t, interactionTypeFunctionCall, steps[2][interactionKeyType])
	require.Equal(t, "second", steps[2][interactionKeyID])
	require.NotContains(t, steps[2], interactionKeySignature)
}
