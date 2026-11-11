package harness

import (
	"context"
	"encoding/json"
	"testing"
)

func continuationReceiptFixture() (Interaction, Invocation, Item, Item, Item) {
	interaction := Interaction{
		ID: "interaction-A", TurnID: "turn-A", InvocationID: "owner-A", Status: InteractionResolved,
	}
	owner := Invocation{ID: "owner-A", TurnID: "turn-A", ExecutionRefID: "run-A", Status: InvocationRunning}
	response := auditVerifiedHostReceipt(interaction, owner)
	response.Seq = 11
	claim := Item{
		ID: stableID("hcontclaim", interaction.ID), TurnID: interaction.TurnID,
		Seq: 12, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: response.ID,
		Payload: json.RawMessage(`{"interactionID":"interaction-A","boundary":"runtime_continuation_claimed"}`),
	}
	completed := Item{
		ID: interactionContinuationCompletedItemID(interaction.ID), TurnID: interaction.TurnID,
		Seq: 13, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: claim.ID,
		Payload: json.RawMessage(`{"interactionID":"interaction-A","boundary":"runtime_continuation_completed"}`),
	}
	return interaction, owner, response, claim, completed
}

func TestContinuationCompletionNeedsOriginalVerifiedClaimAndReceipt(t *testing.T) {
	interaction, owner, response, claim, done := continuationReceiptFixture()
	source := auditResolvedInteractionSource(interaction, owner)
	source.Seq = response.Seq - 1
	for _, case_ := range []struct {
		name                                   string
		alter                                  func(*Item, *Item, *Item)
		removeResponse, removeClaim, duplicate bool
	}{
		{"valid", func(_, _, _ *Item) {}, false, false, false},
		{"no-business-receipt", func(_, _, _ *Item) {}, true, false, false},
		{"no-claim", func(_, _, _ *Item) {}, false, true, false},
		{"claim-wrong-parent", func(_, c, _ *Item) { c.ParentItemID = "unrelated" }, false, false, false},
		{"claim-wrong-run", func(_, c, _ *Item) { c.RunID = "other-run" }, false, false, false},
		{"claim-wrong-boundary", func(_, c, _ *Item) {
			c.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"other"}`)
		}, false, false, false},
		{"claim-earlier-than-response", func(r, c, _ *Item) { c.Seq = r.Seq }, false, false, false},
		{"done-wrong-boundary", func(_, _, d *Item) {
			d.Payload = json.RawMessage(`{"interactionID":"interaction-A","boundary":"wrong"}`)
		}, false, false, false},
		{"done-wrong-interaction", func(_, _, d *Item) {
			d.Payload = json.RawMessage(`{"interactionID":"other","boundary":"runtime_continuation_completed"}`)
		}, false, false, false},
		{"done-malformed-json", func(_, _, d *Item) { d.Payload = json.RawMessage(`{"boundary":null}`) }, false, false, false},
		{"done-before-claim", func(_, c, d *Item) { d.Seq = c.Seq }, false, false, false},
		{"done-wrong-run", func(_, _, d *Item) { d.RunID = "other-run" }, false, false, false},
		{"duplicate-completion", func(_, _, _ *Item) {}, false, false, true},
	} {
		t.Run(case_.name, func(t *testing.T) {
			r, c, d := response, claim, done
			case_.alter(&r, &c, &d)
			items := []Item{source}
			if !case_.removeResponse {
				items = append(items, r)
			}
			if !case_.removeClaim {
				items = append(items, c)
			}
			items = append(items, d)
			if case_.duplicate {
				extra := d
				extra.ID = "duplicate-completion"
				extra.Seq = d.Seq + 1
				items = append(items, extra)
			}
			want := case_.name == "valid"
			if got := hasCompletedInteractionContinuation(items, interaction, owner); got != want {
				t.Fatalf("completion proof %q got %t want %t", case_.name, got, want)
			}
		})
	}
}

func TestContinuationClaimNeedsActualHostReceiptAndOriginalRun(t *testing.T) {
	interaction, owner, response, claim, _ := continuationReceiptFixture()
	source := auditResolvedInteractionSource(interaction, owner)
	source.Seq = response.Seq - 1
	for _, tc := range []struct {
		name         string
		mutate       func(*Item)
		omitResponse bool
	}{
		{"valid", func(*Item) {}, false},
		{"no-business-receipt", func(*Item) {}, true},
		{"wrong-run", func(c *Item) { c.RunID = "other-run" }, false},
		{"wrong-parent", func(c *Item) { c.ParentItemID = "unrelated" }, false},
		{"wrong-payload", func(c *Item) { c.Payload = json.RawMessage(`{"boundary":"wrong"}`) }, false},
		{"wrong-seq", func(c *Item) { c.Seq = response.Seq }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := []Item{source}
			if !tc.omitResponse {
				items = append(items, response)
			}
			c := claim
			tc.mutate(&c)
			items = append(items, c)
			store := &proofReadOnlyStore{items: items, owners: map[string]Invocation{owner.ID: owner}}
			runner := &Runner{store: store}
			claimed, err := runner.interactionContinuationClaimed(context.Background(), interaction)
			if tc.name == "valid" {
				if err != nil || !claimed {
					t.Fatalf("valid claim not recognized: claimed=%t err=%v", claimed, err)
				}
			} else if err == nil && claimed {
				t.Fatalf("unsafe claim accepted: %s", tc.name)
			}
		})
	}
}
