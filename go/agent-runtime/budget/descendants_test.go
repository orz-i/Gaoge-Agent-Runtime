package budget_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestUnfinishedDescendantAdmissionIsAtomicAndSurvivesRestart(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxConcurrentDescendants: 3})
	var group sync.WaitGroup
	results := make(chan error, 32)
	for i := range 32 {
		group.Go(func() {
			_, err := c.RegisterRun(t.Context(), "turn", fmt.Sprint("child-", i), budget.RunBudget{ParentRunID: "root"})
			results <- err
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, budget.ErrWaiting) {
			t.Fatal(err)
		}
	}
	ledger, err := c.Store.LoadBudget(t.Context(), "turn")
	if err != nil || winners != 3 || ledger.View("").UnfinishedDescendants != 3 || ledger.View("").Usage.ChildRuns != 3 || ledger.View("").ActiveRuns != 0 {
		t.Fatalf("winners=%d ledger=%+v err=%v", winners, ledger.View(""), err)
	}
	restarted := budget.Coordinator{Store: c.Store}
	for id, binding := range ledger.Runs {
		if id == "root" {
			continue
		}
		if _, err = restarted.RegisterRun(t.Context(), "turn", id, binding); err != nil {
			t.Fatal(err)
		}
		if _, err = restarted.FinishRuns(t.Context(), "turn", id); err != nil {
			t.Fatal(err)
		}
		if _, err = restarted.FinishRuns(t.Context(), "turn", id); err != nil {
			t.Fatal(err)
		}
		break
	}
	ledger, err = restarted.RegisterRun(t.Context(), "turn", "replacement", budget.RunBudget{ParentRunID: "root"})
	if err != nil || ledger.View("").UnfinishedDescendants != 3 || ledger.View("").Usage.ChildRuns != 4 {
		t.Fatalf("replacement=%+v err=%v", ledger.View(""), err)
	}
}

func TestWaitingCoordinatorCountsAndTerminalReleaseDoesNotRefundUnknownWork(t *testing.T) {
	c := newLedger(t, budget.Limits{MaxConcurrentDescendants: 3})
	for _, pair := range [][2]string{{"a", "root"}, {"b", "root"}, {"g1", "a"}} {
		if _, err := c.RegisterRun(t.Context(), "turn", pair[0], budget.RunBudget{ParentRunID: pair[1]}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "g2", budget.RunBudget{ParentRunID: "a"}); !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("waiting parent did not occupy capacity: %v", err)
	}
	request := reservation("unknown", 30, 20)
	request.RunID = "b"
	if _, err := c.Reserve(t.Context(), "turn", request, true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Dispatch(t.Context(), "turn", request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CancelRun(t.Context(), "turn", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(t.Context(), "turn", "g2", budget.RunBudget{ParentRunID: "a"}); !errors.Is(err, budget.ErrWaiting) {
		t.Fatal("cancellation intent released a nonterminal Run")
	}
	if _, err := c.FinishRuns(t.Context(), "turn", "b"); err != nil {
		t.Fatal(err)
	}
	ledger, err := c.RegisterRun(t.Context(), "turn", "g2", budget.RunBudget{ParentRunID: "a"})
	if err != nil || ledger.View("").UnfinishedDescendants != 3 || ledger.View("a").UnfinishedDescendants != 2 || !ledger.View("").UnknownUsage || ledger.Reservations[request.ID].Status != budget.ReservationHeld {
		t.Fatalf("nested=%+v err=%v", ledger.View(""), err)
	}
}
