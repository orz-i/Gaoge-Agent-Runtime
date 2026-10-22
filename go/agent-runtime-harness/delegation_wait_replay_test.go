package harness

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type countingFrozenDelegationPolicy struct {
	calls int
}

func (policy *countingFrozenDelegationPolicy) PrepareDelegation(
	_ context.Context, _ tools.ExecutionRequest, request DelegateRequest,
) (DelegateRequest, error) {
	policy.calls++
	request.Goal += " [host-approved]"
	return request, nil
}

// ItemWaiting is a persistent frozen attempt, not an invitation to re-run
// product-side preparation when the same logical Tool is resumed.
func TestPreparedDelegationReusesFrozenWaitingOrStartedIntent(t *testing.T) {
	for _, status := range []ItemStatus{ItemWaiting, ItemStarted} {
		t.Run(string(status), func(t *testing.T) {
			store, _, modelRequest := hostedBudgetFixture(t, budget.Limits{MaxConcurrentDescendants: 1})
			invocation, err := store.GetInvocationByExecutionRefID(t.Context(), modelRequest.RunID)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := store.GetTurn(t.Context(), invocation.TurnID)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{store: store, clock: hostedBudgetClock{}}
			policy := &countingFrozenDelegationPolicy{}
			handler := NewDelegationToolHandler(policy)
			callID := "child-at-capacity"
			execution := tools.ExecutionRequest{RunID: modelRequest.RunID, Call: tools.Call{
				ID: callID, ToolKey: DelegationToolKey, Arguments: json.RawMessage(`{"roleID":"researcher","goal":"lookup"}`),
			}}
			prepared, err := handler.prepareRequest(t.Context(), runner, execution, DelegateRequest{RoleID: "researcher", Goal: "lookup"})
			if err != nil || policy.calls != 1 {
				t.Fatalf("initial preparation=%+v calls=%d err=%v", prepared, policy.calls, err)
			}
			delegation := handoff.Delegation{
				ID: delegationToolID(invocation, callID), RoleID: prepared.RoleID, Goal: prepared.Goal,
				ChildRunID: "not-started-child", Status: handoff.StatusQueued,
			}
			if _, err = runner.recordDelegationItem(t.Context(), turn, invocation, delegation, status, ""); err != nil {
				t.Fatal(err)
			}
			frozen, found, err := runner.frozenDelegation(t.Context(), turn.ID, delegation.ID)
			if err != nil || !found || frozen.Goal != prepared.Goal {
				t.Fatalf("durable frozen request lost: found=%v frozen=%+v err=%v", found, frozen, err)
			}
			// Resumed args cannot override the identity and host-approved goal.
			recovered, err := handler.prepareRequest(t.Context(), runner, execution,
				DelegateRequest{RoleID: "attacker", Goal: "changed goal"})
			if err != nil || recovered.RoleID != prepared.RoleID ||
				recovered.Goal != prepared.Goal || recovered.callID != callID || policy.calls != 1 {
				t.Fatalf("resumption repeated policy or ignored freeze: recovered=%+v calls=%d err=%v", recovered, policy.calls, err)
			}
			// Multiple wakeups must remain idempotent.
			_, err = handler.prepareRequest(t.Context(), runner, execution, DelegateRequest{RoleID: "attacker", Goal: "another change"})
			if err != nil || policy.calls != 1 {
				t.Fatalf("duplicate wakeup re-ran policy: calls=%d err=%v", policy.calls, err)
			}
		})
	}
}
