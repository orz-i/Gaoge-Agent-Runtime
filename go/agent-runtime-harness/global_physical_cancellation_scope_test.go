package harness

import (
	"encoding/json"
	"testing"
)

// The original globally contradictory receipt belongs to one physical
// Invocation. An unrelated (disjoint) Cancel scope cannot be vetoed merely
// because the Store has historic bad evidence for another owner; the full
// Turn or original owner scope still MUST fail closed.
func TestGlobalPhysicalCancellationKeepsOriginalTargetScope(t *testing.T) {
	store := NewMemoryStore()
	const turnID = "original-cancellation-scope-turn"
	grant := Item{
		ID: "hdispatch_original_other_owner", TurnID: turnID, Seq: 1,
		Kind: ItemDiagnostic, Status: ItemStarted,
		InvocationID: "other-invocation", RunID: "other-physical-run",
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"other-physical-run"}`),
	}
	canonical := Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: turnID, Seq: 2,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		ParentItemID: grant.ID,
		Payload:      json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
	}
	contradictory := canonical
	contradictory.ID = "historical_other_turn_receipt"
	contradictory.TurnID = "wrong-historical-turn"
	contradictory.Kind = ItemArtifact
	contradictory.Seq = 1
	store.items[turnID] = []Item{grant, canonical}
	store.items[contradictory.TurnID] = []Item{contradictory}
	store.itemChildren[grant.ID] = []Item{canonical, contradictory}
	runner := &Runner{store: store}

	targetOwner := map[string]struct{}{"target-invocation": {}}
	targetRuns := map[string]struct{}{"target-physical-run": {}}
	pending, err := runner.pendingExecutionDispatch(t.Context(), turnID, targetOwner, targetRuns)
	if err != nil || pending {
		t.Fatalf("unrelated original receipt incorrectly blocked disjoint cancellation: pending=%v err=%v", pending, err)
	}
	pending, err = runner.pendingExecutionDispatch(t.Context(), turnID, nil, nil)
	if err != nil || !pending {
		t.Fatalf("Turn-wide read hid globally contradictory original receipt: pending=%v err=%v", pending, err)
	}
	pending, err = runner.pendingExecutionDispatch(t.Context(), turnID,
		map[string]struct{}{grant.InvocationID: {}}, targetRuns)
	if err != nil || !pending {
		t.Fatalf("original owner read hid globally contradictory receipt: pending=%v err=%v", pending, err)
	}
	pending, err = runner.pendingExecutionDispatch(t.Context(), turnID, targetOwner,
		map[string]struct{}{grant.RunID: {}})
	if err != nil || !pending {
		t.Fatalf("original physical Run read hid globally contradictory receipt: pending=%v err=%v", pending, err)
	}
}

func FuzzGlobalPhysicalCancellationScope(f *testing.F) {
	for _, mask := range []uint8{0, 1, 2, 3, 4, 7, 15, 31, 63, 255} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		store := NewMemoryStore()
		const tid = "fuzz-physical-scope"
		grant := Item{
			ID: "hdispatch_fuzz_scoped_original", TurnID: tid, Seq: 1,
			Kind: ItemDiagnostic, Status: ItemStarted,
			InvocationID: "owner-a", RunID: "run-a",
			ParentItemID: ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"run-a"}`),
		}
		store.items[tid] = []Item{grant}
		receipts := []Item{}
		if mask&1 == 0 {
			canonical := Item{
				ID: stableID("hdispatchdone", grant.ID), TurnID: tid, Seq: 2,
				Kind: ItemDiagnostic, Status: ItemCompleted,
				InvocationID: grant.InvocationID, RunID: grant.RunID,
				ParentItemID: grant.ID,
				Payload:      json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
			}
			store.items[tid] = append(store.items[tid], canonical)
			receipts = append(receipts, canonical)
		}
		if mask&2 != 0 {
			sibling := Item{ID: "legacy_fuzz_bad_completion", TurnID: "foreign-turn", Seq: 1,
				Kind: ItemArtifact, Status: ItemStarted, InvocationID: "owner-b",
				RunID: "run-b", ParentItemID: grant.ID}
			receipts = append(receipts, sibling)
		}
		store.itemChildren[grant.ID] = receipts
		owners := map[string]struct{}{"owner-b": {}}
		runs := map[string]struct{}{"run-b": {}}
		relevant := mask&4 != 0
		if relevant {
			if mask&8 == 0 {
				owners[grant.InvocationID] = struct{}{}
			} else {
				runs[grant.RunID] = struct{}{}
			}
		}
		pending, err := (&Runner{store: store}).pendingExecutionDispatch(t.Context(), tid, owners, runs)
		if err != nil {
			t.Fatal(err)
		}
		expect := relevant && (mask&1 != 0 || mask&2 != 0)
		if pending != expect {
			t.Fatalf("wrong target scope or receipt classification mask=%d pending=%v expected=%v", mask, pending, expect)
		}
	})
}
