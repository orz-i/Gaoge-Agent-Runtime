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

// Model one exact interleaving: after the elected Harness budget cancellation
// CAS is durable but before its ACK, a native continuation attempts Resume.
type nativeAdmissionCancelStore struct {
	*MemoryStore
	afterCancel   func()
	cancelACKCut  bool
	cancelACKHits int
}

func (s *nativeAdmissionCancelStore) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	if err := s.MemoryStore.SaveBudget(ctx, l, rev); err != nil {
		return err
	}
	if l.Cancelled && s.cancelACKCut {
		s.cancelACKCut = false
		s.cancelACKHits++
		return errOriginalBudgetCancelACK
	}
	if l.Cancelled && s.afterCancel != nil {
		action := s.afterCancel
		s.afterCancel = nil
		action()
	}
	return nil
}

type nativeAdmissionCancelModel struct {
	store *nativeAdmissionCancelStore
	calls int
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
	c := budget.Coordinator{Store: m.store}
	if _, err = c.RegisterRun(ctx, inv.TurnID, "peer-run", budget.RunBudget{}); err != nil {
		return model.Response{}, err
	}
	if _, err = c.Reserve(ctx, inv.TurnID, budget.Reservation{ID: "peer-slot", RunID: "peer-run", RequestHash: kernel.ManagedDigest("peer"), Requested: budget.Usage{ToolCalls: 1}}, false); err != nil {
		return model.Response{}, err
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "held-call", ToolKey: "local.read", Arguments: json.RawMessage("{}")}}}, nil
}

func TestHarnessElectedCancellationSurvivesNativeRunAdmissionDenial(t *testing.T) {
	ctx := t.Context()
	store := &nativeAdmissionCancelStore{MemoryStore: NewMemoryStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := shared.Adapter()
	if err != nil {
		t.Fatal(err)
	}
	m := &nativeAdmissionCancelModel{store: store}
	physical := 0
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical++
		return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	native, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b})
	if err != nil {
		t.Fatal(err)
	}
	host, err := NewRunner(Dependencies{Runtime: rt, Agent: native, Store: store, Clock: hostedBudgetClock{}, Budget: shared, Catalog: reg})
	if err != nil {
		t.Fatal(err)
	}
	req := StartRequest{HostThread: HostRef{Kind: "conversation", ID: "native-admission-thread"}, HostTurn: HostRef{Kind: "conversation_turn", ID: "native-admission-turn"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "native-admission-thread"}, Goal: "read", Config: ConfigSnapshot{AgentSpec: AgentSpec{Model: "fixture", ToolKeys: []string{"local.read"}}, ToolPolicies: []ToolPolicySnapshot{{Key: "local.read", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never"}}, SharedBudget: &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}}
	started, err := host.StartAgentTurn(ctx, req)
	if err != nil || started.Turn.Status != TurnRunning || m.calls != 1 || physical != 0 {
		t.Fatal("capacity wait setup", err, started.Turn.Status, m.calls, physical)
	}
	inv, ok := TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing root invocation")
	}
	before, err := rt.Load(ctx, inv.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := "tool:" + inv.ExecutionRefID + ":held-call"
	if ledger.Reservations[id].Status != budget.ReservationWaiting {
		t.Fatal("missing original capacity wait", ledger.Reservations[id])
	}
	interleavings := 0
	store.afterCancel = func() {
		interleavings++
		out, obs, err := native.ObservePhysicalCall(ctx, inv.ExecutionRefID, plugin.RunResume, func(c context.Context) (kernel.Snapshot, error) {
			return native.Resume(c, inv.ExecutionRefID, before.Run.Revision)
		})
		if !errors.Is(err, budget.ErrExhausted) || errors.Is(err, agent.ErrRootOutcomeUnknown) || out.Run.Revision != before.Run.Revision || obs.Kind() != agent.PhysicalCallSettled {
			t.Fatal("native cancelled admission not qualified", err, out.Run, obs.Kind())
		}
		if _, err = rt.LookupChildExecution(ctx, inv.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("native refusal retained grant", err)
		}
	}
	stopped, err := host.Cancel(ctx, started.Turn.ID, "finish elected cancellation")
	if err != nil || stopped.Turn.Status != TurnCancelled || interleavings != 1 || physical != 0 || m.calls != 1 {
		t.Fatal("original elected cancellation failed", err, stopped.Turn.Status, interleavings, physical, m.calls)
	}
	final, err := rt.Load(ctx, inv.ExecutionRefID)
	if err != nil || final.Run.Status != kernel.RunStatusCancelled {
		t.Fatal("Kernel did not commit original cancel", err, final.Run.Status)
	}
	ledger, err = store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := ledger.Reservations[id]
	if r.Status != budget.ReservationReleased || r.Dispatched || r.Active || r.Usage != (budget.Usage{}) {
		t.Fatal("unsent cancellation changed accounting", r)
	}
	items, err := store.ListItems(ctx, started.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if ActiveCancellationFence(items) {
		t.Fatal("original cancellation election retained fence")
	}
}

type nativeDispatchCancelKernelStore struct {
	*memory.Store
	action         func()
	pending        bool
	hits           int
	closureCuts    int
	cuts           int
	retirementCut  string
	retirementHits int
	interrupted    bool
	cutGrant       kernel.ChildExecutionGrant
}

func (s *nativeDispatchCancelKernelStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, event := range m.Events {
		if event.Type == "tool.admission_unsent" && s.closureCuts > 0 {
			s.closureCuts--
			s.cuts++
			return kernel.Snapshot{}, errors.New("native unsent closure cut")
		}
	}
	next, err := s.Store.ApplyExecution(ctx, l, rev, m)
	if err == nil && s.action != nil {
		for _, e := range m.Events {
			if e.Type == "tool.dispatch_intent" {
				s.pending = true
			}
		}
	}
	return next, err
}

var errOriginalRetirementCut = errors.New("original native receipt/retirement interrupted")
var errOriginalBudgetCancelACK = errors.New("elected budget cancellation ACK interrupted")

func (s *nativeDispatchCancelKernelStore) interrupt(g kernel.ChildExecutionGrant) error {
	s.retirementHits++
	s.interrupted = true
	s.cutGrant = g
	return errOriginalRetirementCut
}
func (s *nativeDispatchCancelKernelStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	if s.interrupted {
		return kernel.Snapshot{}, errOriginalRetirementCut
	}
	if s.retirementCut == "recovery-before" {
		return kernel.Snapshot{}, s.interrupt(g)
	}
	next, err := s.Store.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
	if err == nil && s.retirementCut == "recovery-ack" {
		return kernel.Snapshot{}, s.interrupt(g)
	}
	return next, err
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

func TestHarnessElectedCancellationAtActualToolDispatch(t *testing.T) {
	for _, order := range []string{"none", "inner", "closure-cas"} {
		t.Run(order, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, order, "") })
	}
}
func TestHarnessElectedCancellationRetirementWindows(t *testing.T) {
	t.Run("elected-without-busy-witness/finish-ack", func(t *testing.T) {
		testHarnessCancelAtActualToolDispatch(t, "closure-cas", "finish-ack/no-busy")
	})
	for _, cut := range []string{"recovery-before", "recovery-ack", "finish-before", "finish-ack"} {
		t.Run("receipt-only/"+cut, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, "closure-cas", cut) })
		if cut != "recovery-before" && cut != "recovery-ack" {
			t.Run("terminal/"+cut, func(t *testing.T) { testHarnessCancelAtActualToolDispatch(t, "none", cut) })
		}
	}
}
func testHarnessCancelAtActualToolDispatch(t *testing.T, timelineOrder, retirementCut string) {
	noBusyWitness := retirementCut == "finish-ack/no-busy"
	if noBusyWitness {
		retirementCut = "finish-ack"
	}
	ctx := t.Context()
	store := &nativeAdmissionCancelStore{MemoryStore: NewMemoryStore()}
	ks := &nativeDispatchCancelKernelStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: ks})
	if err != nil {
		t.Fatal(err)
	}
	m := &nativeAdmissionCancelModel{store: store}
	physical := 0
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "local.read", Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, q tools.ExecutionRequest) (tools.ExecutionResult, error) {
		physical++
		return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: q.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	compose := func(rt *kernel.Runtime) (*agent.Runner, *Runner) {
		t.Helper()
		shared, err := NewBudgetAuthority(BudgetAuthorityDependencies{Store: store, Clock: hostedBudgetClock{}})
		if err != nil {
			t.Fatal(err)
		}
		b, err := shared.Adapter()
		if err != nil {
			t.Fatal(err)
		}
		var timeline *agent.BoundedToolTimelineAdapter
		if timelineOrder != "none" && timelineOrder != "closure-cas" {
			var e error
			timeline, e = NewBoundedToolTimelineAdapter(store, hostedBudgetClock{}, nil)
			if e != nil {
				t.Fatal(e)
			}
		}
		d := agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, BoundedBudget: b, ToolTimeline: timeline}
		native, err := agent.NewRunner(d)
		if err != nil {
			t.Fatal(err)
		}
		host, err := NewRunner(Dependencies{Runtime: rt, Agent: native, Store: store, Clock: hostedBudgetClock{}, Budget: shared, Catalog: reg})
		if err != nil {
			t.Fatal(err)
		}
		return native, host
	}
	native, host := compose(rt)
	req := StartRequest{HostThread: HostRef{Kind: "conversation", ID: "native-admission-thread"}, HostTurn: HostRef{Kind: "conversation_turn", ID: "native-admission-turn"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "native-admission-thread"}, Goal: "read", Config: ConfigSnapshot{AgentSpec: AgentSpec{Model: "fixture", ToolKeys: []string{"local.read"}}, ToolPolicies: []ToolPolicySnapshot{{Key: "local.read", DefinitionVersion: "v1", ExecutionMode: "local_dispatch", ApprovalMode: "never"}}, SharedBudget: &budget.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}}
	started, err := host.StartAgentTurn(ctx, req)
	if err != nil || started.Turn.Status != TurnRunning || m.calls != 1 || physical != 0 {
		t.Fatal("capacity wait setup", err, started.Turn.Status, m.calls, physical)
	}
	inv, ok := TopLevelInvocation(started)
	if !ok {
		t.Fatal("missing root invocation")
	}
	before, err := rt.Load(ctx, inv.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	id := "tool:" + inv.ExecutionRefID + ":held-call"
	if ledger.Reservations[id].Status != budget.ReservationWaiting {
		t.Fatal("missing original capacity wait", ledger.Reservations[id])
	}

	// Release peer capacity, then make the elected cancellation race AFTER
	// the last Ledger read and successful native Store check, before actual
	// Budget Dispatch. No extra guard read can repair this interleaving.
	c := budget.Coordinator{Store: store}
	if _, err = c.Release(ctx, started.Turn.ID, "peer-slot"); err != nil {
		t.Fatal(err)
	}
	var initialCancelErr error
	if timelineOrder == "closure-cas" {
		ks.closureCuts = 2
	} else if retirementCut != "" {
		ks.retirementCut = retirementCut
	}
	store.cancelACKCut = noBusyWitness
	ks.action = func() {
		_, initialCancelErr = host.Cancel(ctx, started.Turn.ID, "cancel at actual native dispatch")
	}
	got, obs, resumeErr := native.ObservePhysicalCall(ctx, inv.ExecutionRefID, plugin.RunResume, func(c context.Context) (kernel.Snapshot, error) {
		return native.Resume(c, inv.ExecutionRefID, before.Run.Revision)
	})
	wantCancelErr := kernel.ErrChildExecutionBusy
	if noBusyWitness {
		wantCancelErr = errOriginalBudgetCancelACK
		if store.cancelACKHits != 1 {
			t.Fatal("budget cancel ACK cut not reached")
		}
	}
	if ks.hits != 1 || !errors.Is(initialCancelErr, wantCancelErr) {
		t.Fatal("original elected cancellation cut missing", ks.hits, initialCancelErr)
	}
	if noBusyWitness {
		items, err := store.ListItems(ctx, started.Turn.ID, 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.ParentItemID == DeferredChildGrantCancellationParentItemID {
				t.Fatal("unexpected busy witness", item)
			}
		}
		if !ActiveCancellationFence(items) {
			t.Fatal("election lost before Kernel control")
		}
	}
	if !errors.Is(resumeErr, budget.ErrExhausted) || physical != 0 || m.calls != 1 {
		t.Fatal("actual native dispatch denial was not returned", resumeErr, got.Run, obs.Kind(), physical, m.calls)
	}
	if timelineOrder == "closure-cas" {
		if ks.cuts != 2 || !errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || obs.Kind() != agent.PhysicalCallDurableYield {
			t.Fatal("original native no-send return missing at cleanup cut", resumeErr, obs.Kind(), ks.cuts)
		}
		g, e := rt.LookupChildExecution(ctx, inv.ExecutionRefID)
		if e != nil {
			t.Fatal(e)
		}
		c, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
		b, found, bErr := c.ToolReturns.Find("held-call")
		if e != nil || bErr != nil || !c.HostCompleted || !found || !b.Unsent {
			t.Fatal("original no-send return not sealed", c, e, b, bErr)
		}
	} else if retirementCut != "" {
		if ks.retirementHits != 1 || !errors.Is(resumeErr, errOriginalRetirementCut) || !errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || got.Run.Status != kernel.RunStatusFailed {
			t.Fatal("terminal original retirement cut not reached", resumeErr, got.Run, ks.retirementHits)
		}
	} else {
		if errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || obs.Kind() != agent.PhysicalCallSettled {
			t.Fatal("actual native dispatch denial did not close its original grant", resumeErr, got.Run, obs.Kind())
		}
		if _, err = rt.LookupChildExecution(ctx, inv.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatal("actual native dispatch denial retained grant", err)
		}
		if _, marked, err := agent.FrozenPendingDispatch(got); err != nil || marked {
			t.Fatal("native denial retained marker", err)
		}
	}
	var originalRetired kernel.OwnedRetirementReceipt
	var originalGrant kernel.ChildExecutionGrant
	if retirementCut != "" {
		if timelineOrder == "closure-cas" {
			ks.retirementCut = retirementCut
			var firstErr error
			if noBusyWitness {
				// The independent receipt-only owner finishes after the
				// election's budget ACK failure, without a Kernel busy witness.
				current, e := rt.Load(ctx, inv.ExecutionRefID)
				if e != nil {
					t.Fatal(e)
				}
				_, firstErr = native.ReconcileRootDeferredChildren(ctx, current.Run.ID, current.Run.Revision)
			} else {
				_, firstErr = host.Cancel(ctx, started.Turn.ID, "recover original receipt/retirement")
			}
			if !errors.Is(firstErr, errOriginalRetirementCut) || ks.retirementHits != 1 {
				t.Fatal("original receipt/retirement cut not reached", firstErr, ks.retirementHits)
			}
		}
		originalGrant = ks.cutGrant
		if originalGrant.ID == "" {
			t.Fatal("original grant missing")
		}
		// Rebuild from the underlying Store, not the interrupted callback
		// or its Runner. Inspect exact durable state at the failed boundary.
		rt, err = kernel.New(kernel.Dependencies{Store: ks.Store})
		if err != nil {
			t.Fatal(err)
		}
		native, host = compose(rt)
		current, e := rt.Load(ctx, inv.ExecutionRefID)
		if e != nil {
			t.Fatal(e)
		}
		_, marked, e := agent.FrozenPendingDispatch(current)
		if e != nil || marked != (retirementCut == "recovery-before") {
			t.Fatal("unexpected receipt consumption", retirementCut, marked, e)
		}
		originalRetired, e = rt.LoadOwnedRetirement(ctx, originalGrant.ID)
		if retirementCut == "finish-ack" {
			if e != nil {
				t.Fatal(e)
			}
			if !originalRetired.Valid() {
				t.Fatal("invalid committed retirement fact", originalRetired)
			}
			if _, e = rt.LookupChildExecution(ctx, inv.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("Finish retained original grant", e)
			}
		} else {
			if !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("premature retirement", e)
			}
			g, e := rt.LookupChildExecution(ctx, inv.ExecutionRefID)
			if e != nil || g != originalGrant {
				t.Fatal("original grant changed", e, g)
			}
			completion, e := rt.LookupOwnedPhysicalCallbackReturn(ctx, g)
			if e != nil || !completion.HostCompleted {
				t.Fatal("original callback evidence lost", e, completion)
			}
		}
	}
	if noBusyWitness {
		current, e := rt.Load(ctx, inv.ExecutionRefID)
		if e != nil {
			t.Fatal(e)
		}
		turn, e := store.GetTurn(ctx, started.Turn.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !host.nativeRootCancellationReady(ctx, turn, inv.ExecutionRefID, current) {
			t.Fatal("exact retired original did not qualify same election before Kernel busy")
		}
	}
	stopped, err := host.Cancel(ctx, started.Turn.ID, "finish original elected cancellation")
	if err != nil || !terminalTurnStatus(stopped.Turn.Status) {
		t.Fatal("original cancellation could not finish", err, stopped.Turn.Status)
	}
	final, err := rt.Load(ctx, inv.ExecutionRefID)
	if timelineOrder == "closure-cas" {
		if err != nil || final.Run.Status != kernel.RunStatusCancelled {
			t.Fatal("elected cancellation could not consume original no-send return", err, final.Run)
		}
	} else if err != nil || final.Run.Status != kernel.RunStatusFailed || final.Run.ErrorCode != "agent.shared_budget_exhausted" {
		t.Fatal("original terminal denial was not preserved", err, final.Run)
	}
	if _, err = rt.LookupChildExecution(ctx, inv.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("elected cancellation retained original grant", err)
	}
	if retirementCut != "" {
		receipt, e := rt.LoadOwnedRetirement(ctx, originalGrant.ID)
		if e != nil || !receipt.Valid() || receipt.Evidence.Grant != originalGrant || (originalRetired.Version != 0 && receipt.Identity() != originalRetired.Identity()) {
			t.Fatal("original retirement was not acknowledged exactly", e, receipt)
		}
		currentReceipt, e := rt.LookupOwnedRetirementForRun(ctx, inv.ExecutionRefID)
		if kernel.ManagedDigest(final) == kernel.ManagedDigest(receipt.Evidence.Snapshot) {
			if e != nil || kernel.ManagedDigest(currentReceipt) != kernel.ManagedDigest(receipt) {
				t.Fatal("original final receipt changed", e)
			}
		} else if !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("historical receipt selected for changed source", e)
		}

	}
	ledger, err = store.LoadBudget(ctx, started.Turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := ledger.Reservations[id]
	if !ledger.Cancelled || r.Status != budget.ReservationReleased || r.Dispatched || r.Active || r.Usage != (budget.Usage{}) || len(r.Receipt) != 0 {
		t.Fatal("cancelled reservation acquired effects", r)
	}
	items, err := store.ListItems(ctx, started.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if ActiveCancellationFence(items) {
		t.Fatal("elected cancellation retained fence")
	}
	startedItems, failedItems := 0, 0
	for _, item := range items {
		if item.Kind != ItemTool {
			continue
		}
		var p toolTimelinePayload
		if err = json.Unmarshal(item.Payload, &p); err != nil || p.ExecutionID != "" || p.Disposition != "" {
			t.Fatal("invented physical Tool result", item, err)
		}
		switch item.Status {
		case ItemStarted:
			startedItems++
		case ItemFailed:
			failedItems++
		case ItemWaiting, ItemCompleted, ItemCancelled:
			t.Fatal("incorrect Tool timeline", item)
		default:
			t.Fatal("unknown Tool item status", item)
		}
	}
	if startedItems != 0 || failedItems != 0 {
		t.Fatal("capacity denial entered the Tool timeline", startedItems, failedItems)
	}
}
