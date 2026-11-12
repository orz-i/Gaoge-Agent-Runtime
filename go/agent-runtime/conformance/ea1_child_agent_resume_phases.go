package conformance

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// RunEA1ChildAgentResumePhases measures actual Resume only, after PrepareRun
// has created the original child and retired its setup grant without dispatch.
// Child grants retain their original protocol, not Root-owned lease bindings.
func RunEA1ChildAgentResumePhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, kind := range []string{"missing", "stale-revision", "terminal", "parent-wins", "stale-admission"} {
		t.Run("P0/"+kind, func(t *testing.T) { ea1ChildResumePreflight(t, factory, kind) })
	}
	t.Run("P1/host-before-next", func(t *testing.T) { ea1ChildResumePhase(t, factory, "P1", "host") })
	for _, kind := range []string{"host", "model", "tool"} {
		t.Run("P2/"+kind, func(t *testing.T) { ea1ChildResumePhase(t, factory, "P2", kind) })
	}
}

type ea1ChildResumeHost struct{ host *ea1ChildStartHost }

func (ea1ChildResumeHost) Name() string { return "ea1.actual.child.resume" }
func (h ea1ChildResumeHost) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	want := h.host.request
	if inv.Operation != plugin.RunResume || inv.RunID != want.ID || inv.ParentRunID != want.ParentRunID || inv.Actor != want.Actor || inv.Thread != want.Thread {
		return kernel.Snapshot{}, agent.ErrInvalidRequest
	}
	h.host.calls++
	if h.host.hook != nil {
		h.host.hook()
	}
	if h.host.failure != nil {
		if h.host.sendUnknown {
			h.host.sends++
		}
		return kernel.Snapshot{}, h.host.failure
	}
	return next(ctx)
}

type ea1ChildResumeFixture struct {
	*ea1ChildStartFixture
	prepared kernel.Snapshot
}

func ea1NewChildResume(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), tool bool) *ea1ChildResumeFixture {
	f := &ea1ChildResumeFixture{ea1ChildStartFixture: ea1NewChildStart(t, factory, tool, false)}
	setup := f.ea1ChildStartFixture.runner(f.rt, "prepare-only", false)
	s, e := setup.PrepareRun(t.Context(), f.request)
	if e != nil {
		t.Fatal(e)
	}
	f.prepared = s
	if f.created.Run.ID != s.Run.ID || !setup.VerifyFrozenStartRequest(f.created, f.request) || !setup.VerifyFrozenStartRequest(s, f.request) {
		t.Fatal("PrepareRun lost original child source")
	}
	if s.Run.Status != kernel.RunStatusRunning || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || f.host.calls != 0 {
		t.Fatal("PrepareRun dispatched physical work")
	}
	if _, e = f.rt.LookupChildExecution(t.Context(), s.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("setup grant retained: %v", e)
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	f.store.admissions, f.store.marks = 0, 0
	f.store.acquired = kernel.ExecutionLease{}
	return f
}
func (f *ea1ChildResumeFixture) runner(rt *kernel.Runtime, owner string, host bool) *agent.Runner {
	d := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if host {
		d.RunMiddleware = []plugin.RunMiddleware{ea1ChildResumeHost{f.host}}
	}
	r, e := agent.NewRunner(d)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ChildResumeFixture) grant(rt *kernel.Runtime) kernel.ChildExecutionGrant {
	g, e := rt.LookupChildExecution(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	if g.IsOwned() || g.ChildRunID != f.request.ID || g.ExpectedRevision != f.prepared.Run.Revision {
		f.t.Fatal("Resume grant not bound to original prepared revision")
	}
	return g
}
func ea1ChildResumePreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	f := ea1NewChildResume(t, factory, false)
	ctx := t.Context()
	id, rev := f.request.ID, f.prepared.Run.Revision
	winner := f.prepared
	expected := kernel.ErrConflict
	switch kind {
	case "missing":
		id = "missing"
		expected = kernel.ErrNotFound
	case "stale-revision":
		rev++
	case "terminal":
		s, e := f.peer.Cancel(ctx, id, rev, "before Resume")
		if e != nil {
			t.Fatal(e)
		}
		winner = s
		rev = s.Run.Revision
		expected = agent.ErrRunTerminal
	case "parent-wins":
		expected = kernel.ErrExecutionConflict
		f.store.beforeAdmission = func(kernel.ChildExecutionGrant) {
			s, e := f.peer.Cancel(ctx, f.ancestors[0].Run.ID, f.ancestors[0].Run.Revision, "parent wins Resume admission")
			if e != nil {
				t.Fatal(e)
			}
			f.ancestors[0] = s
		}
	case "stale-admission":
		expected = kernel.ErrExecutionConflict
		f.store.beforeAdmission = func(g kernel.ChildExecutionGrant) {
			l, e := f.peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: g.ChildRunID, OwnerID: "admission-winner", LeaseDuration: 2 * time.Second})
			if e != nil {
				t.Fatal(e)
			}
			rt, e := f.peer.WithExecution(l)
			if e != nil {
				t.Fatal(e)
			}
			s := f.load(f.peer)
			_, e = rt.Apply(ctx, s.Run.ID, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: s.State})
			if e != nil {
				t.Fatal(e)
			}
			if e = f.peer.ReleaseExecution(ctx, l); e != nil {
				t.Fatal(e)
			}
			winner = f.load(f.peer)
		}
	}
	_, e := f.runner(f.rt, "actual-resume", true).Resume(ctx, id, rev)
	if !errors.Is(e, expected) {
		t.Fatalf("actual Resume %s: %v want %v", kind, e, expected)
	}
	if f.store.admissions != 0 || f.host.calls != 0 || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
		t.Fatal("denied Resume admitted or dispatched")
	}
	if got := f.load(f.peer); !reflect.DeepEqual(got, winner) {
		t.Fatal("denied Resume changed full child snapshot/Budget")
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	if _, e = f.peer.LookupChildExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("denial retained new grant: %v", e)
	}
}
func ea1ChildResumePhase(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), phase, kind string) {
	f := ea1NewChildResume(t, factory, kind == "tool")
	ctx := t.Context()
	stop := errors.New("original Resume Host outcome unknown")
	cuts := 0
	barrier := func() {
		cuts++
		s, g := f.load(f.peer), f.grant(f.peer)
		if f.store.acquired.LeaseID == "" {
			t.Fatal("Resume Host/physical callback ran before native owner")
		}
		f.fence(s, g, true)
	}
	if kind == "host" {
		f.host.hook = barrier
		f.host.failure = stop
		f.host.sendUnknown = phase == "P2"
	} else {
		f.store.event = "agent.model_invocation.completed"
		f.physical.modelHook = barrier
		if kind == "tool" {
			f.store.event = "tool.completed"
			f.physical.modelHook = nil
			f.physical.toolHook = barrier
		}
	}
	_, err := f.runner(f.rt, "original-resume-owner", kind == "host").Resume(ctx, f.request.ID, f.prepared.Run.Revision)
	f.host.hook = nil
	f.physical.modelHook = nil
	f.physical.toolHook = nil
	expected := f.store.injected
	if kind == "host" {
		expected = stop
	}
	if !errors.Is(err, expected) || !errors.Is(err, kernel.ErrChildExecutionBusy) || cuts != 1 {
		t.Fatalf("actual Resume cutpoint %d: %v", cuts, err)
	}
	s, g := f.load(f.peer), f.grant(f.peer)
	if f.store.admissions != 1 || f.store.marks != 0 {
		t.Fatal("Resume did not retain exactly one original unmarked grant")
	}
	f.unchanged(f.peer, s, g)
	if kind == "host" {
		if !reflect.DeepEqual(s, f.prepared) || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatal("pre-Next Resume changed source or dispatched")
		}
	} else {
		if f.store.failures != 1 {
			t.Fatal("actual receipt fault not reached")
		}
		f.receipts(s, kind == "tool")
	}
	// All Resume cutpoints, including Host-before-Next, follow native acquisition.
	// A real expiry must not authorize replay or invent a callback-return marker.
	ea1ControlWaitUntil(f.store.acquired.ExpiresAt.Add(150 * time.Millisecond))
	freshStore := f.reopen()
	freshRT := ea1StartRuntime(t, freshStore)
	f.physical.rt = freshRT
	f.unchanged(freshRT, s, g)
	recovery := f.runner(freshRT, "fresh-resume-owner", kind == "host")
	if e := recovery.ReconcileChildPhysicalGrant(ctx, s.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown Resume scope retired: %v", e)
	}
	expected = kernel.ErrChildExecutionBusy
	if kind == "model" {
		expected = agent.ErrModelOutcomeUnknown
	}
	if kind == "tool" {
		expected = agent.ErrToolOutcomeUnknown
	}
	if _, e := recovery.Resume(ctx, s.Run.ID, s.Run.Revision); !errors.Is(e, expected) {
		t.Fatalf("unknown Resume replay after TTL: %v want %v", e, expected)
	}
	f.unchanged(freshRT, s, g)
	if e := freshStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &f.store.acquired); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("original Resume lease not expired: %v", e)
	}
	if kind == "host" {
		sends := 0
		if phase == "P2" {
			sends = 1
		}
		if f.host.calls != 1 || f.host.sends != sends || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatal("Host/physical replay")
		}
	} else {
		f.receipts(s, kind == "tool")
	}
}
