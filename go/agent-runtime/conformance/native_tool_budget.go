package conformance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type nativeToolBudgetPorts struct{ nativeModelBudgetPorts }

func (nativeToolBudgetPorts) ResolveScope(_ context.Context, id string) (agent.BoundedBudgetScope, error) {
	s := agent.BoundedBudgetScope{LedgerID: "scope", Limits: budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 2, MaxConcurrentRuns: 2}}
	if id == "child" {
		s.Runs = []agent.BoundedBudgetRunBinding{{RunID: "parent"}, {RunID: id, Binding: budget.RunBudget{ParentRunID: "parent"}}}
	} else {
		s.Runs = []agent.BoundedBudgetRunBinding{{RunID: id}}
	}
	return s, nil
}
func (nativeToolBudgetPorts) ToolReservationActive(plugin.ToolInvocation) bool { return true }

func runNativeToolBudget(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	for _, tc := range []struct {
		name, event           string
		after, contend, child bool
		sends                 int
		dispatched, settled   bool
	}{
		{"success", "", false, false, false, 1, true, true},
		{"child-success", "", false, false, true, 1, true, true},
		{"refusal", "", false, false, false, 1, true, true},
		{"pending", "", false, false, false, 1, true, false},
		{"unknown", "", false, false, false, 1, true, false},
		{"dispatch-before", "tool.dispatch_intent", false, false, false, 0, false, false},
		{"dispatch-ack", "tool.dispatch_intent", true, false, false, 0, true, false},
		{"receipt-before", "tool.receipt_recovered", false, false, false, 1, true, false},
		{"receipt-ack", "tool.receipt_recovered", true, false, false, 1, true, true},
		{"receipt-contended", "tool.receipt_recovered", false, true, false, 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := factory(t)
			ctx := t.Context()
			wrapped := &nativeBudgetFault{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, f.Store), LedgerStore: f.Ledger, event: tc.event, after: tc.after, contend: tc.contend}
			var store kernel.Store = wrapped
			var ledgerStore budget.LedgerStore = wrapped
			if tc.child {
				store, ledgerStore = f.Store, f.Ledger
			}
			rt := ea1StartRuntime(t, store)
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "thread", ID: "thread"}
			start := agent.StartRequest{ID: "run", Actor: actor, Thread: thread, Goal: "finish", Model: "test", ToolKeys: []string{"effect"}}
			if tc.child {
				if _, err := rt.Create(ctx, kernel.CreateRequest{ID: "parent", Kind: "workflow", Budget: &budget.RunEnrollment{ScopeID: "scope", Limits: budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 2, MaxConcurrentRuns: 2}}, Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				start.ID, start.ParentRunID, start.RequestID, start.OriginFingerprint = "child", "parent", "child-source", agent.FrozenOriginFingerprint([]byte(`{}`))
			}
			models, sends := 0, 0
			var physical tools.ExecutionRequest
			var receipt tools.ExecutionResult
			call := tools.Call{ID: "call", ToolKey: "effect", Arguments: json.RawMessage(`{}`)}
			p := nativeModelBudgetClient(func(_ context.Context, q model.Request) (model.Response, error) {
				models++
				if models == 1 {
					return model.Response{ToolCalls: []tools.Call{call}, Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
				}
				return model.Response{Content: "done", Usage: &model.Usage{InputTokens: 1, OutputTokens: 1}}, nil
			})
			registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(c context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				sends++
				physical = tools.CloneExecutionRequest(q)
				snap, e := f.Store.Load(c, q.RunID)
				if e != nil {
					t.Fatal(e)
				}
				found, ok, e := agent.FrozenPendingDispatch(snap)
				l, e2 := f.Ledger.LoadBudget(c, "scope")
				res := l.Reservations["tool:"+q.RunID+":"+q.Call.ID]
				if e != nil || e2 != nil || !ok || found.ID != q.Call.ID || !res.Dispatched || res.Status != budget.ReservationHeld || res.RequestHash != kernel.ManagedDigest(q) {
					t.Fatalf("physical handler without paired admission: %v %v %+v", e, e2, res)
				}
				receipt = tools.ExecutionResult{Content: json.RawMessage(`{ "result": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("owner")}}
				switch tc.name {
				case "unknown":
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				case "pending":
					return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: tools.ReceiptDispositionPending}}, nil
				case "refusal":
					return tools.RecoverableNoEffectResult(q.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("tool.denied", "denied", nil))
				}
				return tools.CloneExecutionResult(receipt), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, verify bool) *agent.Runner {
				b, e := agent.NewBoundedBudgetAdapter(l, nativeToolBudgetPorts{})
				if e != nil {
					t.Fatal(e)
				}
				d := agent.Dependencies{Runtime: rt, Model: p, Catalog: registry, Executor: registry, BoundedBudget: b, StartControlPreflight: func(context.Context, agent.StartRequest) error { return nil }}
				if verify {
					d.ToolOutcomeVerifier = agent.ToolOutcomeVerifierFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
						if tc.sends == 0 {
							return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
						}
						if kernel.ManagedDigest(q) != kernel.ManagedDigest(physical) {
							t.Fatal("verifier request changed")
						}
						return tools.CloneExecutionResult(receipt), nil
					})
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			r := makeRunner(rt, ledgerStore, false)
			final, callErr := r.StartRun(ctx, start)
			success := tc.name == "success" || tc.name == "child-success" || tc.name == "refusal" || tc.name == "receipt-contended"
			if success && (callErr != nil || final.Run.Status != kernel.RunStatusCompleted) {
				t.Fatalf("complete: %s %v", final.Run.Status, callErr)
			}
			if !success && tc.name != "pending" && callErr == nil {
				t.Fatal("fault lost")
			}
			if sends != tc.sends || tc.event != "" && !wrapped.hit {
				t.Fatalf("effect count %d fault=%v %v", sends, wrapped.hit, callErr)
			}
			if f.Reopen != nil {
				f.Store, f.Ledger = f.Reopen(t)
			}
			snap, e := f.Store.Load(ctx, start.ID)
			if e != nil {
				t.Fatal(e)
			}
			l, e := f.Ledger.LoadBudget(ctx, "scope")
			if e != nil {
				t.Fatal(e)
			}
			res, exists := l.Reservations["tool:"+start.ID+":call"]
			_, pending, e := agent.FrozenPendingDispatch(snap)
			if e != nil || exists != tc.dispatched || res.Dispatched != tc.dispatched || (res.Status == budget.ReservationSettled) != tc.settled || pending != (tc.dispatched && !tc.settled) {
				t.Fatalf("split Tool facts: pending=%v ledger=%+v err=%v original=%v", pending, res, e, callErr)
			}
			if tc.settled && res.Usage.ToolCalls != 1 {
				t.Fatal("double charge or missing charge")
			}
			if tc.name == "receipt-before" || tc.name == "receipt-ack" || tc.name == "unknown" || tc.name == "pending" || tc.name == "dispatch-ack" {
				reopened := makeRunner(ea1StartRuntime(t, f.Store), f.Ledger, true)
				if !tc.settled {
					repaired, e := reopened.ReconcileToolOutcome(ctx, start.ID, snap.Run.Revision, "call")
					if tc.sends == 0 {
						if e == nil || sends != 0 {
							t.Fatal("unknown dispatch replayed")
						}
						return
					}
					if e != nil {
						t.Fatalf("reconcile original Tool: %v", e)
					}
					snap = repaired
				}
				completed, e := reopened.Resume(ctx, start.ID, snap.Run.Revision)
				if e != nil || completed.Run.Status != kernel.RunStatusCompleted || sends != 1 {
					t.Fatalf("consume original Tool receipt: %s %v sends=%d", completed.Run.Status, e, sends)
				}
				l, e = f.Ledger.LoadBudget(ctx, "scope")
				if e != nil || l.Reservations["tool:"+start.ID+":call"].Status != budget.ReservationSettled || l.View(start.ID).Usage.ToolCalls != 1 {
					t.Fatalf("original Tool accounting: %v %+v", e, l)
				}
			}
		})
	}
}
