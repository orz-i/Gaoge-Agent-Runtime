package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func newAuthorityMemoryStore() (*MemoryStore, Interaction, Invocation, Item, Item, Item) {
	i, owner, response, claim, done := continuationReceiptFixture()
	store := NewMemoryStore()
	store.interactions[i.ID] = i
	store.invocations[owner.ID] = owner
	resolved := auditResolvedInteractionSource(i, owner)
	store.items[i.TurnID] = []Item{resolved}
	store.itemIDs[resolved.ID] = resolved
	return store, i, owner, response, claim, done
}

func TestMemoryAuthorityStoreRejectsForgedHostAndContinuationFacts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  string
		mutate func(*Item)
	}{
		{"response-foreign-run", "response", func(v *Item) { v.RunID = "foreign" }},
		{"response-foreign-turn", "response", func(v *Item) { v.TurnID = "foreign" }},
		{"response-wrong-boundary", "response", func(v *Item) {
			v.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"wrong"}`)
		}},
		{"response-mismatched-ID", "response", func(v *Item) { v.ID = "hbizverified_mismatched" }},
		{"claim-wrong-parent", "claim", func(v *Item) { v.ParentItemID = "unrelated" }},
		{"claim-wrong-run", "claim", func(v *Item) { v.RunID = "unrelated" }},
		{"claim-wrong-boundary", "claim", func(v *Item) {
			v.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"wrong"}`)
		}},
		{"claim-wrong-ID", "claim", func(v *Item) { v.ID = "hcontclaim_forged" }},
		{"done-wrong-parent", "done", func(v *Item) { v.ParentItemID = "foreign" }},
		{"done-wrong-run", "done", func(v *Item) { v.RunID = "foreign" }},
		{"done-wrong-boundary", "done", func(v *Item) {
			v.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"wrong"}`)
		}},
		{"done-wrong-ID", "done", func(v *Item) { v.ID = "hcontdone_forged" }},
		{"done-alternate-ID-same-parent", "done", func(v *Item) { v.ID = "new-contradictory-item" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _, response, claim, done := newAuthorityMemoryStore()
			input := map[string]Item{"response": response, "claim": claim, "done": done}
			for _, stage := range []string{"response", "claim", "done"} {
				if stage == tc.stage {
					break
				}
				if _, fresh, err := store.AppendItem(context.Background(), input[stage]); err != nil || !fresh {
					t.Fatalf("valid prerequisite %s: fresh=%t err=%v", stage, fresh, err)
				}
			}
			bad := input[tc.stage]
			tc.mutate(&bad)
			_, _, err := store.AppendItem(context.Background(), bad)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("forged %s fact admitted: %v", tc.stage, err)
			}
			items, err := store.ListItems(context.Background(), "turn-A", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := map[string]int{"response": 1, "claim": 2, "done": 3}[tc.stage]
			if len(items) != wantCount {
				t.Fatalf("forged fact was persisted: got %d items want %d", len(items), wantCount)
			}
		})
	}
}

func TestMemoryAuthorityStoreValidChainAndIdempotentReplays(t *testing.T) {
	store, interaction, owner, response, claim, done := newAuthorityMemoryStore()
	for _, fact := range []Item{response, claim, done} {
		saved, fresh, err := store.AppendItem(context.Background(), fact)
		if err != nil || !fresh {
			t.Fatalf("canonical fact %s rejected: fresh=%t err=%v", fact.ID, fresh, err)
		}
		if saved.ID != fact.ID {
			t.Fatalf("canonical ID changed: %s", saved.ID)
		}
		if _, repeat, err := store.AppendItem(context.Background(), fact); err != nil || repeat {
			t.Fatalf("idempotent exact fact replay: fresh=%t err=%v", repeat, err)
		}
	}
	items, err := store.ListItems(context.Background(), interaction.TurnID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !hasVerifiedInteractionResponse(items, interaction, owner) ||
		!hasCompletedInteractionContinuation(items, interaction, owner) {
		t.Fatal("original linked verified Host and continuation facts not accepted")
	}
	// A second completion with an alternate Item ID is not a second vote.
	other := done
	other.ID = "another-completion"
	if _, _, err := store.AppendItem(context.Background(), other); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate completion admitted: %v", err)
	}
}
