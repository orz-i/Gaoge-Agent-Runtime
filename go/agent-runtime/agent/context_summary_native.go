package agent

import (
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const summaryNativeHarnessPackage = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"

type summaryRecoveryHost interface {
	ContextSummaryRecoveryHost() kernel.ManagedRecoveryHostStore
}

// Native hard recovery is a first-party trusted-code boundary, not a property
// arbitrary callbacks can self-certify. Exact private Go type identity rejects
// wrappers, same-named foreign types and public marker methods. Renaming an
// audited bridge fails closed. Unsafe/module replacement is outside this
// boundary, just as replacement of the configured trusted Store is.
func summaryNativeBridge(value any, name string, runtime *kernel.Runtime) bool {
	typeOf := reflect.TypeOf(value)
	if typeOf == nil || typeOf.PkgPath() != summaryNativeHarnessPackage || typeOf.Name() != name {
		return false
	}
	storage, ok := value.(summaryRecoveryHost)
	return ok && runtime.UsesManagedRecoveryHost(storage.ContextSummaryRecoveryHost())
}

func validManagedDependencies(d Dependencies) bool {
	a := d.BoundedContext
	return d.Runtime != nil && d.Runtime.SupportsManagedRecovery() && d.Runtime.HasManagedRecoveryHost() &&
		a != nil && a.manager != nil && a.ports != nil && d.PrepareModelRequest == nil && contextOnlyRun(d, a) &&
		len(d.ModelMiddleware) == 0 && len(d.ToolMiddleware) == 0 && len(d.Observers) == 0 && len(d.Telemetry) == 0 && len(d.CompletionPolicies) == 0 &&
		d.NormalizeModelPolicy == nil && d.ToolDiscovery == nil &&
		summaryNativeBridge(a.ports, "boundedContextPorts", d.Runtime) &&
		(d.ContextSummary == nil || summaryNativeBridge(d.ContextSummary.ports, "contextSummaryPorts", d.Runtime)) &&
		(d.BoundedBudget == nil || (d.Runtime.UsesLedgerStore(d.BoundedBudget.coordinator.Store) && summaryNativeBridge(d.BoundedBudget.ports, "boundedBudgetPorts", d.Runtime)))
}

func summaryNativePhase(state runState) (ModelInvocation, bool) {
	if !state.ManagedRecovery || state.WorkingStateSummary == nil || state.InputCursor != 0 || len(state.ModelInvocations) != 1 ||
		len(state.Messages) != 1 || len(state.PendingCalls) != 0 || state.PendingApproval != nil || state.ToolDispatchID != "" ||
		state.ToolWait != nil || len(state.DeferredChildren) != 0 || len(state.RecoveredTools) != 0 {
		return ModelInvocation{}, false
	}
	invocation := state.ModelInvocations[0]
	if invocation.Purpose != ModelInvocationContextSummary || !validContextSummaryInvocation(invocation) {
		return ModelInvocation{}, false
	}
	switch invocation.Status {
	case ModelInvocationPending:
		if invocation.DispatchAttempt != 0 || invocation.ExecutionAttempt != 0 || state.Budget.Usage.LLMCalls != 0 {
			return ModelInvocation{}, false
		}
	case ModelInvocationCompleted:
		if !validModelInvocationReceipt(invocation) || state.Budget.Usage.LLMCalls != 0 {
			return ModelInvocation{}, false
		}
	case ModelInvocationConsumed:
		if !validModelInvocationReceipt(invocation) || state.Budget.Usage.LLMCalls != 1 {
			return ModelInvocation{}, false
		}
	default:
		return ModelInvocation{}, false
	}
	return invocation, true
}
