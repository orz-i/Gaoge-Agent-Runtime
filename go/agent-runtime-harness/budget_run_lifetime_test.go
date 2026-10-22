package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestRunBudgetLifetimeEndsOnlyOnCommittedTerminalSnapshot(t *testing.T) {
	for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled} {
		t.Run(string(status), func(t *testing.T) {
			store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxConcurrentDescendants: 3})
			invocation := plugin.RunInvocation{RunID: request.RunID}
			_, err := shared.Run(t.Context(), invocation, func(context.Context) (kernel.Snapshot, error) {
				return kernel.Snapshot{Run: kernel.Run{ID: request.RunID, Status: status}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ledger, err := store.LoadBudget(t.Context(), "hosted-budget-turn")
			if err != nil {
				t.Fatal(err)
			}
			if ledger.FinishedRuns[request.RunID] != terminalRuntimeStatus(status) {
				t.Fatalf("status=%s finished=%v", status, ledger.FinishedRuns)
			}
		})
	}
}

func TestDescendantAdmissionReconcilesCommittedFailureAndFreezesCapacityWait(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxConcurrentDescendants: 1})
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	turn, err := store.GetTurn(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	parent := Invocation{ExecutionRefID: request.RunID}
	a := handoff.Delegation{ID: "delegation-a", ChildRunID: "child-a", Status: handoff.StatusQueued}
	if err = runner.admitRoleChild(t.Context(), turn, parent, a); err != nil {
		t.Fatal(err)
	}
	b := handoff.Delegation{ID: "delegation-b", ChildRunID: "child-b", Status: handoff.StatusQueued, Instructions: "frozen input"}
	if err = runner.admitRoleChild(t.Context(), turn, parent, b); !errors.Is(err, budget.ErrWaiting) {
		t.Fatal(err)
	}
	b.RoleID = "frozen-role"
	if _, err = runner.recordDelegationItem(t.Context(), turn, parent, b, ItemWaiting, ""); err != nil {
		t.Fatal(err)
	}
	frozen, found, err := runner.frozenDelegation(t.Context(), turn.ID, b.ID)
	if err != nil || !found || frozen.Instructions != b.Instructions {
		t.Fatalf("frozen=%+v %v", frozen, err)
	}
	b.RoleID = ""
	ledger, err := store.LoadBudget(t.Context(), turn.ID)
	if err != nil || ledger.View("").Usage.ChildRuns != 1 || len(ledger.Runs) != 2 {
		t.Fatalf("queued charged a child: %+v %v", ledger, err)
	}
	child, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: a.ChildRunID, Kind: "test", Actor: kernel.ActorRef{TenantID: "test", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "child", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: child.State, ErrorCode: "test.failure", ErrorDetail: "committed failure"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate process loss before Run middleware can release the lifetime slot.
	restarted := &Runner{store: store, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	if err = restarted.admitRoleChild(t.Context(), turn, parent, b); err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), turn.ID)
	if err != nil || !ledger.FinishedRuns[a.ChildRunID] || ledger.View("").Usage.ChildRuns != 2 || ledger.View("").UnfinishedDescendants != 1 {
		t.Fatalf("recovered=%+v %v", ledger, err)
	}
}
