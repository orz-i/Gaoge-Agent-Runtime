package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// commitNativeTransition is the sole ordinary execution-loop mutation
// entrance. Reducers supply only their normal business mutation. Select
// the next eligible managed phase from the actual encoded target and its
// event; attach its proof in the SAME Store CAS, or retain the ordinary
// non-transferable transition when original obligations are still pending.
// Controls and read-only original receipt recovery retain their distinct
// Kernel authorization protocols; they do not pass as fresh execution.
func (r *Runner) commitNativeTransition(ctx context.Context, source kernel.Snapshot, m kernel.Mutation) (kernel.Snapshot, error) {
	point, err := r.qualifyNativeTransition(ctx, source, m)
	if err != nil {
		return source, err
	}
	if point == nil {
		// The initial marker exists solely to persist a qualified frontier.
		// It has no business mutation to commit for an opaque/ineligible
		// frame. Do not add a revision or invalidate a prior point merely
		// because the native driver probed for a preparation qualification.
		if len(m.Events) == 1 && m.Events[0].Type == "agent.native_preparation.ready" {
			return source, nil
		}
		return r.runtime.Apply(ctx, source.Run.ID, source.Run.Revision, m)
	}
	proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "managed_intent_source", r.activePhysicalGrant.ID, source.State, *point)
	return r.runtime.ApplyManagedNativeTransition(ctx, source.Run.ID, source.Run.Revision, m, *point, proof)
}

// commitReturnedState binds the existing receipt-only transaction to its
// original grant, exact source, and exact state/ledger proposal. Callers
// qualify the original result or no-send evidence before using it.
func (r *Runner) commitReturnedState(ctx context.Context, s kernel.Snapshot, g kernel.ChildExecutionGrant, m kernel.Mutation) (kernel.Snapshot, error) {
	source := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, s.State, s.Checkpoint)
	target := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, m.State, kernel.ReceiptRecoveryTarget(m.Checkpoint, m.Ledger))
	return r.runtime.ApplyOwnedPhysicalRecovery(ctx, g, s.Run.Revision, m, source, target)
}

// The existing Run admission selects the original co-located scope. No
// Host resolution or ledger lookup can substitute a different scope.
func (r *Runner) nativeRunBudget(ctx context.Context, id string) *runBudgetAdmission {
	a := r.boundedBudget
	if a == nil {
		return nil
	}
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	if frame == nil || frame.invocation.RunID != id {
		return nil
	}
	bound := frame.budget.Load()
	if bound == nil || bound.adapter != a || bound.ledger.ID == "" {
		return nil
	}
	return bound
}

func (r *Runner) qualifyNativeTransition(ctx context.Context, source kernel.Snapshot, m kernel.Mutation) (*kernel.ManagedSafePoint, error) {
	// Only the original native owner can publish a first-owner point.
	// A selected successor consumes its frozen obligations without another point.
	g := r.activePhysicalGrant
	if m.Status != kernel.RunStatusRunning || !r.SupportsManagedRecovery() || g == nil ||
		r.activeRunLease == nil || source.Run.ParentRunID != "" || g.ChildRunID != source.Run.ID ||
		g.AdmissionOperation == "managed_resume" {
		return nil, nil
	}
	state, err := decodeState(m.State)
	if err != nil {
		return nil, err
	}
	if len(m.Events) == 1 {
		switch m.Events[0].Type {
		case "agent.model_invocation.pending", "agent.model_invocation.completed":
			inv, ok := activeModelInvocation(state)
			if !ok {
				return nil, ErrInvalidRequest
			}
			return r.qualifyManagedModelBoundary(ctx, source, state, *inv, m.Ledger)
		case EventContextSummaryConsumed:
			// Its exact bound artifact/checkpoint installation proof is
			// retained, including the existing pure Summary profile.
			if len(state.ModelInvocations) == 0 {
				return nil, ErrInvalidRequest
			}
			return r.qualifyManagedModelBoundary(ctx, source, state, state.ModelInvocations[len(state.ModelInvocations)-1], m.Ledger)
		}
	}
	if managedPreparationState(m.Status, state) {
		if b, ok := r.managedPreparationBinding(ctx, source); ok {
			return r.qualifyManagedPreparation(ctx, source, state, m, b)
		}
	}
	return nil, nil
}
