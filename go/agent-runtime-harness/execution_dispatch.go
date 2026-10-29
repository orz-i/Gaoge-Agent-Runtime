package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ExecutionDispatchParentItemID marks a physical Feature Start/Resume grant.
// New grants and cancellation intents serialize at the same existing Turn
// Store boundary. An unfinished grant prevents a competing cancellation from
// claiming it stopped the Feature; a cancelled Turn cannot admit a late grant.
const ExecutionDispatchParentItemID = "harness.execution.dispatch"

func PendingExecutionDispatch(items []Item, invocationID string) bool {
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemCompleted {
			closed[item.ParentItemID] = struct{}{}
		}
	}
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if invocationID != "" && item.InvocationID != invocationID {
			continue
		}
		if _, settled := closed[item.ID]; !settled {
			return true
		}
	}
	return false
}

func (runner *Runner) pendingExecutionDispatch(
	ctx context.Context, turnID string, targets map[string]struct{}, targetRuns map[string]struct{},
) (bool, error) {
	items, err := listAllItems(ctx, runner.store, turnID)
	if err != nil {
		return false, err
	}
	closed := make(map[string]struct{})
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.Status == ItemCompleted {
			closed[item.ParentItemID] = struct{}{}
		}
	}
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if _, done := closed[item.ID]; done {
			continue
		}
		var phase struct {
			Phase         string `json:"phase"`
			PhysicalRunID string `json:"physicalRunID"`
		}
		if json.Unmarshal(item.Payload, &phase) != nil {
			return false, ErrConflict
		}
		if targets != nil {
			_, matchedOwner := targets[item.InvocationID]
			_, matchedRun := targetRuns[phase.PhysicalRunID]
			if !matchedOwner && (phase.PhysicalRunID == "" || !matchedRun) {
				continue
			}
		}
		if phase.Phase == "start" || phase.Phase == "start-retry" {
			// A Kernel Run that is already durably running/completed belongs
			// to Kernel control, whose cancellation CAS can now fence it.
			// Before the Run exists, the Feature may still be about to
			// CREATE it, so the pending grant must block cancellation.
			if started, loadErr := runner.runtime.Load(ctx, item.RunID); loadErr == nil &&
				started.Run.ID == item.RunID {
				continue
			}
		}
		return true, nil
	}
	return false, nil
}

// withPhysicalInvocationDispatch is the single Harness physical Start/Resume
// boundary. The grant is durable BEFORE any Feature call and competes with
// cancellation-intent admission in the same Store transaction. It is closed
// only when a returned Kernel snapshot proves which Run was reached. An
// ambiguous no-snapshot outcome stays fenced for explicit reconciliation.
func (runner *Runner) withPhysicalInvocationDispatch(
	ctx context.Context, invocation Invocation, phase string,
	physical func() (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	return runner.withPhysicalInvocationDispatchFor(ctx, invocation, phase, invocation.ExecutionRefID, physical)
}

// Workflow child Wait resolution may mutate a proven descendant Run rather
// than the owning Harness Invocation's root Run. The durable grant still
// belongs to the owning Invocation for cancellation arbitration.
func (runner *Runner) withPhysicalInvocationDispatchFor(
	ctx context.Context, invocation Invocation, phase, resultRunID string,
	physical func() (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	if runner == nil || runner.store == nil || strings.TrimSpace(invocation.ID) == "" ||
		invocation.TurnID == "" || invocation.ExecutionRefID == "" || physical == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	identifier, err := runner.runtime.NewID("dispatch")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	payload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID,omitempty"`
	}{Phase: phase, PhysicalRunID: resultRunID})
	if err != nil {
		return kernel.Snapshot{}, err
	}
	now := runner.clock.Now().UTC()
	grant := Item{
		ID:     stableID("hdispatch", invocation.ID, invocation.ExecutionRefID, identifier),
		TurnID: invocation.TurnID, InvocationID: invocation.ID, RunID: invocation.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID, Payload: payload,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, appendErr := runner.store.AppendItem(ctx, grant); appendErr != nil || !fresh {
		return kernel.Snapshot{}, errors.Join(ErrConflict, appendErr)
	}
	// The first-party relation may have been committed before the parent was
	// cancelled, but a nested child can still be Accepted without a Kernel
	// Run. Do not physically start it after its owning parent completed or
	// was cancelled. This read occurs AFTER the durable grant: a concurrent
	// cancel must now contend with the outstanding dispatch.
	if parentErr := runner.requireLiveInvocationParents(ctx, invocation); parentErr != nil {
		return kernel.Snapshot{}, errors.Join(parentErr,
			runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_rejected"))
	}
	snapshot, physicalErr := physical()
	if snapshot.Run.ID == "" {
		// An arbitrary Feature may have dispatched remote side effects
		// before returning a generic error, including a Kernel CAS conflict.
		// Only the concrete first-party Agent control can certify that its
		// own preflight failed BEFORE invoking middleware or ApplyControl.
		// Never accept that marker from a custom wrapper or from a general
		// physical Start/Resume path.
		_, nativeAgent := runner.agent.(*agent.Runner)
		control := phase == "resolve-approval" || phase == "resolve-subtask-approval" ||
			phase == "grant-call-allowance"
		if nativeAgent && control && agent.IsNativeControlNoEffect(physicalErr) {
			return snapshot, errors.Join(physicalErr,
				runner.finishPhysicalDispatchGrant(ctx, grant, "native_agent_control_no_effect"))
		}
		// Unknown effects retain the grant for explicit reconciliation.
		return snapshot, physicalErr
	}
	if snapshot.Run.ID != resultRunID {
		return snapshot, errors.Join(physicalErr, ErrConflict)
	}
	return snapshot, errors.Join(physicalErr, runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_returned"))
}

// The release is appended under the same immutable grant ID, including after
// caller cancellation. A duplicate ACK has the same deterministic identity.
func (runner *Runner) requireLiveInvocationParents(ctx context.Context, invocation Invocation) error {
	if invocation.ParentItemID == "" {
		return nil
	}
	items, err := listAllItems(ctx, runner.store, invocation.TurnID)
	if err != nil {
		return err
	}
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	seen := make(map[string]struct{})
	checked := make(map[string]struct{})
	for id := invocation.ParentItemID; id != ""; {
		if _, cycle := seen[id]; cycle {
			return ErrConflict
		}
		seen[id] = struct{}{}
		item, exists := byID[id]
		if !exists || item.TurnID != invocation.TurnID {
			return ErrConflict
		}
		if ownerID := item.InvocationID; ownerID != "" && ownerID != invocation.ID {
			if _, checkedOwner := checked[ownerID]; !checkedOwner {
				parent, loadErr := runner.store.GetInvocation(ctx, ownerID)
				if loadErr != nil || parent.TurnID != invocation.TurnID ||
					terminalInvocationStatus(parent.Status) {
					return errors.Join(ErrConflict, loadErr)
				}
				if parent.ExecutionClass != ExecutionApplication && parent.ExecutionRefID != "" {
					state, runtimeErr := runner.runtime.Load(ctx, parent.ExecutionRefID)
					if runtimeErr != nil || terminalRuntimeStatus(state.Run.Status) {
						return errors.Join(ErrConflict, runtimeErr)
					}
				}
				checked[ownerID] = struct{}{}
			}
		}
		id = item.ParentItemID
	}
	return nil
}

func (runner *Runner) finishPhysicalDispatchGrant(ctx context.Context, grant Item, boundary string) error {
	now := runner.clock.Now().UTC()
	payload, err := json.Marshal(struct {
		Boundary string `json:"boundary"`
	}{Boundary: boundary})
	if err != nil {
		return err
	}
	_, _, err = runner.store.AppendItem(context.WithoutCancel(ctx), Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		ParentItemID: grant.ID,
		Payload:      payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}
