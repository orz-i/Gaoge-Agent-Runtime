package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestRetryAcceptedHandoffUsesSameOriginalTerminalEvidence(t *testing.T) {
	root, child, facts := originalTerminalHandoffFixture(t)
	root.Status, root.InputHash = InvocationFailed, "original-root-input"
	if err := RetryChildEffectFence([]Invocation{child}, facts, root); err != nil {
		t.Fatalf("verified terminal Accepted child blocked Retry: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Invocation, *[]Item)
	}{
		{"running-host-claim", func(c *Invocation, _ *[]Item) { c.Status = InvocationRunning }},
		{"waiting-host-claim", func(c *Invocation, _ *[]Item) { c.Status = InvocationWaitingInput }},
		{"paused-host-claim", func(c *Invocation, _ *[]Item) { c.Status = InvocationPausedBudget }},
		{"missing-terminal-child", func(_ *Invocation, f *[]Item) { *f = append((*f)[:1], (*f)[2:]...) }},
		{"missing-physical-return", func(_ *Invocation, f *[]Item) { *f = (*f)[:3] }},
		{"foreign-terminal-parent", func(_ *Invocation, f *[]Item) { (*f)[1].InvocationID = "foreign" }},
		{"duplicate-terminal-vote", func(_ *Invocation, f *[]Item) {
			other := (*f)[1]
			other.ID = "another-terminal-vote"
			*f = append(*f, other)
		}},
		{"orphan-frozen-ancestor", func(c *Invocation, _ *[]Item) {
			var source terminalHandoffFrozenInput
			if err := json.Unmarshal(c.Input, &source); err != nil {
				t.Fatal(err)
			}
			source.HarnessRoot.Parents = []childAttemptBinding{{InvocationID: "orphan", ExecutionRefID: "orphan-run", Attempt: 1}}
			c.Input, _ = json.Marshal(source)
			c.InputHash = hashInvocationBytes(c.Input)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, evidence := child, append([]Item(nil), facts...)
			tc.change(&candidate, &evidence)
			if err := RetryChildEffectFence([]Invocation{candidate}, evidence, root); !errors.Is(err, ErrConflict) {
				t.Fatalf("unsettled/ambiguous child authorized new physical Run: %v", err)
			}
		})
	}
	// The same Accepted child remains historical after a committed rotation;
	// no extra mutable child status write is needed for the next generation.
	next := root
	next.Attempt, next.ExecutionRefID = 2, "replacement-root"
	transition, err := RootRetryTransitionFact(root, next.ExecutionRefID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 5
	if err = RetryChildEffectFence([]Invocation{child}, append(facts, transition), next); err != nil {
		t.Fatalf("historical Accepted terminal proof was lost: %v", err)
	}
	if err = RetryChildEffectFence([]Invocation{child}, facts, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("unwitnessed Attempt rotation borrowed a prior child: %v", err)
	}
}
