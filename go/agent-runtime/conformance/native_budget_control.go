package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Real borrowed child execution: the first Tool receipt is committed, the
// second Call is waiting for original approval. A failed cancellation must
// neither revoke the ledger nor preclude the later valid approval.
func runNativeBudgetControl(t *testing.T, factory func(*testing.T) ExecutionBudgetStore) {
	for _, mode := range []string{"cancel", "reject-active-grant-then-approve", "reject-unknown-then-cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := factory(t)
			ctx := t.Context()
			rt := ea1StartRuntime(t, f.Store)
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "thread", ID: "thread"}
			if _, err := rt.Create(ctx, kernel.CreateRequest{ID: "parent", Kind: "workflow", Budget: &budget.RunEnrollment{ScopeID: "scope", Limits: budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 2, MaxConcurrentRuns: 2}}, Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			call1 := tools.Call{ID: "first", ToolKey: "effect", Arguments: json.RawMessage(`{}`)}
			call2 := tools.Call{ID: "second", ToolKey: "effect", Arguments: json.RawMessage(`{}`)}
			sends, models := 0, 0
			modelClient := nativeModelBudgetClient(func(_ context.Context, _ model.Request) (model.Response, error) {
				models++
				if models == 1 {
					return model.Response{ToolCalls: []tools.Call{call1, call2}}, nil
				}
				return model.Response{Content: "done"}, nil
			})
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				sends++
				return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			gate, err := interaction.New(rt)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := agent.NewBoundedToolApproval(func(_ context.Context, q tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
				if q.Call.ID == "second" {
					return plugin.ApprovalRequired, nil
				}
				return plugin.ApprovalNotRequired, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			a, err := agent.NewBoundedBudgetAdapter(f.Ledger, nativeToolBudgetPorts{})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: modelClient, Catalog: reg, Executor: reg, BoundedBudget: a, Approvals: interactionadapter.New(gate), ApprovalPolicies: []plugin.ApprovalPolicy{policy},
				StartControlPreflight: func(context.Context, agent.StartRequest) error { return nil }, ApprovalControlPreflight: func(context.Context, kernel.Snapshot) error { return nil }})
			if err != nil {
				t.Fatal(err)
			}
			waiting, err := runner.StartRun(ctx, agent.StartRequest{ID: "child", ParentRunID: "parent", RequestID: "source", OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{}`)), Actor: actor, Thread: thread, Goal: "finish", Model: "test", ToolKeys: []string{"effect"}})
			if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput || waiting.Checkpoint == nil || sends != 1 {
				t.Fatalf("real second approval wait: %s %v sends=%d", waiting.Run.Status, err, sends)
			}
			before, err := f.Ledger.LoadBudget(ctx, "scope")
			if err != nil {
				t.Fatal(err)
			}
			if before.RunCancelled("child") || before.View("child").Usage.ToolCalls != 1 || before.FinishedRuns["child"] {
				t.Fatalf("unexpected waiting ledger: %+v", before)
			}
			if mode != "cancel" {
				var grant kernel.ChildExecutionGrant
				if mode == "reject-active-grant-then-approve" {
					grant, err = AdmitChildFixture(ctx, rt, "child", waiting.Run.Revision)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					// Another original admitted effect exists in the same subtree.
					// Keep its quota and prevent terminal control until known outcome.
					c := budget.Coordinator{Store: f.Ledger}
					q := budget.Reservation{ID: "tool:child:unknown", RunID: "child", RequestHash: "original", Requested: budget.Usage{ToolCalls: 1}}
					if _, err = c.Reserve(ctx, "scope", q, true); err != nil {
						t.Fatal(err)
					}
					if err = DispatchToolFixture(ctx, c, "scope", q); err != nil {
						t.Fatal(err)
					}
					before, err = f.Ledger.LoadBudget(ctx, "scope")
					if err != nil {
						t.Fatal(err)
					}
				}
				_, err = rt.Cancel(ctx, "child", waiting.Run.Revision, "stop")
				if !errors.Is(err, kernel.ErrChildExecutionBusy) || !kernel.IsControlNoEffect(err) {
					t.Fatalf("unsafe cancellation: %v", err)
				}
				observed, e := rt.Load(ctx, "child")
				if e != nil || !reflect.DeepEqual(waiting, observed) {
					t.Fatalf("denial changed original checkpoint: %v", e)
				}
				actual, e := f.Ledger.LoadBudget(ctx, "scope")
				if e != nil || !reflect.DeepEqual(before, actual) {
					t.Fatalf("denial changed original budget: %v", e)
				}
				if mode == "reject-active-grant-then-approve" {
					// Test fixture owns this synthetic no-entry Grant and may retire
					// it; no production observer is permitted to forge such a proof.
					proof := featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision, "agent_physical_settled", grant.ID, waiting.State, waiting)
					if err = rt.RecordPhysicalCallbackReturn(ctx, grant, waiting.Run.Revision, kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}, featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision, "agent_physical_callback_return", grant.ID, kernel.PhysicalCallbackReturnPayload(waiting, kernel.PhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: true}), nil)); err != nil {
						t.Fatal(err)
					}
					if err = rt.FinishChildExecution(ctx, grant, proof); err != nil {
						t.Fatal(err)
					}
					approved, e := runner.ResolveApproval(ctx, "child", waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
					if e != nil {
						t.Fatal(e)
					}
					if approved.Run.Status == kernel.RunStatusRunning {
						approved, e = runner.Resume(ctx, "child", approved.Run.Revision)
					}
					if e != nil || approved.Run.Status != kernel.RunStatusCompleted || sends != 2 {
						t.Fatalf("original approval blocked: %s %v sends=%d", approved.Run.Status, e, sends)
					}
					l, e := f.Ledger.LoadBudget(ctx, "scope")
					if e != nil || l.RunCancelled("child") || !l.FinishedRuns["child"] || l.View("child").Usage.ToolCalls != 2 {
						t.Fatalf("terminal accounting: %v %+v", e, l)
					}
					return
				}
				// The test owns the synthetic original receipt; only now can Cancel
				// close this subtree. It cannot refund that original charge.
				if _, err = (budget.Coordinator{Store: f.Ledger}).Settle(ctx, "scope", "tool:child:unknown", budget.Usage{ToolCalls: 1}, json.RawMessage(`{}`)); err != nil {
					t.Fatal(err)
				}
			}
			cancelled, err := rt.Cancel(ctx, "child", waiting.Run.Revision, "stop")
			if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled || sends != 1 {
				t.Fatalf("cancel: %s %v sends=%d", cancelled.Run.Status, err, sends)
			}
			if f.Reopen != nil {
				f.Store, f.Ledger = f.Reopen(t)
			}
			actual, err := f.Ledger.LoadBudget(ctx, "scope")
			if err != nil || !actual.RunCancelled("child") || actual.RunCancelled("parent") || !actual.FinishedRuns["child"] || actual.Reservations["tool:child:first"].Status != budget.ReservationSettled {
				t.Fatalf("split cancellation: %v %+v", err, actual)
			}
			snap, err := f.Store.Load(ctx, "child")
			if err != nil || snap.Run.Status != kernel.RunStatusCancelled || snap.Checkpoint != nil {
				t.Fatalf("reopen cancellation: %v %+v", err, snap)
			}
		})
	}
}
