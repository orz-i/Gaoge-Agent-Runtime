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

// This mimics a process failing between the ledger CAS and the Harness item
// projection (before the child Kernel Run exists). The same logical child
// registration must replay without a second charge.
type budgetProjectionCrashStore struct{ *MemoryStore }

func (store budgetProjectionCrashStore) AppendItem(ctx context.Context, item Item) (Item, bool, error) {
	if item.Kind == ItemBudget {
		return Item{}, false, errors.New("injected budget projection crash")
	}
	return store.MemoryStore.AppendItem(ctx, item)
}

func TestChildRegistrationProjectionCrashCanReplayOriginalRunID(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxChildRuns: 1, MaxConcurrentDescendants: 1})
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := store.GetTurn(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	parent := Invocation{ExecutionRefID: request.RunID}
	delegation := handoff.Delegation{ID: "crashed", ChildRunID: "child-crashed", Status: handoff.StatusQueued}
	failed := &Runner{store: budgetProjectionCrashStore{store}, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	if err = failed.admitRoleChild(t.Context(), turn, parent, delegation); err == nil {
		t.Fatal("fault not injected after budget registration")
	}
	if _, e := runtime.Load(t.Context(), delegation.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("child unexpectedly exists: %v", e)
	}
	resumed := &Runner{store: store, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	if err = resumed.admitRoleChild(t.Context(), turn, parent, delegation); err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.View("").Usage.ChildRuns != 1 || ledger.View("").UnfinishedDescendants != 1 {
		t.Fatalf("replay charged twice or lost admission: %+v", ledger.View(""))
	}
	if _, err = runtime.Create(t.Context(), kernel.CreateRequest{ID: delegation.ChildRunID, Kind: "test", Actor: kernel.ActorRef{TenantID: "test", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "replayed", State: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestRegisteredButNeverCreatedChildIsFencedAfterTerminalParent(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxConcurrentDescendants: 2})
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
	a := handoff.Delegation{ID: "owner", ChildRunID: "owner-run", Status: handoff.StatusQueued}
	if err = runner.admitRoleChild(t.Context(), turn, parent, a); err != nil {
		t.Fatal(err)
	}
	owner, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: a.ChildRunID, Kind: "test", Actor: kernel.ActorRef{TenantID: "test", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "owner", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	orphan := handoff.Delegation{ID: "lost", ChildRunID: "never-created", Status: handoff.StatusQueued}
	if err = runner.admitRoleChild(t.Context(), turn, Invocation{ExecutionRefID: a.ChildRunID}, orphan); err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(t.Context(), turn.ID)
	if err != nil || ledger.View("").UnfinishedDescendants != 2 {
		t.Fatalf("initial=%+v %v", ledger.View(""), err)
	}
	if _, err = runtime.Apply(t.Context(), owner.Run.ID, owner.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: owner.State, ErrorCode: "lost.parent"}); err != nil {
		t.Fatal(err)
	}
	b := handoff.Delegation{ID: "replacement", ChildRunID: "replacement-run", Status: handoff.StatusQueued}
	if err = runner.admitRoleChild(t.Context(), turn, parent, b); err != nil {
		t.Fatal(err)
	}
	ledger, err = store.LoadBudget(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ledger.FinishedRuns[orphan.ChildRunID] || !ledger.CancelledRuns[orphan.ChildRunID] ||
		ledger.View("").UnfinishedDescendants != 1 || ledger.View("").Usage.ChildRuns != 3 {
		t.Fatalf("recovery must fence orphan and release only capacity: %+v", ledger)
	}
	coord := budget.Coordinator{Store: store}
	_, err = coord.Reserve(t.Context(), turn.ID, budget.Reservation{ID: "late-model", RunID: orphan.ChildRunID,
		RequestHash: "fenced", Requested: budget.Usage{LLMCalls: 1}}, true)
	if !errors.Is(err, budget.ErrExhausted) {
		t.Fatalf("late child could execute after abandoned-owner reconciliation: %v", err)
	}
}
