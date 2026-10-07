package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestHarnessFreezesRuntimeOwnedExecutionDefaultsForNewTurns(t *testing.T) {
	runner := newExecutionPolicyHarness(t, harness.ExecutionPolicy{}, true)
	request := testStartRequest()
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.SharedBudget == nil ||
		snapshot.Config.SharedBudget.MaxChildRuns != 32 ||
		snapshot.Config.SharedBudget.MaxConcurrentRuns != 8 ||
		snapshot.Config.SharedBudget.MaxLLMCalls != 0 ||
		snapshot.Config.DelegationPolicy.MaxDepth != 4 {
		t.Fatalf("resolved execution policy = %#v / %#v", snapshot.Config.SharedBudget, snapshot.Config.DelegationPolicy)
	}
	if snapshot.Budget == nil || snapshot.Budget.Limits.MaxChildRuns != 32 || snapshot.Budget.Limits.MaxConcurrentRuns != 8 {
		t.Fatalf("runtime-owned shared ledger was not created: %#v", snapshot.Budget)
	}
}

func TestHarnessTurnPolicyCanTightenButNotWidenDeploymentPolicy(t *testing.T) {
	runner := newExecutionPolicyHarness(t, harness.ExecutionPolicy{
		SharedBudget: budget.Limits{
			MaxLLMCalls:       20,
			MaxChildRuns:      64,
			MaxConcurrentRuns: 16,
		},
		MaxDelegationDepth: 8,
	}, true)
	request := testStartRequest()
	request.Config.SharedBudget = &budget.Limits{
		MaxLLMCalls:       50,
		MaxToolCalls:      7,
		MaxChildRuns:      3,
		MaxConcurrentRuns: 40,
	}
	request.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 20}
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Config.SharedBudget
	if got == nil || got.MaxLLMCalls != 20 || got.MaxToolCalls != 7 ||
		got.MaxChildRuns != 3 || got.MaxConcurrentRuns != 16 {
		t.Fatalf("resolved shared budget = %#v", got)
	}
	if snapshot.Config.DelegationPolicy.MaxDepth != 8 {
		t.Fatalf("delegation depth widened deployment policy: %#v", snapshot.Config.DelegationPolicy)
	}
}

func TestHarnessWithoutSharedLedgerStillFreezesDelegationSafety(t *testing.T) {
	runner := newExecutionPolicyHarness(t, harness.ExecutionPolicy{}, false)
	request := testStartRequest()
	snapshot, err := runner.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Config.SharedBudget != nil || snapshot.Config.DelegationPolicy.MaxDepth != 4 {
		t.Fatalf("minimal harness policy = %#v / %#v", snapshot.Config.SharedBudget, snapshot.Config.DelegationPolicy)
	}

	runner = newExecutionPolicyHarness(t, harness.ExecutionPolicy{}, false)
	request = testStartRequest()
	request.Config.SharedBudget = &budget.Limits{MaxChildRuns: 1}
	if _, err = runner.Start(t.Context(), request); err == nil {
		t.Fatal("shared budget was accepted without BudgetMiddleware")
	}
}

func newExecutionPolicyHarness(t *testing.T, execution harness.ExecutionPolicy, withBudget bool) *harness.Runner {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	dependencies := harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{}, Execution: execution,
	}
	if withBudget {
		shared, budgetErr := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{
			Store: store, Clock: fixedClock{},
		})
		if budgetErr != nil {
			t.Fatal(budgetErr)
		}
		dependencies.Budget = shared
	}
	runner, err := harness.NewRunner(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
