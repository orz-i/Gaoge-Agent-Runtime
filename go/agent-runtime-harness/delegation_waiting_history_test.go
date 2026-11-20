package harness

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWaitingHistoryHasSameMeaningForTerminalAndNoRunCancellation(t *testing.T) {
	old, child, facts := originalTerminalHandoffFixture(t)
	old.Status, old.InputHash = InvocationCancelled, "original-root"
	child.Status, child.Attempt, child.Revision = InvocationCompleted, 1, 1
	child.CreatedAt = time.Now().UTC()
	child.UpdatedAt = child.CreatedAt
	frozen, err := decodeFrozenHandoffDelegation(child.Input)
	if err != nil {
		t.Fatal(err)
	}
	var payload delegationItemPayload
	if err = json.Unmarshal(facts[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Execution = &frozen
	wire, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for i := range facts {
		facts[i].Seq++
	}
	waiting := Item{ID: stableID("hid", old.TurnID, frozen.ID, string(ItemWaiting)), TurnID: old.TurnID,
		InvocationID: old.ID, RunID: child.ExecutionRefID, Kind: ItemDelegation, Status: ItemWaiting, Seq: 1, Payload: wire}
	facts = append(facts, waiting)
	current := old
	current.Attempt, current.ExecutionRefID, current.Status = 2, "replacement-unstarted", InvocationAccepted
	transition, err := RootRetryTransitionFact(old, current.ExecutionRefID, child.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 6
	facts = append(facts, transition)
	accept := func(candidate Invocation, evidence []Item) bool {
		terminal := current
		terminal.Status = InvocationCompleted
		return ValidTurnTerminalDescendants(terminal, []Invocation{terminal, candidate}, evidence) &&
			validUnstartedRetryHistoricalChildren(current, []Invocation{candidate}, evidence, nil)
	}
	if !accept(child, facts) {
		t.Fatal("complete Waiting -> Started -> terminal history lost its original child proof")
	}
	for _, tc := range []struct {
		name   string
		change func(*Invocation, *[]Item)
	}{
		{"live-child", func(c *Invocation, _ *[]Item) { c.Status = InvocationRunning }},
		{"missing-terminal-receipt", func(_ *Invocation, f *[]Item) { *f = append((*f)[:1], (*f)[2:]...) }},
		{"missing-attempt-transition", func(_ *Invocation, f *[]Item) { *f = (*f)[:len(*f)-1] }},
		{"foreign-wait-owner", func(_ *Invocation, f *[]Item) { (*f)[4].InvocationID = "foreign" }},
		{"ambiguous-wait-JSON", func(_ *Invocation, f *[]Item) {
			(*f)[4].Payload = append([]byte(`{"goal":"substituted",`), (*f)[4].Payload[1:]...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, evidence := child, append([]Item(nil), facts...)
			tc.change(&candidate, &evidence)
			if validUnstartedRetryHistoricalChildren(current, []Invocation{candidate}, evidence, nil) {
				t.Fatal("invalid waiting history authorized no-Run cancellation")
			}
		})
	}
}
