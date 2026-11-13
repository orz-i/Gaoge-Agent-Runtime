package agent

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ReconcileChildPhysicalGrant verifies the child Feature state AND consumes
// the original Store settlement marker issued after callback completion.
// A clean persisted state alone is insufficient to free a still-live Grant. It must be invoked
// only by the trusted Host recovery/continuation coordinator: it never
// retries Models/Tools or assumes a timed-out external operation did nothing.
// Unsettled provider/Tool intents remain quarantined for authoritative
// receipt recovery or explicit operator reconciliation.
func (runner *Runner) ReconcileChildPhysicalGrant(ctx context.Context, childRunID string) error {
	if runner == nil || runner.runtime == nil {
		return ErrInvalidRequest
	}
	snapshot, err := runner.runtime.Load(ctx, childRunID)
	if err != nil {
		return err
	}
	if snapshot.Run.Kind != RunKind || snapshot.Run.ParentRunID == "" {
		return ErrInvalidRequest
	}
	if !knownAgentPhysicalSettlement(snapshot) {
		return kernel.ErrChildExecutionBusy
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, childRunID)
	if err != nil {
		return err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision, "agent_child_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
	err = runner.runtime.FinishChildExecution(ctx, grant, proof)
	if errors.Is(err, kernel.ErrExecutionConflict) {
		// A clean-looking Run cannot retire the Grant without its original
		// post-callback settlement marker. This remains quarantined, not a
		// generic no-effect conflict eligible for automatic retry.
		return errors.Join(kernel.ErrChildExecutionBusy, err)
	}
	return err
}
