package agentruntime

import (
	"context"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (r *workflowRunner) startWorkflowNodeSpan(node model.WorkflowNode, path, scopeKey string) (context.Context, Span) {
	fields := runTelemetryFields(r.run,
		String("gen_ai.operation.name", "workflow_node"),
		String("run.id", r.run.RunID),
		String("workflow.definition.id", r.run.WorkflowDefinition.ID),
		String("workflow.node.id", node.ID),
		String("workflow.node.type", node.Type),
		String("workflow.activation.path", path),
		String("workflow.scope", scopeKey),
		Bool("workflow.compensation", strings.HasPrefix(path, "compensation/")),
	)
	return r.service.startSpan(r.ctx, "agentruntime.workflow.node", fields...)
}

func (r *workflowRunner) recordWorkflowWaitSpan(wait model.WorkflowWait) {
	fields := runTelemetryFields(r.run,
		String("gen_ai.operation.name", "workflow_wait"),
		String("run.id", r.run.RunID),
		String("step.id", wait.StepID),
		String("workflow.wait.id", wait.WaitID),
		String("workflow.wait.kind", wait.Kind),
		String("workflow.activation.path", wait.ActivationKey),
		String("workflow.child_run.id", wait.ChildRunID),
		String("interaction.id", wait.InteractionID),
	)
	_, span := r.service.startSpan(r.ctx, "agentruntime.workflow.wait", fields...)
	span.End()
}
