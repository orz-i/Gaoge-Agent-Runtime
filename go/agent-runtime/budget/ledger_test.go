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
	} else if dimension, ok := budget.DeniedDimension(err); !ok || dimension != budget.DimensionTotalTokens {
		t.Fatalf("denied dimension=%q ok=%v err=%v", dimension, ok, err)
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
	} else if dimension, ok := budget.DeniedDimension(err); !ok || dimension != budget.DimensionConcurrentRuns {
		t.Fatalf("concurrency dimension=%q ok=%v err=%v", dimension, ok, err)
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

func TestProvenUnsentBudgetDispatchRequeueFencesStaleProof(t *testing.T) {
	coordinator := newLedger(t, budget.Limits{MaxLLMCalls: 1, MaxConcurrentRuns: 1})
	request := reservation("model:retry-same-id", 0, 0)
	if _, err := coordinator.Reserve(t.Context(), "turn", request, true); err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.Dispatch(t.Context(), "turn", request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, 0); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("empty evidence accepted: %v", err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, first.Revision-1); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("stale dispatch revision accepted: %v", err)
	}
	queued, err := coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	value := queued.Reservations[request.ID]
	if value.Status != budget.ReservationWaiting || value.Dispatched || value.Active ||
		value.RequestHash != request.RequestHash || queued.View("").UnknownUsage || queued.View("").Reserved.LLMCalls != 0 {
		t.Fatalf("proven unsent request cannot be retried: value=%+v view=%+v", value, queued.View(""))
	}
	// The next legitimate execution claims exactly the same logical request.
	if _, err = coordinator.Reserve(t.Context(), "turn", request, true); err != nil {
		t.Fatalf("re-admission with stable identity failed: %v", err)
	}
	newer, err := coordinator.Dispatch(t.Context(), "turn", request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, first.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("old worker revoked newer Dispatch: %v", err)
	}
	checked, err := coordinator.Store.LoadBudget(t.Context(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	if !checked.View("").UnknownUsage || !checked.Reservations[request.ID].Dispatched || checked.Revision != newer.Revision {
		t.Fatalf("new worker's uncertain request was refunded: %+v", checked)
	}
	usage := budget.Usage{LLMCalls: 1}
	if _, err = coordinator.Settle(t.Context(), "turn", request.ID, usage, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, newer.Revision+1); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("settled receipt was rewound: %v", err)
	}
}

func TestProvenUnsentBudgetRequeueRefusesUnrelatedLedgerRevisionChange(t *testing.T) {
	coordinator := newLedger(t, budget.Limits{MaxLLMCalls: 3})
	request := reservation("model:side-effects", 0, 0)
	if _, err := coordinator.Reserve(t.Context(), "turn", request, true); err != nil {
		t.Fatal(err)
	}
	snapshot, err := coordinator.Dispatch(t.Context(), "turn", request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.Reserve(t.Context(), "turn", reservation("unrelated", 0, 0), true); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, snapshot.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("concurrent ledger mutation was ignored: %v", err)
	}
	latest, err := coordinator.Store.LoadBudget(t.Context(), "turn")
	if err != nil || !latest.Reservations[request.ID].Dispatched || !latest.View("").UnknownUsage {
		t.Fatalf("conservative uncertain charge was removed: %+v, %v", latest, err)
	}
}

func TestProvenUnsentRequeueNeverRefundsPhysicalToolDispatch(t *testing.T) {
	coordinator := newLedger(t, budget.Limits{MaxToolCalls: 1})
	request := budget.Reservation{ID: "tool:physical-effect", RunID: "root", RequestHash: "tool-input", Requested: budget.Usage{ToolCalls: 1}}
	if _, err := coordinator.Reserve(t.Context(), "turn", request, true); err != nil {
		t.Fatal(err)
	}
	dispatched, err := coordinator.Dispatch(t.Context(), "turn", request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RequeueProvenUnsent(t.Context(), "turn", request.ID, dispatched.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("model proof gate requeued a dispatched Tool: %v", err)
	}
	latest, err := coordinator.Store.LoadBudget(t.Context(), "turn")
	if err != nil || !latest.View("").UnknownUsage || latest.View("").Reserved.ToolCalls != 1 {
		t.Fatalf("physical Tool reservation was refunded: %+v err=%v", latest, err)
	}
}
