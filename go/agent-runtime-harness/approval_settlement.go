package harness

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The receipt and decision are two durable facts of one Host settlement.
// Only the original control CAS (not a returned terminal Run) certifies
// that the requested decision was actually applied.
const approvalNotAppliedBoundary = "native_approval_not_applied"

type approvalSettlementKey struct{}
type approvalSettlement struct{ GrantID, PayloadDigest, Boundary string }

// OriginalApprovalRequestID identifies the parent of the captured approval
// obligation. Older two-field dispatches have no such original proof.
func OriginalApprovalRequestID(grant Item) (string, bool) {
	a, ok := originalDispatchApproval(grant)
	if !ok {
		return "", false
	}
	return stableID("hia", grant.TurnID, a.CheckpointID), true
}

func originalApprovalProjection(grant, request Item, children []Item, now time.Time) (Item, bool, error) {
	a, ok := originalDispatchApproval(grant)
	phase, valid := VerifiedExecutionDispatchGrant(grant)
	if !ok || !valid || request.Seq >= grant.Seq {
		return Item{}, false, ErrConflict
	}
	return approvalProjection(grant.TurnID, grant.InvocationID, grant.RunID, phase == "resolve-subtask-approval", *a, request, children, grant.Seq, now)
}

// Both native journal projection and the remaining external dispatch
// settlement use the original request and the same canonical decision.
func approvalProjection(turnID, invID, runID string, subtask bool, a kernel.ManagedApprovalDecision, request Item, children []Item, after uint64, now time.Time) (Item, bool, error) {
	decision, err := approvalDecisionFact(turnID, invID, runID, subtask, a, now)
	if err != nil {
		return Item{}, false, err
	}
	var pending approvalRequestItemPayload
	if request.ID != decision.ParentItemID || request.Kind != ItemApproval || request.Status != ItemWaiting ||
		request.TurnID != turnID || request.InvocationID != invID || request.RunID != runID ||
		request.Seq == 0 || !decodeTypedAuthorityJSON(request.Payload, &pending) ||
		pending.CheckpointID != a.CheckpointID || pending.Subtask != subtask || pending.ToolCallID == "" || pending.ToolKey == "" {
		return Item{}, false, ErrConflict
	}
	switch len(children) {
	case 0:
		return decision, false, nil
	case 1:
		existing := children[0]
		if existing.Seq <= max(request.Seq, after) || !sameItemIdentity(existing, decision) {
			return Item{}, false, ErrConflict
		}
		return decision, true, nil
	default:
		return Item{}, false, ErrConflict
	}
}

// PlanOriginalApprovalAdmission requires the exact waiting request before
// a NEW physical approval is admitted. A prior decision cannot authorize
// another dispatch, even if the earlier decision happens to be identical.
func PlanOriginalApprovalAdmission(grant, request Item, children []Item) (Item, error) {
	if _, ok := originalDispatchApproval(grant); !ok {
		return Item{}, nil
	}
	decision, exists, err := originalApprovalProjection(grant, request, children, grant.CreatedAt)
	if err != nil || exists {
		return Item{}, errors.Join(ErrConflict, err)
	}
	return decision, nil
}

// PlanOriginalApprovalSettlement is shared by ordinary AppendItem and
// Managed Closure under the owning Turn lock. An exact earlier projection
// is consumed idempotently; no foreign or ambiguous sibling is tolerated.
func PlanOriginalApprovalSettlement(grant, receipt, request Item, children []Item) (Item, bool, error) {
	if _, ok := originalDispatchApproval(grant); !ok {
		return Item{}, false, nil
	}
	boundary, valid := VerifiedExecutionDispatchReceipt(grant, []Item{receipt})
	if !valid {
		return Item{}, false, ErrConflict
	}
	decision, exists, err := originalApprovalProjection(grant, request, children, receipt.CreatedAt)
	if err != nil {
		return Item{}, false, err
	}
	switch boundary {
	case "physical_dispatch_returned", "managed_dispatch_transferred", "managed_dispatch_cancelled_before_next_effect":
		if exists && receipt.Seq > 0 {
			// A previously projected decision must precede the new closure.
			if children[0].Seq >= receipt.Seq {
				return Item{}, false, ErrConflict
			}
		}
		return decision, !exists, nil
	default:
		if exists {
			return Item{}, false, ErrConflict
		}
		return Item{}, false, nil
	}
}

// Ordinary approval completion must be minted by the actual Runner after
// it checked the original Kernel control journal. Managed closure instead
// consumes its existing opaque Kernel closure proof.
func ValidOriginalApprovalSettlement(ctx context.Context, grant, receipt Item) bool {
	if _, ok := originalDispatchApproval(grant); !ok {
		return true
	}
	boundary, valid := VerifiedExecutionDispatchReceipt(grant, []Item{receipt})
	if !valid {
		return false
	}
	if boundary != "physical_dispatch_returned" && boundary != approvalNotAppliedBoundary {
		return true
	}
	p, ok := ctx.Value(approvalSettlementKey{}).(approvalSettlement)
	return ok && p.GrantID == grant.ID && p.PayloadDigest == kernel.ManagedDigest(grant.Payload) && p.Boundary == boundary
}

func (r *Runner) qualifyApprovalSettlement(ctx context.Context, grant Item, boundary string) (context.Context, string, error) {
	a, ok := originalDispatchApproval(grant)
	if !ok || boundary != "physical_dispatch_returned" {
		return ctx, boundary, nil
	}
	applied, err := r.runtime.CommittedApprovalDispatch(ctx, grant.RunID, grant.ID, *a)
	if err != nil {
		return ctx, boundary, err
	}
	if !applied {
		// A known returned callback that did not commit this control can
		// close without a decision only while the exact gate is still pending.
		s, e := r.runtime.Load(ctx, grant.RunID)
		if e != nil || s.Checkpoint == nil || s.Checkpoint.ID != a.CheckpointID || s.Checkpoint.Status != kernel.CheckpointPending {
			return ctx, boundary, errors.Join(ErrExecutionOutcomeUnknown, e)
		}
		boundary = approvalNotAppliedBoundary
	}
	return context.WithValue(ctx, approvalSettlementKey{}, approvalSettlement{grant.ID, kernel.ManagedDigest(grant.Payload), boundary}), boundary, nil
}

func verifyNativeApprovalProjection(turnID, invID, runID string, a kernel.ManagedApprovalDecision, items []Item) error {
	parent := stableID("hia", turnID, a.CheckpointID)
	var request Item
	var children []Item
	for _, item := range items {
		if item.ID == parent {
			request = item
		}
		if item.ParentItemID == parent {
			children = append(children, item)
		}
	}
	_, _, err := approvalProjection(turnID, invID, runID, false, a, request, children, 0, time.Time{})
	return err
}

func (r *Runner) readNativeApprovalProjection(ctx context.Context, turn Turn, inv Invocation, a kernel.ManagedApprovalDecision, now time.Time) (Item, bool, error) {
	parent := stableID("hia", turn.ID, a.CheckpointID)
	request, err := r.store.GetItem(ctx, parent)
	if err != nil {
		return Item{}, false, err
	}
	children, err := r.store.ListItemChildren(ctx, parent, ItemChildProofAll)
	if err != nil {
		return Item{}, false, err
	}
	return approvalProjection(turn.ID, inv.ID, inv.ExecutionRefID, false, a, request, children, 0, now)
}

func (r *Runner) recordNativeApprovalDecisions(ctx context.Context, turn Turn, inv Invocation, snapshot kernel.Snapshot) error {
	if inv.ParentItemID != "" || snapshot.Run.Kind != "agent" || !r.runtime.SupportsOwnedPhysicalExecution() ||
		(turn.Status != TurnWaitingInput && inv.Status != InvocationWaitingInput) {
		return nil
	}
	decisions, err := r.runtime.NativeApprovalDecisions(ctx, snapshot)
	if err != nil {
		return err
	}
	for _, decision := range decisions {
		item, exists, err := r.readNativeApprovalProjection(ctx, turn, inv, decision, snapshot.Run.UpdatedAt)
		if err != nil {
			return err
		}
		if !exists {
			if _, err = appendItemFact(ctx, r.store, r.turnFeed, item); err != nil {
				return err
			}
		}
	}
	return nil
}
