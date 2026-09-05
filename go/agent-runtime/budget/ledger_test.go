package budget_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func newLedger(t *testing.T, limits budget.Limits) budget.Coordinator {
	t.Helper()
	c := budget.Coordinator{Store: budget.NewMemoryLedgerStore()}
	if _, err := c.Ensure(t.Context(), "turn", limits); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "root", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	return c
}

func reservation(id string, input, output int64) budget.Reservation {
	return budget.Reservation{ID: id, RunID: "root", RequestHash: id, Requested: budget.Usage{
		LLMCalls: 1, InputTokens: input, OutputTokens: output, TotalTokens: input + output,
	}}
}

func TestSharedBudgetConcurrentAdmissionAndSettlement(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxTotalTokens: 100, MaxLLMCalls: 3})
	const count = 16
	results := make(chan error, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() {
			_, err := c.Reserve(t.Context(), "turn", reservation(fmt.Sprint(index), 30, 20), true)
			results <- err
		})
	}
	group.Wait()
	close(results)
	winners, waiting := 0, 0
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, budget.ErrWaiting) {
			waiting++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 2 || waiting != 14 {
		t.Fatalf("winners=%d waiting=%d", winners, waiting)
	}
	ledger, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	for id, value := range ledger.Reservations {
		if value.Status != budget.ReservationHeld {
			continue
		}
		if _, err = c.Dispatch(t.Context(), "turn", id); err != nil {
			t.Fatal(err)
		}
		usage := budget.Usage{LLMCalls: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		for range 2 {
			if _, err = c.Settle(t.Context(), "turn", id, usage, []byte(`{"ok":true}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	ledger, err = c.Store.LoadBudget(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	view := ledger.View("")
	if view.Usage.TotalTokens != 30 || view.Usage.LLMCalls != 2 || view.Reserved.TotalTokens != 0 {
		t.Fatalf("view=%+v", view)
	}
	if _, err = c.Reserve(t.Context(), "turn", reservation("next", 40, 20), true); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Reserve(t.Context(), "turn", reservation("too-large", 80, 20), true); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("err=%v", err)
	}
}

func TestSharedBudgetUnknownDispatchSurvivesCancelAndRestart(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 1, MaxTotalTokens: 100})
	if _, err := c.Reserve(t.Context(), "turn", reservation("call", 60, 40), true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", "call"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Release(t.Context(), "turn", "call"); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("refund=%v", err)
	}
	restarted := budget.Coordinator{Store: c.Store}
	ledger, err := restarted.Cancel(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	if ledger.View("").Reserved.TotalTokens != 100 {
		t.Fatal("cancellation refunded unknown request")
	}
	usage := budget.Usage{LLMCalls: 1, InputTokens: 20, OutputTokens: 10, TotalTokens: 30}
	ledger, err = restarted.Settle(t.Context(), "turn", "call", usage, []byte(`{}`))
	if err != nil || ledger.View("").Reserved.TotalTokens != 0 {
		t.Fatalf("settle=%+v %v", ledger, err)
	}
	if _, err = restarted.Reserve(t.Context(), "turn", reservation("new", 1, 1), true); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("cancelled admission=%v", err)
	}
}

func TestSharedBudgetChildAndRoleLimitsCannotWidenTurn(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxLLMCalls: 5, MaxChildRuns: 2, MaxConcurrentRuns: 1})
	binding := budget.RunBudget{ParentRunID: "root", Limits: budget.Limits{MaxLLMCalls: 1}}
	for range 2 {
		if _, err := c.RegisterRun(t.Context(), "turn", "child", binding); err != nil {
			t.Fatal(err)
		}
	}
	call := reservation("child-call", 0, 0)
	call.RunID = "child"
	if _, err := c.Reserve(t.Context(), "turn", call, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reserve(t.Context(), "turn", reservation("root-call", 0, 0), true); !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("concurrency=%v", err)
	}
	if _, err := c.Settle(t.Context(), "turn", call.ID, call.Requested, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	call.ID, call.RequestHash = "child-next", "child-next"
	if _, err := c.Reserve(t.Context(), "turn", call, true); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("local limit=%v", err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "grandchild", budget.RunBudget{ParentRunID: "child"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "third", budget.RunBudget{ParentRunID: "root"}); !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("children=%v", err)
	}
	ledger, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil || ledger.View("").Usage.ChildRuns != 2 {
		t.Fatalf("children=%+v %v", ledger, err)
	}
}

// A published child relation may be cancelled before any worker registers it.
func TestCancellationFencesUnregisteredChildAcrossCoordinatorRestart(t *testing.T) {
	store := budget.NewMemoryLedgerStore()
	coordinator := budget.Coordinator{Store: store}
	ctx := t.Context()
	if _, err := coordinator.Ensure(ctx, "turn-cancel-queued", budget.Limits{MaxChildRuns: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RegisterRun(ctx, "turn-cancel-queued", "parent", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CancelRun(ctx, "turn-cancel-queued", "queued"); err != nil {
		t.Fatal(err)
	}
	resumed := budget.Coordinator{Store: store}
	ledger, err := resumed.RegisterRun(ctx, "turn-cancel-queued", "queued", budget.RunBudget{ParentRunID: "parent"})
	if !errors.Is(err, budget.ErrExhausted) || !ledger.View("queued").Cancelled || ledger.View("").Usage.ChildRuns != 0 {
		t.Fatalf("cancelled child admitted: %+v, %v", ledger, err)
	}
}
