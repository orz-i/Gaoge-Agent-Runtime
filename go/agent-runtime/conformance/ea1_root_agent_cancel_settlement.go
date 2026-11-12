package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func ea1RootCancelSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), phase string, withTool bool) {
	t.Helper()
	backing, _, reopen := factory(t)
	fault := errors.New("test-only original settlement transport fault")
	mode := phase
	wrapped := &ea1StartSettlementStore{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), mode: mode, fault: fault}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	physical := &ea1StartSettlementPhysical{rt: rt, tool: withTool, hostFault: errors.New("original Host send outcome unknown")}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	catalog, err := tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	makeRunner := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog, RunMiddleware: []plugin.RunMiddleware{physical}, ToolMiddleware: []plugin.ToolMiddleware{physical}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	runner := makeRunner(rt, "original-start-owner")
	req := agent.StartRequest{ID: "root-start-settlement", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original root settlement", Instructions: "Keep the original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		req.ToolKeys = []string{"effect"}
	}
	var created kernel.Snapshot
	wrapped.created = func(s kernel.Snapshot) {
		created = s
		if s.Run.ParentRunID != "" || req.OriginFingerprint != "" || !runner.VerifyFrozenStartRequest(s, req) {
			t.Fatal("actual Create did not preserve original Root source")
		}
	}
	_, startErr := runner.StartRun(t.Context(), req)
	if created.Run.ID != req.ID {
		t.Fatal("actual StartRun never created original source")
	}
	if !errors.Is(startErr, fault) || !errors.Is(startErr, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("actual original settlement cutpoint not reached: %v", startErr)
	}
	before, err := rt.Load(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rt.LookupChildExecution(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExecutionOwnerID != "original-start-owner" || grant.ExecutionLeaseID == "" || grant.ExecutionEpoch == 0 || grant.AdmissionOperation != "start" || len(grant.AdmissionSourceHash) != 64 || grant.ExpectedRevision != created.Run.Revision {
		t.Fatal("original owned source binding missing")
	}
	if phase == "P4" {
		if wrapped.markCommits != 1 || wrapped.finishCalls == 0 {
			t.Fatal("actual original Mark commit/ACK-loss not reached")
		}
	} else if wrapped.markCommits != 0 || wrapped.markCalls < 2 {
		t.Fatal("P3 did not retain unmarked callback-return fact")
	}
	completion, completionErr := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
	if phase == "P3" {
		if completionErr != nil || !completion.HostCompleted || !completion.LeaseOwned || completion.ReturnRevision != before.Run.Revision {
			t.Fatalf("original callback return fact: %+v %v", completion, completionErr)
		}
	} else if !errors.Is(completionErr, kernel.ErrNotFound) {
		t.Fatalf("marked ACK-loss fabricated callback-return fallback: %v", completionErr)
	}
	ea1AssertStartSettlementReceipt(t, physical, req, definition, before, withTool)
	wantCancel := kernel.ErrChildExecutionBusy
	if before.Run.Status == kernel.RunStatusCompleted {
		wantCancel = kernel.ErrTerminal
	}
	ea1CancelRejected(t, rt, before, grant, wantCancel)
	freshStore := reopen()
	freshRT, e := kernel.New(kernel.Dependencies{Store: freshStore})
	if e != nil {
		t.Fatal(e)
	}
	fresh := makeRunner(freshRT, "fresh-recovery-owner")
	assertUnchanged := func() {
		t.Helper()
		snap, e := freshRT.Load(t.Context(), req.ID)
		if e != nil || !reflect.DeepEqual(snap, before) {
			t.Fatal("recovery/denial changed full original snapshot")
		}
		ea1AssertStartSettlementReceipt(t, physical, req, definition, snap, withTool)
	}
	original, e := freshRT.LookupChildExecution(t.Context(), req.ID)
	if e != nil || original != grant {
		t.Fatal("reopen lost original immutable scope")
	}
	assertUnchanged()
	ea1CancelRejected(t, freshRT, before, grant, wantCancel)
	// Exact identity remains required even if the caller knows a real marker.
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ID += "-foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = "resume" }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("0", 64) }} {
		wrong := grant
		mutate(&wrong)
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), wrong); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign authority retired original: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("denied identity changed grant")
		}
		assertUnchanged()
	}
	if phase == "P3" {
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("receipt alone retired unmarked scope: %v", e)
		}
		assertUnchanged()
	}
	e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID)
	if e != nil {
		t.Fatalf("original settlement could not recover: %v", e)
	}
	if _, e = freshRT.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("scope not retired: %v", e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("duplicate reconciliation invented work: %v", e)
	}
	assertUnchanged()
	if withTool {
		ea1CancelRejected(t, freshRT, before, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
	} else {
		out, cancelErr := freshRT.Cancel(t.Context(), req.ID, before.Run.Revision, "original cancel")
		if cancelErr != nil {
			t.Fatal("settled original receipt still blocked cancellation", cancelErr)
		}
		ea1CancelCommitted(t, freshRT, before, out)
	}
}

// This wrapper only intercepts the actual public Cancel's ControlStore CAS.
// It forwards every native capability through the original admission wrapper.
type ea1CancelControlStore struct {
	*ea1AgentAdmissionStore
	before  func()
	fault   error
	commits int
}

func (s *ea1CancelControlStore) ApplyControl(ctx context.Context, id string, revision uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	if s.before != nil {
		s.before()
	}
	out, revoked, e := s.ControlStore.ApplyControl(ctx, id, revision, g, m)
	if e == nil {
		s.commits++
		if s.fault != nil {
			return kernel.Snapshot{}, false, s.fault
		}
	}
	return out, revoked, e
}
func ea1RootCancelACK(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewResumeFixture(t, factory, false)
	lease, e := f.observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "original-cancel-owner", LeaseDuration: 2 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	lost := errors.New("committed original cancellation ACK lost")
	s := &ea1CancelControlStore{ea1AgentAdmissionStore: f.store, fault: lost}
	rt := ea1StartRuntime(t, s)
	_, e = rt.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
	if !errors.Is(e, lost) || kernel.IsControlNoEffect(e) || s.commits != 1 {
		t.Fatal("actual Cancel committed ACK-loss not reached", e)
	}
	committed, e := f.observer.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	ea1CancelCommitted(t, f.observer, f.prepared, committed)
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	ea1CompBarrierUnchanged(t, freshRT, committed)
	_, e = freshRT.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
	if !errors.Is(e, kernel.ErrConflict) {
		t.Fatal("old cancellation revision not rejected", e)
	}
	ea1CancelRejected(t, freshRT, committed, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
	_, e = freshRT.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 2 * time.Second})
	if !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatal("cancelled authority revived", e)
	}
	ea1CompBarrierUnchanged(t, freshRT, committed)
	if s.commits != 1 {
		t.Fatal("ACK retry duplicated physical cancellation")
	}
	f.zero()
}
func ea1RootCancelElection(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), deadline bool) {
	var f *ea1ResumeFixture
	if deadline {
		f = ea1NewCancelDeadlineFixture(t, factory, false)
	} else {
		f = ea1NewResumeFixture(t, factory, false)
	}
	// Claim an actual idle native owner. The winning control must revoke it
	// atomically, while losing controls never rotate or recreate authority.
	lease, e := f.observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "original-election-owner", LeaseDuration: 10 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	if deadline {
		if f.prepared.Run.DeadlineAt == nil {
			t.Fatal("source deadline absent")
		}
		delay := time.Until(f.prepared.Run.DeadlineAt.Add(30 * time.Millisecond))
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			}
		}
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	wait := func() {
		entered <- struct{}{}
		select {
		case <-release:
		case <-t.Context().Done():
		}
	}
	a := &ea1CancelControlStore{ea1AgentAdmissionStore: f.store, before: wait}
	b := &ea1CancelControlStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, f.peer), before: wait}
	ar, br := ea1StartRuntime(t, a), ea1StartRuntime(t, b)
	type outcome struct {
		snapshot kernel.Snapshot
		err      error
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s, e := ar.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
		results <- outcome{s, e}
	}()
	go func() {
		defer wg.Done()
		if deadline {
			s, _, e := br.Expire(t.Context(), f.request.ID, f.prepared.Run.Revision)
			results <- outcome{s, e}
		} else {
			s, e := br.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
			results <- outcome{s, e}
		}
	}()
	for range 2 {
		select {
		case <-entered:
		case <-t.Context().Done():
			close(release)
			wg.Wait()
			t.Fatal("control barrier not reached")
		}
	}
	close(release)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	var winner kernel.Snapshot
	for result := range results {
		if result.err == nil {
			wins++
			winner = result.snapshot
		} else if errors.Is(result.err, kernel.ErrConflict) {
			conflicts++
		} else {
			t.Fatal("unexpected competing control error", result.err)
		}
	}
	if wins != 1 || conflicts != 1 || a.commits+b.commits != 1 {
		t.Fatal("controls did not elect one actual CAS")
	}
	if winner.Run.Status == kernel.RunStatusCancelled {
		ea1CancelCommitted(t, f.observer, f.prepared, winner)
	} else {
		if !deadline || winner.Run.Status != kernel.RunStatusFailed || winner.Run.ErrorCode != "run.deadline_exceeded" || winner.Run.Revision != f.prepared.Run.Revision+1 || !ea1RecoveryJSONEqual(winner.State, f.prepared.State) {
			t.Fatal("invalid deadline winner")
		}
		expectedRun := f.prepared.Run
		expectedRun.Status, expectedRun.Revision = winner.Run.Status, winner.Run.Revision
		expectedRun.ErrorCode, expectedRun.ErrorDetail = winner.Run.ErrorCode, winner.Run.ErrorDetail
		expectedRun.UpdatedAt, expectedRun.EndedAt = winner.Run.UpdatedAt, winner.Run.EndedAt
		if !ea1RecoveryJSONEqual(expectedRun, winner.Run) || winner.EventHead != f.prepared.EventHead+1 || winner.Run.EndedAt == nil || winner.Checkpoint != nil || winner.Result != nil {
			t.Fatal("deadline mutated original source or journal")
		}
		events, readErr := f.observer.ListEvents(t.Context(), winner.Run.ID, f.prepared.EventHead, 10)
		if readErr != nil || len(events) != 1 || events[0].Type != "run.deadline_exceeded" {
			t.Fatal("deadline journal not exactly once", readErr)
		}
		ea1CancelRejected(t, f.observer, winner, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
	}
	_, e = f.observer.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 2 * time.Second})
	if !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatal("election revived original lease", e)
	}
	ea1CompBarrierUnchanged(t, f.observer, winner)
	f.zero()
}
func ea1NewCancelDeadlineFixture(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), tool bool) *ea1ResumeFixture {
	backing, peer, reopen := factory(t)
	wrapped := ea1AgentAdmissionWrap(t, backing)
	rt := ea1StartRuntime(t, wrapped)
	f := &ea1ResumeFixture{t: t, peer: peer, reopen: reopen, store: wrapped, rt: rt, observer: ea1StartRuntime(t, peer)}
	f.physical = &ea1RootRecoveryPhysical{ea1UnknownAgentPhysical: &ea1UnknownAgentPhysical{rt: rt, tool: tool}, binding: "original"}
	var e error
	f.catalog, e = tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(f.physical.execute)}})
	if e != nil {
		t.Fatal(e)
	}
	f.request = agent.StartRequest{ID: "actual-root-resume", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original Resume effect", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Instructions: "Preserve original source."}
	if tool {
		f.request.ToolKeys = []string{"effect"}
	}
	deadline := time.Now().UTC().Add(3 * time.Second)
	f.request.DeadlineAt = &deadline
	f.host = &ea1ResumeHost{t: t, request: f.request}
	setup := f.runner(rt, "prepare-owner", false)
	wrapped.afterCreate = func(_ context.Context, s kernel.Snapshot) error {
		f.created = s
		if !setup.VerifyFrozenStartRequest(s, f.request) || s.Run.ParentRunID != "" || f.request.OriginFingerprint != "" {
			t.Fatal("actual Create did not freeze original Root source")
		}
		return nil
	}
	f.prepared, e = setup.PrepareRun(t.Context(), f.request)
	if e != nil {
		t.Fatal(e)
	}
	if f.created.Run.ID != f.request.ID || f.prepared.Run.Status != kernel.RunStatusRunning || !setup.VerifyFrozenStartRequest(f.prepared, f.request) {
		t.Fatal("PrepareRun source not preserved")
	}
	m, n := f.physical.counts()
	if m != 0 || n != 0 || f.host.calls != 0 {
		t.Fatal("Prepare dispatched measured callbacks")
	}
	if _, e = rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("Prepare did not retire own separate scope: %v", e)
	}
	ea1CompBarrierUnchanged(t, f.observer, f.prepared)
	return f
}
