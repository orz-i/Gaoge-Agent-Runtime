package harness

import (
	"encoding/json"
	"testing"
	"time"
)

func TestHistoricalAttemptProofIsSharedByTerminalAndUnstartedCancellation(t *testing.T) {
	old, child, facts := originalTerminalHandoffFixture(t)
	old.Status, old.InputHash = InvocationCancelled, "original-frozen-input"
	child.Attempt, child.Revision, child.Status = 1, 1, InvocationCompleted
	child.CreatedAt = time.Now().UTC()
	child.UpdatedAt = child.CreatedAt
	second := old
	second.Attempt, second.ExecutionRefID = 2, "second-root"
	firstTransition, err := RootRetryTransitionFact(old, second.ExecutionRefID, child.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	firstTransition.Seq = 5
	third := second
	third.Attempt, third.ExecutionRefID = 3, "third-root"
	secondTransition, err := RootRetryTransitionFact(second, third.ExecutionRefID, child.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	secondTransition.Seq = 6
	facts = append(facts, firstTransition, secondTransition)
	third.Status = InvocationCompleted
	if !ValidTurnTerminalDescendants(third, []Invocation{third, child}, facts) ||
		!validUnstartedRetryHistoricalChildren(third, []Invocation{child}, facts, nil) {
		t.Fatal("Store-owned unbudgeted ancestry did not preserve historical child proof")
	}
	for _, tc := range []struct {
		name   string
		change func(*Invocation, *[]Item)
	}{
		{"no-transition", func(_ *Invocation, items *[]Item) { *items = (*items)[:4] }},
		{"missing-link", func(_ *Invocation, items *[]Item) { *items = append((*items)[:4], (*items)[5:]...) }},
		{"foreign-predecessor", func(_ *Invocation, items *[]Item) {
			var transition rootRetryTransition
			if err := json.Unmarshal((*items)[5].Payload, &transition); err != nil {
				t.Fatal(err)
			}
			transition.Previous.ExecutionRefID = "foreign-root"
			(*items)[5].Payload, _ = json.Marshal(transition)
		}},
		{"wrong-frozen-input", func(_ *Invocation, items *[]Item) {
			var transition rootRetryTransition
			if err := json.Unmarshal((*items)[5].Payload, &transition); err != nil {
				t.Fatal(err)
			}
			transition.InputHash = "substituted"
			(*items)[5].Payload, _ = json.Marshal(transition)
		}},
		{"live-prior-child", func(c *Invocation, _ *[]Item) { c.Status = InvocationRunning }},
		{"missing-terminal-receipt", func(_ *Invocation, items *[]Item) { *items = append((*items)[:1], (*items)[2:]...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changedChild, changedFacts := child, append([]Item(nil), facts...)
			tc.change(&changedChild, &changedFacts)
			if ValidTurnTerminalDescendants(third, []Invocation{third, changedChild}, changedFacts) ||
				validUnstartedRetryHistoricalChildren(third, []Invocation{changedChild}, changedFacts, nil) {
				t.Fatal("one lifecycle path accepted broken historical execution authority")
			}
		})
	}
}
