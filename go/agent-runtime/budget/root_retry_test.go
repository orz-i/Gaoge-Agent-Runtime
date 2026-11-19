package budget_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestRootRetryPreservesHistoricalReservationsAndCancelsOnlyNewGeneration(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 100})
	before := reservation("original", 12, 9)
	if _, err := c.Reserve(t.Context(), "turn", before, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", before.ID); err != nil {
		t.Fatal(err)
	}
	usage := budget.Usage{LLMCalls: 1, InputTokens: 12, OutputTokens: 9, TotalTokens: 21}
	if _, err := c.Settle(t.Context(), "turn", before.ID, usage, []byte(`{"original":"receipt"}`)); err != nil {
		t.Fatal(err)
	}
	cancelled, err := c.CancelRoot(t.Context(), "turn", "root")
	if err != nil || !cancelled.Cancelled || !cancelled.View("root").Cancelled {
		t.Fatalf("original not closed: %+v %v", cancelled, err)
	}
	if _, err := c.Reserve(t.Context(), "turn", reservation("late-original", 1, 1), true); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("original Root reopened: %v", err)
	}
	prepared, err := c.BeginRootRetry(t.Context(), "turn", "invocation", "root", "root-2", 2)
	if err != nil || prepared.RootRetries["root-2"].Active || !prepared.View("root-2").Cancelled {
		t.Fatalf("prepare dispatched before CAS: %+v %v", prepared.RootRetries, err)
	}
	bad, err := c.CommitRootRetry(t.Context(), "turn", "foreign", "root", "root-2", 2)
	if !errors.Is(err, budget.ErrLedgerConflict) || bad.RootRetries["root-2"].Active {
		t.Fatalf("foreign intent accepted: %v", err)
	}
	active, err := c.CommitRootRetry(t.Context(), "turn", "invocation", "root", "root-2", 2)
	if err != nil || !active.RootRetries["root-2"].Active || active.View("root-2").Cancelled || active.View("").Cancelled || !active.View("root").Cancelled {
		t.Fatalf("wrong generation reopened: %+v %v", active.RootRetries, err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("stale original cancelled new Attempt: %v", err)
	}
	replayed, err := c.CommitRootRetry(t.Context(), "turn", "invocation", "root", "root-2", 2)
	if err != nil || replayed.Revision != active.Revision {
		t.Fatalf("replay wrote again: %d vs %d %v", replayed.Revision, active.Revision, err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "root-2", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	second := budget.Reservation{ID: "second", RunID: "root-2", RequestHash: "second", Requested: budget.Usage{LLMCalls: 1, TotalTokens: 22, InputTokens: 13, OutputTokens: 9}}
	if _, err := c.Reserve(t.Context(), "turn", second, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Settle(t.Context(), "turn", second.ID, budget.Usage{LLMCalls: 1, InputTokens: 13, OutputTokens: 9, TotalTokens: 22}, []byte(`{"second":"receipt"}`)); err != nil {
		t.Fatal(err)
	}
	restarted := budget.Coordinator{Store: c.Store}
	current, err := restarted.CancelRoot(t.Context(), "turn", "root-2")
	if err != nil || !current.View("root-2").Cancelled || !current.View("").Cancelled || !current.View("root").Cancelled || !current.CancelledRuns["root-2"] {
		t.Fatalf("new generation did not close: %+v %v", current, err)
	}
	if current.View("").Usage.LLMCalls != 2 || current.View("").Usage.TotalTokens != 43 ||
		string(current.Reservations["original"].Receipt) != `{"original":"receipt"}` ||
		string(current.Reservations["second"].Receipt) != `{"second":"receipt"}` {
		t.Fatalf("historical charges lost: %+v", current)
	}
	if _, err := restarted.BeginRootRetry(t.Context(), "turn", "invocation", "root-2", "root-3", 3); err != nil {
		t.Fatalf("known terminal second attempt must remain retryable: %v", err)
	}
	if _, err := restarted.CommitRootRetry(t.Context(), "turn", "invocation", "root-2", "root-3", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.RegisterRun(t.Context(), "turn", "root-3", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.CancelRoot(t.Context(), "turn", "root-2"); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("stale second Attempt cancelled third: %v", err)
	}
	if _, err := restarted.Reserve(t.Context(), "turn", budget.Reservation{ID: "third", RunID: "root-3", RequestHash: "third", Requested: budget.Usage{LLMCalls: 1, TotalTokens: 2, InputTokens: 1, OutputTokens: 1}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reserve(t.Context(), "turn", budget.Reservation{ID: "fourth", RunID: "root-3", RequestHash: "fourth", Requested: budget.Usage{LLMCalls: 1, TotalTokens: 2, InputTokens: 1, OutputTokens: 1}}, true); !errors.Is(err, budget.ErrWaiting) && !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("overdrawn retry not blocked: %v", err)
	}
}

func TestRootRetryKeepsUnresolvedDispatchAndExhaustionFailClosed(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 50})
	if _, err := c.Reserve(t.Context(), "turn", reservation("uncertain", 20, 10), true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", "uncertain"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
		t.Fatal(err)
	}
	old, err := c.BeginRootRetry(t.Context(), "turn", "inv", "root", "next", 2)
	if !errors.Is(err, budget.ErrLedgerConflict) || !errors.Is(err, budget.ErrUnmetered) || old.RootRetries["next"].Active {
		t.Fatalf("unresolved original permitted retry: %+v %v", old, err)
	}
	if _, err := c.Settle(t.Context(), "turn", "uncertain", budget.Usage{LLMCalls: 1, TotalTokens: 20, InputTokens: 10, OutputTokens: 10}, []byte(`{"settled":true}`)); err != nil {
		t.Fatal(err)
	}
	old, err = c.BeginRootRetry(t.Context(), "turn", "inv", "root", "next", 2)
	if !errors.Is(err, budget.ErrExhausted) || len(old.RootRetries) != 0 {
		t.Fatalf("exhausted attempt rotated: %+v %v", old, err)
	}
}

func TestRootRetryIntentCASConcurrentClaimAndCancelPreCreate(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 5})
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
		t.Fatal(err)
	}
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "next-a"
			if i%2 == 1 {
				id = "next-b"
			}
			_, err := c.BeginRootRetry(t.Context(), "turn", "inv", "root", id, 2)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	accepted, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, budget.ErrLedgerConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent prepare: %v", err)
		}
	}
	if accepted != n/2 || conflicts != n/2 {
		t.Fatalf("non-serializable prepare accepted=%d conflicts=%d", accepted, conflicts)
	}
	l, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	var winner string
	for id := range l.RootRetries {
		winner = id
	}
	if len(l.RootRetries) != 1 {
		t.Fatalf("multiple attempts issued: %+v", l.RootRetries)
	}
	if _, err := c.CommitRootRetry(t.Context(), "turn", "inv", "root", winner, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", winner); err != nil {
		t.Fatalf("cannot cancel prepared but not yet created Root: %v", err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", winner, budget.RunBudget{}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("cancelled generation admitted after restart: %v", err)
	}
}
