package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// A cancellation intent is another globally unique original operation. Its
// canonical completion plus one foreign-Turn child must remain active to the
// Memory Store, matching SQL's globally indexed pendingCancellationFenceTx.
func seedGloballyContradictoryCancellation(t *testing.T, fx frozenApplicationParentFixture) {
	t.Helper()
	ctx := t.Context()
	intentID := "hcancel_original_globally_unique_intent"
	intent, fresh, err := fx.store.AppendItem(ctx, Item{
		ID: intentID, TurnID: fx.root.TurnID,
		RunID: fx.root.ExecutionRefID, Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"original-agent-run"}`),
		CreatedAt:    fx.now, UpdatedAt: fx.now,
	})
	if err != nil || !fresh {
		t.Fatalf("original cancellation intent: fresh=%v err=%v", fresh, err)
	}
	_, fresh, err = fx.store.AppendItem(ctx, Item{
		ID: stableID("hcancelclosed", intent.ID), TurnID: fx.root.TurnID,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		RunID: intent.RunID, ParentItemID: intent.ID,
		Payload:   json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: fx.now, UpdatedAt: fx.now,
	})
	if err != nil || !fresh {
		t.Fatalf("original cancellation completion: fresh=%v err=%v", fresh, err)
	}
	if ActiveCancellationFence(fx.store.items[fx.root.TurnID]) {
		t.Fatal("valid original cancellation completion not settled")
	}
	child := fx.store.itemChildren[intent.ID][0]
	child.ID = "historical_foreign_turn_cancel_sibling"
	child.TurnID = "legacy-other-turn"
	child.Kind = ItemArtifact
	child.Seq = 1
	fx.store.items[child.TurnID] = append(fx.store.items[child.TurnID], child)
	fx.store.itemIDs[child.ID] = child
	fx.store.itemChildren[intent.ID] = append(fx.store.itemChildren[intent.ID], child)
	evidence, err := fx.store.ListItemChildren(ctx, intent.ID, ItemChildProofAll)
	if err != nil || len(evidence) != 2 ||
		VerifiedCancellationFenceReceipt(intent, evidence) {
		t.Fatalf("historical Cancellation contradiction not constructed: count=%d err=%v", len(evidence), err)
	}
}

func TestGlobalOriginalCancellationEvidenceFencesMemoryAdmission(t *testing.T) {
	t.Run("Retry-after-corrupt-completion", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		seedGloballyContradictoryCancellation(t, fx)
		_, err := retryOriginalRootWithWitness(t.Context(), fx, "next-run-after-cancel")
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("Retry rotated Attempt despite original Cancellation conflict: %v", err)
		}
	})
	t.Run("new-Dispatch-and-Child-after-corrupt-completion", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		next := fx.retryRoot(t)
		seedGloballyContradictoryCancellation(t, fx)
		// The contradictory cancellation completion must retain the Turn
		// fence against both a fresh physical grant and a new child effect.
		grant := Item{
			ID:     "new-physical-grant-after-cancel-contradiction",
			TurnID: fx.root.TurnID, Kind: ItemDiagnostic, Status: ItemStarted,
			InvocationID: next.ID, RunID: next.ExecutionRefID,
			ParentItemID: ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"new-agent-run"}`),
			CreatedAt:    fx.now, UpdatedAt: fx.now,
		}
		_, created, err := fx.store.AppendItem(t.Context(), grant)
		if !errors.Is(err, ErrConflict) || created {
			t.Fatalf("new physical grant bypassed global cancellation fence: fresh=%v err=%v", created, err)
		}
		input := json.RawMessage(`{"goal":"new child"}`)
		_, created, err = fx.store.CreateInvocation(t.Context(), Invocation{
			ID: "new-child-after-invalid-cancel", TurnID: fx.root.TurnID,
			ParentItemID: "original-parent-item", CapabilityKey: "application.child",
			DefinitionVersion: "v1", ExecutionClass: ExecutionApplication,
			Input: input, InputHash: hashInvocationBytes(input),
			ExecutionRefID: "new-child-effect", Status: InvocationAccepted,
			Attempt: 1, Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		if !errors.Is(err, ErrConflict) || created {
			t.Fatalf("new child bypassed globally active cancellation: fresh=%v err=%v", created, err)
		}
	})
	t.Run("read-only-pending-remains-active", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		seedGloballyContradictoryCancellation(t, fx)
		facts, err := (&Runner{store: fx.store}).loadGlobalPhysicalEvidence(t.Context(), fx.root.TurnID)
		if err != nil || !ActiveCancellationFence(facts) {
			t.Fatalf("Runner wrongly believed corrupt cancellation receipt settled: err=%v", err)
		}
	})
}
