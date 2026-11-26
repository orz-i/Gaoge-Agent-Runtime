package agent

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Only the registered concrete first-party Context bridge can read this
// preparation source. It is a storage read, never a configurable preparation
// callback, and cannot select or enable an execution owner.
type managedPreparationContextPort interface {
	ReadManagedPreparationContext(context.Context, kernel.ManagedHostBinding, *kernel.ManagedPreparationContext, []model.Message) (kernel.ManagedPreparationContext, error)
}

func (r *Runner) supportsManagedPreparation() bool {
	return (r.SupportsManagedContextRecovery() || r.SupportsSummaryNativeRecovery()) &&
		r.nativeContext != nil && summaryNativeBridge(r.nativeContext.ports, "boundedContextPorts", r.runtime)
}

func (r *Runner) readManagedPreparationContext(ctx context.Context, b kernel.ManagedHostBinding, expected *kernel.ManagedPreparationContext, state runState) (kernel.ManagedPreparationContext, error) {
	if !r.supportsManagedPreparation() {
		return kernel.ManagedPreparationContext{}, ErrInvalidRequest
	}
	port, ok := r.nativeContext.ports.(managedPreparationContextPort)
	if !ok {
		return kernel.ManagedPreparationContext{}, ErrInvalidRequest
	}
	var messages []model.Message
	if expected != nil {
		_, _, messages = projectModelPreparation(state, nil, nil)
	}
	return port.ReadManagedPreparationContext(ctx, b, expected, messages)
}

func managedPreparationState(s kernel.Snapshot, state runState) bool {
	if s.Run.Status != kernel.RunStatusRunning || !managedObligationsQuiescent(state) || !knownRootPhysicalSettlement(s) {
		return false
	}
	_, active := activeModelInvocation(state)
	return !active
}

func managedPreparationLedger(state runState, l budget.Ledger) bool {
	for _, inv := range state.ModelInvocations {
		if inv.Status != ModelInvocationConsumed || !managedModelLedgerPhase(inv, l) {
			return false
		}
	}
	return true
}

// A first-owner native preparation point is established BEFORE input reading
// or candidate preparation. It covers no dispatched request. A previously
// elected successor still owns its original transfer, never a new selection.
func (r *Runner) managedPreparationBinding(ctx context.Context, s kernel.Snapshot, state runState) (kernel.ManagedHostBinding, bool) {
	if !r.supportsManagedPreparation() || !r.combinedManaged(state) || r.activeRootGrant == nil ||
		r.activeRunLease == nil || r.activeRootGrant.AdmissionOperation != "start" || s.Run.ParentRunID != "" {
		return kernel.ManagedHostBinding{}, false
	}
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	if frame == nil || frame.invocation.RunID != s.Run.ID || !frame.entered.Load() || frame.receiptOnly {
		return kernel.ManagedHostBinding{}, false
	}
	return r.runtime.ManagedDispatch(ctx, s.Run.ID)
}

func (r *Runner) ensureManagedPreparation(ctx context.Context, s kernel.Snapshot, state runState) (kernel.Snapshot, error) {
	b, ok := r.managedPreparationBinding(ctx, s, state)
	if !ok || !managedPreparationState(s, state) {
		return s, nil
	}
	current, err := r.runtime.LoadManagedRecovery(ctx, s.Run.ID)
	if err == nil && current.State == kernel.ManagedAvailable && current.SafePoint.OriginalGrant == *r.activeRootGrant &&
		kernel.ManagedSourceMatches(current.SafePoint, s) {
		if o, ok := current.SafePoint.AgentObligations(); ok && o.Phase == "prepare" {
			return s, nil
		}
	}
	if err != nil && !kernel.ManagedRecoveryAbsent(err) {
		return s, err
	}
	m := kernel.Mutation{Status: kernel.RunStatusRunning, State: s.State, Checkpoint: s.Checkpoint, ErrorCode: s.Run.ErrorCode, ErrorDetail: s.Run.ErrorDetail,
		Events: []kernel.EventDraft{{Type: "agent.native_preparation.ready", Message: "First-party model preparation frontier"}}}
	next, qualified, err := r.commitManagedPreparation(ctx, s, state, m, b)
	if err != nil || !qualified {
		return s, err
	}
	return next, nil
}

// Commit the actual input cursor and its next preparation boundary together.
// Ineligible (e.g. opaque) execution keeps its existing ordinary CAS.
func (r *Runner) commitManagedInput(ctx context.Context, s kernel.Snapshot, state runState, m kernel.Mutation) (kernel.Snapshot, error) {
	if b, ok := r.managedPreparationBinding(ctx, s, state); ok {
		next, qualified, err := r.commitManagedPreparation(ctx, s, state, m, b)
		if qualified || err != nil {
			return next, err
		}
	}
	return r.runtime.Apply(ctx, s.Run.ID, s.Run.Revision, m)
}

func (r *Runner) commitManagedPreparation(ctx context.Context, s kernel.Snapshot, state runState, m kernel.Mutation, b kernel.ManagedHostBinding) (kernel.Snapshot, bool, error) {
	target := s
	target.State = m.State
	if !managedPreparationState(target, state) {
		return s, false, nil
	}
	p, l, eligible, err := r.freezeManagedSource(ctx, s, state, b)
	if err != nil || !eligible {
		return s, false, err
	}
	if p.LedgerID != "" && !managedPreparationLedger(state, l) {
		return s, false, budget.ErrLedgerConflict
	}
	source, err := r.readManagedPreparationContext(ctx, b, nil, state)
	if err != nil {
		return s, false, err
	}
	p.Phase = "prepare"
	p.PreparationContext = &source
	journal, ok := callbackscope.ManagedPreparationSuspension(ctx, r.nativeContext, r.managedBudgetOwner(),
		strconv.FormatUint(p.SourceRevision, 10), struct {
			Revision, Cursor uint64
			Context          kernel.ManagedPreparationContext
		}{p.SourceRevision, p.InputCursor, source})
	if !ok {
		return s, false, nil
	}
	p.Journal = journal
	raw, err := json.Marshal(p)
	if err != nil {
		return s, false, err
	}
	id := "managed-safe-" + kernel.ManagedDigest(struct {
		Grant, Phase string
		Revision     uint64
	}{r.activeRootGrant.ID, p.Phase, p.SourceRevision})
	next, err := r.applyManagedSafePoint(ctx, s, ModelInvocation{}, m, b, id, kernel.ManagedLifecycleAgentObligations, raw)
	return next, true, err
}
