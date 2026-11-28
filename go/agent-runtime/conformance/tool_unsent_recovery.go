package conformance

import (
	"bytes"
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

type unsentRecoveryFault struct {
	*ea1AgentReceiptFault
	beforeEvent  string
	beforeCount  int
	ackDispatch  bool
	ackAction    func()
	checkCut     error
	checkPending bool
	readCuts     int
	hits         int
}

func (f *unsentRecoveryFault) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if f.readCuts > 0 && id == "unsent-root" {
		f.readCuts--
		return kernel.Snapshot{}, f.injected
	}
	return f.Store.Load(ctx, id)
}

func (f *unsentRecoveryFault) CheckExecution(ctx context.Context, id string, lease *kernel.ExecutionLease) error {
	if f.checkPending {
		f.checkPending = false
		return f.checkCut
	}
	return f.ExecutionStore.CheckExecution(ctx, id, lease)
}

func (f *unsentRecoveryFault) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, ev := range m.Events {
		if f.beforeCount > 0 && ev.Type == f.beforeEvent {
			f.beforeCount--
			f.hits++
			return kernel.Snapshot{}, f.injected
		}
		if f.ackAction != nil && ev.Type == "tool.dispatch_intent" {
			action := f.ackAction
			f.ackAction = nil
			next, err := f.ExecutionStore.ApplyExecution(ctx, l, rev, m)
			if err == nil {
				f.hits++
				action()
			}
			return next, err
		}
		if f.ackDispatch && ev.Type == "tool.dispatch_intent" {
			f.ackDispatch = false
			f.hits++
			if _, err := f.ExecutionStore.ApplyExecution(ctx, l, rev, m); err != nil {
				return kernel.Snapshot{}, err
			}
			// Both immediate ACK disambiguation and the live no-send
			// cleanup read fail. The original callback return still knows
			// the native chain was never entered.
			f.readCuts = 2
			return kernel.Snapshot{}, f.injected
		}
	}
	return f.ExecutionStore.ApplyExecution(ctx, l, rev, m)
}

type unsentLedgerFault struct {
	budget.LedgerStore
	failure error
	after   bool
	hits    int
}

func (f *unsentLedgerFault) SaveBudget(ctx context.Context, l budget.Ledger, revision uint64) error {
	if f.hits == 0 && l.Reservations["tool:unsent-root:original-call"].Status == budget.ReservationReleased {
		f.hits++
		if f.after {
			if err := f.LedgerStore.SaveBudget(ctx, l, revision); err != nil {
				return err
			}
		}
		return f.failure
	}
	return f.LedgerStore.SaveBudget(ctx, l, revision)
}

type unsentCancelObserver struct {
	cancel context.CancelFunc
	callID string
	calls  int
}

func (*unsentCancelObserver) Name() string { return "fixture.cancel-before-chain" }
func (o *unsentCancelObserver) Observe(_ context.Context, e plugin.Event) {
	if e.Type == agent.EventToolStarted {
		if o.callID != "" {
			var call tools.Call
			if json.Unmarshal(e.Data, &call) != nil || call.ID != o.callID {
				return
			}
		}
		o.calls++
		o.cancel()
	}
}

type unsentProjectionPorts struct {
	capacityAdmissionPorts
	cut  error
	hits *int
}

func (p unsentProjectionPorts) Project(_ context.Context, l budget.Ledger) error {
	if p.cut != nil && *p.hits == 0 {
		r := l.Reservations["tool:unsent-root:original-call"]
		if r.Status == budget.ReservationHeld && !r.Dispatched {
			*p.hits++
			return p.cut
		}
	}
	return nil
}

func toolUnsentRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, mode := range []string{"terminal-cas", "release-before", "release-ack", "cancel-before-chain", "park-cas", "dispatch-before", "dispatch-ack-unknown", "unmetered-dispatch-ack", "reserve-projection", "cancel-after-chain-entry", "cancel-intent-ack", "unmetered-cancel-intent-ack", "owner-intent-ack", "ledger-cancel-intent-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			base, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
			const scope = "unsent-ledger"
			const root = "unsent-root"
			const id = "tool:unsent-root:original-call"
			terminal := mode == "terminal-cas" || mode == "release-before" || mode == "release-ack"
			unmetered := mode == "unmetered-dispatch-ack" || mode == "unmetered-cancel-intent-ack"
			ledgerCancelled := mode == "ledger-cancel-intent-ack"
			limits := budget.Limits{MaxToolCalls: 1, MaxConcurrentRuns: 1}
			coord := budget.Coordinator{Store: ledger}
			cut := errors.New("original unsent protocol fault")
			blockPeer := func() {
				if _, e := coord.Ensure(ctx, scope, limits); e != nil {
					t.Fatal(e)
				}
				if _, e := coord.RegisterRun(ctx, scope, "peer", budget.RunBudget{}); e != nil {
					t.Fatal(e)
				}
				if _, e := coord.Reserve(ctx, scope, budget.Reservation{ID: "peer-slot", RunID: "peer", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}, true); e != nil {
					t.Fatal(e)
				}
				if _, e := coord.Dispatch(ctx, scope, "peer-slot"); e != nil {
					t.Fatal(e)
				}
			}
			peerBlocked := false
			m := &settledToolModel{}
			physical, policies, reads, projectHits := 0, 0, 0, 0
			reg, e := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				if mode == "cancel-after-chain-entry" {
					return tools.ExecutionResult{}, context.Canceled
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{ "answer": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("owner")}}, nil
			})}})
			if e != nil {
				t.Fatal(e)
			}
			track := &toolConsumptionTimeline{}
			makeRunner := func(rt *kernel.Runtime, l budget.LedgerStore, o plugin.Observer, policy, projectionFault bool) *agent.Runner {
				t.Helper()
				timeline, e := agent.NewBoundedToolTimelineAdapter(track)
				if e != nil {
					t.Fatal(e)
				}
				var bounded *agent.BoundedBudgetAdapter
				if !unmetered {
					ports := unsentProjectionPorts{capacityAdmissionPorts: capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: scope}, limits: limits}, hits: &projectHits}
					if projectionFault {
						ports.cut = cut
					}
					b, e := agent.NewBoundedBudgetAdapter(l, ports)
					if e != nil {
						t.Fatal(e)
					}
					bounded = b
				}
				p, e := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policies++
					if terminal && !peerBlocked {
						current, err := ledger.LoadBudget(ctx, scope)
						if err != nil || current.View("").Usage.LLMCalls != 1 {
							t.Fatalf("Model not settled before Tool wait: %v", err)
						}
						blockPeer()
						peerBlocked = true
					}
					return plugin.ApprovalNotRequired, nil
				})
				if e != nil {
					t.Fatal(e)
				}
				reader := agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads++
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				})
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: bounded, ToolTimeline: timeline, ToolOutcomeVerifier: reader, DelegatedToolResults: reader}
				if policy {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{p}
				}
				if o != nil {
					d.Observers = []plugin.Observer{o}
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r
			}
			faultFor := func(s kernel.Store) *unsentRecoveryFault {
				return &unsentRecoveryFault{ea1AgentReceiptFault: &ea1AgentReceiptFault{Store: s, ExecutionStore: s.(kernel.ExecutionStore), ControlStore: s.(kernel.ControlStore), ChildExecutionStore: s.(kernel.ChildExecutionStore), ExecutionQuarantineStore: s.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: s.(kernel.OwnedPhysicalExecutionStore), injected: cut}}
			}
			req := agent.StartRequest{ID: root, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}}
			ks := base
			useLedger := ledger
			var fault *unsentRecoveryFault
			var ledgerFault *unsentLedgerFault
			if !terminal {
				switch mode {
				case "park-cas", "dispatch-before", "dispatch-ack-unknown", "unmetered-dispatch-ack", "cancel-intent-ack", "unmetered-cancel-intent-ack", "owner-intent-ack", "ledger-cancel-intent-ack":
					fault = faultFor(base)
					ks = fault
					switch mode {
					case "park-cas":
						fault.beforeEvent = "tool.admission_requeued"
						fault.beforeCount = 2
					case "dispatch-before":
						fault.beforeEvent = "tool.dispatch_intent"
						fault.beforeCount = 2
					case "dispatch-ack-unknown", "unmetered-dispatch-ack":
						fault.ackDispatch = true
					case "owner-intent-ack":
						fault.checkCut = kernel.ErrExecutionConflict
						fault.ackAction = func() { fault.checkPending = true }
					case "ledger-cancel-intent-ack":
						fault.ackAction = func() {
							if _, err := coord.CancelRoot(ctx, scope, root); err != nil {
								t.Fatal(err)
							}
						}
					case "cancel-intent-ack", "unmetered-cancel-intent-ack":
						// Set the cancel action below, after creating the Context.
					default:
						t.Fatal("unexpected fault mode", mode)
					}
				}
			}
			var o *unsentCancelObserver
			var observation plugin.Observer
			callCtx := ctx
			if mode == "cancel-before-chain" || mode == "park-cas" {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(ctx)
				defer cancel()
				o = &unsentCancelObserver{cancel: cancel}
				observation = o
			}
			if mode == "cancel-intent-ack" || mode == "unmetered-cancel-intent-ack" {
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(ctx)
				defer cancel()
				fault.ackAction = cancel
			}
			r := makeRunner(ea1StartRuntime(t, ks), useLedger, observation, true, mode == "reserve-projection")
			initial, e := r.StartRun(callCtx, req)
			if terminal {
				if e != nil || initial.Run.Status != kernel.RunStatusRunning || physical != 0 || policies != 1 {
					t.Fatal("original capacity wait", e, initial.Run.Status, physical, policies)
				}
				if _, e = coord.Settle(ctx, scope, "peer-slot", budget.Usage{ToolCalls: 1}, json.RawMessage("{}")); e != nil {
					t.Fatal(e)
				}
				// Close both original handles once. The injected terminal
				// closure and the later replacement use the newly opened
				// durable stores, not a closed pre-restart observer.
				ks = reopen()
				observer = ea1StartRuntime(t, ks)
				useLedger = reopenLedger()
				if mode == "terminal-cas" {
					fault = faultFor(ks)
					fault.beforeEvent = "agent.failed"
					fault.beforeCount = 1
					ks = fault
				} else {
					ledgerFault = &unsentLedgerFault{LedgerStore: useLedger, failure: cut, after: mode == "release-ack"}
					useLedger = ledgerFault
				}
				r = makeRunner(ea1StartRuntime(t, ks), useLedger, nil, false, false)
				_, e = r.Resume(ctx, root, initial.Run.Revision)
			}
			if ledgerCancelled {
				if !errors.Is(e, budget.ErrExhausted) || fault.hits != 1 || physical != 0 || track.starts != 0 || reads != 0 || len(m.calls) != 1 || policies != 1 {
					t.Fatal("ledger cancellation crossed chain boundary", e, physical, track.starts)
				}
				cur, e := observer.Load(ctx, root)
				if e != nil || cur.Run.Status != kernel.RunStatusFailed {
					t.Fatal("elected original denial was not consumed", e, cur.Run)
				}
				if _, e = observer.LookupChildExecution(ctx, root); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatal("ledger cancellation retained physical grant", e)
				}
				l, e := ledger.LoadBudget(ctx, scope)
				if e != nil {
					t.Fatal(e)
				}
				res := l.Reservations[id]
				if res.Status != budget.ReservationReleased || res.Active || res.Dispatched || res.Usage != (budget.Usage{}) {
					t.Fatal("elected cancel altered original accounting", res)
				}
				return
			}
			if mode == "cancel-after-chain-entry" {
				if e == nil || physical != 1 || track.starts != 1 {
					t.Fatal("actual chain entry fixture", e, physical, track.starts)
				}
				cur, e := observer.Load(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				l, e := ledger.LoadBudget(ctx, scope)
				if e != nil {
					t.Fatal(e)
				}
				res := l.Reservations[id]
				if !res.Dispatched || res.Status != budget.ReservationHeld {
					t.Fatal("actual dispatched effect was refunded", res)
				}
				_, marked, e := agent.FrozenPendingDispatch(cur)
				if e != nil || !marked {
					t.Fatal("actual effect lost marker", e)
				}
				g, e := observer.LookupChildExecution(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				c, e := observer.LookupOwnedPhysicalCallbackReturn(ctx, g)
				if e != nil {
					t.Fatal(e)
				}
				b, _, e := c.ToolReturns.Find("original-call")
				if e != nil || b.Unsent {
					t.Fatal("actual effect got unsent proof", b, e)
				}
				r = makeRunner(ea1StartRuntime(t, reopen()), reopenLedger(), nil, false, false)
				if _, e = r.Resume(ctx, root, cur.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) {
					t.Fatal("actual effect automatically replayed", e)
				}
				return
			}
			if e == nil || physical != 0 || track.starts != 0 || reads != 0 || len(m.calls) != 1 || policies != 1 {
				t.Fatal("no-send cut was not reached", mode, e, physical, track.starts, reads, len(m.calls), policies)
			}
			if o != nil && o.calls != 1 {
				t.Fatal("observer cut missing", o.calls)
			}
			if fault != nil && fault.hits == 0 {
				t.Fatal("store cut missing")
			}
			if ledgerFault != nil && ledgerFault.hits != 1 {
				t.Fatal("ledger cut missing")
			}
			if mode == "reserve-projection" && projectHits != 1 {
				t.Fatal("projection cut missing")
			}
			cur, e := observer.Load(ctx, root)
			if e != nil {
				t.Fatal(e)
			}
			var rt *kernel.Runtime
			var l budget.LedgerStore
			if terminal {
				rt = observer
				l = useLedger
				if ledgerFault != nil {
					l = ledgerFault.LedgerStore
				}
			} else {
				rt = ea1StartRuntime(t, reopen())
				l = reopenLedger()
			}
			r = makeRunner(rt, l, nil, false, false)
			marker := mode == "dispatch-ack-unknown" || mode == "unmetered-dispatch-ack"
			if marker {
				g, e := rt.LookupChildExecution(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				c, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
				if e != nil || !c.HostCompleted {
					t.Fatal("original no-send return missing", c, e)
				}
				b, found, e := c.ToolReturns.Find("original-call")
				if e != nil || !found || !b.Unsent || b.ReceiptDigest != "" || b.PendingKind != "" {
					t.Fatal("wrong original no-send evidence", b, e)
				}
				if !unmetered {
					foreign := makeRunner(rt, budget.NewMemoryLedgerStore(), nil, false, false)
					if _, err := foreign.Resume(ctx, root, cur.Run.Revision); !errors.Is(err, agent.ErrToolReceiptMismatch) {
						t.Fatal("foreign ledger consumed original no-send proof", err)
					}
					unchanged, err := rt.Load(ctx, root)
					if err != nil || unchanged.Run.Revision != cur.Run.Revision || !bytes.Equal(unchanged.State, cur.State) {
						t.Fatal("wrong ledger modified original marker", err)
					}
					if retained, err := rt.LookupChildExecution(ctx, root); err != nil || retained != g {
						t.Fatal("wrong ledger retired original grant", err)
					}
				}
				cur, e = r.Resume(ctx, root, cur.Run.Revision)
				if e != nil || physical != 0 || reads != 0 || !bytes.Contains(cur.State, []byte(`"phase":"unsent"`)) {
					t.Fatal("original no-send receipt-only recovery failed", e, physical, reads)
				}
			}
			if _, marked, e := agent.FrozenPendingDispatch(cur); e != nil || marked {
				t.Fatal("known unsent still owns physical marker", mode, e, marked)
			}
			if _, e = rt.LookupChildExecution(ctx, root); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("known unsent retained original grant", mode, e)
			}
			if !unmetered {
				saved, e := l.LoadBudget(ctx, scope)
				if e != nil {
					t.Fatal(e)
				}
				res := saved.Reservations[id]
				if res.Dispatched || res.Active || res.Status == budget.ReservationHeld || len(res.Receipt) != 0 || res.Usage != (budget.Usage{}) {
					t.Fatal("known unsent retained physical usage or active capacity", mode, res)
				}
			}
			// No replacement policy or result verifier participates. The
			// existing original admission alone drives closure/re-admission.
			if bytes.Contains(cur.State, []byte(`"phase":"unsent"`)) {
				cur, e = r.Resume(ctx, root, cur.Run.Revision)
				if terminal {
					dimension, ok := budget.DeniedDimension(e)
					if !errors.Is(e, budget.ErrExhausted) || !ok || dimension != budget.DimensionToolCalls || cur.Run.Status != kernel.RunStatusFailed || cur.Run.ErrorCode != "agent.shared_tool_calls_budget" {
						t.Fatal("original permanent denial lost", e, cur.Run)
					}
				} else if e != nil || cur.Run.Status != kernel.RunStatusRunning {
					t.Fatal("no-send closure replay failed", e, cur.Run)
				}
			} else if terminal {
				t.Fatal("permanent denial not persisted", cur.Run)
			}
			if !terminal {
				if !unmetered {
					// A peer can acquire and release the formerly occupied slot.
					coord.Store = l
					if _, e = coord.RegisterRun(ctx, scope, "peer", budget.RunBudget{}); e != nil {
						t.Fatal(e)
					}
					peerRes := budget.Reservation{ID: "peer-slot", RunID: "peer", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}
					if _, e = coord.Reserve(ctx, scope, peerRes, true); e != nil {
						t.Fatal("no-send held peer capacity", e)
					}
					if _, e = coord.Release(ctx, scope, peerRes.ID); e != nil {
						t.Fatal(e)
					}
				}
				cur, e = r.Resume(ctx, root, cur.Run.Revision)
				if e != nil || cur.Run.Status != kernel.RunStatusCompleted || physical != 1 || track.starts != 1 || track.ends != 1 || len(m.calls) != 2 {
					t.Fatal("original re-admission failed", mode, e, cur.Run, physical, track.starts, track.ends)
				}
			}
			if policies != 1 || reads != 0 {
				t.Fatal("original approval or result lookup repeated", policies, reads)
			}
			if !unmetered {
				saved, e := l.LoadBudget(ctx, scope)
				if e != nil {
					t.Fatal(e)
				}
				res := saved.Reservations[id]
				if terminal {
					if physical != 0 || res.Status != budget.ReservationReleased || res.Dispatched || res.Usage != (budget.Usage{}) {
						t.Fatal("terminal unsent accounting changed", res)
					}
				} else if res.Status != budget.ReservationSettled || !res.Dispatched || res.Usage.ToolCalls != 1 {
					t.Fatal("one exact physical charge missing", res)
				}
				if saved.View("").Reserved.ToolCalls != 0 || saved.View("").ActiveRuns != 0 {
					t.Fatal("capacity leaked", saved.View(""))
				}
			}
		})
	}
}
