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
	ackEvent     string
	ackAction    func()
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
		return kernel.ErrExecutionConflict
	}
	return f.ExecutionStore.CheckExecution(ctx, id, lease)
}
func (f *unsentRecoveryFault) Commit(ctx context.Context, id string, rev uint64, m kernel.StoreMutation, auth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	for _, ev := range m.Events {
		if f.beforeCount > 0 && ev.Type == f.beforeEvent {
			f.beforeCount--
			f.hits++
			return kernel.Snapshot{}, false, f.injected
		}
		if f.ackAction != nil && ev.Type == f.ackEvent {
			next, created, err := f.Store.Commit(ctx, id, rev, m, auth)
			if err == nil {
				action := f.ackAction
				f.ackAction = nil
				f.hits++
				action()
			}
			return next, created, err
		}
		if f.ackDispatch && ev.Type == "tool.dispatch_intent" {
			f.ackDispatch = false
			f.hits++
			if _, _, err := f.Store.Commit(ctx, id, rev, m, auth); err != nil {
				return kernel.Snapshot{}, false, err
			}
			// Prevent immediate ACK disambiguation; only the original
			// unmetered path may subsequently prove its own no-send return.
			f.readCuts = 2
			return kernel.Snapshot{}, false, f.injected
		}
	}
	return f.ea1AgentReceiptFault.Commit(ctx, id, rev, m, auth)
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

func toolUnsentRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	// Native dispatch reserves and marks the Run in ONE commit: the old
	// reserve-projection and independent release-before/release-ACK states
	// cannot exist. Their atomic cut coverage is runNativeToolBudget's
	// dispatch-before/dispatch-ack plus toolDispatchDenial's terminal cuts.
	// Keep the unmetered no-send protocol, and distinguish safe pre-commit
	// retries from committed native obligations, even when no handler sent.
	for _, mode := range []string{"cancel-before-chain", "park-cas", "dispatch-before", "dispatch-ack-unknown", "unmetered-dispatch-ack", "cancel-after-chain-entry", "cancel-intent-ack", "unmetered-cancel-intent-ack", "owner-intent-ack", "ledger-cancel-intent-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			base, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger := newToolTestLedger(t, base)
			const scope, root, id = "unsent-ledger", "unsent-root", "tool:unsent-root:original-call"
			unmetered := mode == "park-cas" || mode == "unmetered-dispatch-ack" || mode == "unmetered-cancel-intent-ack"
			cut := errors.New("original native dispatch interrupted")
			fault := &unsentRecoveryFault{ea1AgentReceiptFault: &ea1AgentReceiptFault{Store: base, LedgerStore: ledger, ExecutionStore: base.(kernel.ExecutionStore), ExecutionQuarantineStore: base.(kernel.ExecutionQuarantineStore), PhysicalExecutionStore: base.(kernel.PhysicalExecutionStore), injected: cut}, ackEvent: "tool.dispatch_intent"}
			m := &settledToolModel{}
			physical, policies, reads := 0, 0, 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				if mode == "cancel-after-chain-entry" {
					return tools.ExecutionResult{}, context.Canceled
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{ "answer": true }`), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed", OwnerReceiptDigest: kernel.ManagedDigest("owner")}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			track := &toolConsumptionTimeline{}
			makeRunner := func(s kernel.Store, o plugin.Observer, original bool) (*agent.Runner, *kernel.Runtime) {
				t.Helper()
				rt := ea1StartRuntime(t, s)
				timeline, e := agent.NewBoundedToolTimelineAdapter(track)
				if e != nil {
					t.Fatal(e)
				}
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, ToolTimeline: timeline, ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads++
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				})}
				if !unmetered {
					d.BoundedBudget, e = agent.NewBoundedBudgetAdapter(newToolTestLedger(t, s), capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: scope}, limits: budget.Limits{MaxToolCalls: 1, MaxConcurrentRuns: 1}})
					if e != nil {
						t.Fatal(e)
					}
				}
				if original {
					policy, e := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
						policies++
						return plugin.ApprovalNotRequired, nil
					})
					if e != nil {
						t.Fatal(e)
					}
					d.ApprovalPolicies = []plugin.ApprovalPolicy{policy}
				}
				if o != nil {
					d.Observers = []plugin.Observer{o}
				}
				r, e := agent.NewRunner(d)
				if e != nil {
					t.Fatal(e)
				}
				return r, rt
			}
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var observation plugin.Observer
			var o *unsentCancelObserver
			switch mode {
			case "cancel-before-chain", "park-cas":
				o = &unsentCancelObserver{cancel: cancel}
				observation = o
				if mode == "park-cas" {
					fault.beforeEvent, fault.beforeCount = "tool.admission_requeued", 2
				}
			case "dispatch-before":
				fault.beforeEvent, fault.beforeCount = "tool.dispatch_intent", 1
			case "dispatch-ack-unknown", "unmetered-dispatch-ack":
				fault.ackDispatch = true
			case "cancel-intent-ack", "unmetered-cancel-intent-ack":
				fault.ackAction = cancel
			case "owner-intent-ack":
				fault.ackAction = func() { fault.checkPending = true }
			case "ledger-cancel-intent-ack":
				// Cancellation must precede the atomic native dispatch, not
				// race a nonexistent independent ledger Dispatch afterward.
				fault.ackEvent = "tool.authorization_committed"
				fault.ackAction = func() {
					if _, e := (budget.Coordinator{Store: ledger}).CancelRoot(ctx, scope, root); e != nil {
						t.Fatal(e)
					}
				}
			}
			r, _ := makeRunner(fault, observation, true)
			_, callErr := r.StartRun(callCtx, agent.StartRequest{ID: root, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}})
			wantPhysical := 0
			if mode == "cancel-after-chain-entry" {
				wantPhysical = 1
			}
			if callErr == nil || physical != wantPhysical || track.starts != wantPhysical || reads != 0 || len(m.calls) != 1 || policies != 1 || o != nil && o.calls != 1 {
				t.Fatal("original interruption not reached", callErr, physical, track.starts, reads, policies)
			}
			if mode != "cancel-before-chain" && mode != "cancel-after-chain-entry" && fault.hits == 0 {
				t.Fatal("native Store fault not reached", callErr)
			}
			before, err := observer.Load(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			fresh := reopen()
			r, rt := makeRunner(fresh, nil, false)
			pendingCall, marked, err := agent.FrozenPendingDispatch(before)
			if err != nil {
				t.Fatal(err)
			}
			unknown := !unmetered && (mode == "dispatch-ack-unknown" || mode == "cancel-after-chain-entry")
			if !unmetered {
				l, e := newToolTestLedger(t, fresh).LoadBudget(ctx, scope)
				res, exists := l.Reservations[id]
				// The unreadable dispatch ACK exits before the native suspend
				// transition: both the Call quota and its live slot stay held.
				active := mode == "dispatch-ack-unknown"
				hasReceipt := len(res.Receipt) != 0 && !bytes.Equal(res.Receipt, []byte("null"))
				// A returned pre-entry denial after an acknowledged intent
				// atomically requeues the exact original reservation.
				requeued := mode == "cancel-intent-ack" || mode == "owner-intent-ack"
				if requeued && (!exists || marked || res.Status != budget.ReservationWaiting || res.Dispatched || res.Active || hasReceipt || res.Usage != (budget.Usage{}) || l.View(root).Reserved.ToolCalls != 0) {
					t.Fatal("acknowledged no-send did not requeue", res, marked)
				}
				if e != nil || exists != (unknown || requeued) || marked != unknown || unknown && (res.ID != id || res.RunID != root || res.RequestHash != kernel.ManagedDigest(tools.ExecutionRequest{RunID: root, Call: pendingCall}) || res.Requested != (budget.Usage{ToolCalls: 1}) || !res.Dispatched || res.Status != budget.ReservationHeld || res.Active != active || hasReceipt || res.Usage != (budget.Usage{}) || l.View(root).Reserved.ToolCalls != 1 || l.FinishedRuns[root]) {
					t.Fatal("interruption split native dispatch/accounting", e, res, marked)
				}
				if mode == "ledger-cancel-intent-ack" {
					if !errors.Is(callErr, budget.ErrExhausted) || !l.RunCancelled(root) || before.Run.Status != kernel.RunStatusFailed || !l.FinishedRuns[root] {
						t.Fatal("native cancellation did not close original lifetime", callErr, before.Run)
					}
					return
				}
			}
			if unknown {
				if _, e := r.Resume(ctx, root, before.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) || physical != wantPhysical || len(m.calls) != 1 {
					t.Fatal("committed unknown dispatch was replayed", e, physical)
				}
				if _, e := rt.Cancel(ctx, root, before.Run.Revision, "unknown original effect"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
					t.Fatal("unknown dispatch lost its control fence", e)
				}
				return
			}
			if marked {
				// Only the unmetered path retains an original no-send proof
				// across its dispatch ACK cut. Native accounting never refunds
				// a committed obligation on this evidence.
				g, e := rt.LookupChildExecution(ctx, root)
				if e != nil {
					t.Fatal(e)
				}
				c, e := rt.LookupPhysicalCallbackReturn(ctx, g)
				b, found, bindErr := c.ToolReturns.Find("original-call")
				if e != nil || bindErr != nil || !c.HostCompleted || !found || !b.Unsent || b.LedgerID != "" || b.ReceiptDigest != "" || b.PendingKind != "" {
					t.Fatal("original unmetered no-send proof lost", e, b)
				}
				before, err = r.Resume(ctx, root, before.Run.Revision)
				if err != nil || !bytes.Contains(before.State, []byte(`"phase":"unsent"`)) || physical != 0 || reads != 0 {
					t.Fatal("receipt-only no-send recovery performed effects", err)
				}
			}
			// Pre-commit native interruption has no reservation or marker;
			// unmetered interruption may first consume its persisted closure.
			for attempt := 0; attempt < 3 && before.Run.Status != kernel.RunStatusCompleted; attempt++ {
				before, err = r.Resume(ctx, root, before.Run.Revision)
				if err != nil {
					t.Fatal("safe original re-admission", err)
				}
			}
			if before.Run.Status != kernel.RunStatusCompleted || physical != 1 || track.starts != 1 || track.ends != 1 || len(m.calls) != 2 || policies != 1 || reads != 0 {
				t.Fatal("original authorization replayed or re-admission lost", before.Run, physical, policies, reads)
			}
			if _, e := rt.LookupChildExecution(ctx, root); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("completed original Call retained grant", e)
			}
			if !unmetered {
				l, e := newToolTestLedger(t, fresh).LoadBudget(ctx, scope)
				res := l.Reservations[id]
				if e != nil || res.Status != budget.ReservationSettled || !res.Dispatched || res.Usage.ToolCalls != 1 || l.View("").Reserved.ToolCalls != 0 || l.View("").ActiveRuns != 0 || !l.FinishedRuns[root] {
					t.Fatal("safe retry did not charge exactly one native Call", e, res)
				}
			}
		})
	}
}
