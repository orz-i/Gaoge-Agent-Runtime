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

// Model/Tool settlement does not supply a CostUnits or StateBytes measurement.
// Accepting either as a hard ceiling would advertise a constraint that is not enforced.
func TestSharedBudgetRejectsUnmeteredLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits budget.Limits
	}{
		{"cost", budget.Limits{MaxCostUnits: 1}},
		{"state", budget.Limits{MaxStateBytes: 1}},
		{"both", budget.Limits{MaxCostUnits: 1, MaxStateBytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SealConfigSnapshot("turn", ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64), SharedBudget: &tc.limits}, pr82HandoffClock{}.Now())
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("unmetered shared hard limit accepted: %v", err)
			}
		})
	}
}

func TestNamedAgentCannotAuthorizeUnmeteredBudgetLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits budget.Limits
	}{
		{"cost", budget.Limits{MaxCostUnits: 1}},
		{"state", budget.Limits{MaxStateBytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := DelegateRequest{MemberID: "worker", Goal: "inspect", Agent: agent.StartRequest{Limits: tc.limits}}
			if _, err := normalizeDelegateRequest(request); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Host requested unmetered hard limit was accepted: %v", err)
			}
		})
	}
}

// Already-frozen pending child authority may come from an older pre-stable
// binary. New child admission must not discard unsupported limits on replay.
func TestWaitingChildAdmissionRejectsUnmeteredSubtreeLimits(t *testing.T) {
	store, shared, request := hostedBudgetFixture(t, budget.Limits{MaxChildRuns: 4})
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{store: store, runtime: runtime, budgets: shared, clock: hostedBudgetClock{}}
	turn, err := store.GetTurn(t.Context(), "hosted-budget-turn")
	if err != nil {
		t.Fatal(err)
	}
	for _, limits := range []budget.Limits{{MaxCostUnits: 1}, {MaxStateBytes: 1}} {
		frozen := handoff.Delegation{ID: "frozen", MemberID: "member", ChildRunID: "child", Goal: "inspect", Status: handoff.StatusQueued, SubtreeLimits: &limits}
		if err := runner.admitDelegationChild(t.Context(), turn, Invocation{ExecutionRefID: request.RunID}, frozen); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("unsafe frozen child admitted: %+v err=%v", limits, err)
		}
	}
	ledger, err := store.LoadBudget(t.Context(), turn.ID)
	if err != nil && !errors.Is(err, budget.ErrLedgerNotFound) {
		t.Fatal(err)
	}
	if err == nil && (len(ledger.Runs) != 0 || ledger.View("").Usage.ChildRuns != 0) {
		t.Fatalf("invalid frozen child wrote budget authority: %+v", ledger)
	}
}
