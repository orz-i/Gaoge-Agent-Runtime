package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// The elected cancellation commits after the actual final Kernel owner
// check and earlier ledger read, but before the budget layer's Dispatch CAS.
type toolDispatchDenialStore struct {
	*ea1AgentReceiptFault
	afterCheck   func()
	afterCallID  string
	checkPending bool
	closureCuts  int
	hits, cuts   int
}

func (s *toolDispatchDenialStore) cut(m kernel.StoreMutation) bool {
	for _, event := range m.Events {
		if event.Type == "tool.admission_unsent" && s.closureCuts > 0 {
			s.closureCuts--
			s.cuts++
			return true
		}
	}
	return false
}
func (s *toolDispatchDenialStore) arm(m kernel.StoreMutation, err error) {
	if err == nil && s.afterCheck != nil {
		for _, event := range m.Events {
			if event.Type == "tool.dispatch_intent" && (s.afterCallID == "" || event.Message == s.afterCallID) {
				s.checkPending = true
			}
		}
	}
}
func (s *toolDispatchDenialStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.cut(m) {
		return kernel.Snapshot{}, s.injected
	}
	next, err := s.Store.Apply(ctx, id, rev, m)
	s.arm(m, err)
	return next, err
}
func (s *toolDispatchDenialStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.cut(m) {
		return kernel.Snapshot{}, s.injected
	}
	next, err := s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
	s.arm(m, err)
	return next, err
}
func (s *toolDispatchDenialStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	err := s.ExecutionStore.CheckExecution(ctx, id, l)
	if err == nil && s.checkPending && s.afterCheck != nil {
		action := s.afterCheck
		s.afterCheck = nil
		s.checkPending = false
		s.hits++
		action()
	}
	return err
}

// Even a Store write error with the same public classification as a native
// denial must not qualify a speculative dispatched candidate.
type toolDispatchWriteFault struct {
	budget.LedgerStore
	after bool
	hits  int
}

func (s *toolDispatchWriteFault) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	if s.hits == 0 && l.Reservations["tool:dispatch-denial-root:original-call"].Dispatched {
		s.hits++
		if s.after {
			if err := s.LedgerStore.SaveBudget(ctx, l, rev); err != nil {
				return err
			}
		}
		return budget.ErrExhausted
	}
	return s.LedgerStore.SaveBudget(ctx, l, rev)
}

type dispatchDenialPorts struct {
	capacityAdmissionPorts
	fail error
	hits *int
}

func (p dispatchDenialPorts) Project(_ context.Context, l budget.Ledger) error {
	if l.RunCancelled("dispatch-denial-root") && p.fail != nil {
		*p.hits++
		return p.fail
	}
	return nil
}

type dispatchDenialOpaqueLayer struct {
	mode  string
	calls *int
}

func (dispatchDenialOpaqueLayer) Name() string { return "fixture.opaque-tool-denial" }
func (m dispatchDenialOpaqueLayer) Tool(ctx context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	*m.calls++
	if m.mode == "opaque-before" {
		return tools.ExecutionResult{}, budget.ErrExhausted
	}
	out, err := next(ctx)
	switch m.mode {
	case "opaque-mask":
		return out, nil
	case "opaque-augment":
		return out, errors.Join(err, errors.New("opaque tool callback failed"))
	case "opaque-forged-result":
		return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: "committed"}}, err
	default:
		return out, err
	}
}

func toolDispatchDenial(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, mode := range []string{"budget-only", "timeline-outer", "timeline-inner", "parented", "cleanup-cas", "projection-failure", "timeline-failure", "opaque-before", "opaque-pass", "opaque-mask", "opaque-augment", "opaque-forged-result", "opaque-inner-unentered", "dispatch-write-before", "dispatch-write-ack", "actual-handler-error", "retirement-recovery-before", "retirement-recovery-ack", "retirement-finish-before", "retirement-finish-ack"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			retirementMode := strings.HasPrefix(mode, "retirement-")
			base, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
			const root = "dispatch-denial-root"
			const scope = "dispatch-denial-ledger"
			const id = "tool:dispatch-denial-root:original-call"
			limits := budget.Limits{MaxToolCalls: 2, MaxConcurrentRuns: 1}
			coord := budget.Coordinator{Store: ledger}
			cut := errors.New("native no-send closure CAS failed")
			store := &toolDispatchDenialStore{ea1AgentReceiptFault: &ea1AgentReceiptFault{
				Store: base, ExecutionStore: base.(kernel.ExecutionStore), ControlStore: base.(kernel.ControlStore), ChildExecutionStore: base.(kernel.ChildExecutionStore), ExecutionQuarantineStore: base.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: base.(kernel.OwnedPhysicalExecutionStore), injected: cut,
			}}
			cancelled := mode != "dispatch-write-before" && mode != "dispatch-write-ack" && mode != "actual-handler-error"
			if cancelled {
				store.afterCheck = func() {
					if _, err := coord.CancelRoot(ctx, scope, root); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "cleanup-cas" || retirementMode {
				store.closureCuts = 2
			}
			useLedger := ledger
			var writeFault *toolDispatchWriteFault
			if mode == "dispatch-write-before" || mode == "dispatch-write-ack" {
				writeFault = &toolDispatchWriteFault{LedgerStore: ledger, after: mode == "dispatch-write-ack"}
				useLedger = writeFault
			}
			m := &settledToolModel{}
			physical, policies, reads, opaque, projection := 0, 0, 0, 0, 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				if mode == "actual-handler-error" {
					return tools.ExecutionResult{}, budget.ErrExhausted
				}
				return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			timeline := &toolConsumptionTimeline{}
			if mode == "timeline-failure" {
				timeline.fail = errors.New("outer timeline terminal append failed")
			}
			mk := func(rt *kernel.Runtime, l budget.LedgerStore, original bool) *agent.Runner {
				t.Helper()
				ports := dispatchDenialPorts{capacityAdmissionPorts: capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: scope}, limits: limits}, hits: &projection}
				if original && mode == "projection-failure" {
					ports.fail = errors.New("native denial projection failed")
				}
				b, err := agent.NewBoundedBudgetAdapter(l, ports)
				if err != nil {
					t.Fatal(err)
				}
				layers := []plugin.ToolMiddleware{b}
				if original {
					track, err := agent.NewBoundedToolTimelineAdapter(timeline)
					if err != nil {
						t.Fatal(err)
					}
					switch mode {
					case "timeline-outer", "timeline-failure", "parented":
						layers = []plugin.ToolMiddleware{track, b}
					case "timeline-inner":
						layers = append(layers, track)
					case "opaque-before", "opaque-pass", "opaque-mask", "opaque-augment", "opaque-forged-result":
						layers = append([]plugin.ToolMiddleware{dispatchDenialOpaqueLayer{mode: mode, calls: &opaque}}, layers...)
					case "opaque-inner-unentered":
						layers = append(layers, dispatchDenialOpaqueLayer{mode: mode, calls: &opaque})
					}
				}
				p, err := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policies++
					return plugin.ApprovalNotRequired, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				reader := agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads++
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				})
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, RunMiddleware: []plugin.RunMiddleware{b}, ToolMiddleware: layers, ToolOutcomeVerifier: reader, DelegatedToolResults: reader, StartControlPreflight: func(context.Context, agent.StartRequest) error { return nil }}
				if original && !retirementMode {
					d.ApprovalPolicies = []plugin.ApprovalPolicy{p}
				}
				r, err := agent.NewRunner(d)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			req := agent.StartRequest{ID: root, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}}
			if mode == "parented" {
				parent, err := observer.Create(ctx, kernel.CreateRequest{ID: "dispatch-denial-parent", Kind: "workflow", Actor: req.Actor, Thread: req.Thread, Goal: "parent", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				req.ParentRunID = parent.Run.ID
				req.OriginFingerprint = agent.FrozenOriginFingerprint([]byte("{}"))
			}
			r := mk(ea1StartRuntime(t, store), useLedger, true)
			got, outcome, runErr := r.ObservePhysicalCall(ctx, root, plugin.RunStart, func(c context.Context) (kernel.Snapshot, error) { return r.StartRun(c, req) })
			cur, err := observer.Load(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := ledger.LoadBudget(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			res := saved.Reservations[id]
			if cancelled && (store.hits != 1 || !saved.RunCancelled(root) || res.Status != budget.ReservationReleased || res.Active || res.Dispatched) {
				t.Fatal("real cancellation cut not reached", store.hits, res, runErr)
			}
			if writeFault != nil && writeFault.hits != 1 {
				t.Fatal("dispatch store fault not reached")
			}
			wantPhysical := 0
			if mode == "actual-handler-error" {
				wantPhysical = 1
			}
			wantPolicies := 1
			if retirementMode {
				wantPolicies = 0
			}
			if physical != wantPhysical || policies != wantPolicies || reads != 0 || len(m.calls) != 1 || len(res.Receipt) != 0 || res.Usage != (budget.Usage{}) {
				t.Fatal("original no-send/unknown effect identity changed", physical, policies, reads, len(m.calls), res)
			}
			positive := mode == "budget-only" || mode == "timeline-outer" || mode == "timeline-inner" || mode == "parented" || mode == "opaque-inner-unentered"
			if positive {
				if !errors.Is(runErr, budget.ErrExhausted) || errors.Is(runErr, agent.ErrRootOutcomeUnknown) || errors.Is(runErr, kernel.ErrChildExecutionBusy) || got.Run.Status != kernel.RunStatusFailed || cur.Run.Status != kernel.RunStatusFailed || outcome.Kind() != agent.PhysicalCallSettled {
					t.Fatal("exact actual native denial did not terminalize/retire", runErr, got.Run, cur.Run, outcome.Kind())
				}
				if _, err = observer.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("exact denial retained grant", err)
				}
				wantTimeline := 0
				if mode == "timeline-outer" || mode == "parented" {
					wantTimeline = 1
				}
				if timeline.starts != wantTimeline || timeline.ends != wantTimeline || opaque != 0 {
					t.Fatal("unexpected admitted callbacks", timeline, opaque)
				}
				return
			}
			g, err := observer.LookupChildExecution(ctx, root)
			if err != nil {
				t.Fatal("original unresolved grant removed", err)
			}
			c, err := observer.LookupOwnedPhysicalCallbackReturn(ctx, g)
			if err != nil {
				t.Fatal(err)
			}
			b, found, err := c.ToolReturns.Find("original-call")
			if err != nil {
				t.Fatal(err)
			}
			if retirementMode {
				if store.cuts != 2 || !errors.Is(runErr, cut) || !c.HostCompleted || !found || !b.Unsent || c.ControlCustody != kernel.OwnedCustodyScoped {
					t.Fatal("original no-send proof missing", runErr, c, b)
				}
				stage := strings.TrimPrefix(mode, "retirement-")
				fault := &returnedRootRetirementFault{managedStorePorts: base.(managedStorePorts), mode: stage}
				r = mk(ea1StartRuntime(t, fault), ledger, false)
				original, err := r.RootDeferredCleanupEligibility(ctx, root, cur.Run.Revision)
				if err != nil || !original.Valid() || original.RetirementOnly() {
					t.Fatal("original receipt scope missing", err)
				}
				_, err = r.ReconcileRootDeferredChildren(ctx, root, cur.Run.Revision, original)
				if !errors.Is(err, errReturnedRootRetirementCut) || fault.hits != 1 || fault.grant != g {
					t.Fatal("receipt/retirement cut not reached", err, fault.hits, fault.grant)
				}
				rt := ea1StartRuntime(t, reopen())
				l := reopenLedger()
				r = mk(rt, l, false)
				before, err := rt.Load(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				_, marked, err := agent.FrozenPendingDispatch(before)
				if err != nil || marked != (stage == "recovery-before") {
					t.Fatal("unexpected persisted marker", stage, marked, err)
				}
				receipt, receiptErr := rt.LoadOwnedRetirement(ctx, g.ID)
				switch stage {
				case "finish-ack":
					if receiptErr != nil {
						t.Fatal(receiptErr)
					}
					if !receipt.Valid() || receipt.Evidence.Grant != g || receipt.Evidence.Completion != c {
						t.Fatal("wrong original retirement", receipt)
					}
				default:
					if !errors.Is(receiptErr, kernel.ErrNotFound) {
						t.Fatal("premature retirement", receiptErr)
					}
				}
				if err = rt.VerifyReturnedRootSettlement(ctx, before); err != nil {
					t.Fatal("exact returned original scope not observable", stage, err)
				}
				next, err := r.RootDeferredCleanupEligibility(ctx, root, before.Run.Revision)
				if err != nil || !original.Matches(next) || next.RetirementOnly() == marked {
					t.Fatal("same original retirement scope not selected", stage, err)
				}
				if _, err = r.ReconcileRootDeferredChildren(ctx, root, before.Run.Revision, agent.RootDeferredCleanupScope{}); !errors.Is(err, kernel.ErrExecutionConflict) {
					t.Fatal("foreign cleanup scope accepted", err)
				}
				before, err = r.ReconcileRootDeferredChildren(ctx, root, before.Run.Revision, original)
				if err != nil {
					t.Fatal("receipt-only retirement did not converge", stage, err)
				}
				final, err := rt.Cancel(ctx, root, before.Run.Revision, "finish original elected cancellation")
				if err != nil || final.Run.Status != kernel.RunStatusCancelled || physical != 0 || policies != 0 || reads != 0 || len(m.calls) != 1 {
					t.Fatal("original retirement did not permit elected control", err, final.Run, physical, reads)
				}
				acked, err := rt.LoadOwnedRetirement(ctx, g.ID)
				if err != nil || !acked.Valid() || acked.Evidence.Grant != g || acked.Evidence.Completion != c || (receiptErr == nil && acked.Identity() != receipt.Identity()) {
					t.Fatal("original retirement identity changed", err, acked)
				}
				if _, err = rt.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("original grant retained", err)
				}
				return
			}
			if mode == "cleanup-cas" {
				if store.cuts != 2 || !errors.Is(runErr, cut) || !c.HostCompleted || !found || !b.Unsent || b.ReceiptDigest != "" || b.PendingKind != "" {
					t.Fatal("qualified original no-send proof lost on cleanup failure", runErr, c, b, store.cuts)
				}
				rt := ea1StartRuntime(t, reopen())
				l := reopenLedger()
				r = mk(rt, l, false)
				staged, err := r.Resume(ctx, root, cur.Run.Revision)
				if err != nil || !bytes.Contains(staged.State, []byte(`"phase":"unsent"`)) || physical != 0 || reads != 0 {
					t.Fatal("original native refusal could not recover without verifier", err, staged.Run, physical, reads)
				}
				if _, err = rt.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("recovered no-send marker retained grant", err)
				}
				final, err := rt.Cancel(ctx, root, staged.Run.Revision, "finish elected cancellation")
				if err != nil || final.Run.Status != kernel.RunStatusCancelled {
					t.Fatal("original elected control still blocked", err, final.Run)
				}
				return
			}
			if !errors.Is(runErr, agent.ErrRootOutcomeUnknown) || outcome.Kind() == agent.PhysicalCallSettled || b.Unsent || cur.Run.Status != kernel.RunStatusRunning {
				t.Fatal("unknown/opaque callback was qualified as unsent", runErr, c, b, cur.Run)
			}
			if mode == "projection-failure" && projection != 1 {
				t.Fatal("projection fault not reached", projection)
			}
			if mode == "timeline-failure" && (timeline.starts != 1 || timeline.ends != 1) {
				t.Fatal("timeline fault not reached", timeline)
			}
			if mode == "opaque-before" || mode == "opaque-pass" || mode == "opaque-mask" || mode == "opaque-augment" || mode == "opaque-forged-result" {
				if opaque != 1 {
					t.Fatal("opaque layer not entered", opaque)
				}
			}
			if mode == "dispatch-write-before" && res.Dispatched || (mode == "dispatch-write-ack" || mode == "actual-handler-error") && !res.Dispatched {
				t.Fatal("unexpected actual dispatch fact", mode, res)
			}
			rt := ea1StartRuntime(t, reopen())
			l := reopenLedger()
			r = mk(rt, l, false)
			_, err = r.Resume(ctx, root, cur.Run.Revision)
			if !errors.Is(err, agent.ErrToolOutcomeUnknown) || physical != wantPhysical || reads != 0 {
				t.Fatal("unknown mode redispatched or used unqualified receipt", mode, err, physical, reads)
			}
			if _, err = rt.Cancel(ctx, root, cur.Run.Revision, "unknown effects"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
				t.Fatal("unknown grant allowed cancellation", err)
			}
		})
	}
}
