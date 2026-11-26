package agent

import (
	"context"
	"encoding/json"
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
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

func validSummaryNativeDependencies(dependencies Dependencies) bool {
	adapter := dependencies.ContextSummary
	return dependencies.Runtime != nil && dependencies.Runtime.SupportsManagedRecovery() && dependencies.Runtime.HasManagedRecoveryHost() &&
		adapter != nil && adapter.bounded != nil && adapter.ports != nil && dependencies.ManagedContextRecovery == nil &&
		dependencies.PrepareModelRequest == nil && contextOnlyRun(dependencies, adapter.bounded) &&
		len(dependencies.ModelMiddleware) == 0 && len(dependencies.ToolMiddleware) == 0 &&
		len(dependencies.Observers) == 0 && len(dependencies.Telemetry) == 0 && len(dependencies.CompletionPolicies) == 0 &&
		dependencies.NormalizeModelPolicy == nil && dependencies.ToolDiscovery == nil &&
		summaryNativeBridge(adapter.ports, "contextSummaryPorts", dependencies.Runtime) &&
		summaryNativeBridge(adapter.bounded.ports, "boundedContextPorts", dependencies.Runtime)
}

func (runner *Runner) SupportsSummaryNativeRecovery() bool {
	return runner != nil && runner.summaryNativeRecovery && runner.contextSummary != nil && runner.managedContextRecovery == nil
}

type contextSummaryLifecycle struct {
	Phase          ModelInvocationStatus `json:"phase"`
	SourceRevision uint64                `json:"sourceRevision"`
	InvocationID   string                `json:"invocationID"`
	RequestHash    string                `json:"requestHash"`
	BindingDigest  string                `json:"bindingDigest"`
}

func summaryNativePhase(state runState) (ModelInvocation, bool) {
	if !state.SummaryNativeRecovery || state.WorkingStateSummary == nil || state.InputCursor != 0 || len(state.ModelInvocations) != 1 ||
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

func (runner *Runner) qualifySummaryBoundary(ctx context.Context, snapshot kernel.Snapshot, state runState) (*kernel.ManagedSafePoint, error) {
	if !state.SummaryNativeRecovery {
		return nil, nil
	}
	if !runner.SupportsSummaryNativeRecovery() || runner.activeRootGrant == nil || runner.activeRunLease == nil || snapshot.Run.ParentRunID != "" {
		return nil, ErrInvalidRequest
	}
	// The Host reservation is immutable per original dispatch. This release
	// supports its first transfer and exact same-point ACK repair, not a new
	// cross-phase recovery chain after a successor has already been enabled.
	if runner.activeRootGrant.AdmissionOperation == "managed_resume" {
		return nil, nil
	}
	invocation, valid := summaryNativePhase(state)
	host, bound := runner.runtime.ManagedDispatch(ctx, snapshot.Run.ID)
	if !valid || !bound {
		return nil, kernel.ErrExecutionRequired
	}
	lifecycle := contextSummaryLifecycle{Phase: invocation.Status, SourceRevision: snapshot.Run.Revision + 1,
		InvocationID: invocation.ID, RequestHash: invocation.RequestHash, BindingDigest: kernel.ManagedDigest(invocation.ContextSummary)}
	if !callbackscope.Operation(ctx, runner.contextSummary.bounded, "context.summary.native-phase:"+string(invocation.Status), lifecycle) {
		return nil, ErrInvalidRequest
	}
	raw, err := json.Marshal(lifecycle)
	version := kernel.ManagedLifecycleWorkingStateSummary
	if runner.combinedManaged(state) {
		var eligible bool
		raw, eligible, err = runner.freezeManagedObligations(ctx, snapshot, state, invocation, host)
		if err == nil && !eligible {
			// A receipt already committed with no eligible suspension still
			// has its original ordinary recovery semantics.
			if invocation.Status != ModelInvocationPending {
				return nil, nil
			}
			return nil, ErrRootOutcomeUnknown
		}
		version = kernel.ManagedLifecycleAgentObligations
	}
	if err != nil {
		return nil, err
	}
	id := "summary-safe-" + kernel.ManagedDigest(struct {
		Grant, Invocation, Phase string
		Revision                 uint64
	}{runner.activeRootGrant.ID, invocation.ID, string(invocation.Status), snapshot.Run.Revision + 1})
	return runner.managedTransitionPoint(invocation, host, id, version, raw), nil
}

func (runner *Runner) validateSummarySafePoint(point kernel.ManagedSafePoint) error {
	if !runner.SupportsSummaryNativeRecovery() || point.LifecycleVersion != kernel.ManagedLifecycleWorkingStateSummary || !point.Valid() {
		return ErrInvalidRequest
	}
	state, err := decodeState(point.Source.State)
	if err != nil {
		return err
	}
	invocation, valid := summaryNativePhase(state)
	var lifecycle contextSummaryLifecycle
	if !valid || state.InputBinding != "" || json.Unmarshal(point.LifecycleSource, &lifecycle) != nil ||
		lifecycle.SourceRevision != point.Source.Run.Revision || lifecycle.Phase != invocation.Status ||
		lifecycle.InvocationID != invocation.ID || lifecycle.RequestHash != invocation.RequestHash ||
		lifecycle.BindingDigest != kernel.ManagedDigest(invocation.ContextSummary) || point.ModelInvocationID != invocation.ID ||
		point.ModelRequestHash != invocation.RequestHash || point.Source.Run.ID != invocation.RunID ||
		invocation.ContextSummary.Cursor.Turn.ID != point.Host.TurnID || invocation.ContextSummary.Root.InvocationID != point.Host.InvocationID ||
		uint64(invocation.ContextSummary.Root.Attempt) != uint64(point.Host.Attempt) {
		return ErrInvalidRequest
	}
	return nil
}

func (runner *Runner) preflightSummaryTransfer(ctx context.Context, transfer kernel.ManagedTransfer) error {
	if err := runner.validateSummarySafePoint(transfer.SafePoint); err != nil {
		return err
	}
	current, err := runner.runtime.Load(ctx, transfer.SafePoint.Host.RunID)
	if err != nil {
		return err
	}
	if transfer.State == kernel.ManagedClosed || transfer.State == kernel.ManagedCancelled {
		return nil // Existing exact closure validation remains authoritative.
	}
	if kernel.ManagedSourceMatches(transfer.SafePoint, current) {
		return nil
	}
	// A dispatch invalidates pending suspension. Preserve the native unknown
	// boundary without PARK, a new owner, another Provider call or state rewrite.
	state, decodeErr := decodeState(current.State)
	source, sourceErr := decodeState(transfer.SafePoint.Source.State)
	if decodeErr == nil && sourceErr == nil && state.SummaryNativeRecovery && current.Run.Status == kernel.RunStatusRunning &&
		current.Run.Actor == transfer.SafePoint.Source.Run.Actor && current.Run.Thread == transfer.SafePoint.Source.Run.Thread {
		active, ok := activeModelInvocation(state)
		original, originalOK := activeModelInvocation(source)
		if ok && originalOK && active.Purpose == ModelInvocationContextSummary && active.Status == ModelInvocationPending && active.DispatchAttempt != 0 &&
			active.ID == original.ID && active.RequestHash == original.RequestHash && sameBoundedValue(active.Request, original.Request) &&
			sameBoundedValue(active.ContextSummary, original.ContextSummary) {
			return ErrModelOutcomeUnknown
		}
	}
	return kernel.ErrExecutionConflict
}
