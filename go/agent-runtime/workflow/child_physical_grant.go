package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// withChildPhysicalGrant encloses the complete physical Workflow segment:
// Run effects, compensation and all nested Subworkflow dispatch. A parented
// Workflow may not dispatch a Host Effect after a terminal ancestor committed.
// The original Store's admission/terminal CAS share one transaction; reads of
// parent Status and RunRelation are secondary provenance, not execution grants.
func (runner *Runner) withChildPhysicalGrant(ctx context.Context, snapshot kernel.Snapshot,
	physical func() (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || physical == nil || snapshot.Run.ID == "" {
		return kernel.Snapshot{}, ErrInvalidExecution
	}
	if snapshot.Run.ParentRunID == "" {
		return physical()
	}
	grant, err := runner.runtime.BeginChildExecution(ctx, snapshot.Run.ID)
	if err != nil {
		return snapshot, err
	}

	// A panic, pending unknown remote effect, or callback error without a
	// trustworthy physical receipt leaves the original Grant durable. Do
	// not release on a timer or an unverified child ID.
	result, physicalErr := physical()
	settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := runner.runtime.Load(settlementCtx, snapshot.Run.ID)
	if loadErr != nil || !runner.knownWorkflowPhysicalSettlement(settlementCtx, latest, physicalErr) {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, loadErr)
	}
	proof := featurecontrol.Seal(latest.Run.ID, latest.Run.Revision,
		"workflow_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if err := runner.runtime.MarkChildExecutionSettled(settlementCtx, grant, proof); err != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, err)
	}
	if err := runner.runtime.FinishChildExecution(settlementCtx, grant, proof); err != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, err)
	}
	return result, physicalErr
}

// A Workflow's physical callback returning does not prove that any external
// asynchronous Effect is finished. All dispatched Effects must have a
// persisted terminal receipt, or a statically verified same-Kernel child
// whose own physical Grant fences in-flight work. A newly scheduled pending
// Effect with no receipt may be safely left in the durable state only on an
// explicit segment-yield boundary before its dispatch.
func (runner *Runner) knownWorkflowPhysicalSettlement(ctx context.Context, snapshot kernel.Snapshot, physicalErr error) bool {
	if snapshot.Run.Kind != RunKind || snapshot.Run.ID == "" {
		return false
	}
	if physicalErr != nil && !errors.Is(physicalErr, ErrWaitPending) &&
		!errors.Is(physicalErr, ErrSegmentYielded) &&
		!errors.Is(physicalErr, ErrEffectPending) {
		return false
	}
	state, err := decodeExecutionState(snapshot.State)
	if err != nil {
		return false
	}
	for _, effect := range state.Effects {
		switch effect.Status {
		case EffectCompleted:
			if effect.ReceiptID == "" || !json.Valid(effect.Output) {
				return false
			}
		case EffectPending:
			if effect.ChildRunID == "" {
				// A SegmentYielded effect may be scheduled but not
				// dispatched. ErrEffectPending is an observed external
				// pending outcome and requires separate trusted evidence.
				if !errors.Is(physicalErr, ErrSegmentYielded) {
					return false
				}
				continue
			}
			if runner.relations == nil {
				return false
			}
			reader, ok := runner.relations.(interface {
				GetByChild(context.Context, string) (runrelation.Relation, error)
			})
			if !ok {
				return false
			}
			relation, loadErr := reader.GetByChild(ctx, effect.ChildRunID)
			if loadErr != nil || relation.ParentRunID != snapshot.Run.ID {
				return false
			}
			child, loadErr := runner.runtime.Load(ctx, effect.ChildRunID)
			if loadErr != nil || !runner.VerifyLocalChild(runner.runtime, snapshot, relation, child, false) {
				return false
			}
		case EffectFailed:
			// Failure without a trusted completed physical receipt
			// remains outcome-unknown even if the Kernel Run is terminal.
			return false
		default:
			return false
		}
	}
	return true
}
