package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// AC-R P3/P4/P5 only. PrepareRun creates the child without measured physical
// work. Every measured callback belongs to actual Resume, not Root recovery.
func RunEA1ChildAgentResumeSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	for _, p := range []string{"P3", "P4"} {
		for _, tool := range []bool{false, true} {
			t.Run(p+"/"+map[bool]string{false: "model-yield", true: "terminal-tool"}[tool], func(t *testing.T) { ea1ChildResumeSettlement(t, factory, p, tool) })
		}
	}
	t.Run("P5/two-resumes-parent-cancel", func(t *testing.T) { ea1ChildResumeElection(t, factory, false) })
	t.Run("P5/two-resumes-ancestor-deadline", func(t *testing.T) { ea1ChildResumeElection(t, factory, true) })
}

type ea1ChildResumeSettlementStore struct {
	*ea1AgentAdmissionStore
	mode                    string
	fault                   error
	beforeBegin             func(kernel.ChildExecutionGrant)
	afterBegin              func(context.Context)
	marks, finishes, begins int
}

func (s *ea1ChildResumeSettlementStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.beforeBegin != nil {
		s.beforeBegin(g)
	}
	e := s.ChildExecutionStore.BeginChildExecution(ctx, g)
	if e == nil {
		s.begins++
		if s.afterBegin != nil {
			s.afterBegin(ctx)
		}
	}
	return e
}
func (s *ea1ChildResumeSettlementStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.mode == "P3" {
		return s.fault
	}
	e := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
	if e == nil {
		s.marks++
		if s.mode == "P4" {
			return s.fault
		}
	}
	return e
}
func (s *ea1ChildResumeSettlementStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	e := s.ChildExecutionStore.FinishChildExecution(ctx, g, p)
	if e == nil {
		s.finishes++
	}
	return e
}

type ea1ChildResumeSettlementFixture struct {
	t                                      *testing.T
	store                                  *ea1ChildResumeSettlementStore
	rt                                     *kernel.Runtime
	reopen                                 func() (kernel.Store, kernel.Store)
	request                                agent.StartRequest
	parent, grandparent, created, prepared kernel.Snapshot
	physical                               *ea1StartSettlementPhysical
	definition                             tools.Definition
	registry                               *tools.Registry
}

func ea1ChildSettlementAncestors(t *testing.T, rt *kernel.Runtime, deadline bool) (kernel.Snapshot, kernel.Snapshot) {
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
	var due *time.Time
	if deadline {
		v := time.Now().UTC().Add(3 * time.Second)
		due = &v
	}
	grandparent, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "grandparent", Kind: "agent", Actor: actor, Thread: thread, Goal: "original grandparent", State: json.RawMessage("{}"), DeadlineAt: due})
	if e != nil {
		t.Fatal(e)
	}
	parent, e := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", ParentRunID: grandparent.Run.ID, Kind: "agent", Actor: actor, Thread: thread, Goal: "original parent", State: json.RawMessage("{}")})
	if e != nil {
		t.Fatal(e)
	}
	return parent, grandparent
}

func ea1NewChildResumeSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), tool, deadline bool) *ea1ChildResumeSettlementFixture {
	backing, reopen := factory(t)
	st := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), fault: errors.New("actual Child Resume settlement ACK cut")}
	rt := ea1StartRuntime(t, st)
	f := &ea1ChildResumeSettlementFixture{t: t, store: st, rt: rt, reopen: reopen}
	f.parent, f.grandparent = ea1ChildSettlementAncestors(t, rt, deadline)
	actor, thread := f.parent.Run.Actor, f.parent.Run.Thread
	var e error
	f.request = agent.StartRequest{ID: "child-resume-settlement", RequestID: "original-child-resume", ParentRunID: f.parent.Run.ID, OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child-resume","context":"isolated"}`)), Actor: actor, Thread: thread, Goal: "original child resume", Instructions: "Preserve original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if tool {
		f.request.ToolKeys = []string{"effect"}
	}
	f.physical = &ea1StartSettlementPhysical{rt: rt, tool: tool}
	f.definition = tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	f.registry, e = tools.NewRegistry([]tools.Registration{{Definition: f.definition, Handler: tools.HandlerFunc(f.physical.execute)}})
	if e != nil {
		t.Fatal(e)
	}
	setup := f.runner(rt, "prepare-owner", false)
	st.afterCreate = func(_ context.Context, s kernel.Snapshot) error {
		f.created = s
		if !setup.VerifyFrozenStartRequest(s, f.request) || s.Run.ParentRunID != f.parent.Run.ID {
			t.Fatal("Create lost exact child source")
		}
		return nil
	}
	f.prepared, e = setup.PrepareRun(t.Context(), f.request)
	if e != nil || f.created.Run.ID != f.request.ID || f.prepared.Run.Status != kernel.RunStatusRunning || !setup.VerifyFrozenStartRequest(f.prepared, f.request) {
		t.Fatal("legal deferred Child Prepare failed", e)
	}
	if _, e = rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("Prepare scope not retired", e)
	}
	if len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || st.begins != 1 || st.marks != 1 || st.finishes != 1 {
		t.Fatal("Prepare did not finish its separate zero-send callback")
	}
	st.begins, st.marks, st.finishes = 0, 0, 0
	ea1CompBarrierUnchanged(t, rt, f.parent)
	ea1CompBarrierUnchanged(t, rt, f.grandparent)
	return f
}
func (f *ea1ChildResumeSettlementFixture) runner(rt *kernel.Runtime, owner string, host bool) *agent.Runner {
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.registry, Executor: f.registry, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func ea1ChildResumeSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), phase string, tool bool) {
	f := ea1NewChildResumeSettlement(t, factory, tool, false)
	f.store.mode = phase
	_, e := f.runner(f.rt, "original-resume-owner", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, f.store.fault) || !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatal("actual Resume settlement cut absent", e)
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ChildRunID != f.request.ID || g.ExpectedRevision != f.prepared.Run.Revision || f.store.begins != 1 {
		t.Fatal("not original Child Resume grant", e)
	}
	if (phase == "P3" && f.store.marks != 0) || (phase == "P4" && f.store.marks != 1) || f.store.finishes != 0 {
		t.Fatal("wrong actual Mark/Finish boundary")
	}
	ea1AssertStartSettlementReceipt(t, f.physical, f.request, f.definition, before, tool)
	if f.physical.intents[0].SourceRevision < f.prepared.Run.Revision || f.store.acquired.RunID != f.request.ID || f.store.acquired.OwnerID != "original-resume-owner" || f.store.acquired.LeaseID == "" || f.store.acquired.Epoch == 0 || f.store.acquireCalls != 1 || f.store.releaseCalls != 1 {
		t.Fatal("receipt not from original actual Resume native attempt")
	}
	ea1ChildRecoveryPhaseControls(t, f.rt, before, f.parent, f.grandparent, g)
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	fresh := f.runner(ar, "fresh-recovery", true)
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID)
	if phase == "P3" {
		if !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("Child recovery invented uncommitted marker", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
		// Actual Resume also cannot bypass this original retained physical scope.
		_, e = fresh.Resume(t.Context(), f.request.ID, before.Run.Revision)
		want := kernel.ErrChildExecutionBusy
		if tool {
			want = agent.ErrRunTerminal
		}
		if !errors.Is(e, want) {
			t.Fatal("Resume bypassed original receipt-only scope", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	} else {
		if e != nil {
			t.Fatal("original committed marker could not recover", e)
		}
		if e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("original marker replayed", e)
		}
		ea1CompBarrierUnchanged(t, ar, before)
		ea1CompBarrierUnchanged(t, ar, f.parent)
		ea1CompBarrierUnchanged(t, ar, f.grandparent)
	}
	ea1AssertStartSettlementReceipt(t, f.physical, f.request, f.definition, before, tool)
}
func ea1ChildResumeElection(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), deadline bool) {
	f := ea1NewChildResumeSettlement(t, factory, false, deadline)
	ea1ChildSettlementElection(t, f.reopen, f.request, f.prepared, f.parent, f.grandparent, f.physical, f.definition, f.runner, nil, deadline)
}

// Both controls compete for the exact same original borrowed grant. Approval
// differs from Resume only in its callback, preflight, and lease assertions.
func ea1ChildSettlementElection(t *testing.T, reopen func() (kernel.Store, kernel.Store), request agent.StartRequest, source, parent, grandparent kernel.Snapshot,
	physical *ea1StartSettlementPhysical, definition tools.Definition, runner func(*kernel.Runtime, string, bool) *agent.Runner, approval *ea1ChildApprovalSettlementFixture, deadline bool) {
	t.Helper()
	a, b := reopen()
	sa := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, a)}
	sb := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, b)}
	ar, br := ea1StartRuntime(t, sa), ea1StartRuntime(t, sb)
	physical.rt = ar
	if approval != nil {
		approval.rt = ar
	}
	begin := make(chan struct{}, 2)
	allowBegin := make(chan struct{})
	barrier := func(g kernel.ChildExecutionGrant) {
		if g.ChildRunID != request.ID || g.ExpectedRevision != source.Run.Revision || g.IsOwned() {
			t.Error("wrong original Child admission candidate")
		}
		begin <- struct{}{}
		select {
		case <-allowBegin:
		case <-t.Context().Done():
		}
	}
	sa.beforeBegin, sb.beforeBegin = barrier, barrier
	enteredHost := make(chan struct{}, 1)
	releaseHost := make(chan struct{})
	hold := func(context.Context) {
		enteredHost <- struct{}{}
		select {
		case <-releaseHost:
		case <-t.Context().Done():
		}
	}
	if approval != nil {
		sa.afterBegin, sb.afterBegin = hold, hold
	} else {
		sa.afterAcquire, sb.afterAcquire = hold, hold
	}
	type outcome struct {
		s kernel.Snapshot
		e error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, r := range []*agent.Runner{runner(ar, "candidate-a", true), runner(br, "candidate-b", true)} {
		go func(r *agent.Runner) {
			defer wg.Done()
			var s kernel.Snapshot
			var e error
			if approval != nil {
				s, e = r.ResolveApproval(t.Context(), request.ID, source.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
			} else {
				s, e = r.Resume(t.Context(), request.ID, source.Run.Revision)
			}
			results <- outcome{s, e}
		}(r)
	}
	for range 2 {
		select {
		case <-begin:
		case <-t.Context().Done():
			close(allowBegin)
			close(releaseHost)
			wg.Wait()
			t.Fatal("actual BeginChild barrier absent")
		}
	}
	close(allowBegin)
	defer func() {
		if releaseHost != nil {
			close(releaseHost)
			wg.Wait()
		}
	}()
	select {
	case <-enteredHost:
	case <-t.Context().Done():
		t.Fatal("winner Host absent")
	}
	var loser outcome
	select {
	case loser = <-results:
	case <-t.Context().Done():
		t.Fatal("competing control did not reject")
	}
	if !errors.Is(loser.e, kernel.ErrChildExecutionBusy) || !ea1RecoveryJSONEqual(loser.s, source) {
		t.Fatal("wrong competing control result", loser.e)
	}
	current, e := ar.Load(t.Context(), request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := ar.LookupChildExecution(t.Context(), request.ID)
	if e != nil || g.IsOwned() || g.ExpectedRevision != source.Run.Revision || g.ChildRunID != request.ID {
		t.Fatal("winner scope not original", e)
	}
	ea1CompBarrierUnchanged(t, ar, source)
	lease := sa.acquired
	if lease.LeaseID == "" {
		lease = sb.acquired
	}
	if approval == nil && (lease.RunID != request.ID || lease.LeaseID == "" || lease.Epoch == 0 || sa.acquireCalls+sb.acquireCalls != 1) {
		t.Fatal("winner native authority missing or loser claimed")
	}
	ea1ChildRecoveryPhaseControls(t, ar, current, parent, grandparent, g)
	if approval == nil {
		if e = a.(kernel.ExecutionStore).CheckExecution(t.Context(), request.ID, &lease); e != nil {
			t.Fatal("rejected competing controls revoked winner epoch", e)
		}
	}
	if deadline {
		if delay := time.Until(grandparent.Run.DeadlineAt.Add(30 * time.Millisecond)); delay > 0 {
			select {
			case <-time.After(delay):
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
		}
		_, _, e = br.Expire(t.Context(), grandparent.Run.ID, grandparent.Run.Revision)
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatal("deadline bypassed original admitted control", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, current, parent, grandparent, g)
	}
	if approval != nil {
		if approval.approval.calls != 0 || len(physical.toolRequests) != 0 || len(physical.requests) != 1 || sa.acquireCalls+sb.acquireCalls != 0 {
			t.Fatal("approval winner cutpoint dispatched early")
		}
	} else if len(physical.requests) != 0 || len(physical.toolRequests) != 0 {
		t.Fatal("winner dispatched before Host boundary")
	}
	close(releaseHost)
	wg.Wait()
	releaseHost = nil
	winner := <-results
	if winner.e != nil || sa.begins+sb.begins != 1 || sa.marks+sb.marks != 1 || sa.finishes+sb.finishes != 1 {
		t.Fatal("actual control did not elect one winner", winner.e)
	}
	if approval != nil {
		if approval.preflights.Load() != 2 || sa.acquireCalls+sb.acquireCalls+sa.renewCalls+sb.renewCalls+sa.releaseCalls+sb.releaseCalls != 0 {
			t.Fatal("Child approval invented Root native owner lifecycle or skipped preflight")
		}
		approval.assertReceipt(winner.s)
	} else {
		if sa.acquireCalls+sb.acquireCalls != 1 || sa.releaseCalls+sb.releaseCalls != 1 {
			t.Fatal("unique Resume did not preserve native lease lifecycle")
		}
		ea1AssertStartSettlementReceipt(t, physical, request, definition, winner.s, false)
	}
	ea1CompBarrierUnchanged(t, ar, parent)
	ea1CompBarrierUnchanged(t, ar, grandparent)
	if _, e = ar.LookupChildExecution(t.Context(), request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("winner original scope not retired", e)
	}
	if deadline {
		out, changed, e := ar.Expire(t.Context(), grandparent.Run.ID, grandparent.Run.Revision)
		if e != nil || !changed || out.Run.Status != kernel.RunStatusFailed || out.Run.ErrorCode != "run.deadline_exceeded" || !ea1RecoveryJSONEqual(out.State, grandparent.State) {
			t.Fatal("retired callback failed to release deadline election", e)
		}
	} else {
		out, e := ar.Cancel(t.Context(), grandparent.Run.ID, grandparent.Run.Revision, "original cancel")
		if e != nil {
			t.Fatal(e)
		}
		ea1CancelCommitted(t, ar, grandparent, out)
	}
	ea1CompBarrierUnchanged(t, ar, winner.s)
	ea1CompBarrierUnchanged(t, ar, parent)
}
