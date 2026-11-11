package harness

import (
	"context"
	"errors"
	"testing"
)

// A parent Dispatch ID is globally unique. The canonical receipt may reside
// in the original Turn while a historical, non-canonical child carries an
// unrelated or malformed TurnID. Source completeness is global, not scoped to
// the child's or grant's Turn.
func TestApplicationGlobalParentReceiptChildrenFailClosedInMemory(t *testing.T) {
	cases := []struct {
		name     string
		foreign  bool
		kind     ItemKind
		status   ItemStatus
		otherRun bool
		otherOwn bool
	}{
		{"foreign-turn-artifact", true, ItemArtifact, ItemCompleted, false, false},
		{"foreign-turn-diagnostic", true, ItemDiagnostic, ItemCompleted, false, false},
		{"same-turn-unexpected-kind", false, ItemArtifact, ItemCompleted, false, false},
		{"foreign-turn-wrong-run", true, ItemDiagnostic, ItemCompleted, true, false},
		{"foreign-turn-wrong-owner", true, ItemDiagnostic, ItemCompleted, false, true},
		{"foreign-turn-started", true, ItemDiagnostic, ItemStarted, false, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFrozenApplicationParentFixture(t, true, false)
			if err := fx.observe(); err != nil {
				t.Fatalf("valid original receipt was rejected: %v", err)
			}
			const grantID = "hdispatch_original_agent_resume_after_child"
			prior, err := fx.store.ListItemChildren(t.Context(), grantID, ItemChildProofAll)
			if err != nil || len(prior) != 1 {
				t.Fatalf("expected exactly one original globally indexed receipt, got %v %v", len(prior), err)
			}
			sibling := prior[0]
			sibling.ID = "legacy_global_conflict_" + tt.name
			sibling.Kind = tt.kind
			sibling.Status = tt.status
			sibling.Seq += 100
			if tt.foreign {
				sibling.TurnID = "different-historical-turn"
			}
			if tt.otherRun {
				sibling.RunID = "foreign-execution-ref"
			}
			if tt.otherOwn {
				sibling.InvocationID = "other-original-owner"
			}
			// Historical corruption is inserted directly: the modern
			// AppendItem contract already rejects it. Both global Item
			// identity and its parent index reflect the persisted row.
			fx.store.itemIDs[sibling.ID] = sibling
			fx.store.items[sibling.TurnID] = append(fx.store.items[sibling.TurnID], sibling)
			fx.store.itemChildren[grantID] = append(fx.store.itemChildren[grantID], sibling)
			loaded, err := fx.store.ListItemChildren(t.Context(), grantID, ItemChildProofAll)
			if err != nil || len(loaded) != 2 {
				t.Fatalf("cross-Turn original children were hidden: count=%d err=%v", len(loaded), err)
			}
			if err := fx.observe(); !errors.Is(err, ErrConflict) {
				t.Fatalf("historical %s receipt sibling authorized parent continuation: %v", tt.name, err)
			}
		})
	}
}

func TestApplicationGlobalParentReceiptReaderIsBoundedAndCloned(t *testing.T) {
	fx := newFrozenApplicationParentFixture(t, true, false)
	const grantID = "hdispatch_original_agent_resume_after_child"
	read, err := fx.store.ListItemChildren(context.Background(), grantID, ItemChildProofAll)
	if err != nil || len(read) != 1 {
		t.Fatalf("missing original global receipt: %v %v", read, err)
	}
	read[0].Payload[0] = 'x'
	read[0].RunID = "forged-in-response"
	if err := fx.observe(); err != nil {
		t.Fatalf("caller modified Store-owned original receipt: %v", err)
	}
	if _, err := fx.store.ListItemChildren(context.Background(), "", ItemChildProofAll); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty global parent accepted: %v", err)
	}
	other := fx.store.itemChildren[grantID][0]
	for i := 0; i < 4; i++ {
		sibling := other
		sibling.ID = "legacy_many_" + string(rune('a'+i))
		fx.store.itemChildren[grantID] = append(fx.store.itemChildren[grantID], sibling)
	}
	loaded, err := fx.store.ListItemChildren(context.Background(), grantID, ItemChildProofAll)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("global original parent query not bounded: count=%d err=%v", len(loaded), err)
	}
	if err := fx.observe(); !errors.Is(err, ErrConflict) {
		t.Fatalf("multiple historical children authorized parent continuation: %v", err)
	}
}

func TestApplicationGlobalParentReceiptUnrelatedSiblingsCannotVeto(t *testing.T) {
	fx := newFrozenApplicationParentFixture(t, true, false)
	const grantID = "hdispatch_original_agent_resume_after_child"
	original := fx.store.itemChildren[grantID][0]
	other := original
	other.ID = "unrelated_original_parent_child"
	other.ParentItemID = "unrelated_original_grant"
	other.TurnID = "other-turn"
	fx.store.itemChildren[other.ParentItemID] = append(fx.store.itemChildren[other.ParentItemID], other)
	if err := fx.observe(); err != nil {
		t.Fatalf("unrelated parent child vetoed valid original receipt: %v", err)
	}
}

// Vary the suspect's persisted Turn, Kind, Status, owner and Run while
// keeping one original valid physical settlement. None of those untrusted
// fields may hide the second child of the same globally unique parent.
func FuzzApplicationGlobalParentReceiptCompleteness(f *testing.F) {
	for _, mask := range []uint8{0, 1, 2, 3, 4, 8, 15, 31, 63, 127, 255} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		const grantID = "hdispatch_original_agent_resume_after_child"
		if mask&1 == 0 {
			if err := fx.observe(); err != nil {
				t.Fatalf("baseline genuine original receipt rejected: %v", err)
			}
			return
		}
		sibling := fx.store.itemChildren[grantID][0]
		sibling.ID = "legacy-fuzz-global-sibling"
		if mask&2 != 0 {
			sibling.TurnID = "historical-foreign-turn"
		}
		if mask&4 != 0 {
			sibling.Kind = ItemArtifact
		}
		if mask&8 != 0 {
			sibling.Status = ItemStarted
		}
		if mask&16 != 0 {
			sibling.InvocationID = "other-original-invocation"
		}
		if mask&32 != 0 {
			sibling.RunID = "different-physical-run"
		}
		if mask&64 != 0 {
			sibling.Seq = 1
		}
		fx.store.itemIDs[sibling.ID] = sibling
		fx.store.itemChildren[grantID] = append(fx.store.itemChildren[grantID], sibling)
		if err := fx.observe(); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate persisted global parent accepted (mask=%d): %v", mask, err)
		}
	})
}
