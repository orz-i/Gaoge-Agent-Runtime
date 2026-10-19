package harness_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type denyingManualNewsPolicy struct{ requests []tools.ExecutionRequest }

func (policy *denyingManualNewsPolicy) PrepareDelegation(_ context.Context, request tools.ExecutionRequest, input harness.DelegateRequest) (harness.DelegateRequest, error) {
	policy.requests = append(policy.requests, tools.CloneExecutionRequest(request))
	if input.RoleID != "general" || input.Goal != "Search today's latest AI news and cite current sources" {
		return harness.DelegateRequest{}, harness.ErrInvalidRequest
	}
	return harness.DelegateRequest{}, tools.NewRecoverableCallErrorAllowCompletion(
		"delegation.current_evidence_unavailable", "This local member has no external evidence tools", harness.ErrInvalidRequest,
	)
}

func TestManualDelegationPreflightRejectsUnarmedNewsBeforeAnyChildSideEffect(t *testing.T) {
	t.Parallel()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
	if err != nil {
		t.Fatal(err)
	}
	handoffs, err := handoff.New(direct)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := harness.NewMemoryStore()
	now := time.Now().UTC()
	turn := harness.Turn{ID: "manual-turn", SessionID: "manual-session", HostTurn: harness.HostRef{Kind: "message", ID: "message-1"}, ConfigSnapshotID: "manual-frozen", Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateTurn(t.Context(), turn); err != nil {
		t.Fatal(err)
	}
	parent := harness.Invocation{ID: "manual-parent", TurnID: turn.ID, CapabilityKey: harness.CapabilityAgent,
		ExecutionClass: harness.ExecutionAgent, ExecutionRefID: "manual-root", Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err = store.CreateInvocation(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	policy := &denyingManualNewsPolicy{}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: direct, Store: store, Clock: fixedClock{},
		Handoffs: handoffs, Relations: relations, ManualDelegationPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	input := harness.DelegateRequest{RoleID: "general", Goal: "Search today's latest AI news and cite current sources"}
	for range 2 {
		_, err = runner.Delegate(t.Context(), turn.ID, input)
		code, _, recoverable := tools.RecoverableCallErrorInfo(err)
		if !recoverable || code != "delegation.current_evidence_unavailable" || !tools.RecoverableCallErrorAllowsCompletion(err) {
			t.Fatalf("manual refusal not safe/recoverable: %v", err)
		}
	}
	if len(policy.requests) != 2 || policy.requests[0].RunID != "manual-root" ||
		policy.requests[0].Call.ID == "" || policy.requests[0].Call.ID != policy.requests[1].Call.ID ||
		policy.requests[0].Call.ToolKey != harness.DelegationToolKey {
		t.Fatalf("unstable, unauthorized manual policy identity: %#v", policy.requests)
	}
	var args struct {
		RoleID string `json:"roleID"`
		Goal   string `json:"goal"`
	}
	if json.Unmarshal(policy.requests[0].Call.Arguments, &args) != nil || args.RoleID != input.RoleID || args.Goal != input.Goal {
		t.Fatalf("missing frozen manual input: %+v", args)
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, 100)
	if err != nil || len(items) != 0 {
		t.Fatalf("refused child added durable items: %+v %v", items, err)
	}
	invocations, err := store.ListInvocations(t.Context(), turn.ID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("refused child added invocation: %+v %v", invocations, err)
	}
	children, err := relations.ListChildren(t.Context(), parent.ExecutionRefID)
	if err != nil || len(children) != 0 {
		t.Fatalf("refused child added relation: %+v %v", children, err)
	}
}
