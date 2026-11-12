package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestIssue107DeferredChildGrantCancellationProofRejectsCounterfeit(t *testing.T) {
	intent := Item{ID: "original-intent", TurnID: "turn", RunID: "root",
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: CancellationFenceParentItemID,
		Payload: json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"root"}`), Seq: 4}
	correct := Item{
		ID: stableID("hcancelchildbusy", intent.ID), TurnID: intent.TurnID,
		RunID: intent.RunID, ParentItemID: DeferredChildGrantCancellationParentItemID,
		Kind: ItemDiagnostic, Status: ItemCompleted, Seq: 5,
		Payload: json.RawMessage(`{"intentID":"original-intent","boundary":"kernel_child_grant_parent_cancel_no_effect"}`),
	}
	if !ValidDeferredChildGrantCancellationProofAdmission(correct) ||
		!validDeferredChildGrantCancellation(correct, intent) ||
		!ValidReservedAuthorityOrigin(correct) {
		t.Fatal("original physical busy evidence failed standalone validation")
	}
	for name, edit := range map[string]func(*Item){
		"wrong intent": func(i *Item) {
			i.Payload = json.RawMessage(`{"intentID":"other","boundary":"kernel_child_grant_parent_cancel_no_effect"}`)
		},
		"wrong boundary": func(i *Item) {
			i.Payload = json.RawMessage(`{"intentID":"original-intent","boundary":"unknown-effect"}`)
		},
		"duplicate payload": func(i *Item) {
			i.Payload = json.RawMessage(`{"intentID":"original-intent","intentID":"original-intent","boundary":"kernel_child_grant_parent_cancel_no_effect"}`)
		},
		"unknown payload key": func(i *Item) {
			i.Payload = json.RawMessage(`{"intentID":"original-intent","boundary":"kernel_child_grant_parent_cancel_no_effect","extra":"x"}`)
		},
		"wrong run":          func(i *Item) { i.RunID = "foreign" },
		"wrong turn":         func(i *Item) { i.TurnID = "other" },
		"wrong kind":         func(i *Item) { i.Kind = ItemTool },
		"wrong status":       func(i *Item) { i.Status = ItemWaiting },
		"wrong parent":       func(i *Item) { i.ParentItemID = "foreign" },
		"wrong ID":           func(i *Item) { i.ID = "not-the-reserved-id" },
		"wrong sequence":     func(i *Item) { i.Seq = 2 },
		"foreign Invocation": func(i *Item) { i.InvocationID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			f := correct
			edit(&f)
			if validDeferredChildGrantCancellation(f, intent) {
				t.Fatal("foreign or contradictory deferral accepted as original proof")
			}
			if f.Seq == correct.Seq && f.RunID == correct.RunID && f.TurnID == correct.TurnID &&
				f.InvocationID == correct.InvocationID && (f.Kind != correct.Kind || f.Status != correct.Status ||
				f.ParentItemID != correct.ParentItemID || f.ID != correct.ID || string(f.Payload) != string(correct.Payload)) {
				if ValidReservedAuthorityOrigin(f) && f.ID == correct.ID {
					t.Fatal("reserved proof ID accepted forged origin")
				}
			}
		})
	}
	fake := correct
	fake.ParentItemID = "ordinary-child"
	if ValidReservedAuthorityOrigin(fake) {
		t.Fatal("reserved evidence ID was preoccupied as arbitrary Item")
	}
}

func TestIssue107DeferredChildGrantNeverReplacesMissingRootKernel(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	runner := &Runner{runtime: rt, store: store}
	_, err = runner.resumeDeferredChildGrantCancellation(context.Background(), Turn{ID: "turn"}, "missing", kernel.Snapshot{},
		[]Item{}, map[string][]Item{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("missing original Root was accepted: %v", err)
	}
}
