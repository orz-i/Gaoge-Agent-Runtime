package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type nativeAdmissionCancelModel struct {
	store  *MemoryStore
	ledger budget.LedgerStore
	calls  int
}

func (m *nativeAdmissionCancelModel) Generate(ctx context.Context, q model.Request) (model.Response, error) {
	m.calls++
	if m.calls != 1 {
		return model.Response{}, errors.New("cancelled original unexpectedly sampled again")
	}
	inv, err := m.store.GetInvocationByExecutionRefID(ctx, q.RunID)
	if err != nil {
		return model.Response{}, err
	}
	c := budget.Coordinator{Store: m.ledger}
	if _, err = c.RegisterRun(ctx, inv.TurnID, "peer-run", budget.RunBudget{}); err != nil {
		return model.Response{}, err
	}
	if _, err = c.Reserve(ctx, inv.TurnID, budget.Reservation{ID: "peer-slot", RunID: "peer-run", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}, false); err != nil {
		return model.Response{}, err
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "held-call", ToolKey: "local.read", Arguments: json.RawMessage("{}")}}}, nil
}

type nativeDispatchCancelKernelStore struct {
	*memory.Store
	action         func()
	afterControl   func()
	controlACK     bool
	controlHits    int
	pending        bool
	hits           int
	closureCuts    int
	cuts           int
	retirementCut  string
	retirementHits int
	interrupted    bool
	cutGrant       kernel.ChildExecutionGrant
}

func (s *nativeDispatchCancelKernelStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		for _, event := range commitMutation.Events {
			if event.Type == "tool.admission_unsent" && s.closureCuts > 0 {
				s.closureCuts--
				s.cuts++
				return kernel.Snapshot{}, false, errors.New("native unsent closure cut")
			}
		}
		next, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && s.action != nil {
			for _, e := range commitMutation.Events {
				if e.Type == "tool.dispatch_intent" {
					s.pending = true
				}
			}
		}
		return next, false, err
	}
	if commitAuth.Recovery != nil {
		g := commitAuth.Recovery.Grant

		if s.interrupted {
			return kernel.Snapshot{}, false, errOriginalRetirementCut
		}
		if s.retirementCut == "recovery-before" {
			return kernel.Snapshot{}, false, s.interrupt(g)
		}
		next, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && s.retirementCut == "recovery-ack" {
			return kernel.Snapshot{}, false, s.interrupt(g)
		}
		return next, false, err
	}
	next, replay, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
	if err == nil && commitAuth.Control != nil && commitAuth.Control.Kind == kernel.ControlCancel && (s.afterControl != nil || s.controlACK) {
		s.controlHits++
		if s.afterControl != nil {
			fn := s.afterControl
			s.afterControl = nil
			fn()
		}
		if s.controlACK {
			s.controlACK = false
			return kernel.Snapshot{}, false, errOriginalBudgetCancelACK
		}
	}
	return next, replay, err
}

var errOriginalRetirementCut = errors.New("original native receipt/retirement interrupted")
var errOriginalBudgetCancelACK = errors.New("elected budget cancellation ACK interrupted")

func (s *nativeDispatchCancelKernelStore) interrupt(g kernel.ChildExecutionGrant) error {
	s.retirementHits++
	s.interrupted = true
	s.cutGrant = g
	return errOriginalRetirementCut
}

func (s *nativeDispatchCancelKernelStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.interrupted {
		return errOriginalRetirementCut
	}
	if s.retirementCut == "finish-before" {
		return s.interrupt(g)
	}
	err := s.Store.FinishChildExecution(ctx, g, p)
	if err == nil && s.retirementCut == "finish-ack" {
		return s.interrupt(g)
	}
	return err
}
func (s *nativeDispatchCancelKernelStore) LoadOwnedRetirement(ctx context.Context, id string) (kernel.OwnedRetirementReceipt, error) {
	if s.interrupted && id == s.cutGrant.ID {
		return kernel.OwnedRetirementReceipt{}, errOriginalRetirementCut
	}
	return s.Store.LoadOwnedRetirement(ctx, id)
}

func (s *nativeDispatchCancelKernelStore) CheckExecution(ctx context.Context, id string, l *kernel.ExecutionLease) error {
	err := s.Store.CheckExecution(ctx, id, l)
	if err == nil && s.pending && s.action != nil {
		action := s.action
		s.action = nil
		s.pending = false
		s.hits++
		action()
	}
	return err
}

type nativeCancelFixture struct {
	store         *MemoryStore
	ks            *nativeDispatchCancelKernelStore
	rt            *kernel.Runtime
	native        *agent.Runner
	host          *Runner
	model         *nativeAdmissionCancelModel
	physical      int
	start         Snapshot
	before        kernel.Snapshot
	turnID, runID string
}

func newNativeCancelFixture(t *testing.T, timeline bool) *nativeCancelFixture {
	t.Helper()
	f := &nativeCancelFixture{store: NewMemoryStore(), ks: &nativeDispatchCancelKernelStore{Store: memory.NewStore()}}
	f.model = &nativeAdmissionCancelModel{store: f.store, ledger: f.ks}
	f.compose(t, f.ks, timeline)
	req := StartRequest{HostThread: HostRef{Kind: "conversation", ID: "native-admission-thread"}, HostTurn: HostRef{Kind: "conversation_turn", ID: "native-admission-turn"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "native-admission-thread"}, Goal: "read", Config: ConfigSnapshot{AgentSpec: AgentSpec{Model: "fixture", ToolKeys: []string{"local.read"}}, ToolPolicies: []ToolPolicySnapshot{{Key: "local.read", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never"}}, SharedBudget: &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}}
	var e error
	f.start, e = f.host.StartAgentTurn(t.Context(), req)
	if e != nil || f.start.Turn.Status != TurnRunning || f.model.calls != 1 || f.physical != 0 {
		t.Fatal("native capacity wait", e, f.start.Turn.Status, f.model.calls, f.physical)
	}
	inv, ok := TopLevelInvocation(f.start)
	if !ok {
		t.Fatal("missing original invocation")
	}
	f.turnID, f.runID = f.start.Turn.ID, inv.ExecutionRefID
	f.before, e = f.rt.Load(t.Context(), f.runID)
	if e != nil {
		t.Fatal(e)
	}
	ledger, e := f.rt.BudgetStore().LoadBudget(t.Context(), f.turnID)
	if e != nil || ledger.Reservations["tool:"+f.runID+":held-call"].ID != "" || ledger.View(f.runID).Usage.LLMCalls != 1 {
		t.Fatal("capacity wait invented an effect", ledger, e)
	}
	return f
}

func (f *nativeCancelFixture) compose(t *testing.T, s kernel.Store, timeline bool) {
	t.Helper()
	var e error
	f.rt, e = kernel.New(kernel.Dependencies{Store: s})
	if e != nil {
		t.Fatal(e)
	}
	shared, e := NewBudgetAuthority(BudgetAuthorityDependencies{Runtime: f.rt, Store: f.store, Clock: hostedBudgetClock{}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := shared.Adapter()
	if e != nil {
		t.Fatal(e)
	}
	reg, e := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		f.physical++
		return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if e != nil {
		t.Fatal(e)
	}
	deps := agent.Dependencies{Runtime: f.rt, Model: f.model, Catalog: reg, Executor: reg, BoundedBudget: b}
	if timeline {
		deps.ToolTimeline, e = NewBoundedToolTimelineAdapter(f.store, hostedBudgetClock{}, nil)
		if e != nil {
			t.Fatal(e)
		}
	}
	f.native, e = agent.NewRunner(deps)
	if e != nil {
		t.Fatal(e)
	}
	f.host, e = NewRunner(Dependencies{Runtime: f.rt, Agent: f.native, Store: f.store, Clock: hostedBudgetClock{}, Budget: shared, Catalog: reg})
	if e != nil {
		t.Fatal(e)
	}
}

func (f *nativeCancelFixture) cancel(t *testing.T) (Snapshot, error) {
	return f.host.Cancel(t.Context(), f.turnID, "stop original Run")
}
func (f *nativeCancelFixture) verifyCancelled(t *testing.T) {
	t.Helper()
	final, e := f.rt.Load(t.Context(), f.runID)
	if e != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatal("original Run not cancelled", e, final.Run)
	}
	if _, e = f.rt.LookupChildExecution(t.Context(), f.runID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("original grant retained", e)
	}
	ledger, e := f.rt.BudgetStore().LoadBudget(t.Context(), f.turnID)
	if e != nil {
		t.Fatal(e)
	}
	res := ledger.Reservations["tool:"+f.runID+":held-call"]
	if !ledger.RunCancelled(f.runID) || !ledger.FinishedRuns[f.runID] || res.ID != "" && (res.Status != budget.ReservationReleased || res.Dispatched || res.Active || res.Usage != (budget.Usage{})) || ledger.View(f.runID).Usage.ToolCalls != 0 || f.physical != 0 || f.model.calls != 1 {
		t.Fatal("cancel changed original effects", res, ledger.View(f.runID), f.physical, f.model.calls)
	}
	items, e := f.store.ListItems(t.Context(), f.turnID, 0, 500)
	if e != nil {
		t.Fatal(e)
	}
	if ActiveCancellationFence(items) {
		t.Fatal("original cancellation fence retained")
	}
	for _, item := range items {
		if item.Kind == ItemTool {
			t.Fatal("no-entry created physical timeline", item)
		}
	}
}

// A native control commits Run and budget together. A Resume racing the
// successful control ACK cannot acquire a fresh effect.
func TestHarnessElectedCancellationSurvivesNativeRunAdmissionDenial(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "control_return", true: "control_ack_lost"}[lost], func(t *testing.T) {
			f := newNativeCancelFixture(t, false)
			f.ks.controlACK = lost
			f.ks.afterControl = func() {
				out, e := f.native.Resume(t.Context(), f.runID, f.before.Run.Revision+1)
				if !errors.Is(e, agent.ErrRunTerminal) || out.Run.Status != kernel.RunStatusCancelled || f.physical != 0 || f.model.calls != 1 {
					t.Fatal("native control allowed successor", e, out.Run)
				}
				l, e := f.rt.BudgetStore().LoadBudget(t.Context(), f.turnID)
				if e != nil || !l.RunCancelled(f.runID) || !l.FinishedRuns[f.runID] {
					t.Fatal("terminal control split ledger", e)
				}
			}
			out, e := f.cancel(t)
			if lost {
				if !errors.Is(e, errOriginalBudgetCancelACK) {
					t.Fatal("control ACK fault not reached", e)
				}
				out, e = f.cancel(t)
			}
			if e != nil || out.Turn.Status != TurnCancelled || f.ks.controlHits != 1 {
				t.Fatal("original native control failed", e, out.Turn.Status, f.ks.controlHits)
			}
			f.verifyCancelled(t)
		})
	}
}

func TestHarnessElectedCancellationAtActualToolDispatch(t *testing.T) {
	for _, order := range []string{"none", "inner", "closure-cas"} {
		t.Run(order, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, order, "") })
	}
}
func TestHarnessElectedCancellationRetirementWindows(t *testing.T) {
	for _, cut := range []string{"recovery-before", "recovery-ack", "finish-before", "finish-ack"} {
		t.Run("receipt-only/"+cut, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, "closure-cas", cut) })
		if cut == "finish-before" || cut == "finish-ack" {
			t.Run("original-return/"+cut, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, "none", cut) })
		}
	}
	t.Run("control-ack-after-receipt", func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, "closure-cas", "control-ack") })
}

func testHarnessCancelAtActualToolDispatch(t *testing.T, order, cut string) {
	f := newNativeCancelFixture(t, order == "inner")
	ctx := t.Context()
	if _, e := (budget.Coordinator{Store: f.rt.BudgetStore()}).Release(ctx, f.turnID, "peer-slot"); e != nil {
		t.Fatal(e)
	}
	callCtx, stop := context.WithCancel(ctx)
	defer stop()
	var firstCancel error
	f.ks.action = func() {
		// The original dispatch intent already committed. Native control must
		// reject the active Grant; the actual callback then proves non-entry.
		_, firstCancel = f.cancel(t)
		stop()
	}
	if order == "closure-cas" {
		f.ks.closureCuts = 1
	} else {
		f.ks.retirementCut = cut
	}
	got, obs, e := f.native.ObservePhysicalCall(callCtx, f.runID, plugin.RunResume, func(c context.Context) (kernel.Snapshot, error) {
		return f.native.Resume(c, f.runID, f.before.Run.Revision)
	})
	if f.ks.hits != 1 || !errors.Is(firstCancel, kernel.ErrChildExecutionBusy) || !errors.Is(e, context.Canceled) || f.physical != 0 || f.model.calls != 1 {
		t.Fatal("original dispatch/cancel cut not reached", firstCancel, e, obs.Kind(), f.ks.hits, f.physical, f.model.calls)
	}
	current, readErr := f.rt.Load(ctx, f.runID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	grant, grantErr := f.rt.LookupChildExecution(ctx, f.runID)
	var originalGrant kernel.ChildExecutionGrant
	if order == "closure-cas" {
		if f.ks.cuts != 1 || grantErr != nil {
			t.Fatal("no-send closure cut missing", f.ks.cuts, grantErr, e)
		}
		completion, er := f.rt.LookupPhysicalCallbackReturn(ctx, grant)
		b, found, er2 := completion.ToolReturns.Find("held-call")
		if er != nil || er2 != nil || !completion.HostCompleted || !found || !b.Unsent {
			t.Fatal("original no-entry return not sealed", completion, er, er2)
		}
		l, er := f.rt.BudgetStore().LoadBudget(ctx, f.turnID)
		if er != nil || !l.Reservations["tool:"+f.runID+":held-call"].Dispatched || l.View(f.runID).Usage.ToolCalls != 0 {
			t.Fatal("failed no-send CAS released original hold", er, l)
		}
		originalGrant = grant
		if cut != "control-ack" {
			f.ks.retirementCut = cut
		}
		if cut != "" && cut != "control-ack" {
			_, er = f.native.ReconcileRootDeferredChildren(ctx, current.Run.ID, current.Run.Revision)
			if !errors.Is(er, errOriginalRetirementCut) || f.ks.retirementHits != 1 {
				t.Fatal("receipt recovery cut missing", er, f.ks.retirementHits)
			}
		}
	} else if cut != "" {
		if f.ks.retirementHits != 1 || !errors.Is(e, errOriginalRetirementCut) {
			t.Fatal("original retirement cut missing", e, f.ks.retirementHits, got.Run)
		}
		originalGrant = f.ks.cutGrant
	} else if !errors.Is(grantErr, kernel.ErrNotFound) {
		t.Fatal("acknowledged no-entry retained grant", grantErr)
	}
	if cut != "" && cut != "control-ack" {
		// Drop the faulting Store wrapper and inspect the original durable facts.
		f.compose(t, f.ks.Store, order == "inner")
		current, readErr = f.rt.Load(ctx, f.runID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if cut == "recovery-before" {
			_, marked, er := agent.FrozenPendingDispatch(current)
			if er != nil || !marked {
				t.Fatal("failed receipt recovery cleared original intent", er)
			}
		}
	}
	if cut == "control-ack" {
		current, readErr = f.rt.Load(ctx, f.runID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, er := f.native.ReconcileRootDeferredChildren(ctx, current.Run.ID, current.Run.Revision); er != nil {
			t.Fatal("original no-send recovery", er)
		}
		f.ks.controlACK = true
	}
	out, err := f.cancel(t)
	if cut == "control-ack" {
		if !errors.Is(err, errOriginalBudgetCancelACK) {
			t.Fatal("terminal native control ACK cut missing", err)
		}
		out, err = f.cancel(t)
	}
	if err != nil || out.Turn.Status != TurnCancelled {
		t.Fatal("original cancellation failed", err, out.Turn.Status)
	}
	f.verifyCancelled(t)
	if originalGrant.ID != "" {
		retired, er := f.rt.LoadOwnedRetirement(ctx, originalGrant.ID)
		if er != nil || !retired.Valid() || retired.Evidence.Grant != originalGrant {
			t.Fatal("original retirement identity lost", er, retired)
		}
	}
}
