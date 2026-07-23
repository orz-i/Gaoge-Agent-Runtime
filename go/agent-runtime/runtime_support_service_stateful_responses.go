// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"strings"
)

const (
	valueSystem07D75CAD = "system"
)

type statefulResponseDecision struct {
	PreviousResponseID string
	DisabledReason     string
}

func supportsPreviousResponseIDRoute(route *LLMRoute) bool {
	return route != nil && route.PreviousResponseIDSupported
}

func applyOpenAIResponsesInstructions(route *LLMRoute, endpoint string, input *GenerateInput) {
	if input == nil || endpoint != EndpointResponses || !supportsPreviousResponseIDRoute(route) {
		return
	}
	instructions, messages := extractOpenAIResponsesInstructions(input.Messages)
	if strings.TrimSpace(instructions) == "" {
		return
	}
	input.Instructions = instructions
	input.Messages = messages
}

func extractOpenAIResponsesInstructions(messages []Message) (string, []Message) {
	if len(messages) == 0 {
		return "", nil
	}
	var builder strings.Builder
	result := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message.Role != valueSystem07D75CAD {
			result = append(result, message)
			continue
		}
		text := strings.TrimSpace(systemInstructionText(message))
		if text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(text)
	}
	if builder.Len() == 0 {
		return "", cloneLLMMessages(messages)
	}
	return builder.String(), result
}

func systemInstructionText(message Message) string {
	if strings.TrimSpace(message.Content) != "" || len(message.Parts) == 0 {
		return message.Content
	}
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if strings.TrimSpace(part.Text) != "" {
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}
