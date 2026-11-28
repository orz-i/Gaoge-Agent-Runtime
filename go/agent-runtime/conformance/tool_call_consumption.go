package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type mixedToolModel struct{ requests []model.Request }

func (m *mixedToolModel) Generate(_ context.Context, q model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(q))
	if len(m.requests) == 1 {
		return model.Response{ToolCalls: []tools.Call{
			{ID: "child-a", ToolKey: "read", Arguments: json.RawMessage(`{"q":1}`)},
			{ID: "child-b", ToolKey: "read", Arguments: json.RawMessage(`{"q":2}`)},
			{ID: "terminal-refusal", ToolKey: "read", Arguments: json.RawMessage(`{"q":3}`)},
		}}, nil
	}
	return model.Response{Content: "all original outcomes consumed"}, nil
}

func newToolTestLedger(t *testing.T, factory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) (budget.LedgerStore, func() budget.LedgerStore) {
	if factory != nil {
		return factory(t)
	}
	l := budget.NewMemoryLedgerStore()
	return l, func() budget.LedgerStore { return l }
}

// A single returned physical segment can have several outstanding Calls with
// different outcomes. The first recovered terminal receipt must not replace
// the other Call identities or manufacture a grant-level observation.
func mixedOriginalToolConsumption(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	ctx := t.Context()
	backing, peer, reopen := factory(t)
	observer := ea1StartRuntime(t, peer)
	ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
	children := map[string]kernel.Snapshot{}
	for _, id := range []string{"child-a", "child-b"} {
		s, e := observer.Create(ctx, kernel.CreateRequest{ID: id, Kind: "fixture.child", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "child", State: json.RawMessage("{}")})
		if e != nil {
			t.Fatal(e)
		}
		children[id] = s
	}
	cut := errors.New("rejected Tool correction write cut")
	fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: backing.(kernel.PhysicalExecutionStore), event: "tool.correction_requested", injected: cut}
	m := &mixedToolModel{}
	physical := map[string]int{}
	verifications := map[string]int{}
	reg, e := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical[q.Call.ID]++
		if child, ok := children[q.Call.ID]; ok {
			return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: tools.ReceiptDispositionPending}, Pending: &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: q.Call.ID, ChildRunID: child.Run.ID}}, nil
		}
		return tools.RecoverableNoEffectResult(q.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.denied", "original refusal", nil))
	})}})
	if e != nil {
		t.Fatal(e)
	}
	makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, scope string) *agent.Runner {
		t.Helper()
		a, e := agent.NewBoundedBudgetAdapter(l, settledToolPorts{id: scope})
		if e != nil {
			t.Fatal(e)
		}
		r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: a, ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(c context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
			verifications[q.Call.ID]++
			if _, ok := children[q.Call.ID]; !ok {
				return tools.ExecutionResult{}, errors.New("unexpected original receipt lookup")
			}
			current, e := rt.Load(c, q.Call.ID)
			if e != nil || current.Run.Status != kernel.RunStatusCompleted {
				return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
			}
			return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{ "child": %q }`, q.Call.ID)), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
		})})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	r := makeRunner(ea1StartRuntime(t, fault), ledger, "original-ledger")
	_, e = r.StartRun(ctx, agent.StartRequest{ID: "mixed-root", RequestID: "mixed", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "all children", ToolKeys: []string{"read"}})
	if !errors.Is(e, cut) || len(physical) != 3 || len(m.requests) != 1 {
		t.Fatalf("fixture not reached: %v %+v", e, physical)
	}
	before, e := observer.Load(ctx, "mixed-root")
	if e != nil {
		t.Fatal(e)
	}
	grant, e := observer.LookupChildExecution(ctx, before.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	completion, e := observer.LookupPhysicalCallbackReturn(ctx, grant)
	if e != nil || !completion.MatchesReturnSnapshot(before) || completion.ControlCustody != kernel.OwnedCustodyScoped {
		t.Fatal("wrong original completion", e, completion)
	}
	bindings, e := completion.ToolReturns.Bindings()
	if e != nil || len(bindings) != 3 || bindings[0].CallID != "child-a" || bindings[1].CallID != "child-b" || bindings[2].CallID != "terminal-refusal" || bindings[0].ReceiptDigest != "" || bindings[1].ReceiptDigest != "" || bindings[2].ReceiptDigest == "" {
		t.Fatal("Call evidence not isolated", bindings, e)
	}
	for _, child := range children {
		if _, e = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: []byte("true")}}); e != nil {
			t.Fatal(e)
		}
	}
	rt := ea1StartRuntime(t, reopen())
	l := reopenLedger()
	r = makeRunner(rt, l, "original-ledger")
	staged, e := r.Resume(ctx, before.Run.ID, before.Run.Revision)
	if e != nil {
		t.Fatal("settled rejection recovery", e)
	}
	if len(m.requests) != 1 || len(verifications) != 0 {
		t.Fatal("recovery performed effects")
	}
	for _, id := range []string{"child-a", "child-b"} {
		view, e := agent.ViewState(staged)
		if e != nil || view.PendingTool == nil || view.PendingTool.CallID != id {
			t.Fatalf("wrong sibling: %v %+v", e, view.PendingTool)
		}
		if _, e = r.ReconcileToolOutcome(ctx, staged.Run.ID, before.Run.Revision, id); !errors.Is(e, kernel.ErrConflict) {
			t.Fatal("stale revision accepted", e)
		}
		if _, e = makeRunner(rt, l, "foreign-ledger").ReconcileToolOutcome(ctx, staged.Run.ID, staged.Run.Revision, id); !errors.Is(e, agent.ErrToolOutcomeUnknown) || verifications[id] != 0 {
			t.Fatal("foreign ledger reached verifier", e, verifications)
		}
		staged, e = r.ReconcileToolOutcome(ctx, staged.Run.ID, staged.Run.Revision, id)
		if e != nil || verifications[id] != 1 {
			t.Fatal("sibling could not consume original outcome", id, e)
		}
		if len(m.requests) != 1 {
			t.Fatal("new model dispatched before all sibling outcomes settled")
		}
	}
	final, e := r.Resume(ctx, staged.Run.ID, staged.Run.Revision)
	if e != nil || final.Run.Status != kernel.RunStatusCompleted || len(m.requests) != 2 {
		t.Fatal("original batch did not complete", e, final.Run.Status)
	}
	if e = rt.VerifyExecutionCustody(ctx, final, kernel.CustodyRetired); e != nil || !r.VerifiedDeferredRootControl(ctx, rt, final) {
		t.Fatal("final history not qualified", e)
	}
	if !reflect.DeepEqual(physical, map[string]int{"child-a": 1, "child-b": 1, "terminal-refusal": 1}) {
		t.Fatal("original call re-dispatched", physical)
	}
	charged, e := l.LoadBudget(ctx, "original-ledger")
	if e != nil || charged.View("").Usage.ToolCalls != 3 || charged.View("").Reserved.ToolCalls != 0 {
		t.Fatal("original batch not accounted", e, charged.View(""))
	}
	seen := map[string]int{}
	for _, msg := range m.requests[1].Messages {
		if msg.Role == model.RoleTool {
			seen[msg.ToolCallID]++
		}
	}
	if !reflect.DeepEqual(seen, physical) {
		t.Fatal("missing or duplicate model-facing outcome", seen)
	}
}

type toolConsumptionTimeline struct {
	fail         error
	starts, ends int
}

func (p *toolConsumptionTimeline) ResolveToolTimeline(_ context.Context, inv plugin.ToolInvocation) (agent.BoundedToolTimelineScope, error) {
	return agent.BoundedToolTimelineScope{RunID: inv.Run.ID, TurnID: "original-ledger", InvocationID: "inv", Active: true, SourceBinding: json.RawMessage("{}")}, nil
}
func (p *toolConsumptionTimeline) AppendToolTimeline(_ context.Context, _ agent.BoundedToolTimelineScope, _ plugin.ToolInvocation, _ tools.ExecutionResult, status, _ string) (string, error) {
	if status == "started" {
		p.starts++
		return "started", nil
	}
	p.ends++
	if p.fail != nil {
		return "", p.fail
	}
	return "terminal", nil
}

// Qualification occurs at the actual Call return, before any correction or
// successor Model admission. Budget always encloses Timeline and the handler.
func toolTimelineConsumption(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, refuse := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("rejected=%v/fail=%v", refuse, fail), func(t *testing.T) {
				ctx := t.Context()
				backing, _, _ := factory(t)
				rt := ea1StartRuntime(t, backing)
				ledger, _ := newToolTestLedger(t, ledgerFactory)
				a, e := agent.NewBoundedBudgetAdapter(ledger, settledToolPorts{id: "original-ledger"})
				if e != nil {
					t.Fatal(e)
				}
				timeline := &toolConsumptionTimeline{}
				if fail {
					timeline.fail = errors.New("terminal timeline write failed")
				}
				b, e := agent.NewBoundedToolTimelineAdapter(timeline)
				if e != nil {
					t.Fatal(e)
				}
				m := &settledToolModel{}
				calls := 0
				reg, e := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					calls++
					if refuse {
						return tools.RecoverableNoEffectResult(q.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.denied", "original refusal", nil))
					}
					return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
				})}})
				if e != nil {
					t.Fatal(e)
				}
				r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: a, ToolTimeline: b})
				if e != nil {
					t.Fatal(e)
				}
				final, e := r.StartRun(ctx, agent.StartRequest{ID: "timeline-root", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}})
				if calls != 1 || timeline.starts != 1 || timeline.ends != 1 {
					t.Fatal("fixture failure", calls, timeline)
				}
				l, loadErr := ledger.LoadBudget(ctx, "original-ledger")
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				if fail {
					if !errors.Is(e, agent.ErrToolOutcomeUnknown) || !errors.Is(e, timeline.fail) || final.Run.Status != kernel.RunStatusRunning || len(m.calls) != 1 {
						t.Fatal("unqualified Call advanced", e, final.Run.Status, len(m.calls))
					}
					grant, gErr := rt.LookupChildExecution(ctx, final.Run.ID)
					if gErr != nil {
						t.Fatal(gErr)
					}
					c, cErr := rt.LookupPhysicalCallbackReturn(ctx, grant)
					if cErr != nil || c.HostCompleted || c.ToolReturns != "" {
						t.Fatal("unacknowledged callback certified", cErr, c)
					}
					if _, e = r.Resume(ctx, final.Run.ID, final.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) || calls != 1 || len(m.calls) != 1 {
						t.Fatal("resume bypassed failure", e)
					}
					if l.View("").Usage.ToolCalls != 0 || l.View("").Reserved.ToolCalls != 1 {
						t.Fatal("outer Budget accepted unqualified result", l.View(""))
					}
				} else if e != nil || final.Run.Status != kernel.RunStatusCompleted || len(m.calls) != 2 || l.View("").Usage.ToolCalls != 1 || l.View("").Reserved.ToolCalls != 0 || rt.VerifyExecutionCustody(ctx, final, kernel.CustodyRetired) != nil {
					t.Fatal("qualified outcome lost", e, final.Run.Status, len(m.calls), l.View(""))
				}
			})
		}
	}
}
