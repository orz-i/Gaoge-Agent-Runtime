package harness_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type pendingMemberOwner struct {
	prepared int
	started  bool
	original agent.ExternalToolControl
}

func (p *pendingMemberOwner) Execute(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
	p.prepared++
	deadline := time.Now().UTC().Add(time.Hour)
	return tools.ExecutionResult{Content: []byte("null"), Receipt: tools.Receipt{ExecutionID: req.Call.ID, Disposition: tools.ReceiptDispositionPending},
		Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "external-browser", InputDigest: strings.Repeat("e", 64), DeadlineAt: &deadline}}, nil
}
func (p *pendingMemberOwner) VerifyExternalToolNoEffect(_ context.Context, req tools.ExecutionRequest, control agent.ExternalToolControl) (agent.ExternalToolNoEffectReceipt, error) {
	if p.started || control != p.original || req.Call.ID != control.CallID || req.Call.ToolKey != roleEvidenceTool || req.RunID != control.RunID {
		return agent.ExternalToolNoEffectReceipt{}, agent.ErrToolOutcomeUnknown
	}
	return agent.ExternalToolNoEffectReceipt{ExternalToolControl: control, ReceiptID: "cancel-prepared-browser"}, nil
}

type memberExternalCase struct {
	host    *harness.Runner
	native  *agent.Runner
	runtime *kernel.Runtime
	store   *harness.MemoryStore
	model   *roleBudgetModel
	owner   *pendingMemberOwner
}

func newMemberExternalCase(t *testing.T, withCommittedReader bool, nativeBudget ...bool) memberExternalCase {
	t.Helper()
	fail := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	c := memberExternalCase{store: harness.NewMemoryStore(), model: &roleBudgetModel{}, owner: &pendingMemberOwner{}}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	fail(err)
	c.runtime = rt
	edges, err := runrelation.New(memory.NewRunRelationStore(), nil)
	fail(err)
	delegate := harness.NewDelegationToolHandler()
	registry, err := tools.NewRegistry([]tools.Registration{harness.DelegationToolRegistration(delegate),
		{Definition: tools.Definition{Key: roleEvidenceTool, Name: "prepare_browser", InputSchema: []byte(`{"type":"object"}`)}, Handler: c.owner}})
	fail(err)
	d := harness.BudgetAuthorityDependencies{Store: c.store, Relations: edges, Meter: c.model, Clock: fixedClock{}}
	if len(nativeBudget) != 0 && nativeBudget[0] {
		d.Runtime = rt
	}
	shared, err := harness.NewBudgetAuthority(d)
	fail(err)
	bounded, err := shared.Adapter()
	fail(err)
	approval, err := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
		return plugin.ApprovalNotRequired, nil
	})
	fail(err)
	// A no-effect-only host must retain the same original budget reservation.
	var committed agent.ToolOutcomeVerifier
	if withCommittedReader {
		committed = delegate
	}
	dependencies := agent.Dependencies{Runtime: rt, Model: c.model, Catalog: registry, Executor: registry, ApprovalPolicies: []plugin.ApprovalPolicy{approval},
		ToolOutcomeVerifier: committed, DelegatedToolResults: delegate, ExternalToolNoEffectVerifier: c.owner, RunMiddleware: []plugin.RunMiddleware{bounded},
		ModelMiddleware: []plugin.ModelMiddleware{bounded}, ToolMiddleware: []plugin.ToolMiddleware{bounded}}
	if d.Runtime != nil {
		dependencies.BoundedBudget = bounded
		dependencies.RunMiddleware, dependencies.ModelMiddleware, dependencies.ToolMiddleware = nil, nil, nil
	}
	c.native, err = agent.NewRunner(dependencies)
	fail(err)
	route, err := handoff.NewDeferredAgent(c.native)
	fail(err)
	policy := hostSelectedAgentPolicy{MemberID: "researcher", MemberRevision: "v3", Agent: agent.StartRequest{Model: "specialist-model", Instructions: "Find evidence", ToolKeys: []string{roleEvidenceTool}, Limits: budget.Limits{MaxLLMCalls: 2}}}
	c.host, err = harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: c.native, Store: c.store, Clock: fixedClock{}, Handoffs: route, Relations: edges, Budget: shared, DelegationPolicy: policy})
	fail(err)
	fail(delegate.Bind(c.host))
	return c
}

func TestExternalWaitOnMemberOnlyToolUsesExactElectedSubtree(t *testing.T) {
	for _, target := range []string{"root", "member"} {
		t.Run(target, func(t *testing.T) {
			c := newMemberExternalCase(t, target == "root")
			ctx := t.Context()
			req := testStartRequest()
			req.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 8, MaxToolCalls: 16, MaxTotalTokens: 5000, MaxChildRuns: 3, MaxConcurrentRuns: 1}
			req.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1, LocalToolGrants: []harness.DelegatedLocalToolGrant{{MemberID: "researcher", MemberRevision: "v3", ToolKey: roleEvidenceTool}}}
			req.Config.ToolKeys = []string{harness.DelegationToolKey}
			req.Config.ToolPolicies = []harness.ToolPolicySnapshot{harness.DelegationToolPolicySnapshot(), {Key: roleEvidenceTool, DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalCapability: "per_call", ApprovalMode: "always"}}
			start, err := c.host.StartAgentTurn(ctx, req)
			if err != nil || len(start.Subtasks) != 1 {
				t.Fatalf("start: %+v %v", start.Subtasks, err)
			}
			member := start.Subtasks[0]
			root, ok := harness.TopLevelInvocation(start)
			if !ok {
				t.Fatal("missing root")
			}
			prepared, err := c.native.LoadRun(ctx, member.RunID)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := c.native.Resume(ctx, prepared.Run.ID, prepared.Run.Revision)
			if err != nil || c.owner.prepared != 1 {
				t.Fatalf("member preparation: calls=%d run=%+v err=%v", c.owner.prepared, wait.Run, err)
			}
			control := agent.ExternalToolControl{Actor: wait.Run.Actor, ParentRunID: root.ExecutionRefID, RunID: member.RunID, CallID: "lookup", OperationID: "external-browser", InputDigest: strings.Repeat("e", 64), ControlID: "elected-product-control"}
			c.owner.original = control
			selected := member.RunID
			cancel := func() (harness.Snapshot, error) {
				return c.host.CancelSubtask(ctx, start.Turn.ID, member.ID, "cancel original member")
			}
			if target == "root" {
				selected = root.ExecutionRefID
				cancel = func() (harness.Snapshot, error) { return c.host.Cancel(ctx, start.Turn.ID, "cancel original turn") }
			}
			verify := func(target string, receipt agent.ExternalToolControl) error {
				return c.host.VerifyExternalToolControlScope(ctx, start.Turn.ID, target, receipt)
			}
			if verify(selected, control) == nil {
				t.Fatal("accepted control before native election")
			}
			if _, err = cancel(); err == nil {
				t.Fatal("cancelled before external owner returned proof")
			}
			if err = verify(selected, control); err != nil {
				t.Fatalf("original election: %v", err)
			}
			swapped := control
			swapped.ParentRunID = member.RunID
			if verify(selected, swapped) == nil || verify("sibling", control) == nil {
				t.Fatal("accepted foreign parent or election")
			}
			c.owner.started = true
			if _, err = c.native.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, control); !errors.Is(err, agent.ErrToolOutcomeUnknown) {
				t.Fatalf("started browser: %v", err)
			}
			c.owner.started = false
			applied, err := c.native.ReconcileExternalToolNoEffectForControl(ctx, wait.Run.Revision, control)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.native.ReconcileExternalToolNoEffectForControl(ctx, applied.Run.Revision, control); err != nil {
				t.Fatal(err)
			}
			out, err := cancel()
			if err != nil {
				t.Fatalf("original %s cancellation: %v", target, err)
			}
			child, err := c.native.LoadRun(ctx, member.RunID)
			if err != nil || child.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("member: %+v %v", child.Run, err)
			}
			if target == "root" && out.Turn.Status != harness.TurnCancelled || target == "member" && out.Turn.Status == harness.TurnCancelled {
				t.Fatalf("scope widened or lost: %+v", out.Turn)
			}
			ledger, err := c.store.LoadBudget(ctx, start.Turn.ID)
			if err != nil {
				t.Fatal(err)
			}
			original := ledger.Reservations["tool:"+member.RunID+":lookup"]
			if original.Status != budget.ReservationSettled || original.Usage.ToolCalls != 0 || ledger.View(member.RunID).Usage.ToolCalls != 0 || c.owner.prepared != 1 || len(c.model.requests) != 2 {
				t.Fatalf("budget/effects: %+v calls=%d prepared=%d", original, len(c.model.requests), c.owner.prepared)
			}
		})
	}
}
