package conformance

import (
	"context"
	"errors"
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
	mode      string
	cancel    context.CancelFunc
	grant     kernel.ChildExecutionGrant
	source    kernel.Snapshot
	nativeErr error
	attempted bool
	readbacks int
}

func (s *admissionACKStore) BeginOwnedPhysicalExecution(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, p kernel.OwnedPhysicalAdmission) error {
	s.grant = g
	var err error
	s.source, err = s.Load(ctx, g.ChildRunID)
	if err != nil {
		return err
	}
	defer func() { s.attempted = true }()
	switch s.mode {
	case "denied-before":
		return kernel.ErrExecutionConflict
	case "unknown-before":
		return p.CommitOutcome(errAdmissionTransport)
	}
	s.nativeErr = s.ea1AgentAdmissionStore.BeginOwnedPhysicalExecution(ctx, g, l, p)
	if s.nativeErr != nil {
		if s.mode == "unknown-after-denial" {
			// Even a misclassified response must not let the losing caller
			// adopt another invocation's persisted attempt commitment.
			return p.CommitOutcome(errAdmissionTransport)
		}
		return s.nativeErr
	}
	switch s.mode {
	case "ack-lost", "unreadable-after":
		return p.CommitOutcome(errAdmissionTransport)
	case "plain-ack-lost":
		return errAdmissionTransport
	case "untied-unknown":
		return errors.Join(kernel.ErrPhysicalAdmissionCommitUnknown, errAdmissionTransport)
	case "denied-after":
		return kernel.ErrExecutionConflict
	case "mislabelled-denial":
		return p.CommitOutcome(kernel.ErrExecutionConflict)
	case "mixed-after":
		return errors.Join(p.CommitOutcome(errAdmissionTransport), kernel.ErrExecutionConflict)
	case "cancelled-after":
		s.cancel()
		return p.CommitOutcome(errAdmissionTransport)
	default:
		return nil
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
	for _, mode := range []string{"definite-denial", "unknown-after-denial"} {
		t.Run("admission-ack/race/"+mode, func(t *testing.T) {
			runPhysicalAdmissionACKRace(t, factory, mode)
		})
	}
}

func admissionACKRunner(t *testing.T, rt *kernel.Runtime, m *ea1AdmissionModel, host *ea1AdmissionMiddleware) *agent.Runner {
	t.Helper()
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, RunExecution: &agent.RunExecution{OwnerID: "admission-ack-owner", LeaseDuration: time.Minute}, RunMiddleware: []plugin.RunMiddleware{host}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}, Tool: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1}}})
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
	w := &admissionACKStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), mode: mode, cancel: cancel}
	rt := ea1StartRuntime(t, w)
	m, host := &ea1AdmissionModel{}, &ea1AdmissionMiddleware{}
	var out kernel.Snapshot
	var err error
	var hostCalls, modelCalls, toolCalls, approvalCalls int
	r := admissionACKRunner(t, rt, m, host)
	wantStatus := kernel.RunStatusCompleted
	switch operation {
	case "start":
		out, err = r.StartRun(ctx, ea1AdmissionRequest())
	case "prepare":
		wantStatus = kernel.RunStatusRunning
		out, err = r.PrepareRun(ctx, ea1AdmissionRequest())
	case "resume":
		out, err = r.Resume(ctx, resume.prepared.Run.ID, resume.prepared.Run.Revision)
	case "approval":
		wantStatus = kernel.RunStatusRunning
		r = approval.runner(rt, true, true)
		out, err = r.ResolveApproval(ctx, approval.request.ID, approval.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	}
	if approval != nil {
		modelCalls, toolCalls = approval.physical.counts()
		modelCalls-- // original model result belongs to setup, not this approval
		hostCalls, approvalCalls = approval.host.calls, approval.approval.calls
	} else {
		modelCalls, hostCalls = int(m.calls.Load()), int(host.calls.Load())
	}
	if w.grant.ID == "" || w.grant.AdmissionOperation != operation {
		t.Fatalf("did not measure real %s admission: %+v %v", operation, w.grant, err)
	}
	fresh := ea1StartRuntime(t, reopen())
	g, lookupErr := fresh.LookupChildExecution(t.Context(), w.grant.ChildRunID)
	if mode == "confirmed" || mode == "ack-lost" {
		wantModel, wantApproval := 0, 0
		if operation == "start" || operation == "resume" {
			wantModel = 1
		}
		if operation == "approval" {
			wantApproval = 1
		}
		if err != nil || out.Run.Status != wantStatus || hostCalls != 1 || modelCalls != wantModel || toolCalls != 0 || approvalCalls != wantApproval {
			t.Fatalf("original admission not executed exactly once: status=%s host=%d model=%d tool=%d approval=%d err=%v", out.Run.Status, hostCalls, modelCalls, toolCalls, approvalCalls, err)
		}
		if !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatalf("successful attempt left fence: %+v %v", g, lookupErr)
		}
		retirement, e := fresh.LoadOwnedRetirement(t.Context(), w.grant.ID)
		if e != nil || retirement.Evidence.Grant != w.grant || !retirement.Evidence.Completion.HostCompleted || retirement.Phase != "ACKNOWLEDGED" {
			t.Fatalf("retirement not bound to original attempt: %+v %v", retirement, e)
		}
		if mode == "ack-lost" && w.readbacks < 2 {
			t.Fatalf("uncertain commit did not use an exact readback before scope binding: %d", w.readbacks)
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
	if !errors.Is(err, want) || hostCalls != 0 || modelCalls != 0 || toolCalls != 0 || approvalCalls != 0 {
		t.Fatalf("rejected admission crossed callback boundary: host=%d model=%d tool=%d approval=%d err=%v", hostCalls, modelCalls, toolCalls, approvalCalls, err)
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
	observerRunner := admissionACKRunner(t, fresh, &ea1AdmissionModel{}, &ea1AdmissionMiddleware{})
	if e = observerRunner.ReconcileRootPhysicalExecution(t.Context(), g.ChildRunID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("observer adopted admission without original ACK: %v", e)
	}
	if retained, e := fresh.LookupChildExecution(t.Context(), g.ChildRunID); e != nil || retained != g {
		t.Fatalf("observer changed original barrier: %+v %v", retained, e)
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
	entered := make(chan int, 2)
	releaseHost := make(chan struct{})
	results := make(chan result, 2)
	var runners [2]*agent.Runner
	var stores [2]*admissionACKStore
	var models [2]*ea1AdmissionModel
	var hosts [2]*ea1AdmissionMiddleware
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
		host := &ea1AdmissionMiddleware{before: func(ctx context.Context) error {
			entered <- i
			if i == 0 {
				select {
				case <-releaseHost:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}}
		m := &ea1AdmissionModel{}
		stores[i], hosts[i], models[i] = w, host, m
		runners[i] = admissionACKRunner(t, bound, m, host)
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
	case i := <-entered:
		if i != 0 {
			t.Fatal("unexpected first callback")
		}
	case got := <-results:
		completed++
		t.Fatalf("first admission rejected: %v", got.err)
	case <-ctx.Done():
		t.Fatal("first callback absent")
	}
	close(releaseAdmissions[1])
	select {
	case i := <-entered:
		t.Fatalf("losing contender entered actual Host callback %d", i)
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
	if stores[1].readbacks != wantReads || hosts[1].calls.Load() != 0 || models[1].calls.Load() != 0 {
		t.Fatalf("losing contender crossed ACK/dispatch boundary: reads=%d host=%d model=%d", stores[1].readbacks, hosts[1].calls.Load(), models[1].calls.Load())
	}
	if g, e := f.observer.LookupChildExecution(ctx, f.prepared.Run.ID); e != nil || g != grants[0] {
		t.Fatalf("losing admission changed first active grant: %+v %v", g, e)
	}
	ea1CompBarrierUnchanged(t, f.observer, f.prepared)
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
	if hosts[0].calls.Load() != 1 || models[0].calls.Load() != 1 {
		t.Fatal("original physical callback did not execute exactly once")
	}
	fresh := ea1StartRuntime(t, f.reopen())
	receipt, e := fresh.LoadOwnedRetirement(t.Context(), grants[0].ID)
	if e != nil || receipt.Evidence.Grant != grants[0] || !receipt.Evidence.Completion.HostCompleted || receipt.Phase != "ACKNOWLEDGED" {
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
	m, host := &ea1AdmissionModel{}, &ea1AdmissionMiddleware{}
	r := admissionACKRunner(t, rt, m, host)
	source, e := r.StartRun(t.Context(), ea1AdmissionRequest())
	if !errors.Is(e, kernel.ErrPhysicalAdmissionCommitUnknown) || !errors.Is(w.nativeErr, kernel.ErrPhysicalAdmissionCommitUnknown) ||
		source.Run.ID == "" || host.calls.Load() != 0 || m.calls.Load() != 0 || w.readbacks != 1 {
		t.Fatalf("native COMMIT rollback did not remain non-executable: source=%+v host=%d model=%d readback=%d error=%v native=%v", source.Run, host.calls.Load(), m.calls.Load(), w.readbacks, e, w.nativeErr)
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
	if e != nil || out.Run.Status != kernel.RunStatusCompleted || host.calls.Load() != 1 || m.calls.Load() != 1 || w.grant.ID != aborted.ID || w.grant.AdmissionSourceHash == aborted.AdmissionSourceHash {
		t.Fatalf("rollback did not free original uncommitted reservation: status=%s host=%d model=%d err=%v", out.Run.Status, host.calls.Load(), m.calls.Load(), e)
	}
	receipt, e := observer.LoadOwnedRetirement(t.Context(), w.grant.ID)
	if e != nil || receipt.Evidence.Grant != w.grant || !receipt.Evidence.Completion.HostCompleted {
		t.Fatalf("new successful attempt retirement mismatch: %v", e)
	}
}
