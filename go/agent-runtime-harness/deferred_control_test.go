package harness_test

import (
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestDeferredBoundedAgentObservationAndCancellationAcrossReconstruction(t *testing.T) {
	testDeferredBoundedAgentCancellation(t, false)
}

func TestDeferredBoundedAgentHostApprovalPolicyMayCancelBeforePhysicalCallback(t *testing.T) {
	testDeferredBoundedAgentCancellation(t, true)
}

func testDeferredBoundedAgentCancellation(t *testing.T, withHostApprovalPolicy bool) {
	t.Helper()
	hs := harness.NewMemoryStore()
	ks := memory.NewStore()
	rs := memory.NewRunRelationStore()
	m := &roleBudgetModel{}
	policy := hostSelectedAgentPolicy{
		MemberID: "researcher", MemberRevision: "v3",
		Agent: agent.StartRequest{Model: "specialist-model", Instructions: "Original bounded child", ToolKeys: []string{roleEvidenceTool},
			Limits: budget.Limits{MaxLLMCalls: 2}},
	}
	compose := func() (*harness.Runner, *kernel.Runtime, error) {
		d := harness.NewDelegationToolHandler()
		registry, e := tools.NewRegistry([]tools.Registration{
			harness.DelegationToolRegistration(d),
			{Definition: tools.Definition{Key: roleEvidenceTool, Name: "read_evidence", InputSchema: []byte(`{"type":"object"}`)}, Handler: roleEvidenceHandler{}},
		})
		if e != nil {
			return nil, nil, e
		}
		rt, e := kernel.New(kernel.Dependencies{Store: ks})
		if e != nil {
			return nil, nil, e
		}
		relations, e := runrelation.New(rs, nil)
		if e != nil {
			return nil, nil, e
		}
		shared, e := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: hs, Relations: relations, Meter: m, Clock: fixedClock{}})
		if e != nil {
			return nil, nil, e
		}
		bounded, e := shared.Adapter()
		if e != nil {
			return nil, nil, e
		}
		approvals, e := interaction.New(rt)
		if e != nil {
			return nil, nil, e
		}
		var policyLayers []plugin.ApprovalPolicy
		if withHostApprovalPolicy {
			frozenApproval, policyErr := harness.NewFrozenApprovalPolicy(hs, relations)
			if policyErr != nil {
				return nil, nil, policyErr
			}
			policyLayers = []plugin.ApprovalPolicy{frozenApproval}
		}
		toolVerifier, e := shared.VerifiedToolOutcomeVerifier(d)
		if e != nil {
			return nil, nil, e
		}
		original, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: registry, Executor: registry,
			Approvals: interactionadapter.New(approvals), ApprovalPolicies: policyLayers,
			ToolOutcomeVerifier: toolVerifier,
			RunMiddleware:       []plugin.RunMiddleware{bounded}, ModelMiddleware: []plugin.ModelMiddleware{bounded},
			ToolMiddleware: []plugin.ToolMiddleware{bounded}})
		if e != nil {
			return nil, nil, e
		}
		c, e := handoff.NewDeferredAgent(original)
		if e != nil {
			return nil, nil, e
		}
		r, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: original, Store: hs, Clock: fixedClock{},
			Handoffs: c, Relations: relations, Budget: shared, DelegationPolicy: policy})
		if e != nil {
			return nil, nil, e
		}
		if e = d.Bind(r); e != nil {
			return nil, nil, e
		}
		return r, rt, nil
	}
	r, _, err := compose()
	if err != nil {
		t.Fatal(err)
	}
	req := testStartRequest()
	req.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
	req.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	req.Config.ToolKeys = []string{harness.DelegationToolKey, roleEvidenceTool}
	req.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(),
		{Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "always"}}
	started, err := r.StartAgentTurn(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(started.Subtasks) != 1 {
		t.Fatalf("no deferred child: %+v", started.Subtasks)
	}
	task := started.Subtasks[0]
	if task.Status != "running" {
		t.Fatalf("deferred child should remain pending, got %+v", task)
	}
	var childID string
	for _, inv := range started.Invocations {
		if inv.ParentItemID != "" && inv.ExecutionRefID == task.RunID {
			childID = inv.ID
		}
	}
	if childID == "" {
		t.Fatal("missing immutable Handoff child Invocation")
	}
	second, rt, err := compose()
	if err != nil {
		t.Fatal(err)
	}
	observed, err := second.RefreshInvocation(t.Context(), childID)
	if err != nil || len(observed.Subtasks) != 1 || observed.Subtasks[0].RunID != task.RunID {
		t.Fatalf("reconstructed observation failed: %+v %v", observed.Subtasks, err)
	}
	childBefore, err := rt.Load(t.Context(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if childBefore.Run.Status != kernel.RunStatusRunning || rt.VerifyPreparedRootSettlement(t.Context(), childBefore) != nil {
		t.Fatalf("unexpected prior child status: %s", childBefore.Run.Status)
	}
	cancelled, err := second.CancelSubtask(t.Context(), started.Turn.ID, task.ID, "bounded original cancellation")
	if err != nil || len(cancelled.Subtasks) != 1 || cancelled.Subtasks[0].Status != "cancelled" {
		t.Fatalf("verified original deferred child not cancelled: %+v %v", cancelled.Subtasks, err)
	}
	childAfter, err := rt.Load(t.Context(), task.RunID)
	if err != nil || childAfter.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("kernel Run not cancelled: %+v %v", childAfter.Run, err)
	}
	replayed, retryErr := second.CancelSubtask(t.Context(), started.Turn.ID, task.ID, "duplicate")
	if retryErr != nil && !errors.Is(retryErr, harness.ErrConflict) {
		t.Fatalf("terminal duplicate produced unknown effects: %v", retryErr)
	}
	if retryErr == nil && (len(replayed.Subtasks) != 1 || replayed.Subtasks[0].Status != "cancelled") {
		t.Fatalf("terminal replay changed original outcome: %+v", replayed.Subtasks)
	}
	if _, err = second.RefreshInvocation(t.Context(), childID); err != nil && !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("original terminal observation rejected: %v", err)
	}
	// Only the original parent's Model was dispatched. The deferred child's
	// physical request never ran before the separately authorized cancellation.
	if len(m.requests) != 1 {
		t.Fatalf("unexpected Model effects %d", len(m.requests))
	}
}
