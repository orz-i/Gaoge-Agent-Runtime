package continuationadapter

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Triggers returns first-party self-resumption event policies. Child-terminal
// propagation remains feature-neutral in the Scheduler through RunRelation.
func Triggers() []continuation.TriggerRegistration {
	return []continuation.TriggerRegistration{
		continuation.RegisterTriggers(agent.RunKind, agentTrigger),
	}
}

func agentTrigger(event kernel.EventDraft) (continuation.Trigger, bool) {
	eventType := strings.TrimSpace(event.Type)
	if eventType == agent.EventToolWaitDeadline {
		return continuation.TriggerToolWaitDeadline, true
	}
	if eventType == agent.EventExternalToolResultReady {
		return continuation.TriggerToolResultReady, true
	}
	if eventType == "agent.model_invocation.pending" || eventType == "agent.model_invocation.claimed" ||
		eventType == "agent.model_invocation.retryable" || eventType == "agent.model_invocation.completed" || eventType == "agent.model_continued" {
		return continuation.TriggerModelReady, true
	}
	return continuation.TriggerApprovalResolved,
		eventType == "interaction.resolved" || eventType == "tool.rejected"
}

// Resumers binds only the Agent execution loop; delegated children retain
// their owning ChildRunner and continuation identity.
func Resumers(agentResumer continuation.Resumer) []continuation.ResumerRegistration {
	return []continuation.ResumerRegistration{continuation.RegisterResumer(agent.RunKind, agentResumer)}
}
