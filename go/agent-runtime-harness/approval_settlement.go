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
	if !ok {
		return Item{}, false, ErrConflict
	}
	phase, valid := VerifiedExecutionDispatchGrant(grant)
	parent, _ := OriginalApprovalRequestID(grant)
	subtask := phase == "resolve-subtask-approval"
	var pending approvalRequestItemPayload
	if !valid || request.ID != parent || request.Kind != ItemApproval || request.Status != ItemWaiting ||
		request.TurnID != grant.TurnID || request.InvocationID != grant.InvocationID || request.RunID != grant.RunID ||
		request.Seq == 0 || request.Seq >= grant.Seq || !decodeTypedAuthorityJSON(request.Payload, &pending) ||
		pending.CheckpointID != a.CheckpointID || pending.Subtask != subtask || pending.ToolCallID == "" || pending.ToolKey == "" {
		return Item{}, false, ErrConflict
	}
	decision, err := approvalDecisionFact(grant.TurnID, grant.InvocationID, grant.RunID, subtask, *a, now)
	if err != nil {
		return Item{}, false, err
	}
	switch len(children) {
	case 0:
		return decision, false, nil
	case 1:
		existing := children[0]
		if existing.Seq <= grant.Seq || !sameItemIdentity(existing, decision) {
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
