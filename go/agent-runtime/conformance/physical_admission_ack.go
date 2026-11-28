package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

var errAdmissionTransport = errors.New("injected admission response loss")

// This wrapper changes only the result boundary around a real Store
// transaction. It never creates or substitutes a persisted Grant.
type admissionACKStore struct {
	*ea1AgentAdmissionStore
	mode        string
	cancel      context.CancelFunc
	grant       kernel.ChildExecutionGrant
	source      kernel.Snapshot
	nativeErr   error
	attempted   bool
	readbacks   int
	lease       kernel.ExecutionLease
	entryChecks int
}

func waitAdmissionUntil(ctx context.Context, until time.Time) error {
	delay := time.Until(until)
	if delay <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

func (s *admissionACKStore) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, source kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	s.entryChecks++
	if s.mode == "expired-prelude" {
		if err := waitAdmissionUntil(ctx, s.lease.ExpiresAt.Add(100*time.Millisecond)); err != nil {
			return kernel.OwnedPhysicalEntryWindow{}, err
		}
	}
	if s.mode == "entry-unavailable" || strings.HasPrefix(s.mode, "no-entry-") {
		return kernel.OwnedPhysicalEntryWindow{}, errAdmissionTransport
	}
	window, err := s.OwnedPhysicalExecutionStore.CheckOwnedPhysicalEntry(ctx, g, l, source)
	if err == nil && s.mode == "cancelled-at-entry" {
		s.cancel()
	}
	if err == nil && s.mode == "expired-entry-ack" {
		// Delay a real successful Store entry observation until its
		// returned authority window has ended, not a forged error.
		err = waitAdmissionUntil(ctx, s.lease.ExpiresAt.Add(100*time.Millisecond))
	}
	if err == nil && s.mode == "deadline-entry-ack" {
		if source.Run.DeadlineAt == nil {
			return kernel.OwnedPhysicalEntryWindow{}, errors.New("missing fixture deadline")
		}
		err = waitAdmissionUntil(ctx, source.Run.DeadlineAt.Add(100*time.Millisecond))
	}
	return window, err
}

func (s *admissionACKStore) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, revision uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	err := s.OwnedPhysicalExecutionStore.RecordOwnedPhysicalCallbackReturn(ctx, g, revision, o, p)
	if err == nil && s.mode == "no-entry-record-ack" {
		return errAdmissionTransport
	}
	return err
}

func (s *admissionACKStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.mode == "no-entry-finish-unavailable" {
		return errAdmissionTransport
	}
	err := s.ChildExecutionStore.FinishChildExecution(ctx, g, p)
	if err == nil && s.mode == "no-entry-finish-ack" {
		return errAdmissionTransport
	}
	return err
}

func (s *admissionACKStore) BeginOwnedPhysicalExecution(ctx context.Context, p kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	l := p.Candidate().Lease
	g := p.Candidate().Grant
	s.grant = g
	s.lease = l
	var err error
	s.source, err = s.Load(ctx, g.ChildRunID)
	if err != nil {
		return kernel.OwnedPhysicalAdmissionResult{}, err
	}
	defer func() { s.attempted = true }()
	switch s.mode {
	case "denied-before":
		return kernel.OwnedPhysicalAdmissionResult{}, kernel.ErrExecutionConflict
	case "unknown-before":
		return kernel.OwnedPhysicalAdmissionResult{}, p.CommitOutcome(errAdmissionTransport)
	}
	v, nativeErr := s.ea1AgentAdmissionStore.BeginOwnedPhysicalExecution(ctx, p)
	s.nativeErr = nativeErr
	if v.Grant.ID != "" {
		s.grant, s.lease = v.Grant, v.Lease
	}
	l = s.lease
	if s.nativeErr != nil {
		if s.mode == "unknown-after-denial" {
			// Even a misclassified response must not let the losing caller
			// adopt another invocation's persisted attempt commitment.
			return v, p.CommitOutcome(errAdmissionTransport)
		}
		return v, s.nativeErr
	}
	switch s.mode {
	case "expired-confirmed", "expired-ack-lost":
		if err := waitAdmissionUntil(ctx, l.ExpiresAt.Add(100*time.Millisecond)); err != nil {
			return v, p.CommitOutcome(err)
		}
		if s.mode == "expired-ack-lost" {
			return v, p.CommitOutcome(errAdmissionTransport)
		}
		return v, nil
	case "deadline-after":
		if s.source.Run.DeadlineAt == nil {
			return v, errors.New("missing fixture deadline")
		}
		return v, waitAdmissionUntil(ctx, s.source.Run.DeadlineAt.Add(100*time.Millisecond))
	case "cancelled-confirmed":
		s.cancel()
		return v, nil
	case "ack-lost", "unreadable-after":
		return v, p.CommitOutcome(errAdmissionTransport)
	case "plain-ack-lost":
		return v, errAdmissionTransport
	case "untied-unknown":
		return v, errors.Join(kernel.ErrPhysicalAdmissionCommitUnknown, errAdmissionTransport)
	case "denied-after":
		return v, kernel.ErrExecutionConflict
	case "mislabelled-denial":
		return v, p.CommitOutcome(kernel.ErrExecutionConflict)
	case "mixed-after":
		return v, errors.Join(p.CommitOutcome(errAdmissionTransport), kernel.ErrExecutionConflict)
	case "cancelled-after":
		s.cancel()
		return v, p.CommitOutcome(errAdmissionTransport)
	default:
		return v, nil
	}
}

func (s *admissionACKStore) LookupChildExecution(ctx context.Context, id string) (kernel.ChildExecutionGrant, error) {
	if s.attempted {
		s.readbacks++
		if s.mode == "unreadable-after" {
			return kernel.ChildExecutionGrant{}, errAdmissionTransport
		}
	}
	return s.ChildExecutionStore.LookupChildExecution(ctx, id)
}

// RunPhysicalAdmissionACKSuite verifies the same write-capable admission
// boundary on Memory, GORM, native SQLite and real PostgreSQL.
func RunPhysicalAdmissionACKSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, operation := range []string{"start", "prepare", "resume", "approval"} {
		for _, mode := range []string{"confirmed", "denied-before", "ack-lost", "plain-ack-lost"} {
			t.Run("admission-ack/"+operation+"/"+mode, func(t *testing.T) {
				runPhysicalAdmissionACKCase(t, factory, operation, mode)
			})
		}
	}
	for _, mode := range []string{"unknown-before", "denied-after", "mislabelled-denial", "mixed-after", "cancelled-after", "unreadable-after", "untied-unknown"} {
		t.Run("admission-ack/resume/"+mode, func(t *testing.T) {
			runPhysicalAdmissionACKCase(t, factory, "resume", mode)
		})
	}
	for _, operation := range []string{"start", "prepare", "resume", "approval"} {
		for _, mode := range []string{"expired-confirmed", "expired-ack-lost", "expired-prelude", "expired-entry-ack", "cancelled-confirmed", "cancelled-at-entry", "entry-unavailable"} {
			t.Run("callback-entry/"+operation+"/"+mode, func(t *testing.T) { runPhysicalAdmissionACKCase(t, factory, operation, mode) })
		}
	}
	for _, mode := range []string{"deadline-after", "deadline-entry-ack"} {
		t.Run("callback-entry/start/"+mode, func(t *testing.T) { runPhysicalAdmissionACKCase(t, factory, "start", mode) })
	}
	for _, mode := range []string{"no-entry-finish-unavailable", "no-entry-record-ack", "no-entry-finish-ack"} {
		t.Run("callback-entry/resume/"+mode, func(t *testing.T) { runPhysicalAdmissionACKCase(t, factory, "resume", mode) })
	}
	t.Run("callback-entry/external-lease-preserved", func(t *testing.T) { runPhysicalEntryExternalLease(t, factory) })
	for _, mode := range []string{"definite-denial", "unknown-after-denial"} {
		t.Run("admission-ack/race/"+mode, func(t *testing.T) {
			runPhysicalAdmissionACKRace(t, factory, mode)
		})
	}
}

func admissionACKRunner(t *testing.T, rt *kernel.Runtime, m *ea1AdmissionModel, barrier *agent.ModelAdmissionBarrier, ttl ...time.Duration) *agent.Runner {
	t.Helper()
	duration := time.Minute
	if len(ttl) > 0 {
		duration = ttl[0]
	}
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: &agent.RunExecution{OwnerID: "admission-ack-owner", LeaseDuration: duration}, ModelAdmissionBarrier: barrier, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}, Tool: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func runPhysicalAdmissionACKCase(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), operation, mode string) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var backing kernel.Store
	var reopen func() kernel.Store
	var resume *ea1ResumeFixture
	var approval *ea1ControlFixture
	switch operation {
	case "resume":
		resume = ea1NewResumeFixture(t, factory, false)
		backing, reopen = resume.store.Store, resume.reopen
	case "approval":
		approval = ea1NewControlFixture(t, factory, false)
		backing, reopen = approval.store.Store, approval.reopen
	default:
		backing, _, reopen = factory(t)
	}
	if operation == "resume" && mode == "entry-unavailable" {
		before := resume.observer
		if e := before.VerifyExecutionCustody(t.Context(), resume.prepared, kernel.CustodyPrepared); e != nil {
			t.Fatalf("fixture missing original prepared custody: %v", e)
		}
	}
	w := &admissionACKStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), mode: mode, cancel: cancel}
	rt := ea1StartRuntime(t, w)
	m := &ea1AdmissionModel{}
	var out kernel.Snapshot
	var err error
	var observed agent.PhysicalCallOutcome
	dispatchID := "dispatch-fixture-" + operation
	var modelCalls, toolCalls, approvalCalls int
	ttl := time.Minute
	if strings.HasPrefix(mode, "expired-") {
		ttl = 2100 * time.Millisecond
	}
	r := admissionACKRunner(t, rt, m, nil, ttl)
	wantStatus := kernel.RunStatusCompleted
	switch operation {
	case "start":
		request := ea1AdmissionRequest()
		if strings.HasPrefix(mode, "deadline-") {
			deadline := time.Now().UTC().Add(2 * time.Second)
			request.DeadlineAt = &deadline
		}
		out, observed, err = r.ObservePhysicalDispatch(ctx, request.ID, plugin.RunStart, dispatchID, func(c context.Context) (kernel.Snapshot, error) { return r.StartRun(c, request) })
	case "prepare":
		wantStatus = kernel.RunStatusRunning
		request := ea1AdmissionRequest()
		out, observed, err = r.ObservePhysicalCall(ctx, request.ID, plugin.RunStart, func(c context.Context) (kernel.Snapshot, error) { return r.PrepareRun(c, request) })
	case "resume":
		out, observed, err = r.ObservePhysicalDispatch(ctx, resume.prepared.Run.ID, plugin.RunResume, dispatchID, func(c context.Context) (kernel.Snapshot, error) {
			return r.Resume(c, resume.prepared.Run.ID, resume.prepared.Run.Revision)
		})
	case "approval":
		wantStatus = kernel.RunStatusRunning
		r = approval.runner(rt, true, true)
		out, observed, err = r.ObservePhysicalDispatch(ctx, approval.request.ID, plugin.RunResolveApproval, dispatchID, func(c context.Context) (kernel.Snapshot, error) {
			return r.ResolveApproval(c, approval.request.ID, approval.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
		})
	}
	if approval != nil {
		modelCalls, toolCalls = approval.physical.counts()
		modelCalls-- // original model result belongs to setup, not this approval
		approvalCalls = approval.approval.calls
	} else {
		modelCalls = int(m.calls.Load())
	}
	if w.grant.ID == "" || w.grant.AdmissionOperation != operation {
		t.Fatalf("did not measure real %s admission: %+v %v", operation, w.grant, err)
	}
	if operation != "prepare" {
		id, linkErr := kernel.OwnedPhysicalDispatchGrantID(w.source.Run.ID, operation, dispatchID)
		if linkErr != nil || w.grant.ID != id {
			t.Fatalf("native grant not bound to original host dispatch: got %s want %s", w.grant.ID, id)
		}
	}
	fresh := ea1StartRuntime(t, reopen())
	g, lookupErr := fresh.LookupChildExecution(t.Context(), w.grant.ChildRunID)
	if strings.HasPrefix(mode, "expired-") || strings.HasPrefix(mode, "deadline-") || mode == "cancelled-confirmed" || mode == "cancelled-at-entry" || mode == "entry-unavailable" || strings.HasPrefix(mode, "no-entry-") {
		want := kernel.ErrExecutionConflict
		switch mode {
		case "deadline-after", "deadline-entry-ack":
			want = kernel.ErrDeadline
		case "cancelled-confirmed", "cancelled-at-entry":
			want = context.Canceled
		case "entry-unavailable", "no-entry-finish-unavailable", "no-entry-record-ack", "no-entry-finish-ack":
			want = errAdmissionTransport
		}
		if !errors.Is(err, want) || approvalCalls != 0 || modelCalls != 0 || toolCalls != 0 || w.nativeErr != nil {
			t.Fatalf("acknowledged but invalid entry: approval=%d model=%d tool=%d native=%v err=%v", approvalCalls, modelCalls, toolCalls, w.nativeErr, err)
		}
		if out.Run.ID != w.source.Run.ID {
			t.Fatalf("denial lost original Run: %+v", out.Run)
		}
		if current, e := fresh.Load(t.Context(), w.grant.ChildRunID); e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(w.source) {
			t.Fatalf("no-entry changed original business source: %v", e)
		}
		if mode == "no-entry-finish-unavailable" {
			if observed.Kind() != agent.PhysicalCallUnknown {
				t.Fatal("unacknowledged retirement escaped as completed observation")
			}
			if lookupErr != nil || g != w.grant || !errors.Is(err, agent.ErrRootOutcomeUnknown) {
				t.Fatalf("unacknowledged cleanup lost fence: %+v %v %v", g, lookupErr, err)
			}
			c, e := fresh.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
			if e != nil || !c.MatchesNotEnteredSnapshot(g, w.source) || c.HostCompleted || c.MatchesReturnSnapshot(w.source) {
				t.Fatalf("durable no-entry fact not distinct: %+v %v", c, e)
			}
			obs := admissionACKRunner(t, fresh, &ea1AdmissionModel{}, nil)
			repaired, e := obs.ReconcileUnenteredPhysicalDispatch(t.Context(), g.ChildRunID, plugin.RunResume, dispatchID)
			if e != nil || !repaired {
				t.Fatalf("restart could not consume exact no-entry retirement: %t %v", repaired, e)
			}
		} else if !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("known original no-entry fence was not retired: %+v %v", g, lookupErr)
		}
		assertNoEntryRetirement(t, fresh, w.grant, w.source, true)
		if operation != "prepare" {
			obs := admissionACKRunner(t, fresh, &ea1AdmissionModel{}, nil)
			op := plugin.RunStart
			if operation == "resume" {
				op = plugin.RunResume
			}
			if operation == "approval" {
				op = plugin.RunResolveApproval
			}
			if ok, e := obs.ReconcileUnenteredPhysicalDispatch(t.Context(), w.source.Run.ID, op, dispatchID+"-foreign"); e != nil || ok {
				t.Fatalf("foreign dispatch borrowed no-entry retirement: %t %v", ok, e)
			}
			if ok, e := obs.ReconcileUnenteredPhysicalDispatch(t.Context(), w.source.Run.ID, op, dispatchID); e != nil || !ok {
				t.Fatalf("exact acknowledged retirement not recoverable: %t %v", ok, e)
			}
		}
		if operation == "resume" {
			e := fresh.VerifyExecutionCustody(t.Context(), resume.prepared, kernel.CustodyPrepared)
			t.Logf("original prepared custody after exact no-entry retirement: %v", e)
			if e != nil {
				t.Errorf("no-entry erased original prepared custody: %v", e)
			}
		}
		if operation == "start" || operation == "prepare" {
			if e := fresh.VerifyExecutionCustody(t.Context(), w.source, kernel.CustodyRetired); e == nil {
				t.Fatal("no-entry without prior actual callback manufactured custody")
			}
		}
		if mode != "no-entry-finish-unavailable" {
			op := plugin.RunStart
			if operation == "resume" {
				op = plugin.RunResume
			}
			if operation == "approval" {
				op = plugin.RunResolveApproval
			}
			t.Logf("exact no-entry retirement ACK, op=%s, native physical observation=%v matches=%t", operation, observed.Kind(), observed.Matches(w.source.Run.ID, op))
			if !observed.Matches(w.source.Run.ID, op) || observed.Kind() != agent.PhysicalCallNotEntered {
				t.Errorf("original acknowledged no-entry not consumable by Host dispatch protocol")
			}
		}
		if _, e := fresh.LookupChildExecution(t.Context(), w.grant.ChildRunID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("no-entry still fenced: %v", e)
		}
		// ACK must release only the internally acquired owner. A new
		// original lease can be obtained (unless the Run deadline is due).
		l, e := fresh.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: w.grant.ChildRunID, OwnerID: "next-owner", LeaseDuration: time.Minute})
		if e != nil {
			t.Fatalf("no-entry retirement did not release owned lease: %v", e)
		}
		if l.Epoch <= w.lease.Epoch {
			t.Fatal("next native lease did not advance epoch")
		}
		if e = fresh.ReleaseExecution(t.Context(), l); e != nil {
			t.Fatal(e)
		}
		return
	}
	if mode == "confirmed" || mode == "ack-lost" {
		wantModel, wantApproval := 0, 0
		if operation == "start" || operation == "resume" {
			wantModel = 1
		}
		if operation == "approval" {
			wantApproval = 1
		}
		if err != nil || out.Run.Status != wantStatus || modelCalls != wantModel || toolCalls != 0 || approvalCalls != wantApproval {
			t.Fatalf("original admission not executed exactly once: status=%s model=%d tool=%d approval=%d err=%v", out.Run.Status, modelCalls, toolCalls, approvalCalls, err)
		}
		if !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("successful attempt left fence: %+v %v", g, lookupErr)
		}
		retirement, e := fresh.LoadOwnedRetirement(t.Context(), w.grant.ID)
		if e != nil || retirement.Evidence.Grant != w.grant || !retirement.Evidence.Completion.HostCompleted || !retirement.Valid() {
			t.Fatalf("retirement not bound to original attempt: %+v %v", retirement, e)
		}
		wantReadbacks := 0
		if mode == "ack-lost" {
			wantReadbacks = 1
		}
		if w.readbacks != wantReadbacks {
			t.Fatalf("atomic admission performed unexpected authorization readbacks: %d want %d", w.readbacks, wantReadbacks)
		}
		return
	}
	want := kernel.ErrExecutionConflict
	switch mode {
	case "plain-ack-lost":
		want = errAdmissionTransport
	case "unknown-before", "cancelled-after", "unreadable-after", "untied-unknown":
		want = kernel.ErrPhysicalAdmissionCommitUnknown
	}
	if !errors.Is(err, want) || modelCalls != 0 || toolCalls != 0 || approvalCalls != 0 {
		t.Fatalf("rejected admission crossed callback boundary: model=%d tool=%d approval=%d err=%v", modelCalls, toolCalls, approvalCalls, err)
	}
	wantReadbacks := 0
	if mode == "unknown-before" || mode == "unreadable-after" {
		wantReadbacks = 1
	}
	if w.readbacks != wantReadbacks {
		t.Fatalf("rejection triggered unexpected authorization readback: %d want %d", w.readbacks, wantReadbacks)
	}
	current, e := fresh.Load(t.Context(), w.grant.ChildRunID)
	if e != nil || kernel.ManagedDigest(current) != kernel.ManagedDigest(w.source) {
		t.Fatalf("rejected admission mutated source: %v", e)
	}
	if _, e = fresh.LoadOwnedRetirement(t.Context(), w.grant.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("rejection fabricated retirement: %v", e)
	}
	if mode == "denied-before" || mode == "unknown-before" {
		if !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("uncommitted admission left a fence: %+v %v", g, lookupErr)
		}
		// An automatically acquired lease must be releasable if no Grant
		// committed. A later real admission, not the old error, owns dispatch.
		l, e := fresh.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: current.Run.ID, OwnerID: "subsequent-owner", LeaseDuration: time.Minute})
		if e != nil {
			t.Fatalf("uncommitted attempt leaked lease: %v", e)
		}
		if e = fresh.ReleaseExecution(t.Context(), l); e != nil {
			t.Fatal(e)
		}
		return
	}
	if lookupErr != nil || g != w.grant {
		t.Fatalf("committed but unqualified ACK lost original fence: %+v %v", g, lookupErr)
	}
	if _, e = fresh.LookupOwnedPhysicalCallbackReturn(t.Context(), g); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("unentered callback fabricated return: %v", e)
	}
	// Restart/reconciliation cannot invent a callback from the persisted
	// attempt alone or retire a barrier without its original return.
	observerRunner := admissionACKRunner(t, fresh, &ea1AdmissionModel{}, nil)
	if e = observerRunner.ReconcileRootPhysicalExecution(t.Context(), g.ChildRunID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("observer adopted admission without original ACK: %v", e)
	}
	if retained, e := fresh.LookupChildExecution(t.Context(), g.ChildRunID); e != nil || retained != g {
		t.Fatalf("observer changed original barrier: %+v %v", retained, e)
	}
}

func assertNoEntryRetirement(t *testing.T, rt *kernel.Runtime, g kernel.ChildExecutionGrant, source kernel.Snapshot, owned bool) {
	t.Helper()
	receipt, e := rt.LoadOwnedRetirement(t.Context(), g.ID)
	c := receipt.Evidence.Completion
	if e != nil || !receipt.Valid() || receipt.Evidence.Grant != g || !receipt.Evidence.Completion.NotEntered() ||
		!c.MatchesNotEnteredSnapshot(g, source) || c.HostCompleted || c.LeaseOwned != owned || c.ToolReturns != "" || c.MatchesReturnSnapshot(source) ||
		len(receipt.Closures) != 0 || kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(source) {
		t.Fatalf("invalid original no-entry retirement: %+v %v", receipt, e)
	}
}

func runPhysicalEntryExternalLease(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewResumeFixture(t, factory, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, e := f.observer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: f.prepared.Run.ID, OwnerID: "external-owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	w := &admissionACKStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, f.store.Store), mode: "cancelled-at-entry", cancel: cancel}
	rt, e := ea1StartRuntime(t, w).WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	m := &ea1AdmissionModel{}
	r := admissionACKRunner(t, rt, m, nil)
	_, e = r.Resume(ctx, f.prepared.Run.ID, f.prepared.Run.Revision)
	if !errors.Is(e, context.Canceled) || m.calls.Load() != 0 || w.entryChecks != 1 {
		t.Fatalf("external denial entered callback: %v", e)
	}
	fresh := ea1StartRuntime(t, f.reopen())
	assertNoEntryRetirement(t, fresh, w.grant, w.source, false)
	if e := fresh.VerifyExecutionCustody(t.Context(), w.source, kernel.CustodyRetired); e == nil {
		t.Fatal("no-entry with a borrowed lease created control custody")
	}
	if e = fresh.RequireExecution(t.Context(), w.grant.ChildRunID); !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("unbound observer inherited lease: %v", e)
	}
	bound, e := fresh.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	if e = bound.RequireExecution(t.Context(), w.grant.ChildRunID); e != nil {
		t.Fatalf("no-entry retirement released caller-owned lease: %v", e)
	}
	// The same caller lease can admit a new real attempt, never the old ID.
	r = admissionACKRunner(t, bound, m, nil)
	out, e := r.Resume(t.Context(), f.prepared.Run.ID, f.prepared.Run.Revision)
	if e != nil || out.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 {
		t.Fatalf("fresh physical attempt did not execute: %v", e)
	}
}

// Real concurrent Runners share a Run/Revision/Lease and deliberately
// collide on public ID. Only the actual winner may enter the callback.
func runPhysicalAdmissionACKRace(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), mode string) {
	f := ea1NewResumeFixture(t, factory, false)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	lease, e := f.observer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: f.prepared.Run.ID, OwnerID: "shared-external-owner", LeaseDuration: time.Minute})
	if e != nil {
		t.Fatal(e)
	}
	type attempt struct {
		index int
		grant kernel.ChildExecutionGrant
	}
	type result struct {
		index    int
		snapshot kernel.Snapshot
		err      error
	}
	arrived := make(chan attempt, 2)
	releaseAdmissions := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	entered := [2]chan struct{}{make(chan struct{}, 1), make(chan struct{}, 1)}
	releaseHost := make(chan struct{})
	results := make(chan result, 2)
	var runners [2]*agent.Runner
	var stores [2]*admissionACKStore
	var models [2]*ea1AdmissionModel
	for i, backing := range []kernel.Store{f.store.Store, f.peer} {
		w := &admissionACKStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing)}
		if i == 1 && mode == "unknown-after-denial" {
			w.mode = mode
		}
		w.beforeOwned = func(ctx context.Context, g kernel.ChildExecutionGrant) error {
			arrived <- attempt{i, g}
			select {
			case <-releaseAdmissions[i]:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		rt, e := kernel.New(kernel.Dependencies{Store: w, IDs: fixedGrantIDs{"same-public-admission-id"}})
		if e != nil {
			t.Fatal(e)
		}
		bound, e := rt.WithExecution(lease)
		if e != nil {
			t.Fatal(e)
		}
		barrier := agent.NewModelAdmissionBarrier(entered[i], releaseHost)
		m := &ea1AdmissionModel{}
		stores[i], models[i] = w, m
		runners[i] = admissionACKRunner(t, bound, m, barrier)
	}
	var completed int
	defer func() {
		cancel()
		for ; completed < 2; completed++ {
			<-results
		}
	}()
	for i, r := range runners {
		go func() { s, e := r.Resume(ctx, f.prepared.Run.ID, f.prepared.Run.Revision); results <- result{i, s, e} }()
	}
	var grants [2]kernel.ChildExecutionGrant
	for range 2 {
		select {
		case a := <-arrived:
			grants[a.index] = a.grant
		case got := <-results:
			completed++
			t.Fatalf("unexpected preflight rejection: %v", got.err)
		case <-ctx.Done():
			t.Fatal("both Runners did not reach admission")
		}
	}
	same := grants[0]
	same.AdmissionSourceHash = grants[1].AdmissionSourceHash
	if same != grants[1] || grants[0].AdmissionSourceHash == grants[1].AdmissionSourceHash {
		t.Fatalf("same public identity lacks distinct private attempts: %+v %+v", grants[0], grants[1])
	}
	close(releaseAdmissions[0])
	select {
	case <-entered[0]:
	case got := <-results:
		completed++
		t.Fatalf("first admission rejected: %v", got.err)
	case <-ctx.Done():
		t.Fatal("first callback absent")
	}
	// The fixed Model admission barrier follows committed Model intent.
	// Compare the winner's exact paused snapshot, not the pre-admission one.
	winnerSnapshot, e := f.observer.Load(ctx, f.prepared.Run.ID)
	if e != nil || winnerSnapshot.Run.Revision <= f.prepared.Run.Revision || models[0].calls.Load() != 0 {
		t.Fatalf("winner did not stop after intent and before Provider dispatch: %+v %v", winnerSnapshot.Run, e)
	}
	close(releaseAdmissions[1])
	select {
	case <-entered[1]:
		t.Fatal("losing contender entered model admission barrier")
	case got := <-results:
		completed++
		want := kernel.ErrExecutionConflict
		if mode == "unknown-after-denial" {
			want = kernel.ErrPhysicalAdmissionCommitUnknown
		}
		if got.index != 1 || !errors.Is(got.err, want) || !errors.Is(stores[1].nativeErr, kernel.ErrExecutionConflict) {
			t.Fatalf("contender was not denied by original Store: %+v native=%v", got, stores[1].nativeErr)
		}
	case <-ctx.Done():
		t.Fatal("contender did not return")
	}
	wantReads := 0
	if mode == "unknown-after-denial" {
		wantReads = 1
	}
	if stores[1].readbacks != wantReads || models[1].calls.Load() != 0 {
		t.Fatalf("losing contender crossed ACK/dispatch boundary: reads=%d model=%d", stores[1].readbacks, models[1].calls.Load())
	}
	if g, e := f.observer.LookupChildExecution(ctx, f.prepared.Run.ID); e != nil || g != grants[0] {
		t.Fatalf("losing admission changed first active grant: %+v %v", g, e)
	}
	ea1CompBarrierUnchanged(t, f.observer, winnerSnapshot)
	if _, e = f.observer.LookupOwnedPhysicalCallbackReturn(ctx, grants[0]); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("loser manufactured callback return: %v", e)
	}
	if _, e = f.observer.LoadOwnedRetirement(ctx, grants[0].ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("loser prematurely retired original: %v", e)
	}
	close(releaseHost)
	select {
	case got := <-results:
		completed++
		if got.index != 0 || got.err != nil || got.snapshot.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("original winner did not finish: %+v", got)
		}
	case <-ctx.Done():
		t.Fatal("original callback did not finish")
	}
	if models[0].calls.Load() != 1 {
		t.Fatal("original physical callback did not execute exactly once")
	}
	fresh := ea1StartRuntime(t, f.reopen())
	receipt, e := fresh.LoadOwnedRetirement(t.Context(), grants[0].ID)
	if e != nil || receipt.Evidence.Grant != grants[0] || !receipt.Evidence.Completion.HostCompleted || !receipt.Valid() {
		t.Fatalf("winner retirement not exact: %+v %v", receipt, e)
	}
}

// RunPhysicalAdmissionCommitRollback exercises a genuine engine COMMIT
// failure (e.g. a deferred constraint), not a wrapper-generated ACK loss.
// The caller installs the fault before entering and removes it after
// verification. A rolled back reservation cannot burn the public ID.
func RunPhysicalAdmissionCommitRollback(t *testing.T, backing kernel.Store, removeFault func()) {
	t.Helper()
	w := &admissionACKStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing)}
	rt, e := kernel.New(kernel.Dependencies{Store: w, IDs: fixedGrantIDs{"rollback-admission-id"}})
	if e != nil {
		t.Fatal(e)
	}
	m := &ea1AdmissionModel{}
	r := admissionACKRunner(t, rt, m, nil)
	source, e := r.StartRun(t.Context(), ea1AdmissionRequest())
	if !errors.Is(e, kernel.ErrPhysicalAdmissionCommitUnknown) || !errors.Is(w.nativeErr, kernel.ErrPhysicalAdmissionCommitUnknown) ||
		source.Run.ID == "" || m.calls.Load() != 0 || w.readbacks != 1 {
		t.Fatalf("native COMMIT rollback did not remain non-executable: source=%+v model=%d readback=%d error=%v native=%v", source.Run, m.calls.Load(), w.readbacks, e, w.nativeErr)
	}
	aborted := w.grant
	observer := ea1StartRuntime(t, backing)
	if _, e = observer.LookupChildExecution(t.Context(), source.Run.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("failed COMMIT leaked a visible Grant: %v", e)
	}
	if _, e = observer.LoadOwnedRetirement(t.Context(), aborted.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("failed COMMIT fabricated retirement: %v", e)
	}
	removeFault()
	// The existing Run has not entered any callback. Only a new actual
	// successful admission may execute, including a new attempt commitment.
	out, e := r.Resume(t.Context(), source.Run.ID, source.Run.Revision)
	if e != nil || out.Run.Status != kernel.RunStatusCompleted || m.calls.Load() != 1 || w.grant.ID != aborted.ID || w.grant.ExecutionEpoch != aborted.ExecutionEpoch || w.grant.AdmissionSourceHash == aborted.AdmissionSourceHash {
		t.Fatalf("rollback did not free original uncommitted reservation: status=%s model=%d err=%v", out.Run.Status, m.calls.Load(), e)
	}
	receipt, e := observer.LoadOwnedRetirement(t.Context(), w.grant.ID)
	if e != nil || receipt.Evidence.Grant != w.grant || !receipt.Evidence.Completion.HostCompleted {
		t.Fatalf("new successful attempt retirement mismatch: %v", e)
	}
}
