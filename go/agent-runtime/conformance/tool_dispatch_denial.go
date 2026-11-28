package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Elect cancellation after original authorization, before native dispatch
// planning. There is no separate reservation/Dispatch CAS to interrupt now.
type toolDispatchDenialStore struct {
	*ea1AgentReceiptFault
	afterCheck  func()
	afterCallID string
	hits        int
}

func (s *toolDispatchDenialStore) Commit(ctx context.Context, id string, rev uint64, m kernel.StoreMutation, auth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	next, created, err := s.ea1AgentReceiptFault.Commit(ctx, id, rev, m, auth)
	if err == nil && s.afterCheck != nil {
		for _, event := range m.Events {
			if event.Type == "tool.authorization_committed" && (s.afterCallID == "" || event.Message == s.afterCallID) {
				action := s.afterCheck
				s.afterCheck = nil
				s.hits++
				action()
				break
			}
		}
	}
	return next, created, err
}

func toolDispatchDenial(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	// Split-only speculative-marker cleanup/retirement cuts are gone: an
	// uncommitted dispatch has neither a reservation nor physical custody.
	// Native dispatch-before/ACK and receipt-before/ACK remain covered by
	// runNativeToolBudget; unmetered no-send closure is in toolUnsentRecovery,
	// and final-child receipt/retirement ACK loss remains in toolBatchBoundary.
	for _, mode := range []string{"budget-only", "timeline-inner", "parented", "terminal-before", "terminal-ack", "projection-failure", "dispatch-write-before", "dispatch-write-ack", "actual-handler-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			base, peer, reopen := factory(t)
			ledger := newToolTestLedger(t, base)
			observer := ea1StartRuntime(t, peer)
			const root, scope = "dispatch-denial-root", "dispatch-denial-ledger"
			const reservation = "tool:dispatch-denial-root:original-call"
			limits := budget.Limits{MaxToolCalls: 2, MaxConcurrentRuns: 1}
			cut := errors.New("native terminal commit interrupted")
			fault := &ea1AgentReceiptFault{Store: base, LedgerStore: ledger, ExecutionStore: base.(kernel.ExecutionStore), ExecutionQuarantineStore: base.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: base.(kernel.PhysicalExecutionStore)}
			store := &toolDispatchDenialStore{ea1AgentReceiptFault: fault}
			cancelled := mode != "dispatch-write-before" && mode != "dispatch-write-ack" && mode != "actual-handler-error"
			if cancelled {
				store.afterCheck = func() {
					coord := budget.Coordinator{Store: ledger}
					var err error
					if mode == "parented" {
						_, err = coord.CancelRun(ctx, scope, root)
					} else {
						_, err = coord.CancelRoot(ctx, scope, root)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "terminal-before" || mode == "terminal-ack" {
				fault.event, fault.injected, fault.after = "agent.failed", cut, mode == "terminal-ack"
			}
			if mode == "dispatch-write-before" || mode == "dispatch-write-ack" {
				// A Store error with the public denial classification is NOT
				// evidence that the native accounting decision denied send.
				fault.event, fault.injected, fault.after = "tool.dispatch_intent", budget.ErrExhausted, mode == "dispatch-write-ack"
			}
			m := &settledToolModel{}
			physical, policies, reads := 0, 0, 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				return tools.ExecutionResult{}, budget.ErrExhausted
			})}})
			if err != nil {
				t.Fatal(err)
			}
			timeline := &toolConsumptionTimeline{}
			makeRunner := func(s kernel.Store, original bool) (*agent.Runner, *kernel.Runtime) {
				t.Helper()
				rt := ea1StartRuntime(t, s)
				ports := capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: scope}, limits: limits}
				if mode == "parented" {
					ports.parent = "dispatch-denial-parent"
				}
				if original && mode == "projection-failure" {
					ports.project = func(_ context.Context, l budget.Ledger) error {
						if l.RunCancelled(root) {
							return cut
						}
						return nil
					}
				}
				b, e := agent.NewBoundedBudgetAdapter(newToolTestLedger(t, s), ports)
				if e != nil {
					t.Fatal(e)
				}
				policy, e := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policies++
					return plugin.ApprovalNotRequired, nil
				})
				if e != nil {
					t.Fatal(e)
				}
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b, StartControlPreflight: func(context.Context, agent.StartRequest) error { return nil }, ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads++
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				})}
				if original {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{policy}
					if mode == "timeline-inner" || mode == "parented" {
						d.ToolTimeline, e = agent.NewBoundedToolTimelineAdapter(timeline)
						if e != nil {
							t.Fatal(e)
						}
					}
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r, rt
			}
			req := agent.StartRequest{ID: root, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}}
			if mode == "parented" {
				if _, err = observer.Create(ctx, kernel.CreateRequest{ID: "dispatch-denial-parent", Kind: "workflow", Budget: &budget.RunEnrollment{ScopeID: scope, Limits: limits}, Actor: req.Actor, Thread: req.Thread, Goal: "parent", State: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
				req.ParentRunID, req.RequestID, req.OriginFingerprint = "dispatch-denial-parent", "original", agent.FrozenOriginFingerprint([]byte(`{}`))
			}
			r, _ := makeRunner(store, true)
			_, _, runErr := r.ObservePhysicalCall(ctx, root, plugin.RunStart, func(c context.Context) (kernel.Snapshot, error) { return r.StartRun(c, req) })
			fresh := reopen()
			r, rt := makeRunner(fresh, false)
			cur, err := rt.Load(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := newToolTestLedger(t, fresh).LoadBudget(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			res, exists := saved.Reservations[reservation]
			wantPhysical := 0
			if mode == "actual-handler-error" {
				wantPhysical = 1
			}
			if physical != wantPhysical || policies != 1 || reads != 0 || len(m.calls) != 1 || timeline.starts != 0 || timeline.ends != 0 {
				t.Fatal("denial/unknown crossed original physical boundary", runErr, physical, policies, reads)
			}
			if cancelled {
				if !errors.Is(runErr, budget.ErrExhausted) || store.hits != 1 || !saved.RunCancelled(root) || exists {
					t.Fatal("native cancellation manufactured a dispatched reservation", runErr, res)
				}
				if mode == "terminal-before" || mode == "terminal-ack" {
					if !errors.Is(runErr, cut) || fault.failures != 1 || (cur.Run.Status == kernel.RunStatusFailed) != fault.after || saved.FinishedRuns[root] != fault.after {
						t.Fatal("terminal Run/accounting split at cut", runErr, cur.Run, saved.FinishedRuns)
					}
				} else if cur.Run.Status != kernel.RunStatusFailed || !saved.FinishedRuns[root] {
					t.Fatal("native denial did not close original lifetime", runErr, cur.Run)
				}
				if mode == "parented" && saved.RunCancelled("dispatch-denial-parent") {
					t.Fatal("native child denial cancelled its parent")
				}
				if mode == "projection-failure" {
					// Native no-send/terminal accounting is known, but the
					// original Host projection never acknowledged its return.
					if !errors.Is(runErr, cut) || !errors.Is(runErr, agent.ErrRootOutcomeUnknown) {
						t.Fatal("failed projection acquired completed return", runErr)
					}
					g, err := rt.LookupChildExecution(ctx, root)
					if err != nil {
						t.Fatal("unknown projection lost original grant", err)
					}
					c, err := rt.LookupPhysicalCallbackReturn(ctx, g)
					if err != nil || c.HostCompleted || c.ControlCustody != "" || r.VerifiedDeferredRootControl(ctx, rt, cur) {
						t.Fatal("failed projection manufactured control custody", err, c)
					}
					if err = r.ReconcilePhysicalExecution(ctx, root); !errors.Is(err, agent.ErrRootOutcomeUnknown) {
						t.Fatal("replacement retired unknown original projection", err)
					}
					return
				}
				if _, err = rt.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("known pre-dispatch denial retained physical custody", err)
				}
				return
			}
			if mode != "actual-handler-error" && fault.failures != 1 {
				t.Fatal("native dispatch commit fault not reached", runErr)
			}
			committed := mode != "dispatch-write-before"
			_, pending, err := agent.FrozenPendingDispatch(cur)
			if err != nil || pending != committed || exists != committed || committed && (!res.Dispatched || res.Status != budget.ReservationHeld || len(res.Receipt) != 0 || res.Usage != (budget.Usage{})) || saved.FinishedRuns[root] {
				t.Fatal("Store/handler error certified speculative no-send", runErr, res, pending)
			}
			if committed {
				if _, err = r.Resume(ctx, root, cur.Run.Revision); !errors.Is(err, agent.ErrToolOutcomeUnknown) || physical != wantPhysical || len(m.calls) != 1 {
					t.Fatal("unknown admitted dispatch replayed", err, physical)
				}
				if _, err = rt.Cancel(ctx, root, cur.Run.Revision, "unknown effects"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatal("unknown dispatch permitted cancellation", err)
				}
			}
		})
	}
}
