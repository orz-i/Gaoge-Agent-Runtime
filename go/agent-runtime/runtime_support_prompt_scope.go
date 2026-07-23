// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

const (
	valueUser030083D3      = "user"
	valueSystemC46FEA2F    = "system"
	valueAssistant5B351188 = "assistant"
)

type promptScope struct {
	FullBranchMessages []ContextMessage
	CoveredMessages    []ContextMessage
	RetainedMessages   []ContextMessage
	Compaction         *ThreadCompaction
}

func buildPromptScope(messages []ContextMessage, compaction *ThreadCompaction) promptScope {
	scope := promptScope{FullBranchMessages: append([]ContextMessage(nil), messages...), RetainedMessages: append([]ContextMessage(nil), messages...), Compaction: compaction}
	if compaction == nil {
		return scope
	}
	for index := range messages {
		if messages[index].Projection != compaction.CoveredThrough {
			continue
		}
		scope.CoveredMessages = append([]ContextMessage(nil), messages[:index+1]...)
		if index+1 < len(messages) {
			scope.RetainedMessages = append([]ContextMessage(nil), messages[index+1:]...)
		} else {
			scope.RetainedMessages = nil
		}
		break
	}
	return scope
}

func (s promptScope) activeMessages() []ContextMessage {
	if len(s.RetainedMessages) > 0 {
		return s.RetainedMessages
	}
	return s.FullBranchMessages
}

func (s promptScope) retainedProjectionIDSet() map[string]struct{} {
	result := make(map[string]struct{}, len(s.RetainedMessages))
	for _, message := range s.RetainedMessages {
		if message.Projection.ID != "" {
			result[message.Projection.ID] = struct{}{}
		}
	}
	return result
}

func historyMessagesFromDomain(messages []ContextMessage) []Message {
	result := make([]Message, 0, len(messages))
	for _, item := range messages {
		if item.Role != valueUser030083D3 && item.Role != valueAssistant5B351188 && item.Role != valueSystemC46FEA2F {
			continue
		}
		result = append(result, Message{Role: item.Role, Content: item.Content})
	}
	return result
}
