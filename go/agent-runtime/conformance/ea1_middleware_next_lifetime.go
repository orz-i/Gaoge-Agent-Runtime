package conformance

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1MiddlewareNextLifetime drives actual Agent StartRun, never bare
// plugin.Invoke as a substitute for Store authorization. Async completion is
// ordered by explicit entered/returned/release/done channels.
func RunEA1MiddlewareNextLifetime(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, child := range []bool{false, true} {
		route := map[bool]string{false: "root", true: "child"}[child]
		t.Run(route, func(t *testing.T) {
			for _, mode := range []string{"create-denied", "stale-admission", "duplicate", "foreign-model"} {
				t.Run("P0/"+mode, func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P0", mode, "model") })
			}
			if child {
				t.Run("P0/preflight", func(t *testing.T) { ea1MWExecute(t, factory, true, false, "P0", "preflight", "run") })
			}
			for _, chain := range []string{"run", "model", "tool"} {
				t.Run("P1/"+chain, func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P1", "double", chain) })
				for _, mode := range []string{"async", "unknown", "panic"} {
					t.Run("P2/"+chain+"/"+mode, func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P2", mode, chain) })
				}
				t.Run("P5/"+chain+"/async-election", func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P5", "async", chain) })
			}
			t.Run("P2/model/swallowed-inner", func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P2", "swallow", "model") })
			t.Run("P3/durable-before-whole-return", func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P3", "receipt", "run") })
			t.Run("P4/mark-ack-lost", func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P4", "ack", "run") })
			t.Run("P5/deadline-before-new-send", func(t *testing.T) { ea1MWExecute(t, factory, child, false, "P5", "deadline", "run") })
			t.Run("P1/default-owner", func(t *testing.T) { ea1MWExecute(t, factory, child, true, "P1", "double", "run") })
		})
	}
}

type ea1MWStore struct {
	beforeOwned func(kernel.ChildExecutionGrant)
	*ea1ChildStartStore
	denyCreate, ack                     bool
	failure                             error
	markCalls, markCommits, finishCalls int
}

func (s *ea1MWStore) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	if s.denyCreate {
		return kernel.Snapshot{}, s.failure
	}
	return s.ea1ChildStartStore.Create(ctx, r, events)
}
func (s *ea1MWStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, a kernel.OwnedPhysicalAdmission) error {
	if s.beforeOwned != nil {
		s.beforeOwned(g)
	}
	return s.OwnedPhysicalExecutionStore.BeginOwnedPhysicalExecution(ctx, g, l, a)
}
func (s *ea1MWStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.markCalls++
	if e := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p); e != nil {
		return e
	}
	s.markCommits++
	if s.ack {
		return s.failure
	}
	return nil
}
func (s *ea1MWStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	s.finishCalls++
	if s.ack {
		return s.failure
	}
	return s.ChildExecutionStore.FinishChildExecution(ctx, g)
}

type ea1MWFixture struct {
	*ea1ChildStartFixture
	storeMW                                    *ea1MWStore
	child, automatic                           bool
	phase, mode, chain                         string
	layer                                      *ea1MWLayer
	failure                                    error
	entered, returned, release, downstreamDone chan struct{}
	providerDone                               atomic.Bool
	cuts                                       int
}
type ea1MWLayer struct {
	f                         *ea1MWFixture
	run                       plugin.RunNext
	model                     plugin.ModelNext
	tool                      plugin.ToolNext
	runCtx, modelCtx, toolCtx context.Context
	request                   model.Request
	emit                      model.StreamSink
	calls                     [3]int
}

func (*ea1MWLayer) Name() string { return "ea1.original.middleware.lifetime" }
func (m *ea1MWLayer) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	f := m.f
	m.calls[0]++
	m.run = next
	m.runCtx = ctx
	if i.Operation != plugin.RunStart || i.RunID != f.request.ID || i.ParentRunID != f.request.ParentRunID {
		f.t.Fatal("Run middleware original invocation changed")
	}
	if f.chain == "run" {
		f.before("run")
	}
	if f.mode == "async" && f.chain == "run" {
		go func() { defer close(f.downstreamDone); _, _ = next(ctx) }()
		<-f.entered
		close(f.returned)
		return kernel.Snapshot{}, nil
	}
	out, e := next(ctx)
	if f.phase == "P3" || f.phase == "P4" {
		f.cuts++
		s := f.load(f.peer)
		g := f.scope(f.peer)
		f.receipts(s, true)
		if f.storeMW.markCalls != 0 {
			f.t.Fatal("original scope marked before whole Run callback returned")
		}
		f.fenceMW(s, g)
		m.rejected("all")
	}
	if f.chain == "run" {
		if f.mode == "double" {
			if _, err := next(ctx); !errors.Is(err, plugin.ErrNextAlreadyCalled) {
				f.t.Fatalf("double RunNext: %v", err)
			}
		}
		if f.mode == "unknown" {
			return out, errors.Join(e, f.failure)
		}
		if f.mode == "panic" {
			panic(f.failure)
		}
	}
	return out, e
}
func (m *ea1MWLayer) Model(ctx context.Context, r model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	f := m.f
	m.calls[1]++
	m.model = next
	m.modelCtx = ctx
	m.request = model.CloneRequest(r)
	m.emit = emit
	if f.chain == "model" {
		f.before("model")
	}
	if f.mode == "foreign-model" {
		before, g := f.load(f.peer), f.scope(f.peer)
		candidate := model.CloneRequest(r)
		candidate.RunID = "foreign-run"
		out, e := next(ctx, candidate, emit)
		f.unchanged(f.peer, before, g)
		if !errors.Is(e, agent.ErrInvalidRequest) {
			f.t.Fatalf("foreign candidate exact downstream rejection: %v", e)
		}
		return out, e
	}
	if f.mode == "async" && f.chain == "model" {
		go func() { defer close(f.downstreamDone); _, _ = next(ctx, r, emit) }()
		<-f.entered
		p := f.physical.ea1StartSettlementPhysical
		p.mu.Lock()
		out := model.CloneResponse(p.responses[0])
		p.mu.Unlock()
		close(f.returned)
		return out, nil
	}
	out, e := next(ctx, r, emit)
	if f.chain == "model" {
		if f.mode == "double" {
			if _, err := next(ctx, r, emit); !errors.Is(err, plugin.ErrNextAlreadyCalled) {
				f.t.Fatalf("double ModelNext: %v", err)
			}
		}
		if f.mode == "unknown" {
			return out, errors.Join(e, f.failure)
		}
		if f.mode == "panic" {
			panic(f.failure)
		}
		if f.mode == "swallow" {
			return out, nil
		}
	}
	return out, e
}
func (m *ea1MWLayer) Tool(ctx context.Context, i plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	f := m.f
	m.calls[2]++
	m.tool = next
	m.toolCtx = ctx
	if i.Run.ID != f.request.ID || i.Request.RunID != f.request.ID {
		f.t.Fatal("Tool middleware foreign original identity")
	}
	if f.chain == "tool" {
		f.before("tool")
	}
	if f.mode == "async" && f.chain == "tool" {
		go func() { defer close(f.downstreamDone); _, _ = next(ctx) }()
		<-f.entered
		p := f.physical.ea1StartSettlementPhysical
		p.mu.Lock()
		out := tools.CloneExecutionResult(p.receipts[0])
		p.mu.Unlock()
		close(f.returned)
		return out, nil
	}
	out, e := next(ctx)
	if f.chain == "tool" {
		if f.mode == "double" {
			if _, err := next(ctx); !errors.Is(err, plugin.ErrNextAlreadyCalled) {
				f.t.Fatalf("double ToolNext: %v", err)
			}
		}
		if f.mode == "unknown" {
			return out, errors.Join(e, f.failure)
		}
		if f.mode == "panic" {
			panic(f.failure)
		}
	}
	return out, e
}

type ea1MWInnerUnknown struct{ f *ea1MWFixture }

func (e ea1MWInnerUnknown) Name() string { return "ea1.inner.unknown" }
func (e ea1MWInnerUnknown) Model(ctx context.Context, r model.Request, s model.StreamSink, n plugin.ModelNext) (model.Response, error) {
	out, err := n(context.Background(), r, s)
	return out, errors.Join(err, e.f.failure)
}

// These are the actual saved closures and original contexts, including after
// reopen. Never call a new Runner's Next or replace its original Run binding.
func (m *ea1MWLayer) rejected(which string) {
	t := m.f.t
	t.Helper()
	if m.run != nil && (which == "run" || which == "all") {
		if _, e := m.run(m.runCtx); !errors.Is(e, plugin.ErrNextAlreadyCalled) {
			t.Fatalf("retained RunNext: %v", e)
		}
	}
	if m.model != nil && (which == "model" || which == "all") {
		if _, e := m.model(m.modelCtx, model.CloneRequest(m.request), m.emit); !errors.Is(e, plugin.ErrNextAlreadyCalled) {
			t.Fatalf("retained ModelNext: %v", e)
		}
	}
	if m.tool != nil && (which == "tool" || which == "all") {
		if _, e := m.tool(m.toolCtx); !errors.Is(e, plugin.ErrNextAlreadyCalled) {
			t.Fatalf("retained ToolNext: %v", e)
		}
	}
}
func ea1MWNew(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), child, automatic bool, phase, mode, chain string) *ea1MWFixture {
	base := ea1NewChildStart(t, factory, true, mode == "deadline")
	f := &ea1MWFixture{ea1ChildStartFixture: base, child: child, automatic: automatic, phase: phase, mode: mode, chain: chain, failure: errors.New("original MW callback fault"), entered: make(chan struct{}), returned: make(chan struct{}), release: make(chan struct{}), downstreamDone: make(chan struct{})}
	if !child {
		f.request.ParentRunID = ""
		f.request.OriginFingerprint = ""
		f.ancestors = nil
	}
	f.storeMW = &ea1MWStore{ea1ChildStartStore: f.store, failure: f.failure, ack: phase == "P4"}
	f.rt = ea1StartRuntime(t, f.storeMW)
	f.physical.rt = f.rt
	f.layer = &ea1MWLayer{f: f}
	if mode == "async" {
		t.Cleanup(func() {
			select {
			case <-f.release:
			default:
				close(f.release)
			}
		})
		hook := func() { close(f.entered); <-f.release; f.providerDone.Store(true) }
		if chain == "tool" {
			f.physical.toolHook = hook
		} else {
			f.physical.modelHook = hook
		}
	}
	return f
}
func (f *ea1MWFixture) runnerMW(rt *kernel.Runtime) *agent.Runner {
	d := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, RunMiddleware: []plugin.RunMiddleware{f.layer}, ModelMiddleware: []plugin.ModelMiddleware{f.layer}, ToolMiddleware: []plugin.ToolMiddleware{f.layer}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	leaseWindow := 2 * time.Second
	if f.mode == "async" {
		d.Execution.Model.Timeout = 5 * time.Second
		d.Execution.Tool.Timeout = 5 * time.Second
		leaseWindow = 10 * time.Second
	}
	if !f.automatic {
		d.RunExecution = &agent.RunExecution{OwnerID: "original-mw-owner", LeaseDuration: leaseWindow}
	}
	if f.mode == "swallow" {
		d.ModelMiddleware = append(d.ModelMiddleware, ea1MWInnerUnknown{f})
	}
	if f.child {
		d.StartControlPreflight = func(ctx context.Context, r agent.StartRequest) error {
			if f.mode == "preflight" {
				return f.failure
			}
			if !reflect.DeepEqual(r, f.request) {
				return agent.ErrInvalidRequest
			}
			for _, a := range f.ancestors {
				s, e := rt.Load(ctx, a.Run.ID)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(s, a) {
					return agent.ErrInvalidRequest
				}
			}
			return nil
		}
	}
	r, e := agent.NewRunner(d)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1MWFixture) scope(rt *kernel.Runtime) kernel.ChildExecutionGrant {
	g, e := rt.LookupChildExecution(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	if g.ChildRunID != f.request.ID || g.ExpectedRevision != f.created.Run.Revision || g.IsOwned() == f.child {
		f.t.Fatal("wrong original MW admission/source")
	}
	if !f.child && (g.AdmissionOperation != "start" || g.ExecutionLeaseID == "" || g.ExecutionEpoch == 0 || len(g.AdmissionSourceHash) != 64) {
		f.t.Fatal("Root original lease/source binding missing")
	}
	return g
}
func (f *ea1MWFixture) fenceMW(s kernel.Snapshot, g kernel.ChildExecutionGrant) {
	t := f.t
	for _, a := range append(append([]kernel.Snapshot{}, f.ancestors...), s) {
		terminal := a.Run.Status == kernel.RunStatusCompleted || a.Run.Status == kernel.RunStatusFailed || a.Run.Status == kernel.RunStatusCancelled
		want := kernel.ErrChildExecutionBusy
		if terminal {
			want = kernel.ErrTerminal
		}
		if _, e := f.peer.Cancel(t.Context(), a.Run.ID, a.Run.Revision, "competing middleware cancel"); !errors.Is(e, want) {
			t.Fatalf("MW Cancel %s: %v want %v", a.Run.ID, e, want)
		}
		f.unchanged(f.peer, s, g)
		want = kernel.ErrChildExecutionBusy
		if a.Run.ID == s.Run.ID && (!f.child || !f.automatic) {
			want = kernel.ErrExecutionRequired
		}
		if terminal {
			want = kernel.ErrTerminal
		}
		if _, e := f.peer.Apply(t.Context(), a.Run.ID, a.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: a.State, Checkpoint: a.Checkpoint}); !errors.Is(e, want) {
			t.Fatalf("MW Apply %s: %v want %v", a.Run.ID, e, want)
		}
		f.unchanged(f.peer, s, g)
	}
	if !f.child && s.Run.Status == kernel.RunStatusRunning {
		l := f.store.acquired
		if e := f.peerStore.(kernel.ExecutionStore).CheckExecution(t.Context(), s.Run.ID, &l); e != nil {
			t.Fatalf("original Root epoch changed: %v", e)
		}
		if _, e := f.peer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: s.Run.ID, OwnerID: "foreign", LeaseDuration: 2 * time.Second}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Root owner takeover: %v", e)
		}
		f.unchanged(f.peer, s, g)
	}
}
func (f *ea1MWFixture) before(chain string) {
	if f.phase != "P1" && f.mode != "deadline" {
		return
	}
	f.cuts++
	s, g := f.load(f.peer), f.scope(f.peer)
	f.fenceMW(s, g)
	if f.mode == "deadline" {
		ea1ControlWaitUntil(*s.Run.DeadlineAt)
		for _, current := range append(append([]kernel.Snapshot{}, f.ancestors...), s) {
			until := time.Now().Add(time.Second)
			for {
				_, applied, e := f.peer.Expire(f.t.Context(), current.Run.ID, current.Run.Revision)
				f.unchanged(f.peer, s, g)
				if !applied && errors.Is(e, kernel.ErrChildExecutionBusy) {
					break
				}
				if applied || !errors.Is(e, kernel.ErrDeadline) || time.Now().After(until) {
					f.t.Fatalf("due middleware scope %s: %v", current.Run.ID, e)
				}
			}
		}
	}
}
func ea1MWExecute(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), child, automatic bool, phase, mode, chain string) {
	f := ea1MWNew(t, factory, child, automatic, phase, mode, chain)
	r := f.runnerMW(f.rt)
	if mode == "create-denied" {
		f.storeMW.denyCreate = true
	}
	var retained kernel.Snapshot
	if mode == "stale-admission" {
		winner := func(g kernel.ChildExecutionGrant) {
			current := f.load(f.peer)
			var e error
			retained, e = f.peer.Cancel(t.Context(), g.ChildRunID, current.Run.Revision, "pre-admission control winner")
			if e != nil {
				t.Fatal(e)
			}
		}
		if child {
			f.store.beforeAdmission = winner
		} else {
			f.storeMW.beforeOwned = winner
		}
	}
	if mode == "duplicate" {
		setup := f.ea1ChildStartFixture.runner(f.rt, "prepare", false)
		var e error
		retained, e = setup.PrepareRun(t.Context(), f.request)
		if e != nil {
			t.Fatal(e)
		}
		f.storeMW.markCalls, f.storeMW.markCommits, f.storeMW.finishCalls = 0, 0, 0
	}
	var foreign kernel.Snapshot
	if mode == "foreign-model" {
		var e error
		foreign, e = f.peer.Create(t.Context(), kernel.CreateRequest{ID: "foreign-run", Kind: "workflow", Actor: f.request.Actor, Thread: f.request.Thread, State: []byte(`{"foreign":true}`)})
		if e != nil {
			t.Fatal(e)
		}
	}
	type outcome struct {
		snapshot kernel.Snapshot
		err      error
		panicked any
		early    bool
	}
	execute := func() (out outcome) {
		defer func() { out.panicked = recover() }()
		out.snapshot, out.err = r.StartRun(t.Context(), f.request)
		if mode == "async" {
			out.early = !f.providerDone.Load()
		}
		return out
	}
	var out outcome
	if mode == "async" {
		done := make(chan outcome, 1)
		go func() { done <- execute() }()
		select {
		case <-f.returned:
		case <-time.After(5 * time.Second):
			t.Fatal("actual async Next never entered")
		}
		s, g := f.load(f.peer), f.scope(f.peer)
		f.fenceMW(s, g)
		if phase == "P5" {
			candidate := f.ea1ChildStartFixture.runner(f.peer, "competing-resume", false)
			expected := agent.ErrModelOutcomeUnknown
			if chain == "tool" {
				expected = agent.ErrToolOutcomeUnknown
			}
			if _, e := candidate.Resume(t.Context(), s.Run.ID, s.Run.Revision); !errors.Is(e, expected) {
				t.Fatalf("competing actual Resume: %v want %v", e, expected)
			}
			f.unchanged(f.peer, s, g)
		}
		if f.storeMW.markCalls != 0 {
			t.Fatal("async Next marked before physical return")
		}
		close(f.release)
		select {
		case <-f.downstreamDone:
		case <-time.After(5 * time.Second):
			t.Fatal("admitted Next never completed")
		}
		select {
		case out = <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("outer Start never joined Next")
		}
		if out.early {
			t.Fatal("outer Start returned before actual admitted Next")
		}
	} else {
		out = execute()
	}
	if phase == "P0" {
		expected := f.failure
		if mode == "stale-admission" {
			expected = kernel.ErrExecutionConflict
		}
		if mode == "duplicate" {
			expected = kernel.ErrAlreadyExists
		}
		if mode == "foreign-model" {
			expected = agent.ErrInvalidRequest
		}
		if !errors.Is(out.err, expected) {
			t.Fatalf("actual admission/candidate denial: %v want %v", out.err, expected)
		}
		if len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatal("denied original candidate dispatched")
		}
		if mode != "foreign-model" {
			if f.layer.calls != ([3]int{}) {
				t.Fatal("pre-admission failure entered middleware")
			}
			if mode == "duplicate" || mode == "stale-admission" {
				ea1CompBarrierUnchanged(t, f.peer, retained)
			}
		} else {
			ea1CompBarrierUnchanged(t, f.peer, foreign)
		}
		return
	}
	if mode == "deadline" {
		if !errors.Is(out.err, kernel.ErrDeadline) || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatalf("expired middleware sent new work: %v", out.err)
		}
		return
	}
	s := f.load(f.peer)
	if phase == "P1" || phase == "P3" || phase == "P4" {
		if f.cuts != 1 {
			t.Fatalf("original callback cutpoint count: %d", f.cuts)
		}
	}
	unknown := mode == "unknown" || mode == "panic" || mode == "swallow"
	if unknown {
		if mode == "panic" && chain == "run" {
			if out.panicked != f.failure {
				t.Fatalf("Run panic not observed: %v", out.panicked)
			}
		} else if mode != "swallow" {
			expected := f.failure
			if mode == "panic" {
				expected = agent.ErrModelPanic
				if chain == "tool" {
					expected = agent.ErrToolPanic
				}
			}
			if !errors.Is(out.err, expected) {
				t.Fatalf("actual middleware unknown: %v want %v", out.err, expected)
			}
		}
		view, e := agent.ViewState(s)
		if e != nil || len(view.ModelInvocations) != 1 {
			t.Fatal("unknown original Model source missing")
		}
		committed := view.ModelInvocations[0].Status == agent.ModelInvocationConsumed || view.ModelInvocations[0].Status == agent.ModelInvocationCompleted
		f.receipts(s, committed)
		expectedCalls := [3]int{1, 1, 1}
		if chain == "model" && mode != "swallow" {
			expectedCalls[2] = 0
		}
		if f.layer.calls != expectedCalls {
			t.Fatalf("unknown actual chain cutpoint counts: %v want %v", f.layer.calls, expectedCalls)
		}
		g := f.scope(f.peer)
		if f.storeMW.markCommits != 0 {
			t.Fatal("unknown callback retired original authority")
		}
		f.unchanged(f.peer, s, g)
		f.layer.rejected("all")
		f.unchanged(f.peer, s, g)
		if mode == "unknown" && chain == "model" && f.store.acquired.LeaseID != "" {
			ea1ControlWaitUntil(f.store.acquired.ExpiresAt.Add(150 * time.Millisecond))
		}
		fresh := f.reopen()
		rt := ea1StartRuntime(t, fresh)
		f.physical.rt = rt
		f.unchanged(rt, s, g)
		recovery := f.ea1ChildStartFixture.runner(rt, "fresh", false)
		var e error
		if child {
			e = recovery.ReconcileChildPhysicalGrant(t.Context(), s.Run.ID)
		} else {
			e = recovery.ReconcileRootPhysicalExecution(t.Context(), s.Run.ID)
		}
		want := kernel.ErrChildExecutionBusy
		if !child {
			want = agent.ErrRootOutcomeUnknown
		}
		if !errors.Is(e, want) {
			t.Fatalf("unknown callback fresh retirement: %v want %v", e, want)
		}
		f.unchanged(rt, s, g)
		f.layer.rejected("all")
		f.unchanged(rt, s, g)
		return
	}
	if out.panicked != nil {
		t.Fatalf("unexpected panic: %v", out.panicked)
	}
	if phase == "P4" {
		// Child Mark error returns before Finish. Exercise the real first-party
		// retry on the same Store to reach its held Finish, then reopen.
		if child {
			if e := r.ReconcileChildPhysicalGrant(t.Context(), s.Run.ID); !errors.Is(e, f.failure) {
				t.Fatalf("actual child Finish hold missed: %v", e)
			}
		}
		if !errors.Is(out.err, f.failure) || f.storeMW.markCommits == 0 || f.storeMW.finishCalls == 0 {
			t.Fatalf("actual Mark ACKlost/Finish cut not reached: %v", out.err)
		}
		g := f.scope(f.peer)
		f.receipts(s, true)
		f.layer.rejected("all")
		f.unchanged(f.peer, s, g)
		fresh := f.reopen()
		rt := ea1StartRuntime(t, fresh)
		f.physical.rt = rt
		f.unchanged(rt, s, g)
		recovery := f.ea1ChildStartFixture.runner(rt, "fresh", false)
		var e error
		if child {
			e = recovery.ReconcileChildPhysicalGrant(t.Context(), s.Run.ID)
		} else {
			e = recovery.ReconcileRootPhysicalExecution(t.Context(), s.Run.ID)
		}
		if e != nil {
			t.Fatalf("actual original marker recovery: %v", e)
		}
		if _, e = rt.LookupChildExecution(t.Context(), s.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("recovered marker retained: %v", e)
		}
		f.layer.rejected("all")
		ea1CompBarrierUnchanged(t, rt, s)
		f.receipts(s, true)
		return
	}
	if out.err != nil {
		t.Fatal(out.err)
	}
	f.receipts(s, true)
	if s.Run.Status != kernel.RunStatusCompleted || f.storeMW.markCommits != 1 {
		t.Fatal("actual successful whole callback did not settle once")
	}
	f.layer.rejected("all")
	ea1CompBarrierUnchanged(t, f.peer, s)
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	if _, e := f.peer.LookupChildExecution(t.Context(), s.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("completed callback retained grant")
	}
}
