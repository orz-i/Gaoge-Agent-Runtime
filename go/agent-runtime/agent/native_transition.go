package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// nativeTransitionTarget represents the aggregate the ordinary Kernel CAS
// will commit. Its authority is the original scoped Agent callback, not the
// fact that this derived target happens to look quiescent.
func nativeTransitionTarget(source kernel.Snapshot, m kernel.Mutation) kernel.Snapshot {
	target := source
	target.Run.Status = m.Status
	target.State, target.Checkpoint, target.Result = m.State, m.Checkpoint, m.Result
	return target
}

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
	proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "managed_intent_source", r.activeRootGrant.ID, source.State, *point)
	return r.runtime.ApplyManagedNativeTransition(ctx, source.Run.ID, source.Run.Revision, m, *point, proof)
}

func (r *Runner) qualifyNativeTransition(ctx context.Context, source kernel.Snapshot, m kernel.Mutation) (*kernel.ManagedSafePoint, error) {
	if m.Status != kernel.RunStatusRunning || (!r.SupportsManagedContextRecovery() && !r.SupportsSummaryNativeRecovery()) {
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
			return r.qualifyManagedModelBoundary(ctx, source, state, *inv, m)
		case EventContextSummaryConsumed:
			// Its exact bound artifact/checkpoint installation proof is
			// retained, including the existing pure Summary profile.
			return r.qualifySummaryBoundary(ctx, source, state)
		}
	}
	if managedPreparationState(nativeTransitionTarget(source, m), state) {
		if b, ok := r.managedPreparationBinding(ctx, source, state); ok {
			return r.qualifyManagedPreparation(ctx, source, state, m, b)
		}
	}
	return nil, nil
}
