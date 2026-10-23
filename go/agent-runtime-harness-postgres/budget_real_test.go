package harnesspostgres_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresSharedBudgetRaceAndRestart(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	c := budget.Coordinator{Store: first}
	if _, err = c.Ensure(t.Context(), "turn-budget", budget.Limits{MaxTotalTokens: 400, MaxLLMCalls: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RegisterRun(t.Context(), "turn-budget", "root", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 32)
	var group sync.WaitGroup
	for index := range 32 {
		group.Go(func() {
			worker := budget.Coordinator{Store: first}
			if index%2 == 1 {
				worker.Store = second
			}
			id := fmt.Sprint(index)
			_, reserveErr := worker.Reserve(t.Context(), "turn-budget", budget.Reservation{ID: id, RunID: "root", RequestHash: id,
				Requested: budget.Usage{LLMCalls: 1, InputTokens: 30, OutputTokens: 20, TotalTokens: 50}}, true)
			results <- reserveErr
		})
	}
	group.Wait()
	close(results)
	winners, waiting := 0, 0
	for result := range results {
		if result == nil {
			winners++
		} else if errors.Is(result, budget.ErrWaiting) {
			waiting++
		} else {
			t.Fatal(result)
		}
	}
	if winners != 8 || waiting != 24 {
		t.Fatalf("winners=%d waiting=%d", winners, waiting)
	}
	ledger, err := second.LoadBudget(t.Context(), "turn-budget")
	if err != nil || ledger.View("").Reserved.TotalTokens != 400 {
		t.Fatalf("recovered=%+v %v", ledger, err)
	}
	restarted := budget.Coordinator{Store: second}
	for id, reservation := range ledger.Reservations {
		if reservation.Status != budget.ReservationHeld {
			continue
		}
		if _, err = restarted.Dispatch(t.Context(), ledger.ID, id); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err = restarted.Settle(t.Context(), ledger.ID, id, budget.Usage{LLMCalls: 1, InputTokens: 10, OutputTokens: 5, TotalTokens: 15}, []byte(`{"response":"durable"}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	final, err := first.LoadBudget(t.Context(), ledger.ID)
	if err != nil || final.View("").Usage.LLMCalls != 8 || final.View("").Usage.TotalTokens != 120 || final.View("").Reserved.TotalTokens != 0 {
		t.Fatalf("final=%+v %v", final, err)
	}
	testRealDescendantLifetimeAdmission(t, first, second)
}

func testRealDescendantLifetimeAdmission(t *testing.T, first, second budget.LedgerStore) {
	t.Helper()
	c := budget.Coordinator{Store: first}
	const scope = "descendant-lifetimes"
	if _, err := c.Ensure(t.Context(), scope, budget.Limits{MaxConcurrentDescendants: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RegisterRun(t.Context(), scope, "root", budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 32)
	var group sync.WaitGroup
	for i := range 32 {
		group.Go(func() {
			coordinator := budget.Coordinator{Store: first}
			if i%2 == 1 {
				coordinator.Store = second
			}
			_, err := coordinator.RegisterRun(t.Context(), scope, fmt.Sprint("child-", i), budget.RunBudget{ParentRunID: "root"})
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
	ledger, err := second.LoadBudget(t.Context(), scope)
	if err != nil || winners != 3 || ledger.View("").UnfinishedDescendants != 3 || len(ledger.Runs) != 4 {
		t.Fatalf("admitted=%d ledger=%+v err=%v", winners, ledger.View(""), err)
	}
	restarted := budget.Coordinator{Store: second}
	for id := range ledger.Runs {
		if id == "root" {
			continue
		}
		if _, err = restarted.FinishRuns(t.Context(), scope, id); err != nil {
			t.Fatal(err)
		}
		break
	}
	ledger, err = c.RegisterRun(t.Context(), scope, "replacement", budget.RunBudget{ParentRunID: "root"})
	if err != nil || ledger.View("").UnfinishedDescendants != 3 || ledger.View("").Usage.ChildRuns != 4 {
		t.Fatalf("recovered=%+v err=%v", ledger.View(""), err)
	}
}

// Two independent PostgreSQL connections must agree on the exact ledger
// Revision that permits revoking a proven-unsent Model dispatch. A previously
// revoked worker must never refund another worker's newer physical request.
func TestRealPostgresProvenUnsentModelDispatchRequeueAcrossWorkers(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	const scope = "turn-unsent-requeue"
	const runID = "agent-unsent-requeue"
	const invocationID = "model:invocation-unsent-requeue"
	a := budget.Coordinator{Store: first}
	b := budget.Coordinator{Store: second}
	if _, err = a.Ensure(t.Context(), scope, budget.Limits{MaxLLMCalls: 1, MaxConcurrentRuns: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = a.RegisterRun(t.Context(), scope, runID, budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	request := budget.Reservation{ID: invocationID, RunID: runID, RequestHash: "frozen-request-hash", Requested: budget.Usage{LLMCalls: 1}}
	if _, err = a.Reserve(t.Context(), scope, request, true); err != nil {
		t.Fatal(err)
	}
	firstDispatch, err := a.Dispatch(t.Context(), scope, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := second.LoadBudget(t.Context(), scope)
	if err != nil || !observed.View("").UnknownUsage {
		t.Fatalf("worker B cannot see held dispatch: ledger=%+v err=%v", observed, err)
	}

	reverted, err := b.RequeueProvenUnsent(t.Context(), scope, invocationID, firstDispatch.Revision)
	if err != nil || reverted.View("").UnknownUsage || reverted.View("").Reserved.LLMCalls != 0 {
		t.Fatalf("worker B could not requeue no-send proof: ledger=%+v err=%v", reverted, err)
	}
	if _, err = a.Reserve(t.Context(), scope, request, true); err != nil {
		t.Fatalf("new claimant cannot reserve logical request: %v", err)
	}
	newer, err := a.Dispatch(t.Context(), scope, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.RequeueProvenUnsent(t.Context(), scope, invocationID, firstDispatch.Revision); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("old worker erased newer dispatch: %v", err)
	}
	latest, err := second.LoadBudget(t.Context(), scope)
	if err != nil || latest.Revision != newer.Revision || !latest.View("").UnknownUsage || !latest.Reservations[invocationID].Dispatched {
		t.Fatalf("newer physical request lost its committed marker: ledger=%+v err=%v", latest, err)
	}
	settled, err := b.Settle(t.Context(), scope, invocationID, budget.Usage{LLMCalls: 1}, []byte(`{"content":"ok"}`))
	if err != nil || settled.View("").Usage.LLMCalls != 1 || settled.View("").Reserved.LLMCalls != 0 || settled.View("").UnknownUsage {
		t.Fatalf("recovered invocation not charged exactly once: ledger=%+v err=%v", settled, err)
	}
}
