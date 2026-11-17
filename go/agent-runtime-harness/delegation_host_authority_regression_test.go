package harness

import (
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestUnconfiguredHostDelegationCannotDispatchDirectOrManual(t *testing.T) {
	f := newPR82HandoffFixture(t)
	f.runner.delegationPolicy = nil
	request := DelegateRequest{MemberID: "unapproved", Goal: "execute"}
	if _, err := f.runner.Delegate(t.Context(), f.turn.ID, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("manual bypassed absent Host policy: %v", err)
	}
	if _, err := f.runner.DelegateByExecutionRefID(t.Context(), f.parent.ExecutionRefID, request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("direct Run entry bypassed absent Host policy: %v", err)
	}
	if f.handoff.calls != 0 {
		t.Fatalf("unapproved physical effects: %d", f.handoff.calls)
	}
}

func TestHostSelectedSubtreeCeilingsAreFrozenSeparatelyFromAgentLimits(t *testing.T) {
	f := newPR82HandoffFixture(t)
	config, err := SealConfigSnapshot(f.turn.ID, ConfigSnapshot{
		AdmissionHash: strings.Repeat("a", 64), Model: "root-model",
		SharedBudget: &budget.Limits{MaxChildRuns: 20, MaxConcurrentDescendants: 20},
	}, pr82HandoffClock{}.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	turn := f.turn
	turn.ConfigSnapshotID = config.ID
	limits := budget.Limits{MaxLLMCalls: 2, MaxChildRuns: 1, MaxConcurrentDescendants: 1, MaxConcurrentRuns: 1, MaxCostUnits: 100, MaxStateBytes: 12}
	frozen, _, err := f.runner.prepareDelegation(t.Context(), turn, f.parent, DelegateRequest{
		MemberID: "authorized", Goal: "inspect", Agent: agent.StartRequest{Limits: limits},
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.SubtreeLimits == nil || *frozen.SubtreeLimits != limits {
		t.Fatalf("Host subtree ceilings lost during freeze: got=%+v want=%+v", frozen.SubtreeLimits, limits)
	}
	if frozen.Limits.MaxLLMCalls != 2 || frozen.Limits.MaxChildRuns != 0 ||
		frozen.Limits.MaxConcurrentRuns != 0 || frozen.Limits.MaxConcurrentDescendants != 0 ||
		frozen.Limits.MaxCostUnits != 0 || frozen.Limits.MaxStateBytes != 0 {
		t.Fatalf("physical Agent was given unsupported subtree limits: %+v", frozen.Limits)
	}
}

func TestHostSelectedSubtreeCeilingsSurviveAdmissionAndReplay(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxChildRuns: 8, MaxConcurrentDescendants: 8, MaxCostUnits: 500, MaxStateBytes: 50})
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	turn, err := store.GetTurn(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	policy := budget.Limits{MaxLLMCalls: 2, MaxChildRuns: 1, MaxConcurrentDescendants: 1, MaxConcurrentRuns: 1, MaxCostUnits: 100, MaxStateBytes: 12}
	delegation := handoff.Delegation{ID: "approved-child", ChildRunID: "child-of-root", Status: handoff.StatusQueued,
		Limits: budget.Limits{MaxLLMCalls: 2}, SubtreeLimits: &policy}
	parent := Invocation{ExecutionRefID: request.RunID}
	for range 2 {
		if err = runner.admitDelegationChild(t.Context(), turn, parent, delegation); err != nil {
			t.Fatalf("admission/replay: %v", err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ledger.Runs[delegation.ChildRunID].Limits; got != policy {
		t.Fatalf("persisted subtree budget changed: got=%+v want=%+v", got, policy)
	}
	if ledger.View("").Usage.ChildRuns != 1 {
		t.Fatalf("idempotent replay charged twice: %+v", ledger.View(""))
	}
	coordinator := shared.coordinator()
	if _, err = coordinator.RegisterRun(t.Context(), turn.ID, "grandchild-1", budget.RunBudget{ParentRunID: delegation.ChildRunID}); err != nil {
		t.Fatal(err)
	}
	if _, err = coordinator.RegisterRun(t.Context(), turn.ID, "grandchild-2", budget.RunBudget{ParentRunID: delegation.ChildRunID}); !errors.Is(err, budget.ErrWaiting) {
		t.Fatalf("Host descendant concurrency ceiling not enforced: %v", err)
	}
	changed := delegation
	elevated := policy
	elevated.MaxConcurrentDescendants = 3
	changed.SubtreeLimits = &elevated
	if err = runner.admitDelegationChild(t.Context(), turn, parent, changed); !errors.Is(err, budget.ErrLedgerConflict) {
		t.Fatalf("replay permitted changed subtree budget: %v", err)
	}
}
