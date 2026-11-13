package workflow

import (
	"context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ReconcileChildPhysicalGrant recovers ONLY an already completed original
// Workflow physical callback. It checks the persisted Effect/Wait receipts
// without dispatching a new Effect, then tries to retire the exact original
// Store Grant. Finish requires a prior durable first-party Mark receipt:
// a clean-looking State alone cannot release an active callback's Grant.
//
// Unlike Agent, Workflow also covers its own Root Run. This is the same
// durable physical protocol for root and parented Workflow executions.
func (runner *Runner) ReconcileChildPhysicalGrant(ctx context.Context, runID string) error {
	if runner == nil || runner.runtime == nil || runID == "" {
		return ErrInvalidExecution
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return err
	}
	if snapshot.Run.Kind != RunKind || !runner.knownWorkflowPhysicalSettlement(ctx, snapshot, nil) {
		return kernel.ErrChildExecutionBusy
	}
	grant, err := runner.runtime.LookupChildExecution(ctx, runID)
	if err != nil {
		return err
	}
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision,
		"workflow_child_physical_settled", grant.ID, snapshot.State, snapshot.Checkpoint)
	err = runner.runtime.FinishChildExecution(ctx, grant, proof)
	if errors.Is(err, kernel.ErrExecutionConflict) {
		// A callback that has not itself durably marked completion, even
		// when the persisted state is already clean, MUST stay quarantined.
		return errors.Join(kernel.ErrChildExecutionBusy, err)
	}
	return err
}
