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

// Storage binding is validated independently from whether this composition
// qualifies to take over a returned or suspended native continuation.
func validManagedStores(d Dependencies) bool {
	a := d.BoundedContext
	return d.Runtime != nil && d.Runtime.SupportsManagedRecovery() && d.Runtime.HasManagedRecoveryHost() &&
		a != nil && summaryNativeBridge(a.ports, "boundedContextPorts", d.Runtime) &&
		(d.ContextSummary == nil || summaryNativeBridge(d.ContextSummary.ports, "contextSummaryPorts", d.Runtime)) &&
		(d.BoundedBudget == nil || (d.Runtime.UsesLedgerStore(d.BoundedBudget.store) && summaryNativeBridge(d.BoundedBudget.ports, "boundedBudgetPorts", d.Runtime)))
}

func validManagedDependencies(d Dependencies) bool {
	return validManagedStores(d) && d.PrepareModelRequest == nil && d.ModelAdmissionBarrier == nil && d.ToolTimeline == nil &&
		len(d.Observers) == 0 && len(d.Telemetry) == 0 && len(d.CompletionPolicies) == 0 &&
		d.NormalizeModelPolicy == nil && d.ToolDiscovery == nil
}

func validSummaryNativePhase(state runState) bool {
	if !state.ManagedRecovery || state.WorkingStateSummary == nil || state.InputCursor != 0 || len(state.ModelInvocations) != 1 ||
		len(state.Messages) != 1 || !managedObligationsQuiescent(state) {
		return false
	}
	invocation := state.ModelInvocations[0]
	if invocation.Purpose != ModelInvocationContextSummary || !validContextSummaryInvocation(invocation) {
		return false
	}
	switch invocation.Status {
	case ModelInvocationPending:
		return invocation.DispatchAttempt == 0 && invocation.ExecutionAttempt == 0 && state.Budget.Usage.LLMCalls == 0
	case ModelInvocationCompleted:
		return validModelInvocationReceipt(invocation) && state.Budget.Usage.LLMCalls == 0
	case ModelInvocationConsumed:
		return validModelInvocationReceipt(invocation) && state.Budget.Usage.LLMCalls == 1
	default:
		return false
	}
}
