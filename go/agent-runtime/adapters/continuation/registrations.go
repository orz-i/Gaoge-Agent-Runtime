package continuationadapter

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

const workflowEffectPendingReason = "effect_pending"

// Resumers returns the explicit first-party Runtime resumption bindings.
func Resumers(
	agentResumer continuation.Resumer,
	planResumer continuation.Resumer,
	workflowResumer continuation.Resumer,
	teamResumer continuation.Resumer,
) []continuation.ResumerRegistration {
	return []continuation.ResumerRegistration{
		continuation.RegisterResumer(agent.RunKind, agentResumer),
		continuation.RegisterResumer(planexecute.RunKind, planResumer),
		continuation.RegisterResumer(workflow.RunKind, workflowResumer),
		continuation.RegisterResumer(team.RunKind, teamResumer),
	}
}

// Triggers returns first-party self-resumption event policies. Child-terminal
// propagation remains feature-neutral in the Scheduler through RunRelation.
func Triggers() []continuation.TriggerRegistration {
	return []continuation.TriggerRegistration{
		continuation.RegisterTriggers(agent.RunKind, agentTrigger),
		continuation.RegisterTriggers(planexecute.RunKind, planTrigger),
		continuation.RegisterTriggers(workflow.RunKind, workflowTrigger),
	}
}

func agentTrigger(event kernel.EventDraft) (continuation.Trigger, bool) {
	eventType := strings.TrimSpace(event.Type)
	if eventType == "agent.model_invocation.pending" || eventType == "agent.model_invocation.claimed" ||
		eventType == "agent.model_invocation.retryable" || eventType == "agent.model_invocation.completed" {
		return continuation.TriggerModelReady, true
	}
	return continuation.TriggerApprovalResolved,
		eventType == "interaction.resolved" || eventType == "tool.rejected"
}

func planTrigger(event kernel.EventDraft) (continuation.Trigger, bool) {
	return continuation.TriggerApprovalResolved, strings.TrimSpace(event.Type) == "plan.approved"
}

func workflowTrigger(event kernel.EventDraft) (continuation.Trigger, bool) {
	eventType := strings.TrimSpace(event.Type)
	if eventType == "workflow.wait.resolved" {
		return continuation.TriggerWaitResolved, true
	}
	return continuation.TriggerSegmentYielded, eventType == "workflow.segment.yielded" &&
		strings.TrimSpace(event.Message) != workflowEffectPendingReason
}
