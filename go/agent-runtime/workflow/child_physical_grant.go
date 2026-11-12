package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
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
	physical func(*Runner) (kernel.Snapshot, error)) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil || physical == nil || snapshot.Run.ID == "" {
		return kernel.Snapshot{}, ErrInvalidExecution
	}
	executionRuntime := runner.runtime
	if snapshot.Run.ParentRunID != "" {
		// A built-in child invoked from a Root's scoped interpreter must NOT
		// inherit its parent's mutation permit. Its own immutable parent ID
		// and separate physical Grant control all subsequent child writes.
		executionRuntime = runner.runtime.WithoutPhysicalGrant()
	}
	grant, err := executionRuntime.BeginChildExecution(ctx, snapshot.Run.ID, snapshot.Run.Revision)
	if err != nil {
		return snapshot, err
	}
	// Even a write failure returned by a Store wrapper can follow a
	// committed intent CAS. The first-party interpreter tracks whether it
	// ever reached any physical Effect callback. Only a segment that never
	// reached the callback has a strong, source-local no-effect witness.
	calls := new(atomic.Uint32)
	scoped := *runner
	scoped.runtime = executionRuntime
	scoped.physicalCalls = calls
	// Root and parented Workflow share the SAME Feature-owned per-revision
	// mutation permit. A child may not bypass its own Grant by using the
	// unscoped Kernel Runtime, including when executing compensations.
	proof := featurecontrol.Seal(snapshot.Run.ID, snapshot.Run.Revision,
		"workflow_physical_owner", grant.ID, snapshot.State, snapshot.Checkpoint)
	bound, bindErr := executionRuntime.WithPhysicalGrant(ctx, grant, proof)
	if bindErr != nil {
		return snapshot, errors.Join(kernel.ErrChildExecutionBusy, bindErr)
	}
	scoped.runtime = bound
	physicalRunner := &scoped

	// A panic, pending unknown remote effect, or callback error without a
	// trustworthy physical receipt leaves the original Grant durable. Do
	// not release on a timer or an unverified child ID.
	result, physicalErr := physical(physicalRunner)
	settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	latest, loadErr := executionRuntime.Load(settlementCtx, snapshot.Run.ID)
	noPhysicalDispatch := calls.Load() == 0
	if loadErr != nil || (noPhysicalDispatch && latest.Run.ID != snapshot.Run.ID) ||
		(!noPhysicalDispatch && !runner.knownWorkflowPhysicalSettlement(settlementCtx, latest, physicalErr)) {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, loadErr)
	}
	if noPhysicalDispatch {
		// A *known* no-effect, pre-dispatch segment is authorized to retire
		// its own Grant even if a journal/intent CAS failed or its ACK was
		// lost. Never infer this from a mere RunStatus or an arbitrary
		// executor error: the physical counter was incremented before every
		// statically composed external Effect callback.
		if _, stateErr := decodeExecutionState(latest.State); stateErr != nil {
			return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, stateErr)
		}
	}
	proof = featurecontrol.Seal(latest.Run.ID, latest.Run.Revision,
		"workflow_child_physical_settled", grant.ID, latest.State, latest.Checkpoint)
	if err := executionRuntime.MarkChildExecutionSettled(settlementCtx, grant, proof); err != nil {
		return result, errors.Join(physicalErr, kernel.ErrChildExecutionBusy, err)
	}
	if err := executionRuntime.FinishChildExecution(settlementCtx, grant, proof); err != nil {
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
		!errors.Is(physicalErr, ErrEffectFailed) &&
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
				if !errors.Is(physicalErr, ErrSegmentYielded) &&
					(!errors.Is(physicalErr, ErrEffectPending) || !effect.PhysicalNoEffect ||
						effect.PhysicalNoEffectAttempt != effect.Attempt-1 || effect.ErrorCode == "") {
					return false
				}
				continue
			}
			if runner.relations == nil {
				// The built-in Subworkflow executor is an immutable local
				// Workflow feature, not a dynamically discovered Host effect.
				// A standalone SDK Workflow may have no relation recorder.
				// Prove its deterministic child ID and exact frozen definition,
				// input, actor and Kernel ParentRunID without synthesizing a
				// foreign/remote relation. Agent/custom effects still fail closed.
				if effect.Class != EffectClassSubworkflow || runner.registry == nil ||
					effect.Definition == nil {
					return false
				}
				child, loadErr := runner.runtime.Load(ctx, effect.ChildRunID)
				if loadErr != nil {
					return false
				}
				if !verifiedSubworkflowChildOrigin(EffectRequest{
					RunID: snapshot.Run.ID, EffectID: effect.ID,
					Actor: snapshot.Run.Actor, Thread: snapshot.Run.Thread,
					Definition:  effect.Definition,
					NestedDepth: effect.NestedDepth, Input: effect.Input,
				}, child) {
					return false
				}
				continue
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
			// A failed original attempt with an explicit Host-attested
			// pre-dispatch NoEffect is not unknown physical work.
			// Never apply this to generic failures or timeouts.
			if !effect.PhysicalNoEffect || effect.PhysicalNoEffectAttempt != effect.Attempt || effect.ErrorCode == "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
