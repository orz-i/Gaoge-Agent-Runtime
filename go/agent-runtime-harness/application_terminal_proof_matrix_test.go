package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// A completed Application Invocation is admissible only with the exact
// original Host callback and no contradictory continuation evidence. Exercise
// a cross-product of owner/status, response, Item identity and sibling facts
// rather than testing only the one previously failing recovery path.
func TestCompletedApplicationTerminalProofAdversarialMatrix(t *testing.T) {
	backing := harness.NewMemoryStore()
	_, waiting, turnID, _, _ := runOriginalApplicationCallbackToLostTerminalTurn(t, backing)
	resolved, err := backing.GetInteraction(t.Context(), waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	owners, err := backing.ListInvocations(t.Context(), turnID)
	if err != nil || len(owners) != 1 {
		t.Fatalf("missing original owner: %v %+v", err, owners)
	}
	original, err := backing.ListItems(t.Context(), turnID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(interaction harness.Interaction, owner harness.Invocation, items []harness.Item) bool {
		return harness.ValidTerminalProjectionProof(context.Background(), turnID, "", []harness.Interaction{interaction}, []harness.Invocation{owner}, items)
	}
	if !verify(resolved, owners[0], original) {
		t.Fatal("recovered original Application completion was rejected")
	}
	type mutation func(*harness.Interaction, *harness.Invocation, *[]harness.Item)
	cases := []struct {
		name   string
		mutate mutation
	}{
		{"running-owner", func(_ *harness.Interaction, o *harness.Invocation, _ *[]harness.Item) {
			o.Status = harness.InvocationRunning
		}},
		{"failed-owner", func(_ *harness.Interaction, o *harness.Invocation, _ *[]harness.Item) {
			o.Status = harness.InvocationFailed
		}},
		{"agent-owner", func(_ *harness.Interaction, o *harness.Invocation, _ *[]harness.Item) {
			o.ExecutionClass = harness.ExecutionAgent
		}},
		{"foreign-owner-run", func(_ *harness.Interaction, o *harness.Invocation, _ *[]harness.Item) {
			o.ExecutionRefID = "foreign_application_run"
		}},
		{"changed-business-response", func(i *harness.Interaction, _ *harness.Invocation, _ *[]harness.Item) {
			i.Response = json.RawMessage(`{"forged":true}`)
		}},
		{"unresolved-interaction", func(i *harness.Interaction, _ *harness.Invocation, _ *[]harness.Item) {
			i.Status = harness.InteractionWaiting
		}},
		{"missing-host-receipt", func(_ *harness.Interaction, _ *harness.Invocation, items *[]harness.Item) {
			remaining := make([]harness.Item, 0, len(*items))
			for _, item := range *items {
				if !strings.HasPrefix(item.ID, "hbizverified_") {
					remaining = append(remaining, item)
				}
			}
			*items = remaining
		}},
		{"foreign-receipt-run", func(_ *harness.Interaction, _ *harness.Invocation, items *[]harness.Item) {
			for i := range *items {
				if strings.HasPrefix((*items)[i].ID, "hbizverified_") {
					(*items)[i].RunID = "other_run"
				}
			}
		}},
		{"duplicate-host-receipt", func(_ *harness.Interaction, _ *harness.Invocation, items *[]harness.Item) {
			for _, item := range *items {
				if strings.HasPrefix(item.ID, "hbizverified_") {
					*items = append(*items, item)
					break
				}
			}
		}},
		{"competing-diagnostic-sibling", func(_ *harness.Interaction, _ *harness.Invocation, items *[]harness.Item) {
			for _, item := range *items {
				if strings.HasPrefix(item.ID, "hinteractionitem_") && item.Status == harness.ItemCompleted {
					*items = append(*items, harness.Item{ID: "competing_diagnostic", TurnID: item.TurnID, InvocationID: item.InvocationID,
						Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, Seq: item.Seq + 100, ParentItemID: item.ID, Payload: json.RawMessage(`{}`)})
					break
				}
			}
		}},
		{"orphan-continuation", func(_ *harness.Interaction, _ *harness.Invocation, items *[]harness.Item) {
			for _, item := range *items {
				if strings.HasPrefix(item.ID, "hbizverified_") {
					*items = append(*items, harness.Item{ID: "hcontclaim_orphan", TurnID: item.TurnID, InvocationID: item.InvocationID, RunID: item.RunID,
						Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, Seq: item.Seq + 100, ParentItemID: item.ID, Payload: json.RawMessage(`{}`)})
					break
				}
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidateInteraction := resolved
			candidateOwner := owners[0]
			candidateItems := append([]harness.Item(nil), original...)
			test.mutate(&candidateInteraction, &candidateOwner, &candidateItems)
			if verify(candidateInteraction, candidateOwner, candidateItems) {
				t.Fatalf("accepted modified original App callback evidence: %s", test.name)
			}
		})
	}
}
