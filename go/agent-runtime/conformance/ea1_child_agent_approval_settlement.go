package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Only AC-A P3/P4/P5, actual Child ResolveApproval. Explicit native approval
// yields Running/tool0; default Child authority may finish the approved Tool.
// This is not Root's automatic owned authority or a substitute ResumeToolWait.
func RunEA1ChildAgentApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	for _, native := range []bool{true, false} {
		mode := map[bool]string{true: "native-checkpoint", false: "default-terminal-tool"}[native]
		for _, phase := range []string{"P3", "P4"} {
			t.Run(phase+"/"+mode, func(t *testing.T) { ea1ChildApprovalSettlement(t, factory, phase, native) })
		}
		t.Run("P5/"+mode+"/two-approvals", func(t *testing.T) { ea1ChildApprovalElection(t, factory, native, false) })
	}
	t.Run("P5/ancestor-deadline", func(t *testing.T) { ea1ChildApprovalElection(t, factory, false, true) })
}

type ea1ChildApprovalSettlementHost struct {
	request  agent.StartRequest
	physical *ea1StartSettlementPhysical
	before   func()
}

func (*ea1ChildApprovalSettlementHost) Name() string { return "ea1.original.child.approval" }
func (h *ea1ChildApprovalSettlementHost) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if i.Operation != plugin.RunResolveApproval || i.RunID != h.request.ID || i.ParentRunID != h.request.ParentRunID || i.Actor != h.request.Actor || i.Thread != h.request.Thread || i.Goal != h.request.Goal {
		return kernel.Snapshot{}, agent.ErrInvalidRequest
	}
	h.physical.mu.Lock()
	h.physical.hostCalls++
	h.physical.mu.Unlock()
	if h.before != nil {
		h.before()
	}
	return next(ctx)
}

type ea1ChildApprovalSettlementFixture struct {
	t                                               *testing.T
	native                                          bool
	store                                           *ea1ChildResumeSettlementStore
	rt                                              *kernel.Runtime
	reopen                                          func() (kernel.Store, kernel.Store)
	request                                         agent.StartRequest
	parent, grandparent, created, waiting, approved kernel.Snapshot
	physical                                        *ea1StartSettlementPhysical
	host                                            *ea1ChildApprovalSettlementHost
	approval                                        *ea1ControlApproval
	definition                                      tools.Definition
	registry                                        *tools.Registry
	preflights                                      atomic.Int64
}

func ea1NewChildApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native, deadline bool) *ea1ChildApprovalSettlementFixture {
	backing, reopen := factory(t)
	store := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), fault: errors.New("actual child approval settlement cut")}
	rt := ea1StartRuntime(t, store)
	f := &ea1ChildApprovalSettlementFixture{t: t, native: native, store: store, rt: rt, reopen: reopen}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
	var due *time.Time
	if deadline {
		v := time.Now().UTC().Add(3 * time.Second)
		due = &v
	}
	var e error
	f.grandparent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "grandparent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "original grandparent", State: json.RawMessage("{}"), DeadlineAt: due})
	if e != nil {
		t.Fatal(e)
	}
	f.parent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", ParentRunID: f.grandparent.Run.ID, Kind: "workflow", Actor: actor, Thread: thread, Goal: "original parent", State: json.RawMessage("{}")})
	if e != nil {
		t.Fatal(e)
	}
	f.request = agent.StartRequest{ID: "child-approval-settlement", RequestID: "original-child-approval", ParentRunID: f.parent.Run.ID, OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child-approval","context":"isolated"}`)), Actor: actor, Thread: thread, Goal: "original child approval", Instructions: "Preserve original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), ToolKeys: []string{"effect"}, Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	f.physical = &ea1StartSettlementPhysical{rt: rt, tool: true}
	f.definition = tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	interactions, e := interaction.New(rt)
	if e != nil {
		t.Fatal(e)
	}
	f.approval = &ea1ControlApproval{base: interactionadapter.New(interactions), t: t}
	f.registry, e = tools.NewRegistry([]tools.Registration{{Definition: f.definition, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		approved, err := f.rt.Load(ctx, f.request.ID)
		if err != nil || approved.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(approved.Checkpoint, f.approval.resolved) || !ea1RecoveryJSONEqual(r.Call, f.approval.call) {
			t.Fatal("physical Tool lacks original durable approval", err)
		}
		f.approved = approved
		return f.physical.execute(ctx, r)
	})}})
	if e != nil {
		t.Fatal(e)
	}
	f.host = &ea1ChildApprovalSettlementHost{request: f.request, physical: f.physical}
	setup := f.runner(rt, "setup-owner", false)
	store.afterCreate = func(_ context.Context, s kernel.Snapshot) error {
		f.created = s
		if !setup.VerifyFrozenStartRequest(s, f.request) || s.Run.ParentRunID != f.parent.Run.ID {
			t.Fatal("actual child Create source mismatch")
		}
		return nil
	}
	f.waiting, e = setup.StartRun(t.Context(), f.request)
	if e != nil || f.waiting.Run.Status != kernel.RunStatusWaitingInput || f.created.Run.ID != f.request.ID || !setup.VerifyFrozenStartRequest(f.waiting, f.request) {
		t.Fatal("legal original child approval setup failed", e)
	}
	if f.waiting.Checkpoint == nil || f.waiting.Checkpoint.Status != kernel.CheckpointPending || !ea1RecoveryJSONEqual(f.waiting.Checkpoint, f.approval.checkpoint) || !ea1RecoveryJSONEqual(f.approval.definition, f.definition) || f.approval.call.ID != "original-call" || f.approval.call.ToolKey != "effect" {
		t.Fatal("original pending checkpoint/call/catalog missing")
	}
	if len(f.physical.requests) != 1 || len(f.physical.toolRequests) != 0 || f.physical.hostCalls != 0 || f.approval.calls != 0 || store.begins != 1 || store.marks != 1 || store.finishes != 1 {
		t.Fatal("setup callbacks or source scope count wrong")
	}
	if _, e = rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("original Start scope not retired", e)
	}
	expectedClaims := 0
	if native {
		expectedClaims = 1
	}
	if store.acquireCalls != expectedClaims || store.releaseCalls != expectedClaims {
		t.Fatal("setup did not preserve explicit/default Child native policy")
	}
	store.begins, store.marks, store.finishes = 0, 0, 0
	ea1CompBarrierUnchanged(t, rt, f.parent)
	ea1CompBarrierUnchanged(t, rt, f.grandparent)
	return f
}
func (f *ea1ChildApprovalSettlementFixture) runner(rt *kernel.Runtime, owner string, control bool) *agent.Runner {
	deps := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.registry, Executor: f.registry, Approvals: f.approval, ApprovalPolicies: []plugin.ApprovalPolicy{ea1ControlPolicy{}}, ToolMiddleware: []plugin.ToolMiddleware{f.physical}}
	if f.native {
		deps.RunExecution = &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}
	}
	if control {
		deps.RunMiddleware = []plugin.RunMiddleware{f.host}
		deps.ApprovalControlPreflight = func(ctx context.Context, s kernel.Snapshot) error {
			f.preflights.Add(1)
			if !ea1RecoveryJSONEqual(s, f.waiting) {
				return agent.ErrApprovalRequired
			}
			for _, original := range []kernel.Snapshot{f.parent, f.grandparent} {
				current, e := rt.Load(ctx, original.Run.ID)
				if e != nil {
					return e
				}
				if !ea1RecoveryJSONEqual(current, original) {
					return agent.ErrApprovalRequired
				}
			}
			return nil
		}
	}
	r, e := agent.NewRunner(deps)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ChildApprovalSettlementFixture) assertReceipt(snapshot kernel.Snapshot) {
	f.t.Helper()
	if f.approval.calls != 1 || f.approval.resolved == nil || f.approval.resolved.ID != f.waiting.Checkpoint.ID || f.approval.resolved.Status != kernel.CheckpointResolved || !ea1RecoveryJSONEqual(f.approval.resolved.Payload, f.waiting.Checkpoint.Payload) {
		f.t.Fatal("actual approval replaced checkpoint identity/source")
	}
	expectedCheckpoint := *f.waiting.Checkpoint
	expectedCheckpoint.Status = kernel.CheckpointResolved
	expectedCheckpoint.Response = json.RawMessage(`{"decision":"approve","comment":"original decision"}`)
	expectedCheckpoint.ResolvedAt = f.approval.resolved.ResolvedAt
	if expectedCheckpoint.ResolvedAt == nil || expectedCheckpoint.ResolvedAt.Before(expectedCheckpoint.CreatedAt) || !ea1RecoveryJSONEqual(&expectedCheckpoint, f.approval.resolved) {
		f.t.Fatal("real handler changed full checkpoint or original decision")
	}
	originalView, originalErr := agent.ViewState(f.waiting)
	currentView, currentErr := agent.ViewState(snapshot)
	if originalErr != nil || currentErr != nil {
		f.t.Fatal("approval budget view missing")
	}
	expectedBudget := originalView.Budget
	if !f.native {
		expectedBudget.Usage.ToolCalls++
		if snapshot.Result == nil {
			f.t.Fatal("terminal Tool result absent")
		}
		expectedBudget.Usage.OutputBytes = len(snapshot.Result.Content)
	}
	if !ea1RecoveryJSONEqual(expectedBudget, currentView.Budget) {
		f.t.Fatal("approval changed budget beyond actual original Tool settlement")
	}
	ea1AssertChildRecoveryPhaseReceipt(f.t, f.physical, f.request, f.definition, snapshot, !f.native)
	if f.native {
		if snapshot.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(snapshot.Checkpoint, f.approval.resolved) || len(f.physical.toolRequests) != 0 {
			f.t.Fatal("explicit native approval changed continuation timing")
		}
	} else if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Checkpoint != nil || f.approved.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(f.approved.Checkpoint, f.approval.resolved) {
		f.t.Fatal("default child did not consume exact durable approval")
	}
}
func ea1ChildApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), phase string, native bool) {
	f := ea1NewChildApprovalSettlement(t, factory, native, false)
	f.store.mode = phase
	_, e := f.runner(f.rt, "original-approval-owner", true).ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
	if !errors.Is(e, f.store.fault) || !errors.Is(e, kernel.ErrChildExecutionBusy) || f.preflights.Load() != 1 {
		t.Fatal("actual approval cut not reached", e)
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ExpectedRevision != f.waiting.Run.Revision || g.ChildRunID != f.request.ID || f.store.begins != 1 {
		t.Fatal("original approval ChildGrant missing", e)
	}
	if (phase == "P3" && f.store.marks != 0) || (phase == "P4" && f.store.marks != 1) || f.store.finishes != 0 {
		t.Fatal("wrong original callback Mark/Finish boundary")
	}
	f.assertReceipt(before)
	ea1ChildRecoveryPhaseControls(t, f.rt, before, f.parent, f.grandparent, g)
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	fresh := f.runner(ar, "fresh-recovery", true)
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID)
	if phase == "P3" {
		if !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("receipt alone invented missing approval marker", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	} else {
		if e != nil {
			t.Fatal("original marker ACK recovery failed", e)
		}
		ea1CompBarrierUnchanged(t, ar, before)
		ea1CompBarrierUnchanged(t, ar, f.parent)
		ea1CompBarrierUnchanged(t, ar, f.grandparent)
		if e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("original approval marker replayed", e)
		}
	}
	_, e = fresh.ResolveApproval(t.Context(), f.request.ID, before.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
	if !errors.Is(e, agent.ErrApprovalRequired) || f.preflights.Load() != 1 {
		t.Fatal("completed approval replay passed preflight", e)
	}
	ea1CompBarrierUnchanged(t, ar, before)
	f.assertReceipt(before)
	expectedClaims := 0
	if native {
		expectedClaims = 1
	}
	if f.store.acquireCalls != expectedClaims || f.store.releaseCalls != expectedClaims {
		t.Fatal("approval/recovery fabricated native acquisition")
	}
}
func ea1ChildApprovalElection(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native, deadline bool) {
	f := ea1NewChildApprovalSettlement(t, factory, native, deadline)
	a, b := f.reopen()
	sa := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, a)}
	sb := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, b)}
	ar, br := ea1StartRuntime(t, sa), ea1StartRuntime(t, sb)
	f.rt, f.physical.rt = ar, ar
	begin := make(chan struct{}, 2)
	allowBegin := make(chan struct{})
	barrier := func(g kernel.ChildExecutionGrant) {
		if g.ChildRunID != f.request.ID || g.ExpectedRevision != f.waiting.Run.Revision || g.IsOwned() {
			f.t.Error("wrong waiting child admission candidate")
		}
		begin <- struct{}{}
		select {
		case <-allowBegin:
		case <-t.Context().Done():
		}
	}
	sa.beforeBegin, sb.beforeBegin = barrier, barrier
	hostEntered := make(chan struct{}, 1)
	releaseHost := make(chan struct{})
	f.host.before = func() {
		hostEntered <- struct{}{}
		select {
		case <-releaseHost:
		case <-t.Context().Done():
		}
	}
	type result struct {
		s kernel.Snapshot
		e error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, r := range []*agent.Runner{f.runner(ar, "approval-a", true), f.runner(br, "approval-b", true)} {
		go func(r *agent.Runner) {
			defer wg.Done()
			s, e := r.ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
			results <- result{s, e}
		}(r)
	}
	for range 2 {
		select {
		case <-begin:
		case <-t.Context().Done():
			close(allowBegin)
			close(releaseHost)
			wg.Wait()
			t.Fatal("actual waiting Begin barrier absent")
		}
	}
	close(allowBegin)
	select {
	case <-hostEntered:
	case <-t.Context().Done():
		close(releaseHost)
		wg.Wait()
		t.Fatal("winner Host missing")
	}
	var loser result
	select {
	case loser = <-results:
	case <-t.Context().Done():
		close(releaseHost)
		wg.Wait()
		t.Fatal("loser did not reject")
	}
	if !errors.Is(loser.e, kernel.ErrChildExecutionBusy) || !ea1RecoveryJSONEqual(loser.s, f.waiting) {
		close(releaseHost)
		wg.Wait()
		t.Fatal("wrong competing approval result", loser.e)
	}
	g, e := ar.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	ea1ChildRecoveryPhaseControls(t, ar, f.waiting, f.parent, f.grandparent, g)
	if deadline {
		delay := time.Until(f.grandparent.Run.DeadlineAt.Add(30 * time.Millisecond))
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-t.Context().Done():
				close(releaseHost)
				wg.Wait()
				t.Fatal(t.Context().Err())
			}
		}
		_, _, e = br.Expire(t.Context(), f.grandparent.Run.ID, f.grandparent.Run.Revision)
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			close(releaseHost)
			wg.Wait()
			t.Fatal("ancestor deadline bypassed original approval callback", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, f.waiting, f.parent, f.grandparent, g)
	}
	if f.approval.calls != 0 || len(f.physical.toolRequests) != 0 || len(f.physical.requests) != 1 || sa.acquireCalls+sb.acquireCalls != 0 {
		t.Fatal("approval winner cutpoint dispatched early")
	}
	close(releaseHost)
	wg.Wait()
	winner := <-results
	if winner.e != nil || sa.begins+sb.begins != 1 || sa.marks+sb.marks != 1 || sa.finishes+sb.finishes != 1 || f.preflights.Load() != 2 {
		t.Fatal("actual approval did not elect one winner", winner.e)
	}
	if sa.acquireCalls+sb.acquireCalls+sa.renewCalls+sb.renewCalls+sa.releaseCalls+sb.releaseCalls != 0 {
		t.Fatal("Child approval invented Root native owner lifecycle")
	}
	f.assertReceipt(winner.s)
	ea1CompBarrierUnchanged(t, ar, f.parent)
	ea1CompBarrierUnchanged(t, ar, f.grandparent)
	if _, e = ar.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("winner approval grant not retired", e)
	}
	if deadline {
		out, changed, e := ar.Expire(t.Context(), f.grandparent.Run.ID, f.grandparent.Run.Revision)
		if e != nil || !changed || out.Run.Status != kernel.RunStatusFailed || out.Run.ErrorCode != "run.deadline_exceeded" || !ea1RecoveryJSONEqual(out.State, f.grandparent.State) {
			t.Fatal("retired approval did not release original deadline", e)
		}
	} else {
		out, e := ar.Cancel(t.Context(), f.grandparent.Run.ID, f.grandparent.Run.Revision, "original cancel")
		if e != nil {
			t.Fatal(e)
		}
		ea1CancelCommitted(t, ar, f.grandparent, out)
	}
	ea1CompBarrierUnchanged(t, ar, winner.s)
	ea1CompBarrierUnchanged(t, ar, f.parent)
}
