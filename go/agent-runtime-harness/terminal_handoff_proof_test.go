package harness

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

func originalTerminalHandoffFixture(t *testing.T) (Invocation, Invocation, []Item) {
	t.Helper()
	root := Invocation{ID: "root-invocation", TurnID: "turn-1", ExecutionRefID: "root-run",
		ExecutionClass: ExecutionAgent, CapabilityKey: CapabilityAgent,
		Attempt: 1, Status: InvocationCompleted}
	delegation := handoff.Delegation{
		ID: "original-delegation", ChildRunID: "original-child-run",
		Goal: "investigate", MemberID: "original-member", Status: handoff.StatusQueued,
	}
	startID := stableID("hid", root.TurnID, delegation.ID, string(ItemStarted))
	childID, err := InvocationID(root.TurnID, startID, CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	// A real Handoff is accepted only after its originating Runtime root
	// was still Running. Preserve the exact first-party sealed envelope
	// even though this terminal-proof fixture observes that root later.
	admissionRoot := root
	admissionRoot.Status = InvocationRunning
	input, hash, err = sealChildRootInput(input, hash, admissionRoot)
	if err != nil {
		t.Fatal(err)
	}
	child := Invocation{ID: childID, TurnID: root.TurnID,
		ParentItemID: startID, CapabilityKey: CapabilityAgent,
		ExecutionClass: ExecutionAgent, ExecutionRefID: delegation.ChildRunID,
		Input: input, InputHash: hash, Status: InvocationAccepted}
	startPayload, err := json.Marshal(delegationItemPayload{
		DelegationID: delegation.ID, MemberID: delegation.MemberID,
		ChildRunID: delegation.ChildRunID, Goal: delegation.Goal,
		Status: handoff.StatusQueued, ParentRunID: root.ExecutionRefID,
	})
	if err != nil {
		t.Fatal(err)
	}
	start := Item{ID: startID, TurnID: root.TurnID, InvocationID: root.ID,
		RunID: child.ExecutionRefID, Kind: ItemDelegation, Status: ItemStarted,
		Seq: 1, Payload: startPayload}
	donePayload, err := json.Marshal(delegationItemPayload{
		DelegationID: delegation.ID, MemberID: delegation.MemberID,
		ChildRunID: delegation.ChildRunID, Goal: delegation.Goal,
		Status: handoff.StatusCompleted, ParentRunID: root.ExecutionRefID,
		Result: json.RawMessage(`"result"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	done := Item{ID: stableID("hid", root.TurnID, delegation.ID, string(ItemCompleted)),
		TurnID: root.TurnID, InvocationID: root.ID, RunID: child.ExecutionRefID,
		Kind: ItemDelegation, Status: ItemCompleted, Seq: 2,
		ParentItemID: start.ID, Payload: donePayload}
	grant := Item{ID: "grant-for-child", TurnID: root.TurnID,
		InvocationID: child.ID, RunID: child.ExecutionRefID, Kind: ItemDiagnostic,
		Status: ItemStarted, Seq: 3, ParentItemID: ExecutionDispatchParentItemID,
		Payload: json.RawMessage(`{"phase":"handoff-start","physicalRunID":"original-child-run"}`)}
	returned := Item{ID: stableID("hdispatchdone", grant.ID), TurnID: root.TurnID,
		InvocationID: child.ID, RunID: child.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted, Seq: 4,
		ParentItemID: grant.ID,
		Payload:      json.RawMessage(`{"boundary":"handoff_start_returned"}`)}
	return root, child, []Item{start, done, grant, returned}
}

func TestTerminalHandoffChildProofRequiresOriginalTerminalAndPhysicalReceipts(t *testing.T) {
	root, child, original := originalTerminalHandoffFixture(t)
	if !VerifiedTerminalHandoffChild(root, child, original) ||
		!ValidTurnTerminalDescendants(root, []Invocation{root, child}, original) {
		t.Fatal("complete original terminal Handoff proof rejected")
	}
	for _, tc := range []struct {
		name   string
		change func([]Item) []Item
	}{
		{"terminal-missing", func(items []Item) []Item { return append(items[:1], items[2:]...) }},
		{"grant-missing", func(items []Item) []Item { return append(items[:2], items[3:]...) }},
		{"return-missing", func(items []Item) []Item { return items[:3] }},
		{"terminal-wrong-parent", func(items []Item) []Item { items[1].ParentItemID = "foreign"; return items }},
		{"terminal-wrong-run", func(items []Item) []Item { items[1].RunID = "other-child"; return items }},
		{"terminal-wrong-status", func(items []Item) []Item { items[1].Status = ItemStarted; return items }},
		{"terminal-wrong-id", func(items []Item) []Item { items[1].ID = "not-original-result"; return items }},
		{"terminal-wrong-parent-run", func(items []Item) []Item {
			items[1].Payload = json.RawMessage(`{"delegationID":"original-delegation","childRunID":"original-child-run","goal":"investigate","memberID":"original-member","status":"completed","parentRunID":"foreign-root-run"}`)
			return items
		}},
		{"grant-wrong-status", func(items []Item) []Item { items[2].Status = ItemCompleted; return items }},
		{"receipt-wrong-boundary", func(items []Item) []Item {
			items[3].Payload = json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)
			return items
		}},
		{"contradictory-second-completion", func(items []Item) []Item {
			extra := items[1]
			extra.ID = "another-claimed-result"
			extra.Seq = 5
			return append(items, extra)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := append([]Item(nil), original...)
			facts = tc.change(facts)
			if VerifiedTerminalHandoffChild(root, child, facts) ||
				ValidTurnTerminalDescendants(root, []Invocation{root, child}, facts) {
				t.Fatalf("noncanonical terminal Handoff proved original child complete: %s", tc.name)
			}
		})
	}
}
