package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// ErrExecutionNoEffect may be returned by a host Feature StartRun ONLY when
// the Feature can certify it issued no external effect and created no Kernel
// Run. A generic error (including kernel.ErrConflict) is never such proof.
// It permits a failed Invocation only after the Harness persists the grant's
// no-effect completion receipt, so an uncertain receipt remains fenced.
var ErrExecutionNoEffect = errors.New("harness feature start definitively performed no effect")

// FeatureStartNoEffect preserves the original, host-visible failure while
// explicitly certifying that StartRun did not create a Run or dispatch an
// external effect. Hosts must never use it for timeouts or unknown outcomes.
func FeatureStartNoEffect(cause error) error {
	if cause == nil {
		return ErrExecutionNoEffect
	}
	return featureStartNoEffect{cause: cause}
}

type featureStartNoEffect struct{ cause error }

func (e featureStartNoEffect) Error() string      { return e.cause.Error() }
func (e featureStartNoEffect) Unwrap() error      { return e.cause }
func (featureStartNoEffect) Is(target error) bool { return target == ErrExecutionNoEffect }

// ErrExecutionOutcomeUnknown means a Feature Start returned no authoritative
// Kernel Run snapshot without a proven no-effect outcome. The original Run ID
// and grant must be retained for reconciliation; Retry cannot rotate them.
var ErrExecutionOutcomeUnknown = errors.New("harness physical feature start outcome is unknown")

type dispatchProvenNoEffect struct{ cause error }

func (e dispatchProvenNoEffect) Error() string { return e.cause.Error() }
func (e dispatchProvenNoEffect) Unwrap() error { return e.cause }

func isDispatchProvenNoEffect(err error) bool {
	_, ok := err.(dispatchProvenNoEffect) //nolint:errorlint // Direct Harness-minted proof, not a wrapped error.
	return ok
}

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
		// A direct first-party Agent pre-middleware/pre-Create failure is
		// also definitive. Its private result cannot be minted by a
		// custom Feature or returned from an effectful Run middleware.
		// Every other no-snapshot Agent error remains outcome-unknown.
		if nativeAgent && (phase == "start" || phase == "start-retry") && agent.IsNativeStartNoEffect(physicalErr) {
			if receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "native_agent_start_no_effect"); receiptErr == nil {
				return snapshot, dispatchProvenNoEffect{cause: physicalErr}
			} else {
				return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr, receiptErr)
			}
		}
		// The public Host Feature no-effect contract applies to custom
		// Features/Agents, never to an exact native *agent.Runner Start. A
		// native Run Middleware can perform effects and return that public
		// marker; only the private direct pre-middleware proof above may
		// discharge the native Agent's physical grant.
		customFeatureNoEffect := !nativeAgent || invocation.ExecutionClass != ExecutionAgent
		if customFeatureNoEffect && (phase == "start" || phase == "start-retry") && errors.Is(physicalErr, ErrExecutionNoEffect) {
			if receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "feature_start_no_effect"); receiptErr == nil {
				return snapshot, dispatchProvenNoEffect{cause: physicalErr}
			} else {
				return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr, receiptErr)
			}
		}
		// Unknown effects retain the grant and its exact execution identity
		// for explicit reconciliation, including errors without a snapshot.
		return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr)
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
	seen := make(map[string]struct{})
	checked := make(map[string]struct{})
	for id := invocation.ParentItemID; id != ""; {
		if _, cycle := seen[id]; cycle {
			return ErrConflict
		}
		seen[id] = struct{}{}
		// Fetch only the actual parent chain. Full Turn scans cost O(all
		// Items) per effect boundary even when ancestry is one Item deep.
		item, loadErr := runner.store.GetItem(ctx, id)
		if errors.Is(loadErr, ErrNotFound) || errors.Is(loadErr, ErrInvalidRequest) {
			return ErrConflict
		}
		if loadErr != nil {
			return loadErr
		}
		if item.ID != id || item.TurnID != invocation.TurnID || item.Seq == 0 {
			// A missing/foreign/corrupt parent cannot confer physical
			// execution authority. The old full-page cursor required
			// positive persisted Seq values as well.
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
	// An Item is allowed to describe Host-owned work without an InvocationID.
	// Such a parent does not prove that the owning root execution is live.
	// Recheck the one authoritative root regardless of which ancestor Items
	// carried an Invocation owner; this is the last pre-effect fence after
	// the physical grant (or the Application external-effect claim) commits.
	root, err := runner.loadFrozenChildRoot(ctx, invocation)
	if err != nil {
		return err
	}
	if root.ExecutionClass == ExecutionApplication {
		// Host-only Application roots have no Kernel Run. Their frozen
		// Invocation and Attempt, not a fabricated Run, are authority.
		if root.Status == InvocationAccepted {
			return ErrConflict
		}
		return nil
	}
	if runner.runtime == nil || strings.TrimSpace(root.ExecutionRefID) == "" {
		return ErrConflict
	}
	state, err := runner.runtime.Load(ctx, root.ExecutionRefID)
	if err != nil || state.Run.ID != root.ExecutionRefID || terminalRuntimeStatus(state.Run.Status) {
		return errors.Join(ErrConflict, err)
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
	// The caller may have disconnected while the physical Feature returned.
	// Preserve the completion opportunity, but never wait unboundedly on a
	// blocked Store row. An expired/lost ACK is not a settled grant.
	settlementCtx, stopSettlement := cancellationSettlementContext(ctx)
	defer stopSettlement()
	_, _, err = runner.store.AppendItem(settlementCtx, Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		ParentItemID: grant.ID,
		Payload:      payload, CreatedAt: now, UpdatedAt: now,
	})
	return errors.Join(err, settlementCtx.Err())
}
