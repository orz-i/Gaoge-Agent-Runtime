package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type capacityAdmissionPorts struct {
	settledToolPorts
	limits        budget.Limits
	inactiveChild bool
}

func (p capacityAdmissionPorts) ResolveScope(_ context.Context, id string) (agent.BoundedBudgetScope, error) {
	return agent.BoundedBudgetScope{LedgerID: p.id, Limits: p.limits, Runs: []agent.BoundedBudgetRunBinding{{RunID: id}}}, nil
}
func (p capacityAdmissionPorts) ToolReservationActive(inv plugin.ToolInvocation) bool {
	return !p.inactiveChild || inv.Request.Call.ID != "child-a"
}

func toolCapacityAdmission(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, mode := range []string{"single-no-policy", "single-bounded", "single-checkpoint", "mixed-quota", "mixed-slot"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			backing, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
			const scope = "capacity-ledger"
			const rootID = "capacity-root"
			mixed := mode == "mixed-quota" || mode == "mixed-slot"
			limited := mode == "mixed-quota"
			withPolicy := mode != "single-no-policy"
			limits := budget.Limits{MaxToolCalls: 8, MaxConcurrentRuns: 1}
			if limited {
				limits = budget.Limits{MaxToolCalls: 1}
			}
			coord := budget.Coordinator{Store: ledger}
			if _, err := coord.Ensure(ctx, scope, limits); err != nil {
				t.Fatal(err)
			}
			blockPeer := func() {
				t.Helper()
				if _, err := coord.RegisterRun(ctx, scope, "peer-run", budget.RunBudget{}); err != nil {
					t.Fatal(err)
				}
				if _, err := coord.Reserve(ctx, scope, budget.Reservation{ID: "peer-slot", RunID: "peer-run", RequestHash: kernel.ManagedDigest("peer-slot"), Requested: budget.Usage{ToolCalls: 1}}, true); err != nil {
					t.Fatal(err)
				}
				if _, err := coord.Dispatch(ctx, scope, "peer-slot"); err != nil {
					t.Fatal(err)
				}
			}
			peerBlocked := false
			child, err := observer.Create(ctx, kernel.CreateRequest{ID: "original-child", Kind: "fixture.child", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "child", State: json.RawMessage("{}")})
			if err != nil {
				t.Fatal(err)
			}
			singleModel := &settledToolModel{}
			batchModel := &batchBoundaryModel{}
			var m model.Client = singleModel
			modelCalls := func() int { return len(singleModel.calls) }
			waitingID := "original-call"
			if mixed {
				m = batchModel
				modelCalls = func() int { return len(batchModel.requests) }
				waitingID = "child-b"
			}
			physical, policies, reads := map[string]int{}, map[string]int{}, map[string]int{}
			result := func(id string) tools.ExecutionResult {
				return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{ "answer": %q }`, id)), Receipt: tools.Receipt{ExecutionID: id, Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("owner:" + id)}}
			}
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical[q.Call.ID]++
				if mixed && q.Call.ID == "child-a" {
					if mode == "mixed-slot" {
						blockPeer()
					}
					return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "pending"}, Pending: &tools.PendingExecution{Kind: tools.PendingChildTerminal, OperationID: q.Call.ID, ChildRunID: child.Run.ID}}, nil
				}
				return result(q.Call.ID), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			timeline := &toolConsumptionTimeline{}
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, ledgerID string, policyEnabled bool) *agent.Runner {
				t.Helper()
				track, e := agent.NewBoundedToolTimelineAdapter(timeline)
				if e != nil {
					t.Fatal(e)
				}
				var bounded *agent.BoundedBudgetAdapter
				if ledgerID != "" {
					b, e := agent.NewBoundedBudgetAdapter(l, capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: ledgerID, project: func(_ context.Context, current budget.Ledger) error {
						if !mixed && !peerBlocked {
							for _, res := range current.Reservations {
								if res.RunID == rootID && res.Requested.LLMCalls == 1 && res.Status == budget.ReservationSettled {
									blockPeer() // Compete with Tool admission, not the preceding Model.
									peerBlocked = true
									break
								}
							}
						}
						return nil
					}}, limits: limits, inactiveChild: mode == "mixed-slot"})
					if e != nil {
						t.Fatal(e)
					}
					bounded = b
				}
				p, e := agent.NewBoundedToolApproval(func(_ context.Context, q tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policies[q.Call.ID]++
					if mode == "single-checkpoint" {
						return plugin.ApprovalRequired, nil
					}
					return plugin.ApprovalNotRequired, nil
				})
				if e != nil {
					t.Fatal(e)
				}
				it, e := interaction.New(rt)
				if e != nil {
					t.Fatal(e)
				}
				reader := agent.ToolOutcomeVerifierFunc(func(c context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads[q.Call.ID]++
					if q.RunID != rootID || physical[q.Call.ID] != 1 {
						return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
					}
					if mixed && q.Call.ID == "child-a" {
						ch, e := rt.Load(c, child.Run.ID)
						if e != nil || ch.Run.Status != kernel.RunStatusCompleted {
							return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
						}
					}
					return result(q.Call.ID), nil
				})
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Approvals: interactionadapter.New(it), BoundedBudget: bounded, ToolTimeline: track, ToolOutcomeVerifier: reader, DelegatedToolResults: reader, DeferResumption: true}
				if policyEnabled {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{p}
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			rt := ea1StartRuntime(t, backing)
			r := makeRunner(rt, ledger, scope, withPolicy)
			started, err := r.StartRun(ctx, agent.StartRequest{ID: rootID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}})
			if mode == "single-checkpoint" {
				if err != nil || started.Run.Status != kernel.RunStatusWaitingInput || timeline.starts != 0 {
					t.Fatal("expected approval before capacity", started.Run.Status, err)
				}
				started, err = r.ResolveApproval(ctx, rootID, started.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
				if err == nil {
					started, err = r.Resume(ctx, rootID, started.Run.Revision)
				}
			}
			if err != nil || started.Run.Status != kernel.RunStatusRunning || physical[waitingID] != 0 || modelCalls() != 1 || !bytes.Contains(started.State, []byte(`"phase":"awaiting_capacity"`)) {
				t.Fatal("expected native unsent wait", started.Run.Status, err, physical)
			}
			if withPolicy && policies[waitingID] != 1 {
				t.Fatal("original policy count", policies)
			}
			before, err := ledger.LoadBudget(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			id := "tool:" + rootID + ":" + waitingID
			res := before.Reservations[id]
			if res.Status != budget.ReservationWaiting || res.Dispatched || res.Active || len(res.Receipt) != 0 || res.Usage != (budget.Usage{}) {
				t.Fatal("wait acquired physical effects", res)
			}
			if !mixed {
				if timeline.starts != 0 || timeline.ends != 0 {
					t.Fatal("capacity wait entered middleware", timeline)
				}
				if _, ok, e := agent.FrozenPendingDispatch(started); e != nil || ok {
					t.Fatal("capacity wait got dispatch marker", e)
				}
				if _, e := observer.LookupChildExecution(ctx, rootID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatal("unsent wait retained physical grant", e)
				}
			} else {
				g, e := observer.LookupChildExecution(ctx, rootID)
				if e != nil {
					t.Fatal(e)
				}
				c, e := observer.LookupOwnedPhysicalCallbackReturn(ctx, g)
				if e != nil || !c.HostCompleted {
					t.Fatal("earlier Child return missing", c, e)
				}
				b, e := c.ToolReturns.Bindings()
				if e != nil || len(b) != 1 || b[0].CallID != "child-a" || b[0].PendingKind != "child_terminal" {
					t.Fatal("unsent call was included in physical evidence", b, e)
				}
				if timeline.starts != 1 || physical["child-a"] != 1 {
					t.Fatal("wrong physical frontier", timeline, physical)
				}
				if _, err = observer.Apply(ctx, child.Run.ID, child.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: child.State, Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")}}); err != nil {
					t.Fatal(err)
				}
			}
			rt = ea1StartRuntime(t, reopen())
			l := reopenLedger()
			coord.Store = l
			// Exact original authorization and capacity scope survive Runner replacement.
			r = makeRunner(rt, l, scope, false)
			if _, err = r.ReconcileToolOutcome(ctx, rootID, started.Run.Revision, waitingID); !errors.Is(err, agent.ErrToolOutcomeUnknown) || len(reads) != 0 {
				t.Fatal("unsent capacity wait reached receipt verifier", err, reads)
			}
			if mixed {
				started, err = r.ReconcileRootDeferredChildren(ctx, rootID, started.Run.Revision)
				if err != nil || reads["child-a"] != 1 || reads[waitingID] != 0 || modelCalls() != 1 {
					t.Fatal("original Child could not drain before capacity", err, reads)
				}
				if _, err = rt.LookupChildExecution(ctx, rootID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("original child grant retained", err)
				}
			} else {
				// Wrong or missing adapter must never dispatch the previously bound Call.
				for _, foreign := range []string{"", "foreign-ledger"} {
					if _, err = makeRunner(rt, l, foreign, false).Resume(ctx, rootID, started.Run.Revision); !errors.Is(err, agent.ErrToolReceiptMismatch) || physical[waitingID] != 0 {
						t.Fatal("replacement lost original budget authority", foreign, err)
					}
					started, err = rt.Load(ctx, rootID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if _, err = l.LoadBudget(ctx, "foreign-ledger"); !errors.Is(err, budget.ErrLedgerNotFound) {
					t.Fatal("foreign ledger written", err)
				}
				empty := budget.NewMemoryLedgerStore()
				if _, err = makeRunner(rt, empty, scope, false).Resume(ctx, rootID, started.Run.Revision); !errors.Is(err, agent.ErrToolReceiptMismatch) {
					t.Fatal("same-ID missing ledger was accepted", err)
				}
				if _, err = empty.LoadBudget(ctx, scope); !errors.Is(err, budget.ErrLedgerNotFound) {
					t.Fatal("same-ID ledger manufactured", err)
				}
				started, err = r.Resume(ctx, rootID, started.Run.Revision)
				if err != nil || started.Run.Status != kernel.RunStatusRunning || physical[waitingID] != 0 || timeline.starts != 0 || modelCalls() != 1 {
					t.Fatal("repeated capacity wait not an unsent yield", err, physical, timeline)
				}
				if withPolicy && policies[waitingID] != 1 {
					t.Fatal("waiting repeated original approval", policies)
				}
			}
			if !limited {
				if _, err = coord.Settle(ctx, scope, "peer-slot", budget.Usage{}, json.RawMessage("{}")); err != nil {
					t.Fatal(err)
				}
			}
			// For a mixed batch, use the original policy for the still unexamined
			// third Call; the already decided B must not be re-evaluated.
			if mixed {
				r = makeRunner(rt, l, scope, true)
			}
			final, err := r.Resume(ctx, rootID, started.Run.Revision)
			if limited {
				if !errors.Is(err, budget.ErrExhausted) || final.Run.Status != kernel.RunStatusFailed || physical[waitingID] != 0 || modelCalls() != 1 {
					t.Fatal("permanent capacity denial did not terminate safely", final.Run.Status, err, physical)
				}
			} else if err != nil || final.Run.Status != kernel.RunStatusCompleted || physical[waitingID] != 1 || modelCalls() != 2 {
				t.Fatal("freed capacity did not execute original Call", final.Run.Status, err, physical)
			}
			if withPolicy && policies[waitingID] != 1 {
				t.Fatal("original policy replayed", policies)
			}
			after, err := l.LoadBudget(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			charge := 1
			want := budget.ReservationSettled
			if mode == "mixed-slot" {
				charge = 3
			}
			if limited {
				want = budget.ReservationReleased
			}
			if after.View("").Usage.ToolCalls != charge || after.View("").Reserved.ToolCalls != 0 || after.Reservations[id].Status != want {
				t.Fatal("wrong final settlement", after.View(""), after.Reservations[id])
			}
			if reads[waitingID] != 0 {
				t.Fatal("unsent wait was verified as a physical result", reads)
			}
			if _, err = rt.LookupChildExecution(ctx, rootID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("final grant not retired", err)
			}
			if !limited && withPolicy {
				reader := makeRunner(rt, l, scope, true)
				if !reader.VerifyDeferredToolResults(ctx, rt, final) || !reader.VerifiedDeferredRootControl(ctx, rt, final) {
					t.Fatal("original owner/approval history lost across capacity wait")
				}
			}
		})
	}
}
