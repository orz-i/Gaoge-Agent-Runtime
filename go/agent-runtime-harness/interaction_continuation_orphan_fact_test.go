package harness

import (
	"errors"
	"testing"
)

// Historical or out-of-band Store corruption must not turn an orphan
// continuation completion into permission to replay its physical Resume.
func TestIndependentContinuationOrphanAndContradictoryFactsFailClosed(t *testing.T) {
	i, owner, response, claim, completed := continuationReceiptFixture()
	source := auditResolvedInteractionSource(i, owner)
	source.Seq = response.Seq - 1
	for _, tc := range []struct {
		name  string
		facts []Item
	}{
		{"completion-without-claim", []Item{source, response, completed}},
		{"unexpected-response-child-without-claim", []Item{source, response, {
			ID: "foreign-response-child", TurnID: i.TurnID, RunID: owner.ExecutionRefID,
			InvocationID: owner.ID, Kind: ItemTool, Status: ItemCompleted,
			ParentItemID: response.ID, Seq: completed.Seq + 1,
		}}},
		{"unexpected-claim-child-without-claim", []Item{source, response, {
			ID: "foreign-claim-child", TurnID: i.TurnID, RunID: owner.ExecutionRefID,
			InvocationID: owner.ID, Kind: ItemTool, Status: ItemCompleted,
			ParentItemID: claim.ID, Seq: completed.Seq + 1,
		}}},
		{"second-child-under-verified-business-receipt", []Item{source, response, claim, completed, {
			ID: "extra-physical-proof", TurnID: i.TurnID, RunID: owner.ExecutionRefID,
			InvocationID: owner.ID, Kind: ItemTool, Status: ItemCompleted,
			ParentItemID: response.ID, Seq: completed.Seq + 1,
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &proofReadOnlyStore{
				items: tc.facts, interactions: []Interaction{i},
				owners: map[string]Invocation{owner.ID: owner},
			}
			runner := &Runner{store: store}
			_, claimed, valid := originalInteractionContinuationClaim(tc.facts, i, owner)
			if !claimed || valid {
				t.Fatalf("corrupt continuation evidence accepted: claimed=%v valid=%v", claimed, valid)
			}
			if claimed, err := runner.interactionContinuationClaimed(t.Context(), i); claimed || !errors.Is(err, ErrConflict) {
				t.Fatalf("corrupt replay evidence accepted: claimed=%v err=%v", claimed, err)
			}
			if pending, err := runner.pendingInteractionContinuation(t.Context(), i.TurnID, owner.ID); pending || !errors.Is(err, ErrConflict) {
				t.Fatalf("corrupt continuation admitted physical Resume: pending=%v err=%v", pending, err)
			}
			if hasCompletedInteractionContinuation(tc.facts, i, owner) {
				t.Fatal("corrupt continuation was mistaken for completed")
			}
		})
	}
}

func TestIndependentContinuationCompleteChainRemainsReplaySafe(t *testing.T) {
	i, owner, response, claim, completed := continuationReceiptFixture()
	source := auditResolvedInteractionSource(i, owner)
	source.Seq = response.Seq - 1
	facts := []Item{source, response, claim, completed}
	store := &proofReadOnlyStore{
		items: facts, interactions: []Interaction{i}, owners: map[string]Invocation{owner.ID: owner},
	}
	runner := &Runner{store: store}
	if _, found, verified := originalInteractionContinuationClaim(facts, i, owner); !found || !verified {
		t.Fatal("canonical continuation claim was rejected")
	}
	if !hasCompletedInteractionContinuation(facts, i, owner) {
		t.Fatal("canonical completed continuation was rejected")
	}
	if claimed, err := runner.interactionContinuationClaimed(t.Context(), i); err != nil || !claimed {
		t.Fatalf("canonical claim not recognized: %v %v", claimed, err)
	}
	if pending, err := runner.pendingInteractionContinuation(t.Context(), i.TurnID, owner.ID); err != nil || pending {
		t.Fatalf("canonical completed continuation remains falsely blocked: %v %v", pending, err)
	}
}
