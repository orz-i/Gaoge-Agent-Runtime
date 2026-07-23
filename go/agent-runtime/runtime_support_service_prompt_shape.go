// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"strings"
)

const (
	valueAssistantBE3A9B91 = "assistant"
	valueFullB842CD89      = "full"
	valueStateful5485BCB6  = "stateful"
	valueSystemAEF4768E    = "system"
	valueTool22D74BD5      = "tool"
	valueUser2962EEBE      = "user"
)

type promptShape struct {
	Mode               string
	MessageCount       int
	SystemCount        int
	UserCount          int
	AssistantCount     int
	ToolCount          int
	TotalTokens        int64
	LeadingSystem      int64
	LastUserTokens     int64
	HasUserContext     bool
	HasFiles           bool
	HasEvidence        bool
	HasRAG             bool
	HasSummary         bool
	HasMemory          bool
	HasRecall          bool
	PreviousResponse   bool
	FullMessageCount   int
	FullPromptTokens   int64
	StatefulSavedMsgs  int
	StatefulSavedToken int64
}

func summarizePromptShape(mode string, sent []Message, full []Message, previousResponseID string) promptShape {
	shape := promptShape{
		Mode:             strings.TrimSpace(mode),
		MessageCount:     len(sent),
		PreviousResponse: strings.TrimSpace(previousResponseID) != "",
		FullMessageCount: len(full),
		FullPromptTokens: estimatePromptTokens(full),
	}
	if shape.Mode == "" {
		if shape.PreviousResponse {
			shape.Mode = valueStateful5485BCB6
		} else {
			shape.Mode = valueFullB842CD89
		}
	}
	shape.TotalTokens = estimatePromptTokens(sent)

	leadingSystem := true
	for _, message := range sent {
		shape.applyMessage(message, &leadingSystem)
	}
	shape.applyStatefulSavings(sent, full)
	return shape
}

func (shape *promptShape) applyMessage(message Message, leadingSystem *bool) {
	msgTokens := estimateMessageTokens(message)
	switch message.Role {
	case valueSystemAEF4768E:
		shape.SystemCount++
		if *leadingSystem {
			shape.LeadingSystem += msgTokens
		}
	case valueUser2962EEBE:
		shape.UserCount++
		shape.LastUserTokens = msgTokens
		shape.applyUserContextFlags(promptShapeUserContent(message))
	case valueAssistantBE3A9B91:
		shape.AssistantCount++
	case valueTool22D74BD5:
		shape.ToolCount++
	}
	if message.Role != valueSystemAEF4768E {
		*leadingSystem = false
	}
}

func promptShapeUserContent(message Message) string {
	if len(message.Parts) == 0 {
		return message.Content
	}
	var parts strings.Builder
	for _, part := range message.Parts {
		if part.Kind == ContentPartText || part.Kind == ContentPartFile {
			parts.WriteString(part.Text)
			parts.WriteString("\n")
		}
	}
	return parts.String()
}

func (shape *promptShape) applyUserContextFlags(content string) {
	shape.HasUserContext = shape.HasUserContext || strings.Contains(content, "<ctx>")
	shape.HasFiles = shape.HasFiles || strings.Contains(content, "<files>")
	shape.HasEvidence = shape.HasEvidence || strings.Contains(content, "<evs>")
	shape.HasRAG = shape.HasRAG || strings.Contains(content, "<rag>")
	shape.HasSummary = shape.HasSummary || strings.Contains(content, "<sum")
	shape.HasMemory = shape.HasMemory || strings.Contains(content, "<mems>")
	shape.HasRecall = shape.HasRecall || strings.Contains(content, "<recall>")
}

func (shape *promptShape) applyStatefulSavings(sent []Message, full []Message) {
	if len(full) == 0 {
		return
	}
	shape.StatefulSavedMsgs = len(full) - len(sent)
	shape.StatefulSavedToken = estimatePromptTokens(full) - shape.TotalTokens
	if shape.StatefulSavedMsgs < 0 {
		shape.StatefulSavedMsgs = 0
	}
	if shape.StatefulSavedToken < 0 {
		shape.StatefulSavedToken = 0
	}
}

func promptShapeTraceAttributes(prefix string, shape promptShape) []LogField {
	key := func(name string) string {
		if strings.TrimSpace(prefix) == "" {
			return name
		}
		return prefix + "." + name
	}
	return []LogField{
		String(key("mode"), shape.Mode),
		Bool(key("previous_response"), shape.PreviousResponse),
		Int(key("message_count"), shape.MessageCount),
		Int(key("full_message_count"), shape.FullMessageCount),
		Int64(key("tokens"), shape.TotalTokens),
		Int64(key("full_tokens"), shape.FullPromptTokens),
		Int64(key("leading_system_tokens"), shape.LeadingSystem),
		Int64(key("last_user_tokens"), shape.LastUserTokens),
		Int(key("system_count"), shape.SystemCount),
		Int(key("user_count"), shape.UserCount),
		Int(key("assistant_count"), shape.AssistantCount),
		Int(key("tool_count"), shape.ToolCount),
		Bool(key("has_ctx"), shape.HasUserContext),
		Bool(key("has_files"), shape.HasFiles),
		Bool(key("has_evidence"), shape.HasEvidence),
		Bool(key("has_rag"), shape.HasRAG),
		Bool(key("has_summary"), shape.HasSummary),
		Bool(key("has_memory"), shape.HasMemory),
		Bool(key("has_recall"), shape.HasRecall),
		Int(key("stateful_saved_messages"), shape.StatefulSavedMsgs),
		Int64(key("stateful_saved_tokens"), shape.StatefulSavedToken),
	}
}
