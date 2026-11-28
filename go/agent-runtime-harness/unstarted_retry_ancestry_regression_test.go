package harness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestUnstartedRetryRejectsMissingFrozenIntermediateHandoff(t *testing.T) {
	old, child, facts := originalTerminalHandoffFixture(t)
	var source terminalHandoffFrozenInput
	if err := json.Unmarshal(child.Input, &source); err != nil {
		t.Fatal(err)
	}
	input, hash, err := marshalInvocationValue(source.Delegation)
	if err != nil {
		t.Fatal(err)
	}
	running := old
	running.Status = InvocationRunning
	orphan := childAttemptBinding{InvocationID: "orphan-intermediate-invocation", ExecutionRefID: "orphan-intermediate-run", Attempt: 1}
	input, hash, err = sealChildRootInput(input, hash, running, orphan)
	if err != nil {
		t.Fatal(err)
	}
	child.Input, child.InputHash, child.Status = input, hash, InvocationCompleted
	child.Revision = 1
	child.Attempt = 1
	child.CreatedAt = time.Now().UTC()
	child.UpdatedAt = child.CreatedAt
	for i := range facts {
		if facts[i].Kind != ItemDelegation {
			continue
		}
		var p delegationItemPayload
		if err := json.Unmarshal(facts[i].Payload, &p); err != nil {
			t.Fatal(err)
		}
		p.ParentRunID = orphan.ExecutionRefID
		raw, _ := json.Marshal(p)
		facts[i].Payload = raw
		facts[i].InvocationID = orphan.InvocationID
	}
	// All signed-looking Handoff/physical receipts match that orphan parent,
	// but the original Store never admitted a parent Invocation with that ID.
	if !VerifiedTerminalHandoffChild(old, child, facts) {
		t.Fatal("fixture not independently Handoff-terminal")
	}
	if ValidTurnTerminalDescendants(old, []Invocation{old, child}, facts) {
		t.Fatal("original Turn terminal guard unexpectedly trusts orphan parent")
	}
	current := old
	current.Attempt = 2
	current.ExecutionRefID = "new-root-run"
	current.Status = InvocationAccepted
	l := budget.Ledger{
		ID: old.TurnID, Revision: 4, Cancelled: true,
		Runs: map[string]budget.RunBudget{old.ExecutionRefID: {}},
		RootRetries: map[string]budget.RootRetryGrant{current.ExecutionRefID: {
			InvocationID: old.ID, OriginalRunID: old.ExecutionRefID, Attempt: 2, Active: true}},
		CancelledRuns: map[string]bool{old.ExecutionRefID: true, current.ExecutionRefID: true},
	}
	intent := Item{ID: "intent", TurnID: old.TurnID, RunID: current.ExecutionRefID, Kind: ItemDiagnostic, Status: ItemStarted,
		Seq: 5, ParentItemID: CancellationFenceParentItemID,
		Payload: json.RawMessage("{\"boundary\":\"cancel_scope_pending\",\"runID\":\"new-root-run\"}")}
	previous := old
	previous.Status = InvocationCancelled
	transition, err := RootRetryTransitionFact(previous, current.ExecutionRefID, child.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 5
	intent.Seq = 6
	evidence := append(facts, transition, intent)
	t.Logf("isHandoff=%t rootbind=%t terminal=%t effectFence=%v history=%t", isHandoffDelegationInvocation(child), historicalRetryRootBound(current, func() childRootBinding { b, _ := frozenChildRoot(child); return b }(), &l, evidence), VerifiedTerminalHandoffChild(old, child, facts), RetryChildEffectFence([]Invocation{child}, evidence, current), validUnstartedRetryHistoricalChildren(current, []Invocation{child}, evidence, &l))
	result := validUnstartedRetryHistoricalChildren(current, []Invocation{child}, evidence, &l)
	t.Logf("original terminal Turn ancestry guard=false; new unstarted cancellation guard=%t despite missing nested parent Invocation", result)
	if result {
		t.Fatal("unstarted cancellation trusted missing intermediate Handoff parent")
	}
}

func TestUnstartedRetryRejectsCorruptHandoffClassification(t *testing.T) {
	old, child, facts := originalTerminalHandoffFixture(t)
	// Corrupted historical Handoff child still uses reserved hid_ ancestry,
	// but its payload no longer decodes as a valid Handoff. The original Turn
	// terminal proof intentionally treats hid_ as Handoff, never ordinary Agent.
	child.Input = json.RawMessage("{\"goal\":\"corrupted historical Handoff\"}")
	child.InputHash = hashInvocationBytes(child.Input)
	child.Status = InvocationCompleted
	child.Revision = 1
	child.Attempt = 1
	child.CreatedAt = time.Now().UTC()
	child.UpdatedAt = child.CreatedAt
	if isHandoffDelegationInvocation(child) {
		t.Fatal("fixture still classified Handoff")
	}
	if ValidTurnTerminalDescendants(old, []Invocation{old, child}, facts) {
		t.Fatal("original root terminal contract accepted corrupted Handoff")
	}
	current := old
	current.Attempt = 2
	current.ExecutionRefID = "next-root-run"
	current.Status = InvocationAccepted
	ledger := budget.Ledger{ID: old.TurnID, Revision: 4, Cancelled: true,
		Runs: map[string]budget.RunBudget{old.ExecutionRefID: {}},
		RootRetries: map[string]budget.RootRetryGrant{current.ExecutionRefID: {
			InvocationID: old.ID, OriginalRunID: old.ExecutionRefID, Attempt: 2, Active: true}},
		CancelledRuns: map[string]bool{old.ExecutionRefID: true, current.ExecutionRefID: true},
	}
	intent := Item{ID: "intent", TurnID: old.TurnID, RunID: current.ExecutionRefID, Kind: ItemDiagnostic, Status: ItemStarted,
		Seq: 5, ParentItemID: CancellationFenceParentItemID,
		Payload: json.RawMessage("{\"boundary\":\"cancel_scope_pending\",\"runID\":\"next-root-run\"}")}
	// The Handoff start/terminal items have been corrupted or lost, but an
	// externally dispatched Handoff child still has a settled physical grant.
	previous := old
	previous.Status = InvocationCancelled
	transition, err := RootRetryTransitionFact(previous, current.ExecutionRefID, child.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 5
	intent.Seq = 6
	evidence := append(append([]Item(nil), facts[2:]...), transition, intent)
	if PendingExecutionDispatch(evidence, "") {
		t.Fatal("physical grant fixture must be settled")
	}
	v := validUnstartedRetryHistoricalChildren(current, []Invocation{child}, evidence, &ledger)
	t.Logf("old root terminal guard rejects malformed frozen Handoff; new cancel-original-Run guard=%t with a previously dispatched child", v)
	if v {
		t.Fatal("corrupt Handoff was downgraded to ordinary completed child")
	}
}
