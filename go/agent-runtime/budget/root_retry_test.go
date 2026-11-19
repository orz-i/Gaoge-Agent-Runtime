package budget_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestRootRetryAtomicGenerationPreservesHistoryAndCancellation(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 100})
	old := reservation("original", 12, 9)
	if _, err := c.Reserve(t.Context(), "turn", old, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Settle(t.Context(), "turn", old.ID, budget.Usage{LLMCalls: 1, InputTokens: 12, OutputTokens: 9, TotalTokens: 21}, []byte("{\"receipt\":\"original\"}")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
		t.Fatal(err)
	}
	active, err := c.AdmitRootRetry(t.Context(), "turn", "inv", "root", "root-2", 2)
	if err != nil || !active.RootRetries["root-2"].Active || active.View("root-2").Cancelled || active.View("").Cancelled || !active.View("root").Cancelled {
		t.Fatalf("root Retry lost fenced accounting: %+v err=%v", active, err)
	}
	replay, err := c.AdmitRootRetry(t.Context(), "turn", "inv", "root", "root-2", 2)
	if err != nil || replay.Revision != active.Revision {
		t.Fatalf("idempotent grant was rewritten: %+v err=%v", replay, err)
	}
	if _, err = c.CancelRoot(t.Context(), "turn", "root"); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("obsolete original cancel: %v", err)
	}
	if _, err = c.RegisterRun(t.Context(), "turn", "root-2", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	second := budget.Reservation{ID: "second", RunID: "root-2", RequestHash: "second", Requested: budget.Usage{LLMCalls: 1, InputTokens: 13, OutputTokens: 9, TotalTokens: 22}}
	if _, err = c.Reserve(t.Context(), "turn", second, true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Dispatch(t.Context(), "turn", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Settle(t.Context(), "turn", second.ID, budget.Usage{LLMCalls: 1, InputTokens: 13, OutputTokens: 9, TotalTokens: 22}, []byte("{\"receipt\":\"second\"}")); err != nil {
		t.Fatal(err)
	}
	restarted := budget.Coordinator{Store: c.Store}
	closed, err := restarted.CancelRoot(t.Context(), "turn", "root-2")
	if err != nil || !closed.View("").Cancelled || !closed.View("root-2").Cancelled || closed.View("").Usage.LLMCalls != 2 || closed.View("").Usage.TotalTokens != 43 {
		t.Fatalf("historical charges or active cancellation lost: %+v err=%v", closed, err)
	}
	if string(closed.Reservations["original"].Receipt) != "{\"receipt\":\"original\"}" || string(closed.Reservations["second"].Receipt) != "{\"receipt\":\"second\"}" {
		t.Fatal("receipts lost")
	}
	final, err := restarted.AdmitRootRetry(t.Context(), "turn", "inv", "root-2", "root-3", 3)
	if err != nil || !final.RootRetries["root-3"].Active || final.RootRetries["root-2"].Active || final.View("root-3").Cancelled || final.View("").Cancelled {
		t.Fatalf("third Retry not atomic: %+v err=%v", final.RootRetries, err)
	}
	if _, err = restarted.CancelRoot(t.Context(), "turn", "root-2"); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("stale second cancelled successor: %v", err)
	}
	if _, err = restarted.RegisterRun(t.Context(), "turn", "root-3", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Reserve(t.Context(), "turn", budget.Reservation{ID: "third", RunID: "root-3", RequestHash: "third", Requested: budget.Usage{LLMCalls: 1, InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Reserve(t.Context(), "turn", budget.Reservation{ID: "fourth", RunID: "root-3", RequestHash: "fourth", Requested: budget.Usage{LLMCalls: 1, InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, true); !errors.Is(err, budget.ErrWaiting) && !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("overdraft: %v", err)
	}
}

func TestRootRetryAtomicUnknownAndExhaustionFailBeforeGrant(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 50})
	if _, err := c.Reserve(t.Context(), "turn", reservation("original", 20, 10), true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", "original"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRoot(t.Context(), "turn", "root"); err != nil {
		t.Fatal(err)
	}
	l, err := c.AdmitRootRetry(t.Context(), "turn", "inv", "root", "new", 2)
	if !errors.Is(err, budget.ErrLedgerConflict) || !errors.Is(err, budget.ErrUnmetered) || len(l.RootRetries) != 0 {
		t.Fatalf("unknown effect authorized Retry: %+v %v", l, err)
	}
	if _, err = c.Settle(t.Context(), "turn", "original", budget.Usage{LLMCalls: 1, InputTokens: 10, OutputTokens: 10, TotalTokens: 20}, []byte("{\"settled\":true}")); err != nil {
		t.Fatal(err)
	}
	l, err = c.AdmitRootRetry(t.Context(), "turn", "inv", "root", "new", 2)
	if !errors.Is(err, budget.ErrExhausted) || len(l.RootRetries) != 0 {
		t.Fatalf("exhausted grant entered ledger: %+v %v", l, err)
	}
}

func TestRootRetryConcurrentAtomicAdmissionFencesLateOriginal(t *testing.T) {
	for run := 0; run < 32; run++ {
		c := newLedger(t, budget.Limits{MaxLLMCalls: 1})
		var wg sync.WaitGroup
		out := make(chan error, 2)
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := c.AdmitRootRetry(t.Context(), "turn", "inv", "root", "new", 2)
			out <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := c.Reserve(t.Context(), "turn", budget.Reservation{ID: "late", RunID: "root", RequestHash: fmt.Sprint(run), Requested: budget.Usage{LLMCalls: 1}}, true)
			out <- err
		}()
		close(start)
		wg.Wait()
		close(out)
		success := 0
		for err := range out {
			if err == nil {
				success++
				continue
			}
			if !errors.Is(err, budget.ErrLedgerConflict) && !errors.Is(err, budget.ErrExhausted) && !errors.Is(err, budget.ErrUnmetered) {
				t.Fatalf("unexpected race error: %v", err)
			}
		}
		if success != 1 {
			t.Fatalf("non-atomic competing original/Retry: accepted=%d", success)
		}
		ledger, err := c.Store.LoadBudget(t.Context(), "turn")
		if err != nil {
			t.Fatal(err)
		}
		if ledger.RootRetries["new"].Active && ledger.Reservations["late"].Status == budget.ReservationHeld {
			t.Fatal("old physical reservation granted after new Retry")
		}
	}
}
