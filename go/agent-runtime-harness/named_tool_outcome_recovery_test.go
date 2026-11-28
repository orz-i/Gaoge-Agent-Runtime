package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type namedOutcomeFaultStore struct {
	*memory.Store
	child, event string
	cut          error
	fired        bool
}

func (s *namedOutcomeFaultStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		l := *commitAuth.Lease

		if l.RunID == s.child && !s.fired {
			for _, e := range commitMutation.Events {
				if e.Type == s.event {
					s.fired = true
					return kernel.Snapshot{}, false, s.cut
				}
			}
		}
		return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

// An original locally deferred Named Agent remains a native Root with a
// logical parent. Its recovered result must pass the actual Handoff/Host
// terminal observation and parent receipt path, not just a Kernel unit check.
func TestDeferredNamedAgentRecoveredToolResultReachesParent(t *testing.T) {
	for _, mode := range []string{"committed", "rejected", "bounded-approval-committed"} {
		t.Run(mode, func(t *testing.T) {
			rejected := mode == "rejected"
			boundedApproval := mode == "bounded-approval-committed"
			ctx := t.Context()
			hs := harness.NewMemoryStore()
			ks := &namedOutcomeFaultStore{Store: memory.NewStore(), event: "tool.completed", cut: errors.New("child Tool result CAS cut")}
			if rejected {
				ks.event = "tool.correction_requested"
			}
			rs := memory.NewRunRelationStore()
			m := &roleBudgetModel{}
			physical, childApprovals := 0, 0
			var ownerReceipt tools.ExecutionResult
			compose := func() (*harness.Runner, *agent.Runner, *kernel.Runtime) {
				t.Helper()
				rt, e := kernel.New(kernel.Dependencies{Store: ks})
				if e != nil {
					t.Fatal(e)
				}
				relations, e := runrelation.New(rs, nil)
				if e != nil {
					t.Fatal(e)
				}
				delegation := harness.NewDelegationToolHandler()
				reg, e := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegation), {Definition: tools.Definition{Key: roleEvidenceTool, Name: "read_evidence", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					physical++
					if rejected {
						return tools.RecoverableNoEffectResult(q.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.unavailable", "original no-effect refusal", nil))
					}
					result := tools.ExecutionResult{Content: json.RawMessage(`{ "evidence": "original <>&" }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}
					if boundedApproval {
						result.Receipt.OwnerReceiptDigest = kernel.ManagedDigest("original child owner")
						ownerReceipt = tools.CloneExecutionResult(result)
					}
					return result, nil
				})}})
				if e != nil {
					t.Fatal(e)
				}
				shared, e := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Runtime: rt, Store: hs, Relations: relations, Meter: m, Clock: fixedClock{}})
				if e != nil {
					t.Fatal(e)
				}
				b := mustBudgetAdapter(t, shared)
				deps := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, ToolOutcomeVerifier: delegation, BoundedBudget: b}
				if boundedApproval {
					approval, e := agent.NewBoundedToolApproval(func(_ context.Context, q tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
						if q.Call.ToolKey == roleEvidenceTool {
							childApprovals++
						}
						return plugin.ApprovalNotRequired, nil
					})
					if e != nil {
						t.Fatal(e)
					}
					deps.ApprovalPolicies = []plugin.ApprovalPolicy{approval}
					deps.DelegatedToolResults = agent.ToolOutcomeVerifierFunc(func(ctx context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
						if q.RunID == ks.child && q.Call.ID == ownerReceipt.Receipt.ExecutionID && q.Call.ToolKey == roleEvidenceTool {
							return tools.CloneExecutionResult(ownerReceipt), nil
						}
						return delegation.VerifyCommittedTool(ctx, q)
					})
				}
				native, e := agent.NewRunner(deps)
				if e != nil {
					t.Fatal(e)
				}
				route, e := handoff.NewDeferredAgent(native)
				if e != nil {
					t.Fatal(e)
				}
				h, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: hs, Clock: fixedClock{}, Handoffs: route, Relations: relations, Budget: shared, DelegationPolicy: hostSelectedAgentPolicy{MemberID: "researcher", MemberRevision: "v3", Agent: agent.StartRequest{Model: "specialist-model", ToolKeys: []string{roleEvidenceTool}, Limits: budget.Limits{MaxLLMCalls: 2}}}})
				if e != nil {
					t.Fatal(e)
				}
				if e = delegation.Bind(h); e != nil {
					t.Fatal(e)
				}
				return h, native, rt
			}
			h, native, rt := compose()
			req := testStartRequest()
			req.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
			req.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "researcher", MemberRevision: "v3", ToolKey: roleEvidenceTool}}}
			req.Config.ToolKeys = []string{harness.DelegationToolKey}
			req.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "never"}}
			started, e := h.StartAgentTurn(ctx, req)
			if e != nil || len(started.Subtasks) != 1 {
				t.Fatal("original deferred admission", e, started.Subtasks)
			}
			child := started.Subtasks[0]
			ks.child = child.RunID
			var invID string
			for _, inv := range started.Invocations {
				if inv.ParentItemID != "" && inv.ExecutionRefID == child.RunID {
					invID = inv.ID
				}
			}
			if invID == "" {
				t.Fatal("missing original child invocation")
			}
			prepared, e := rt.Load(ctx, child.RunID)
			if e != nil {
				t.Fatal(e)
			}
			_, e = native.Resume(ctx, child.RunID, prepared.Run.Revision)
			if !errors.Is(e, ks.cut) || !ks.fired || physical != 1 {
				t.Fatal("injection not reached", e, physical)
			}
			h, native, rt = compose()
			interrupted, e := rt.Load(ctx, child.RunID)
			if e != nil {
				t.Fatal(e)
			}
			// Native Resume recovers the original receipt and continues the same
			// child loop; it does not expose the legacy receipt-only yield.
			done, e := native.Resume(ctx, child.RunID, interrupted.Run.Revision)
			if e != nil || done.Run.Status != kernel.RunStatusCompleted || physical != 1 || len(m.requests) != 3 {
				t.Fatal("original receipt recovery and child business consume", e, done.Run.Status, physical, len(m.requests))
			}
			if boundedApproval && (childApprovals != 1 || !native.VerifyDeferredToolResults(ctx, rt, done) || !native.VerifiedDeferredRootControl(ctx, rt, done)) {
				t.Fatal("original bounded approval result/control did not survive child recovery", childApprovals)
			}
			observed, e := h.RefreshInvocation(ctx, invID)
			if e != nil || len(observed.Subtasks) != 1 || observed.Subtasks[0].Status != "completed" {
				t.Fatal("recovered Named Agent result not observable", e, observed.Subtasks)
			}
			root, e := rt.Load(ctx, snapshotExecutionRef(t, started))
			if e != nil {
				t.Fatal(e)
			}
			verified := reconcileDelegationRoot(t, native, root)
			if _, e = native.Resume(ctx, root.Run.ID, verified.Run.Revision); e != nil {
				t.Fatal("parent consume", e)
			}
			final, e := h.Refresh(ctx, started.Turn.ID)
			if e != nil || final.Turn.Status != harness.TurnCompleted || final.Budget == nil || final.Budget.Usage.ToolCalls != 2 || final.Budget.Reserved.ToolCalls != 0 || physical != 1 || len(m.requests) != 4 {
				t.Fatal("recovered Named result did not complete Turn", e, final.Turn.Status, final.Budget, physical, len(m.requests))
			}
			parentRequest := m.requests[len(m.requests)-1]
			var receipt struct {
				Status     string `json:"status"`
				ChildRunID string `json:"childRunID"`
			}
			last := parentRequest.Messages[len(parentRequest.Messages)-1]
			if last.Role != model.RoleTool || json.Unmarshal([]byte(model.TextValue(last.Content)), &receipt) != nil || receipt.Status != "completed" || receipt.ChildRunID != child.RunID {
				t.Fatal("original child result not delivered", model.TextValue(last.Content))
			}
		})
	}
}
