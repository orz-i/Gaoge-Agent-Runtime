package harness

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// DeferredChildGrantCancellationParentItemID is an observational fact, NOT a
// completion receipt or another authorization to dispatch. Its original
// intent is still pending and fences the Turn until full cleanup succeeds.
const DeferredChildGrantCancellationParentItemID = "harness.cancel.deferred.child_grant"

// ValidDeferredChildGrantCancellationProofAdmission reserves the exact
// original-no-effect deferral namespace at the same Memory/SQL Item write
// boundary as the older completion proofs. A public Artifact or ordinary
// Diagnostic must not preoccupy a predictable cancellation recovery ID.
func ValidDeferredChildGrantCancellationProofAdmission(item Item) bool {
	if item.ParentItemID != DeferredChildGrantCancellationParentItemID ||
		item.Kind != ItemDiagnostic || item.Status != ItemCompleted ||
		item.TurnID == "" || item.RunID == "" || item.InvocationID != "" {
		return false
	}
	fields, ok := authorityStringFields(item.Payload, "intentID", "boundary")
	return ok && fields["intentID"] != "" &&
		fields["boundary"] == "kernel_child_grant_parent_cancel_no_effect" &&
		item.ID == stableID("hcancelchildbusy", fields["intentID"])
}

// This evidence is emitted only after an original Kernel ChildExecutionStore
// rejected parent Cancel BEFORE ANY root mutation. Unlike an arbitrary error
// from a Host Canceller it is a direct, source-restricted no-effect proof.
func (runner *Runner) recordDeferredChildGrantCancellation(
	ctx context.Context, turn Turn, intent Item, runID string,
) error {
	if intent.ID == "" || !ValidCancellationFenceIntentAdmission(intent) ||
		intent.RunID != runID || intent.TurnID != turn.ID || intent.Seq == 0 {
		return ErrConflict
	}
	now := runner.clock.Now().UTC()
	payload, err := json.Marshal(struct {
		IntentID string `json:"intentID"`
		Boundary string `json:"boundary"`
	}{IntentID: intent.ID, Boundary: "kernel_child_grant_parent_cancel_no_effect"})
	if err != nil {
		return err
	}
	_, _, err = runner.store.AppendItem(ctx, Item{
		ID: stableID("hcancelchildbusy", intent.ID), TurnID: turn.ID,
		Kind: ItemDiagnostic, Status: ItemCompleted, RunID: runID,
		ParentItemID: DeferredChildGrantCancellationParentItemID,
		Payload:      payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func validDeferredChildGrantCancellation(item Item, intent Item) bool {
	if item.ID != stableID("hcancelchildbusy", intent.ID) ||
		item.ParentItemID != DeferredChildGrantCancellationParentItemID ||
		item.Kind != ItemDiagnostic || item.Status != ItemCompleted ||
		item.TurnID != intent.TurnID || item.RunID != intent.RunID ||
		item.InvocationID != "" || item.Seq <= intent.Seq {
		return false
	}
	fields, ok := authorityStringFields(item.Payload, "intentID", "boundary")
	return ok && fields["intentID"] == intent.ID &&
		fields["boundary"] == "kernel_child_grant_parent_cancel_no_effect"
}

// resumeDeferredChildGrantCancellation only resumes the SAME elected root
// cancellation (not a Subtask or remote Handoff) after cancelling its budget
// when configured, and the original Kernel-minted no-effect ChildGrant fact.
// A generic unknown Store/Host callback outcome can never claim this path.
func (runner *Runner) resumeDeferredChildGrantCancellation(
	ctx context.Context, turn Turn, runID string, current kernel.Snapshot,
	items []Item, receipts map[string][]Item,
) (Item, error) {
	if runner == nil || runner.store == nil || runner.runtime == nil ||
		current.Run.ID != runID ||
		(current.Run.Status != kernel.RunStatusRunning && current.Run.Status != kernel.RunStatusWaitingInput && current.Run.Status != kernel.RunStatusPausedBudget) {
		return Item{}, ErrConflict
	}
	root, err := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	if err != nil || root.ExecutionRefID != runID || root.ParentItemID != "" ||
		validateRootRuntimeOwnership(root, current) != nil {
		return Item{}, ErrConflict
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger != nil && !ledger.Cancelled {
		return Item{}, errors.Join(ErrConflict, err)
	}
	var candidate Item
	for _, item := range items {
		if item.ParentItemID != CancellationFenceParentItemID || item.TurnID != turn.ID || item.RunID != runID {
			continue
		}
		if VerifiedCancellationFenceReceipt(item, receipts[item.ID]) {
			continue
		}
		if candidate.ID != "" {
			return Item{}, ErrConflict
		}
		candidate = item
	}
	if candidate.ID == "" || !ValidCancellationFenceIntentAdmission(candidate) || candidate.Seq == 0 {
		return Item{}, ErrConflict
	}
	count := 0
	for _, item := range items {
		if item.ParentItemID != DeferredChildGrantCancellationParentItemID {
			continue
		}
		if item.TurnID != turn.ID || item.RunID != runID || !validDeferredChildGrantCancellation(item, candidate) {
			return Item{}, ErrConflict
		}
		count++
	}
	if count != 1 {
		return Item{}, ErrConflict
	}
	return candidate, nil
}

// cleanupReturnedRootChildren uses the already elected cancellation and frozen
// child routes. Eligibility is an observation, never authority to dispatch:
// the original cancellation intent and, when configured, cancelled shared
// budget supply that authority. Unknown or still-running Host callbacks cannot enter this path.
func (runner *Runner) cleanupReturnedRootChildren(ctx context.Context, turn Turn, intent Item, invocation Invocation, reason string) (attempted bool, outcome error) {
	native, ok := runner.agent.(*agent.Runner)
	if !ok || invocation.ParentItemID != "" || invocation.ExecutionClass != ExecutionAgent {
		return false, nil
	}
	current, err := runner.runtime.Load(ctx, invocation.ExecutionRefID)
	if err != nil {
		return false, err
	}
	if err = validateRootRuntimeOwnership(invocation, current); err != nil {
		return false, err
	}
	ledger, err := runner.loadTurnBudget(ctx, turn)
	if err != nil || ledger != nil && !ledger.Cancelled {
		return false, errors.Join(ErrConflict, err)
	}
	scope, err := native.RootDeferredCleanupEligibility(ctx, current.Run.ID, current.Run.Revision)
	if err != nil || !scope.Valid() {
		return false, errors.Join(ErrConflict, err)
	}
	// Persist the same-election recovery witness BEFORE any remote cleanup.
	if err = runner.recordDeferredChildGrantCancellation(ctx, turn, intent, current.Run.ID); err != nil {
		return true, err
	}
	if _, err = runner.cancelTurnDescendants(ctx, turn.ID, invocation.ID, current.Run.ID, reason); err != nil {
		return true, err
	}
	latest, err := runner.runtime.Load(ctx, current.Run.ID)
	if err != nil {
		return true, err
	}
	after, err := native.RootDeferredCleanupEligibility(ctx, latest.Run.ID, latest.Run.Revision)
	if err != nil || !scope.Matches(after) {
		return true, errors.Join(ErrConflict, err)
	}
	_, err = native.ReconcileRootDeferredChildren(ctx, latest.Run.ID, latest.Run.Revision)
	return true, err
}
