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

// executionDispatchReceiptIndex preserves every claimed settlement, including
// malformed and conflicting ones. Dropping a second receipt or an incorrect
// owner would turn inconsistent physical execution evidence into authority.
func executionDispatchReceiptIndex(items []Item) map[string][]Item {
	byGrant := make(map[string][]Item)
	for _, item := range items {
		if item.Kind == ItemDiagnostic && item.ParentItemID != "" &&
			item.ParentItemID != ExecutionDispatchParentItemID {
			byGrant[item.ParentItemID] = append(byGrant[item.ParentItemID], item)
		}
	}
	return byGrant
}

// VerifiedExecutionDispatchReceipt is the single durable grant-settlement
// contract used by the Memory and SQL Stores, retry, cancellation and native
// recovery. A physical grant has exactly ONE recognized completion fact,
// committed later in the same Turn with the exact Invocation and Run owner.
// A foreign/unknown/contradictory completion is NOT a settlement.
func VerifiedExecutionDispatchReceipt(grant Item, receipts []Item) (string, bool) {
	if grant.ID == "" || grant.Seq == 0 || grant.Kind != ItemDiagnostic ||
		grant.Status != ItemStarted || grant.ParentItemID != ExecutionDispatchParentItemID ||
		grant.TurnID == "" || grant.InvocationID == "" || grant.RunID == "" {
		return "", false
	}
	var receipt Item
	count := 0
	for _, item := range receipts {
		if item.Kind != ItemDiagnostic || item.ParentItemID != grant.ID {
			continue
		}
		count++
		receipt = item
	}
	if count != 1 || receipt.ID == "" || receipt.Status != ItemCompleted ||
		receipt.TurnID != grant.TurnID || receipt.InvocationID != grant.InvocationID ||
		receipt.RunID != grant.RunID || receipt.Seq <= grant.Seq {
		return "", false
	}
	var payload struct {
		Boundary string `json:"boundary"`
	}
	if json.Unmarshal(receipt.Payload, &payload) != nil {
		return "", false
	}
	switch payload.Boundary {
	case "physical_dispatch_rejected", "feature_start_no_effect", "native_agent_start_no_effect",
		"native_agent_control_no_effect", "physical_dispatch_returned", "handoff_start_returned",
		"native_kernel_epoch_recovered":
		return payload.Boundary, true
	default:
		return "", false
	}
}

func PendingExecutionDispatch(items []Item, invocationID string) bool {
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if invocationID != "" && item.InvocationID != invocationID {
			continue
		}
		if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); !settled {
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
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.Kind != ItemDiagnostic || item.Status != ItemStarted ||
			item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if _, done := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); done {
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
		if (phase.Phase == "start" || phase.Phase == "start-retry") &&
			runner.nativeAgentStartOwnedByKernel(ctx, item, phase.PhysicalRunID) {
			// Only the exact first-party Agent with an authoritative
			// Kernel Run can be cancelled before its Start receipt. Native
			// Agent execution checks Kernel CAS (and Epoch when configured)
			// before continuing; composite Features can still create children
			// after their parent Kernel Run has been created.
			continue
		}
		return true, nil
	}
	return false, nil
}

// nativeAgentStartOwnedByKernel is the sole exception to an outstanding
// physical Start grant blocking cancellation. Run existence is not sufficient:
// only a concrete first-party Agent, exact Root Invocation ownership and
// native Agent Run can use Kernel cancellation CAS (and an acquired native
// execution Epoch where present). A custom Agent, composite Feature or
// delegated child never inherits this exception.
func (runner *Runner) nativeAgentStartOwnedByKernel(ctx context.Context, grant Item, physicalRunID string) bool {
	if runner == nil || runner.runtime == nil || runner.store == nil || physicalRunID == "" ||
		physicalRunID != grant.RunID || grant.InvocationID == "" {
		return false
	}
	native, ok := runner.agent.(*agent.Runner)
	if !ok || !native.NativeStartCancellationFencedByKernel(runner.runtime) {
		return false
	}
	// A custom or separately bound RuntimeCanceller cannot claim that
	// cancelling this Kernel Run fences the native Agent's execution.
	canceller, nativeCancel := runner.cancellation.(*kernel.Runtime)
	if !nativeCancel || canceller != runner.runtime {
		return false
	}
	owner, err := runner.store.GetInvocation(ctx, grant.InvocationID)
	if err != nil || owner.ID != grant.InvocationID || owner.TurnID != grant.TurnID ||
		owner.ParentItemID != "" || owner.ExecutionClass != ExecutionAgent ||
		owner.ExecutionRefID != grant.RunID {
		return false
	}
	started, err := runner.runtime.Load(ctx, grant.RunID)
	if err != nil || started.Run.ID != grant.RunID ||
		started.Run.Kind != agent.RunKind || started.Run.Status != kernel.RunStatusRunning {
		return false
	}
	// Native Agent Runs can be unleased (Kernel CAS) or require a bound
	// Epoch (Kernel Store fencing). Either case has a first-party native
	// continuation guard. Any other Store error gives no cancel authority.
	proof := runner.runtime.RequireExecution(ctx, grant.RunID)
	return proof == nil || errors.Is(proof, kernel.ErrExecutionRequired)
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
	if physical == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	grant, err := runner.beginPhysicalInvocationDispatch(ctx, invocation, phase, resultRunID)
	if err != nil {
		return kernel.Snapshot{}, err
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

// beginPhysicalInvocationDispatch is shared by Kernel Feature operations and
// Role Handoff starts. Admission is serialized with cancellation and retry by
// the Store; the frozen root/ancestor Attempt check happens after the grant,
// before calling any physical executor. A failed pre-effect check settles the
// grant as definitive no-effect, without ever invoking the executor.
func (runner *Runner) beginPhysicalInvocationDispatch(
	ctx context.Context, invocation Invocation, phase, physicalRunID string,
) (Item, error) {
	if runner == nil || runner.store == nil || runner.runtime == nil || runner.clock == nil ||
		strings.TrimSpace(invocation.ID) == "" || invocation.TurnID == "" ||
		invocation.ExecutionRefID == "" || strings.TrimSpace(physicalRunID) == "" {
		return Item{}, ErrInvalidRequest
	}
	identifier, err := runner.runtime.NewID("dispatch")
	if err != nil {
		return Item{}, err
	}
	payload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID,omitempty"`
	}{Phase: phase, PhysicalRunID: physicalRunID})
	if err != nil {
		return Item{}, err
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
		return Item{}, errors.Join(ErrConflict, appendErr)
	}
	if parentErr := runner.requireLiveInvocationParents(ctx, invocation); parentErr != nil {
		return Item{}, errors.Join(parentErr,
			runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_rejected"))
	}
	return grant, nil
}

// The release is appended under the same immutable grant ID, including after
// caller cancellation. A duplicate ACK has the same deterministic identity.
func (runner *Runner) requireLiveInvocationParents(ctx context.Context, invocation Invocation) error {
	if invocation.ParentItemID == "" {
		return nil
	}
	// The Input was sealed at the first admission. A parent's stable ID
	// becoming Running on Attempt 2 cannot authorize an older descendant.
	proof, err := frozenChildRoot(invocation)
	if err != nil {
		return err
	}
	expected := make(map[string]childAttemptBinding, len(proof.Parents))
	for _, bound := range proof.Parents {
		expected[bound.InvocationID] = bound
	}
	seen := make(map[string]struct{})
	for id := invocation.ParentItemID; id != ""; {
		if _, cycle := seen[id]; cycle {
			return ErrConflict
		}
		seen[id] = struct{}{}
		item, loadErr := runner.store.GetItem(ctx, id)
		if errors.Is(loadErr, ErrNotFound) || errors.Is(loadErr, ErrInvalidRequest) {
			return ErrConflict
		}
		if loadErr != nil {
			return loadErr
		}
		if item.ID != id || item.TurnID != invocation.TurnID || item.Seq == 0 {
			return ErrConflict
		}
		if ownerID := item.InvocationID; ownerID != "" && ownerID != invocation.ID {
			bound := proof.attempt()
			if ownerID != proof.InvocationID {
				var found bool
				bound, found = expected[ownerID]
				if !found {
					return ErrConflict // Unfrozen ancestor cannot grant authority.
				}
			}
			if err := runner.requireItemOwnerAttempt(ctx, item, bound); err != nil {
				return err
			}
		}
		id = item.ParentItemID
	}
	// An Item ancestry may end at a Host delegation boundary. Validate ALL
	// inherited owners even when no Item in this immediate chain names them.
	for _, bound := range proof.Parents {
		if err := runner.requireLiveParentAttempt(ctx, invocation.TurnID, bound); err != nil {
			return err
		}
	}
	root, err := runner.loadFrozenChildRoot(ctx, invocation)
	if err != nil {
		return err
	}
	if root.ExecutionClass == ExecutionApplication {
		if unclaimedApplicationParent(root) {
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
