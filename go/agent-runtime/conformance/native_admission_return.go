package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type admissionCancelPorts struct {
	capacityAdmissionPorts
	projectionErr error
}

func (p admissionCancelPorts) Project(_ context.Context, l budget.Ledger) error {
	if l.RunCancelled("cancel-admission-root") {
		return p.projectionErr
	}
	return nil
}

type admissionOpaqueRun struct{ mode string }

func (admissionOpaqueRun) Name() string { return "fixture.opaque-admission" }
func (m admissionOpaqueRun) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if m.mode == "opaque-before" {
		return kernel.Snapshot{}, budget.ErrExhausted
	}
	s, err := next(ctx)
	if m.mode == "opaque-mask" {
		return s, nil
	}
	return s, errors.Join(err, errors.New("unqualified outer callback error"))
}

// One original native ledger cancellation, with the exact Run middleware
// invocation and its complete callback lifetime. No typed Host error or
// failed projection can manufacture a completed Grant.
func nativeAdmissionReturns(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), ledgerFactory func(*testing.T) (budget.LedgerStore, func() budget.LedgerStore)) {
	for _, mode := range []string{"tool-only", "run-and-tool", "parented-run-and-tool", "projection-failure", "opaque-before", "opaque-after", "opaque-mask"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			base, peer, reopen := factory(t)
			observer := ea1StartRuntime(t, peer)
			ledger, reopenLedger := newToolTestLedger(t, ledgerFactory)
			const root = "cancel-admission-root"
			const scope = "cancel-admission-ledger"
			limits := budget.Limits{MaxToolCalls: 1}
			coord := budget.Coordinator{Store: ledger}
			if _, err := coord.Ensure(ctx, scope, limits); err != nil {
				t.Fatal(err)
			}
			if _, err := coord.RegisterRun(ctx, scope, "peer", budget.RunBudget{}); err != nil {
				t.Fatal(err)
			}
			if _, err := coord.Reserve(ctx, scope, budget.Reservation{ID: "peer-slot", RunID: "peer", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := coord.Dispatch(ctx, scope, "peer-slot"); err != nil {
				t.Fatal(err)
			}
			m := &settledToolModel{}
			physical, policy, reads := 0, 0, 0
			reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
				physical++
				return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			projectionErr := errors.New("original budget projection did not acknowledge")
			mk := func(rt *kernel.Runtime, l budget.LedgerStore, replace bool) *agent.Runner {
				t.Helper()
				ports := admissionCancelPorts{capacityAdmissionPorts: capacityAdmissionPorts{settledToolPorts: settledToolPorts{id: scope}, limits: limits}}
				if replace && mode == "projection-failure" {
					ports.projectionErr = projectionErr
				}
				a, err := agent.NewBoundedBudgetAdapter(l, ports)
				if err != nil {
					t.Fatal(err)
				}
				p, err := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
					policy++
					return plugin.ApprovalNotRequired, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				reader := agent.ToolOutcomeVerifierFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
					reads++
					return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
				})
				d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, ApprovalPolicies: []plugin.ApprovalPolicy{p}, ToolMiddleware: []plugin.ToolMiddleware{a}, ToolOutcomeVerifier: reader, DelegatedToolResults: reader, StartControlPreflight: func(context.Context, agent.StartRequest) error { return nil }}
				if mode != "tool-only" {
					d.RunMiddleware = []plugin.RunMiddleware{a}
				}
				if replace && (mode == "opaque-before" || mode == "opaque-after" || mode == "opaque-mask") {
					d.RunMiddleware = append([]plugin.RunMiddleware{admissionOpaqueRun{mode: mode}}, d.RunMiddleware...)
				}
				r, err := agent.NewRunner(d)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			req := agent.StartRequest{ID: root, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "read", ToolKeys: []string{"read"}}
			if mode == "parented-run-and-tool" {
				parent, err := observer.Create(ctx, kernel.CreateRequest{ID: "cancel-admission-parent", Kind: "workflow", Actor: req.Actor, Thread: req.Thread, Goal: "parent", State: json.RawMessage("{}")})
				if err != nil {
					t.Fatal(err)
				}
				req.ParentRunID = parent.Run.ID
				req.OriginFingerprint = agent.FrozenOriginFingerprint([]byte("{}"))
			}
			initial, err := mk(ea1StartRuntime(t, base), ledger, false).StartRun(ctx, req)
			if err != nil || initial.Run.Status != kernel.RunStatusRunning || physical != 0 || policy != 1 || len(m.calls) != 1 {
				t.Fatal("did not reach capacity wait", err, initial.Run, physical, policy)
			}
			if _, err = observer.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("capacity wait retained grant", err)
			}
			cancelled, err := coord.CancelRoot(ctx, scope, root)
			if err != nil {
				t.Fatal(err)
			}
			res := cancelled.Reservations["tool:"+root+":original-call"]
			if res.Status != budget.ReservationReleased || res.Active || res.Dispatched {
				t.Fatal("original cancellation did not release capacity", res)
			}
			rt := ea1StartRuntime(t, reopen())
			l := reopenLedger()
			r := mk(rt, l, true)
			got, outcome, resumeErr := r.ObservePhysicalCall(ctx, root, plugin.RunResume, func(c context.Context) (kernel.Snapshot, error) { return r.Resume(c, root, initial.Run.Revision) })
			cur, err := rt.Load(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			if physical != 0 || policy != 1 || len(m.calls) != 1 || reads != 0 {
				t.Fatal("cancelled scope dispatched new work", physical, policy, reads)
			}
			if mode == "tool-only" {
				if !errors.Is(resumeErr, budget.ErrExhausted) || got.Run.Status != kernel.RunStatusFailed || outcome.Kind() != agent.PhysicalCallSettled {
					t.Fatal("tool-only original denial changed", resumeErr, got.Run, outcome.Kind())
				}
				return
			}
			if mode == "run-and-tool" || mode == "parented-run-and-tool" {
				if !errors.Is(resumeErr, budget.ErrExhausted) || errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || errors.Is(resumeErr, kernel.ErrChildExecutionBusy) || !reflect.DeepEqual(initial, cur) || !reflect.DeepEqual(got, cur) || outcome.Kind() != agent.PhysicalCallSettled {
					t.Fatal("native no-entry return not qualified", resumeErr, got.Run, cur.Run, outcome.Kind())
				}
				if _, err = rt.LookupChildExecution(ctx, root); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("known cancelled admission retained grant", err)
				}
				final, err := rt.Cancel(ctx, root, cur.Run.Revision, "finish original election")
				if err != nil || final.Run.Status != kernel.RunStatusCancelled {
					t.Fatal("native admission denied its original cancellation", err, final.Run)
				}
			} else {
				if !errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || outcome.Kind() == agent.PhysicalCallSettled || !reflect.DeepEqual(initial, cur) {
					t.Fatal("unknown callback acquired native completion", resumeErr, outcome.Kind(), cur.Run)
				}
				g, err := rt.LookupChildExecution(ctx, root)
				if err != nil {
					t.Fatal("unknown grant removed", err)
				}
				c, err := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
				if err != nil || c.HostCompleted {
					t.Fatal("unknown callback got original Host completion", err, c)
				}
				if _, err = rt.Cancel(ctx, root, cur.Run.Revision, "unknown callback"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
					t.Fatal("unknown physical cancellation accepted", err)
				}
			}
		})
	}
}
