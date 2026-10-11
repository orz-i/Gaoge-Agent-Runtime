package workflowhttp

import (
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

// WorkflowTrace is owner-visible and intentionally excludes effect bodies.
// Codes may still originate in arbitrary host adapters, so project them at
// the HTTP edge rather than returning untrusted values as safe diagnostics.
func publicWorkflowTrace(trace workflow.Trace) workflow.Trace {
	trace.Activations = append([]workflow.ActivationTrace(nil), trace.Activations...)
	for i := range trace.Activations {
		trace.Activations[i].ErrorCode = runtimehttp.PublicFailureCode(trace.Activations[i].ErrorCode)
	}
	trace.Effects = append([]workflow.EffectTrace(nil), trace.Effects...)
	for i := range trace.Effects {
		trace.Effects[i].ErrorCode = runtimehttp.PublicFailureCode(trace.Effects[i].ErrorCode)
	}
	trace.Compensations = append([]workflow.CompensationTrace(nil), trace.Compensations...)
	for i := range trace.Compensations {
		trace.Compensations[i].ErrorCode = runtimehttp.PublicFailureCode(trace.Compensations[i].ErrorCode)
	}
	return trace
}
