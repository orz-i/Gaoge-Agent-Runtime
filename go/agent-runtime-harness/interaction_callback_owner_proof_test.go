package harness

import (
	"context"
	"encoding/json"
	"testing"
)

// The callback witness is one physical continuation's private authority,
// not a per-Invocation privilege. Multiple historical Resolved Interactions
// may share the same Invocation; a newly elected callback must not certify
// any older pending one just because the Turn and Invocation IDs match.
func TestOriginalInteractionCallbackWitnessCannotCertifySameInvocationSibling(t *testing.T) {
	interactionA, owner, responseA, claimA, completedA := continuationReceiptFixture()
	sourceA := auditResolvedInteractionSource(interactionA, owner)
	sourceA.Seq = 10
	responseA.Seq = 11
	claimA.Seq = 12
	completedA.Seq = 13
	interactionB := interactionA
	interactionB.ID = "interaction-B"
	sourceB := auditResolvedInteractionSource(interactionB, owner)
	sourceB.Seq = 20
	responseB := auditVerifiedHostReceipt(interactionB, owner)
	responseB.Seq = 21
	claimB := claimA
	claimB.ID = stableID("hcontclaim", interactionB.ID)
	claimB.ParentItemID = responseB.ID
	claimB.Payload = json.RawMessage(`{"interactionID":"interaction-B","boundary":"runtime_continuation_claimed"}`)
	claimB.Seq = 22
	pendingBoth := []Item{sourceA, responseA, claimA, sourceB, responseB, claimB}
	ctxB := context.WithValue(t.Context(), completedInteractionCallbackKey{}, completedInteractionCallback{
		turnID: interactionB.TurnID, invocationID: owner.ID, interactionID: interactionB.ID,
	})
	if !interactionCallbackConfirmed(ctxB, interactionB.TurnID, owner.ID, interactionB.ID) {
		t.Fatal("original elected callback B lost its own permission")
	}
	if interactionCallbackConfirmed(ctxB, interactionA.TurnID, owner.ID, interactionA.ID) {
		t.Fatal("callback B inherited physical authority for unrelated Interaction A")
	}
	if ValidTerminalProjectionProof(ctxB, interactionA.TurnID, owner.ID,
		[]Interaction{interactionA, interactionB}, []Invocation{owner}, pendingBoth) {
		t.Fatal("different Interaction sharing the same Invocation was terminalized without its own completion")
	}
	store := &proofReadOnlyStore{
		interactions: []Interaction{interactionA, interactionB},
		owners:       map[string]Invocation{owner.ID: owner}, items: pendingBoth,
	}
	runner := &Runner{store: store}
	pending, err := runner.pendingInteractionContinuation(ctxB, interactionA.TurnID, owner.ID)
	if err != nil || !pending {
		t.Fatalf("another Interaction's callback was admitted through its sibling: pending=%v err=%v", pending, err)
	}
	// Completed A is no longer an active barrier. The elected physical
	// continuation of B must still be able to finish its own projection.
	completedOriginal := []Item{sourceA, responseA, claimA, completedA, sourceB, responseB, claimB}
	store.items = completedOriginal
	if !ValidTerminalProjectionProof(ctxB, interactionA.TurnID, owner.ID,
		[]Interaction{interactionA, interactionB}, []Invocation{owner}, completedOriginal) {
		t.Fatal("verified original A and elected callback B were incorrectly blocked")
	}
	pending, err = runner.pendingInteractionContinuation(ctxB, interactionA.TurnID, owner.ID)
	if err != nil || pending {
		t.Fatalf("elected callback B cannot resume after A was fully completed: pending=%v err=%v", pending, err)
	}
}
