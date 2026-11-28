package harness

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// Historical Handoff completion is not a current-generation external effect.
// Its original ChildRunner receipts and original root Attempt must be verified
// before closing an accepted-but-unstarted replacement under cancellation.
func TestUnstartedRetryCancellationPreservesHistoricalHandoffProof(t *testing.T) {
	original, completedChild, facts := originalTerminalHandoffFixture(t)
	original.Status = InvocationCancelled
	now := time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)
	completedChild.Status = InvocationCompleted
	completedChild.Attempt = 1
	completedChild.Revision = 1
	completedChild.CreatedAt, completedChild.UpdatedAt = now, now

	next := original
	next.Attempt = 2
	next.ExecutionRefID = "next-root-run"
	next.Status = InvocationAccepted
	next.Revision = 2
	next.CreatedAt, next.UpdatedAt = now, now

	turn := Turn{ID: next.TurnID, Status: TurnRunning}
	ledger := budget.Ledger{
		ID: next.TurnID, Revision: 3, Cancelled: true,
		Runs: map[string]budget.RunBudget{original.ExecutionRefID: {}},
		RootRetries: map[string]budget.RootRetryGrant{
			next.ExecutionRefID: {InvocationID: next.ID, OriginalRunID: original.ExecutionRefID, Attempt: next.Attempt, Active: true},
		},
		CancelledRuns: map[string]bool{original.ExecutionRefID: true, next.ExecutionRefID: true},
	}
	transition, err := RootRetryTransitionFact(original, next.ExecutionRefID, now)
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 5
	facts = append(facts, transition)
	intent := Item{ID: "original-replacement-cancel", TurnID: turn.ID, RunID: next.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted, Seq: 6,
		ParentItemID: CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"next-root-run"}`),
	}
	all := append(append([]Item(nil), facts...), intent)
	accepted := func(children ...Invocation) bool {
		return validUnstartedRetryHistoricalChildren(next, children, all, &ledger)
	}
	if !accepted(completedChild) {
		t.Fatal("complete terminal Handoff from previous Attempt blocked its unstarted successor cancellation")
	}
	// The immutable chain also works after multiple subsequent Attempt
	// rotations; no reconstruction from a mutable current root RunID.
	third := next
	third.Attempt = 3
	third.ExecutionRefID = "third-root-run"
	third.Revision++
	ledger.RootRetries[third.ExecutionRefID] = budget.RootRetryGrant{
		InvocationID: third.ID, OriginalRunID: next.ExecutionRefID, Attempt: third.Attempt, Active: true,
	}
	ledger.RootRetries[next.ExecutionRefID] = budget.RootRetryGrant{
		InvocationID: next.ID, OriginalRunID: original.ExecutionRefID, Attempt: next.Attempt, Active: false,
	}
	ledger.CancelledRuns[third.ExecutionRefID] = true
	previous := next
	previous.Status = InvocationCancelled
	transition, err = RootRetryTransitionFact(previous, third.ExecutionRefID, now)
	if err != nil {
		t.Fatal(err)
	}
	transition.Seq = 6
	all = append(all[:len(all)-1], transition, all[len(all)-1])
	all[len(all)-1].Seq = 7
	all[len(all)-1].RunID = third.ExecutionRefID
	all[len(all)-1].Payload = json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"third-root-run"}`)
	if !validUnstartedRetryHistoricalChildren(third, []Invocation{completedChild}, all, &ledger) {
		t.Fatal("multi-Attempt original terminal Handoff chain incorrectly rejected")
	}

	// A broken historical link, a new running child, or missing physical
	// return evidence must NEVER promote a terminal-looking child to safe.
	for _, tc := range []struct {
		name   string
		change func(*Invocation, *[]Item, *budget.Ledger)
	}{
		{"forged-original-attempt", func(c *Invocation, _ *[]Item, _ *budget.Ledger) {
			c.Input = json.RawMessage(`{"delegationID":"foreign","harnessRoot":{"invocationID":"other"}}`)
		}},
		{"active-historical-child", func(c *Invocation, _ *[]Item, _ *budget.Ledger) {
			c.Status = InvocationRunning
		}},
		{"missing-handoff-return", func(_ *Invocation, items *[]Item, _ *budget.Ledger) {
			*items = append((*items)[:3], (*items)[4:]...)
		}},
		{"missing-terminal-delegation", func(_ *Invocation, items *[]Item, _ *budget.Ledger) {
			*items = append((*items)[:1], (*items)[2:]...)
		}},
		{"orphan-delegation", func(_ *Invocation, items *[]Item, _ *budget.Ledger) {
			*items = append(*items, Item{ID: "orphan", TurnID: turn.ID, Kind: ItemDelegation,
				Status: ItemStarted, RunID: "unknown-remote-child", Seq: 7})
		}},
		{"foreign-previous-run", func(_ *Invocation, _ *[]Item, l *budget.Ledger) {
			grant := l.RootRetries[third.ExecutionRefID]
			grant.OriginalRunID = "foreign-predecessor"
			l.RootRetries[third.ExecutionRefID] = grant
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := completedChild
			items := append([]Item(nil), all...)
			currentLedger := budget.CloneLedger(ledger)
			tc.change(&child, &items, &currentLedger)
			if validUnstartedRetryHistoricalChildren(third, []Invocation{child}, items, &currentLedger) {
				t.Fatal("unverified historical Handoff escaped the original physical effect fence")
			}
		})
	}
}
