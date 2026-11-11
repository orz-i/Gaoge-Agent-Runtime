package harness

import (
	"context"
	"errors"
	"testing"
)

func TestApplicationHostOriginalSourceGlobalAuthorityInMemory(t *testing.T) {
	sourceID := func(fx frozenApplicationParentFixture) string {
		return stableID("hinteractionitem", fx.interaction.ID, string(InteractionResolved))
	}
	t.Run("ordinary-Artifact-attachments-remain-business-data", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		original := fx.store.itemChildren[sourceID(fx)][0]
		ordinary := make([]Item, 0, 25)
		for i := 0; i < 25; i++ {
			ordinary = append(ordinary, Item{
				ID:           "ordinary-attachment-" + string(rune('a'+i)),
				ParentItemID: sourceID(fx), TurnID: fx.child.TurnID,
				Kind: ItemArtifact, Status: ItemCompleted, Seq: uint64(i + 20),
			})
		}
		// The canonical Host receipt is deliberately after 25 ordinary
		// attachments in the index. A generic LIMIT 2 without typed
		// Host-authority filtering would incorrectly reject this history.
		fx.store.itemChildren[sourceID(fx)] = append(ordinary, original)
		claimed, err := fx.store.ListItemChildren(context.Background(), sourceID(fx), ItemChildProofHostAuthority)
		if err != nil || len(claimed) != 1 || claimed[0].ID != original.ID {
			t.Fatalf("Host authority is obscured by ordinary attachments: count=%d err=%v", len(claimed), err)
		}
		if err = fx.observe(); err != nil {
			t.Fatalf("ordinary Artifact attachments incorrectly vetoed Host proof: %v", err)
		}
	})

	for _, tt := range []struct {
		name    string
		kind    ItemKind
		id      string
		foreign bool
	}{
		{"foreign-Turn-Diagnostic", ItemDiagnostic, "legacy-host-conflict", true},
		{"foreign-Turn-reserved-ID-Artifact", ItemArtifact, "hbizverified_legacy_corrupt_host", true},
		{"same-Turn-Diagnostic", ItemDiagnostic, "legacy-same-turn-host-conflict", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFrozenApplicationParentFixture(t, true, false)
			legacy := Item{
				ID: tt.id, ParentItemID: sourceID(fx), TurnID: fx.child.TurnID,
				Kind: tt.kind, Status: ItemCompleted, Seq: 12345,
			}
			if tt.foreign {
				legacy.TurnID = "legacy-foreign-host-turn"
			}
			fx.store.itemIDs[legacy.ID] = legacy
			fx.store.itemChildren[sourceID(fx)] = append(fx.store.itemChildren[sourceID(fx)], legacy)
			if got := fx.observe(); !errors.Is(got, ErrConflict) {
				t.Fatalf("malformed Host source sibling was hidden: %v", got)
			}
		})
	}

	t.Run("foreign-child-of-host-response-is-contradictory", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		responseID := verifiedInteractionResponseItemID(fx.interaction.ID)
		fx.store.itemChildren[responseID] = append(fx.store.itemChildren[responseID], Item{
			ID: "legacy-host-response-child", ParentItemID: responseID,
			TurnID: "other-turn", Kind: ItemArtifact, Seq: 3,
		})
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("stray foreign-Turn Host response child authorized recovery: %v", got)
		}
	})

	for _, typeName := range []string{"claim", "done"} {
		t.Run("foreign-Turn-orphan-"+typeName+"-ID", func(t *testing.T) {
			fx := newFrozenApplicationParentFixture(t, true, false)
			orphanID := stableID("hcontclaim", fx.interaction.ID)
			if typeName == "done" {
				orphanID = interactionContinuationCompletedItemID(fx.interaction.ID)
			}
			orphan := Item{
				ID: orphanID, ParentItemID: "unrelated-legacy-parent",
				TurnID: "damaged-historical-turn", Kind: ItemDiagnostic,
				Status: ItemCompleted, Seq: 1,
			}
			fx.store.itemIDs[orphanID] = orphan
			fx.store.items[orphan.TurnID] = append(fx.store.items[orphan.TurnID], orphan)
			fx.store.itemChildren[orphan.ParentItemID] = append(fx.store.itemChildren[orphan.ParentItemID], orphan)
			if got := fx.observe(); !errors.Is(got, ErrConflict) {
				t.Fatalf("orphan original Continuation %s with malformed Turn/parent authorized recovery: %v", typeName, got)
			}
		})
	}

	t.Run("missing-global-authority-is-not-a-success", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		fx.store.itemChildren[sourceID(fx)] = nil
		if got := fx.observe(); !errors.Is(got, ErrConflict) {
			t.Fatalf("missing original Host response became success: %v", got)
		}
	})

	t.Run("unknown-query-mode-rejected", func(t *testing.T) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		_, err := fx.store.ListItemChildren(context.Background(), sourceID(fx), ItemChildProofScope(255))
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid global proof selector accepted: %v", err)
		}
	})
}

func FuzzApplicationGlobalHostAuthoritySiblings(f *testing.F) {
	for _, mask := range []uint8{0, 1, 2, 4, 8, 15, 31, 127, 255} {
		f.Add(mask)
	}
	f.Fuzz(func(t *testing.T, mask uint8) {
		fx := newFrozenApplicationParentFixture(t, true, false)
		sourceID := stableID("hinteractionitem", fx.interaction.ID, string(InteractionResolved))
		if mask&1 != 0 {
			for i := 0; i < 3; i++ {
				fx.store.itemChildren[sourceID] = append([]Item{{
					ID: "ordinary-artifact-" + string(rune('a'+i)), TurnID: fx.child.TurnID,
					ParentItemID: sourceID, Kind: ItemArtifact,
				}}, fx.store.itemChildren[sourceID]...)
			}
		}
		contradiction := mask&2 != 0
		if contradiction {
			sibling := Item{
				ID: "legacy-foreign-Diagnostic", ParentItemID: sourceID,
				TurnID: "historical-other-turn", Kind: ItemDiagnostic,
			}
			if mask&4 != 0 {
				sibling.ID = "hbizverified_malformed_reserved_child"
				sibling.Kind = ItemArtifact
			}
			if mask&8 != 0 {
				sibling.TurnID = fx.child.TurnID
			}
			if mask&16 != 0 {
				sibling.RunID = "foreign-run"
			}
			fx.store.itemChildren[sourceID] = append(fx.store.itemChildren[sourceID], sibling)
		}
		if mask&32 != 0 {
			fx.store.itemChildren[verifiedInteractionResponseItemID(fx.interaction.ID)] = []Item{{
				ID: "foreign-host-receipt-child", ParentItemID: verifiedInteractionResponseItemID(fx.interaction.ID),
				TurnID: "historical-other-turn", Kind: ItemArtifact,
			}}
			contradiction = true
		}
		if mask&64 != 0 {
			orphanID := stableID("hcontclaim", fx.interaction.ID)
			if mask&128 != 0 {
				orphanID = interactionContinuationCompletedItemID(fx.interaction.ID)
			}
			fx.store.itemIDs[orphanID] = Item{
				ID: orphanID, ParentItemID: "unrelated-historical-parent",
				TurnID: "historical-foreign-turn", Kind: ItemDiagnostic,
			}
			contradiction = true
		}
		result := fx.observe()
		if contradiction && !errors.Is(result, ErrConflict) {
			t.Fatalf("Host authority contradiction accepted (mask=%d): %v", mask, result)
		}
		if !contradiction && result != nil {
			t.Fatalf("legitimate Host source with artifacts rejected (mask=%d): %v", mask, result)
		}
	})
}
