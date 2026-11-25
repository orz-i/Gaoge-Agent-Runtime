package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type originalApprovalPolicy struct{ calls *int }

func (p originalApprovalPolicy) Name() string { return "fixture.opaque.approval" }
func (p originalApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	*p.calls++
	return plugin.ApprovalNotRequired, nil
}

// Original approval/owner evidence follows the physical Call across receipt
// CAS cuts. A successor cannot borrow its own (or another Call's) approval.
func originalToolApprovalRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, mode := range []string{"normal", "bounded-recovery", "checkpoint-recovery", "result-only-policy", "unmetered-recovery", "held-approved-child", "lost-recovery-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			backing, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
			fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), event: "tool.completed", injected: errors.New("original approved Tool CAS cut")}
			initial := backing
			if mode != "normal" && mode != "held-approved-child" {
				initial = fault
			}
			m := &settledToolModel{}
			physical, approvals, verifications := 0, 0, 0
			const original = `{ "answer": "<>&" }`
			owner := tools.ExecutionResult{Content: json.RawMessage(original), Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("independent owner")}}
			current := tools.CloneExecutionResult(owner)
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				if q.Call.ID != owner.Receipt.ExecutionID {
					t.Fatal("wrong original Call")
				}
				if mode == "held-approved-child" {
					return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "pending"}, Pending: &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: q.Call.ID, ChildRunID: "original-child"}}, nil
				}
				return tools.CloneExecutionResult(owner), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			reader := agent.ToolOutcomeVerifierFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				verifications++
				if q.RunID != "approved-root" || q.Call.ID != "original-call" || string(q.Call.Arguments) != `{"query":"original"}` {
					return tools.ExecutionResult{}, errors.New("foreign owner request")
				}
				return tools.CloneExecutionResult(current), nil
			})
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, withPolicy bool) *agent.Runner {
				t.Helper()
				b, err := agent.NewBoundedBudgetAdapter(l, settledToolPorts{id: "original-ledger"})
				if err != nil {
					t.Fatal(err)
				}
				policy, err := agent.NewBoundedToolApproval(func(_ context.Context, _ tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					approvals++
					if mode == "checkpoint-recovery" {
						return plugin.ApprovalRequired, nil
					}
					return plugin.ApprovalNotRequired, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "result-only-policy" {
					policy = originalApprovalPolicy{calls: &approvals}
				}
				interactions, err := interaction.New(rt)
				if err != nil {
					t.Fatal(err)
				}
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Approvals: interactionadapter.New(interactions), DelegatedToolResults: reader, ToolOutcomeVerifier: reader, DeferResumption: true}
				if mode != "unmetered-recovery" {
					d.ToolMiddleware = []plugin.ToolMiddleware{b}
				}
				if withPolicy {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{policy}
				}
				r, err := agent.NewRunner(d)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			rt := ea1StartRuntime(t, initial)
			r := makeRunner(rt, ledger, true)
			req := agent.StartRequest{ID: "approved-root", RequestID: "original", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "original approved read", ToolKeys: []string{"read"}}
			final, err := r.StartRun(ctx, req)
			if mode == "checkpoint-recovery" {
				if err != nil || final.Run.Status != kernel.RunStatusWaitingInput || physical != 0 || approvals != 1 {
					t.Fatal("original checkpoint not reached", err, final.Run.Status, physical, approvals)
				}
				final, err = r.ResolveApproval(ctx, final.Run.ID, final.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
				if err == nil && final.Run.Status == kernel.RunStatusRunning {
					final, err = r.Resume(ctx, final.Run.ID, final.Run.Revision)
				}
			}
			if mode == "normal" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if mode == "held-approved-child" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, fault.injected) {
					t.Fatal("fault did not fire", err)
				}
				if physical != 1 || approvals != 1 || len(m.calls) != 1 {
					t.Fatal("original physical scope not reached", physical, approvals, len(m.calls))
				}
				before, err := observer.Load(ctx, req.ID)
				if err != nil {
					t.Fatal(err)
				}
				g, err := observer.LookupChildExecution(ctx, req.ID)
				if err != nil {
					t.Fatal(err)
				}
				c, err := observer.LookupOwnedPhysicalCallbackReturn(ctx, g)
				if err != nil {
					t.Fatal(err)
				}
				b, found, err := c.ToolReturns.Find("original-call")
				if err != nil || !found || b.ApprovalDigest == "" || !c.HostCompleted || !c.MatchesReturnSnapshot(before) {
					t.Fatal("missing original approval evidence", c, b, err)
				}
				wantCustody := kernel.OwnedBoundedApprovalReturn
				if mode == "result-only-policy" {
					wantCustody = kernel.OwnedToolResultReturn
				}
				if mode == "checkpoint-recovery" {
					wantCustody = kernel.OwnedCustodyScoped
				}
				if c.ControlCustody != wantCustody {
					t.Fatal("wrong original custody", c.ControlCustody, wantCustody)
				}
				if mode == "held-approved-child" {
					if b.OwnerResultDigest != "" || b.ReceiptDigest != "" {
						t.Fatal("pending call borrowed commit", b)
					}
				} else if b.OwnerResultDigest == "" {
					t.Fatal("known owner result not sealed", b)
				}
				fresh := reopen()
				l := reopenLedger()
				rt = ea1StartRuntime(t, fresh)
				// A replacement without the original approval policy can consume
				// evidence but cannot create new approval authority.
				r = makeRunner(rt, l, false)
				if mode == "unmetered-recovery" {
					current.Content = json.RawMessage(`{"answer":"<>&"}`)
					if _, err = r.ReconcileToolOutcome(ctx, req.ID, before.Run.Revision, "original-call"); !errors.Is(err, agent.ErrToolReceiptMismatch) {
						t.Fatal("changed owner bytes accepted", err)
					}
					current = tools.CloneExecutionResult(owner)
				}
				var staged kernel.Snapshot
				if mode == "lost-recovery-ack" {
					under := &ea1AgentReceiptFault{Store: fresh, ExecutionStore: fresh.(kernel.ExecutionStore), ControlStore: fresh.(kernel.ControlStore), ChildExecutionStore: fresh.(kernel.ChildExecutionStore), ExecutionQuarantineStore: fresh.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: fresh.(kernel.OwnedPhysicalExecutionStore), injected: fault.injected}
					cut := &ea1RootRecoveryFault{ea1AgentReceiptFault: under, phase: "P3"}
					r = makeRunner(ea1StartRuntime(t, cut), l, false)
					if _, err = r.ReconcileToolOutcome(ctx, req.ID, before.Run.Revision, "original-call"); !errors.Is(err, fault.injected) {
						t.Fatal("recovery ACK cut not reached", err)
					}
					staged, err = rt.Load(ctx, req.ID)
					r = makeRunner(rt, l, false)
				} else {
					staged, err = r.ReconcileToolOutcome(ctx, req.ID, before.Run.Revision, "original-call")
				}
				if err != nil || staged.Run.Revision != before.Run.Revision+1 || physical != 1 || approvals != 1 || len(m.calls) != 1 {
					t.Fatal("receipt-only recovery changed physical authority", err, staged.Run, physical, approvals, len(m.calls))
				}
				if mode != "result-only-policy" {
					controlReader := makeRunner(rt, l, true)
					if mode != "lost-recovery-ack" {
						if !controlReader.VerifiedDeferredRootControl(ctx, rt, staged) {
							t.Fatal("original staged owner proof not observable")
						}
					}
					current.Content = json.RawMessage(`{"answer":"<>&"}`)
					if controlReader.VerifiedDeferredRootControl(ctx, rt, staged) {
						t.Fatal("staged receipt bypassed independent owner proof")
					}
					current = tools.CloneExecutionResult(owner)
				}
				final, err = r.Resume(ctx, req.ID, staged.Run.Revision)
				if err != nil {
					t.Fatal("consume original approval result", err)
				}
				r = makeRunner(rt, l, true)
			}
			if final.Run.Status != kernel.RunStatusCompleted || physical != 1 || approvals != 1 || len(m.calls) != 2 {
				t.Fatal("original approval executed again", final.Run.Status, physical, approvals, len(m.calls))
			}
			if !r.VerifyDeferredToolResults(ctx, rt, final) {
				t.Fatal("original approval result history not observable", verifications)
			}
			if got := r.VerifiedDeferredRootControl(ctx, rt, final); got != (mode != "result-only-policy") {
				t.Fatal("approval control classification changed", got, mode)
			}
			if rt.VerifyOwnedRootSettlement(ctx, final) == nil {
				t.Fatal("original approval policy upgraded to unrestricted scoped custody")
			}
			current.Content = json.RawMessage(`{"answer":"<>&"}`)
			if r.VerifyDeferredToolResults(ctx, rt, final) || r.VerifiedDeferredRootControl(ctx, rt, final) {
				t.Fatal("changed owner result acquired observation/control")
			}
			current = tools.CloneExecutionResult(owner)
			if !r.VerifyDeferredToolResults(ctx, rt, final) {
				t.Fatal("exact original owner result lost")
			}
			seen := 0
			for _, msg := range m.calls[1].Messages {
				if msg.Role == model.RoleTool && msg.ToolCallID == "original-call" {
					seen++
					if model.TextValue(msg.Content) != original {
						t.Fatal("changed model-facing bytes", model.TextValue(msg.Content))
					}
				}
			}
			if seen != 1 {
				t.Fatal("missing or duplicate original result", seen)
			}
		})
	}
}
