package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Only AR-R P0/P1/P2. PrepareRun establishes source without Model/Tool sends;
// all measured admission/callback/dispatch cutpoints belong to actual Resume.
// Root has no ancestors and Resume has no caller-actor authentication parameter.
func RunEA1RootAgentResumePhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, vector := range []string{"missing", "stale", "terminal", "admission-revision", "admission-cancel"} {
		t.Run("P0/"+vector, func(t *testing.T) { ea1RootResumePreflight(t, factory, vector) })
	}
	t.Run("P1/admitted-before-dispatch", func(t *testing.T) { ea1RootResumeBarrier(t, factory) })
	for _, kind := range []string{"model", "tool", "host"} {
		t.Run("P2/"+kind, func(t *testing.T) { ea1RootResumeUnknown(t, factory, kind) })
	}
}

type ea1ResumeFixture struct {
	clockOffset       time.Duration
	t                 *testing.T
	peer              kernel.Store
	reopen            func() kernel.Store
	store             *ea1AgentAdmissionStore
	rt, observer      *kernel.Runtime
	physical          *ea1RootRecoveryPhysical
	catalog           *tools.Registry
	request           agent.StartRequest
	created, prepared kernel.Snapshot
	host              *ea1ResumeHost
}
type ea1ResumeHost struct {
	t            *testing.T
	request      agent.StartRequest
	calls, sends int
	before       func(context.Context) error
	unknown      error
}

func (*ea1ResumeHost) Name() string { return "ea1.actual.resume.host" }
func (h *ea1ResumeHost) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	h.calls++
	if inv.Operation != plugin.RunResume || inv.RunID != h.request.ID || inv.Actor != h.request.Actor || inv.Thread != h.request.Thread || inv.Goal != h.request.Goal || inv.ParentRunID != "" {
		h.t.Fatal("callback did not preserve actual original Resume identity")
	}
	if h.before != nil {
		if e := h.before(ctx); e != nil {
			return kernel.Snapshot{}, e
		}
	}
	if h.unknown != nil {
		h.sends++
		return kernel.Snapshot{}, h.unknown
	}
	return next(ctx)
}
func ea1NewResumeFixture(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), tool bool) *ea1ResumeFixture {
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
func (f *ea1ResumeFixture) runner(rt *kernel.Runtime, owner string, withHost bool) *agent.Runner {
	var middleware []plugin.RunMiddleware
	if withHost {
		middleware = []plugin.RunMiddleware{f.host}
	}
	r, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, RunMiddleware: middleware, Clock: ea1UnknownAgentClock{offset: f.clockOffset}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}})
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ResumeFixture) zero() {
	f.t.Helper()
	m, n := f.physical.counts()
	if m != 0 || n != 0 || f.host.calls != 0 || f.host.sends != 0 {
		f.t.Fatal("rejected Resume dispatched callback")
	}
}
func ea1RootResumePreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), vector string) {
	f := ea1NewResumeFixture(t, factory, false)
	runner := f.runner(f.rt, "resume-owner", true)
	id, revision := f.request.ID, f.prepared.Run.Revision
	winner := f.prepared
	want := kernel.ErrConflict
	hits := 0
	switch vector {
	case "missing":
		id = "missing-root"
		want = kernel.ErrNotFound
	case "stale":
		revision++
	case "terminal":
		var e error
		winner, e = f.observer.Cancel(t.Context(), id, revision, "terminal before Resume")
		if e != nil {
			t.Fatal(e)
		}
		revision = winner.Run.Revision
		want = agent.ErrRunTerminal
	case "admission-revision", "admission-cancel":
		want = kernel.ErrExecutionConflict
		f.store.beforeOwned = func(ctx context.Context, g kernel.ChildExecutionGrant) error {
			hits++
			if g.AdmissionOperation != "resume" || g.ExpectedRevision != f.prepared.Run.Revision {
				t.Fatal("not actual Resume admission")
			}
			var e error
			if vector == "admission-revision" {
				bound, e2 := f.observer.WithExecution(f.store.acquired)
				if e2 != nil {
					t.Fatal(e2)
				}
				winner, e = bound.Apply(ctx, id, revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: f.prepared.State, Checkpoint: f.prepared.Checkpoint})
			} else {
				winner, e = f.observer.Cancel(ctx, id, revision, "Cancel wins actual admission")
			}
			if e != nil {
				t.Fatalf("legitimate competing mutation failed: %v", e)
			}
			return nil
		}
	}
	_, err := runner.Resume(t.Context(), id, revision)
	if !errors.Is(err, want) {
		t.Fatalf("%s rejected at wrong boundary: %v want %v", vector, err, want)
	}
	f.zero()
	ea1CompBarrierUnchanged(t, f.observer, winner)
	if _, e := f.observer.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("rejected Resume retained/adopted scope: %v", e)
	}
	if vector == "admission-revision" || vector == "admission-cancel" {
		if hits != 1 {
			t.Fatal("actual Store admission race not reached")
		}
	} else if hits != 0 {
		t.Fatal("preflight entered Store admission")
	}
}
func ea1RootResumeBarrier(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewResumeFixture(t, factory, false)
	runner := f.runner(f.rt, "resume-owner", true)
	stop := errors.New("actual Resume Host held before physical dispatch")
	checkpoints := 0
	var original kernel.ChildExecutionGrant
	check := func(ctx context.Context, hostEntered bool) {
		checkpoints++
		before, e := f.observer.Load(ctx, f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		if !ea1RecoveryJSONEqual(before, f.prepared) || !runner.VerifyFrozenStartRequest(before, f.request) {
			t.Fatal("admission changed full prepared source")
		}
		g, e := f.observer.LookupChildExecution(ctx, f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		lease := f.store.acquired
		if g.AdmissionOperation != "resume" || g.ExpectedRevision != f.prepared.Run.Revision || g.ChildRunID != f.request.ID || g.ExecutionOwnerID != "resume-owner" || g.ExecutionLeaseID != lease.LeaseID || g.ExecutionEpoch != lease.Epoch || len(g.AdmissionSourceHash) != 64 {
			t.Fatal("wrong original Resume grant/native authority")
		}
		if original.ID == "" {
			original = g
		} else if g != original {
			t.Fatal("callback borrowed another scope")
		}
		if f.host.calls != map[bool]int{false: 0, true: 1}[hostEntered] {
			t.Fatal("wrong callback cutpoint")
		}
		m, n := f.physical.counts()
		if m != 0 || n != 0 {
			t.Fatal("P1 already physically dispatched")
		}
		unchanged := func() {
			ea1CompBarrierUnchanged(t, f.observer, before)
			same, e := f.observer.LookupChildExecution(ctx, f.request.ID)
			if e != nil || same != g {
				t.Fatal("competing control changed scope")
			}
			if e = f.peer.(kernel.ExecutionStore).CheckExecution(ctx, f.request.ID, &lease); e != nil {
				t.Fatalf("original native authority revoked: %v", e)
			}
			if _, e = f.observer.LookupOwnedPhysicalCallbackReturn(ctx, g); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatalf("active callback fabricated return fact: %v", e)
			}
		}
		unchanged()
		_, e = f.observer.Apply(ctx, f.request.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
		if !errors.Is(e, kernel.ErrExecutionRequired) {
			t.Fatalf("unbound write gate: %v", e)
		}
		unchanged()
		bound, e := f.observer.WithExecution(lease)
		if e != nil {
			t.Fatal(e)
		}
		_, e = bound.Apply(ctx, f.request.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("original lease without physicalpermit escaped: %v", e)
		}
		unchanged()
		_, e = f.observer.Cancel(ctx, f.request.ID, before.Run.Revision, "competing cancel")
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("cancel escaped: %v", e)
		}
		unchanged()
		_, e = f.observer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "foreign", LeaseDuration: 2 * time.Second})
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Acquire escaped: %v", e)
		}
		unchanged()
		e = f.observer.ReleaseExecution(ctx, lease)
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("Release escaped: %v", e)
		}
		unchanged()
	}
	f.store.afterOwned = func(ctx context.Context, _ kernel.ChildExecutionGrant) error { check(ctx, false); return nil }
	f.host.before = func(ctx context.Context) error { check(ctx, true); return stop }
	_, e := runner.Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, stop) || checkpoints != 2 || f.host.calls != 1 {
		t.Fatalf("actual Resume P1 not hit: %v checks=%d", e, checkpoints)
	}
	ea1CompBarrierUnchanged(t, f.observer, f.prepared)
	g, e := f.observer.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g != original {
		t.Fatal("opaque Host error lost original scope")
	}
	completion, e := f.observer.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || completion.HostCompleted {
		t.Fatalf("opaque Host failure fabricated completion: %v", e)
	}
	m, n := f.physical.counts()
	if m != 0 || n != 0 {
		t.Fatal("P1 physically dispatched")
	}
}
func ea1RootResumeUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	f := ea1NewResumeFixture(t, factory, kind == "tool")
	injected := errors.New("actual Resume physical outcome missing")
	event := "agent.model_invocation.completed"
	if kind == "tool" {
		event = "tool.completed"
	}
	fault := &ea1AgentReceiptFault{Store: f.store, ExecutionStore: f.store, ControlStore: f.store, ChildExecutionStore: f.store, ExecutionQuarantineStore: f.store, OwnedPhysicalExecutionStore: f.store, event: event, injected: injected}
	rt := ea1StartRuntime(t, fault)
	f.physical.rt = rt
	if kind == "host" {
		f.host.unknown = injected
	}
	runner := f.runner(rt, "resume-owner", true)
	_, e := runner.Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, injected) || f.host.calls != 1 {
		t.Fatalf("actual Resume unknown cut missing: %v", e)
	}
	before, e := rt.Load(t.Context(), f.request.ID)
	if e != nil || !runner.VerifyFrozenStartRequest(before, f.request) {
		t.Fatal("original Resume source lost")
	}
	g, e := rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	lease := f.store.acquired
	if g.AdmissionOperation != "resume" || g.ExpectedRevision != f.prepared.Run.Revision || g.ExecutionOwnerID != lease.OwnerID || g.ExecutionLeaseID != lease.LeaseID || g.ExecutionEpoch != lease.Epoch {
		t.Fatal("unknown lost original Resume authority")
	}
	completion, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || completion.HostCompleted {
		t.Fatalf("actual opaque Host error not retained: %v", e)
	}
	m, n := f.physical.counts()
	if kind == "host" {
		if m != 0 || n != 0 || f.host.sends != 1 || fault.failures != 0 {
			t.Fatal("Host fixture substituted Model/Tool send")
		}
	} else {
		if fault.failures != 1 || m != 1 || (kind == "tool" && n != 1) || (kind == "model" && n != 0) {
			t.Fatal("actual Resume dispatch not reached")
		}
		view, e := agent.ViewState(before)
		if e != nil || len(view.ModelInvocations) != 1 {
			t.Fatal("original invocation missing")
		}
		inv := view.ModelInvocations[0]
		f.physical.assertOriginal(t, f.request, inv)
		canonical := model.CloneRequest(f.physical.intents[0].Request)
		canonical.InvocationID = ""
		sourceBytes, sourceErr := json.Marshal(struct {
			Request             model.Request `json:"request"`
			CatalogFingerprints []string      `json:"catalogFingerprints"`
			Provider            string        `json:"provider"`
			BindingFingerprint  string        `json:"bindingFingerprint"`
		}{canonical, inv.CatalogFingerprints, inv.Provider, inv.BindingFingerprint})
		hash := sha256.Sum256(sourceBytes)
		if sourceErr != nil || inv.RequestHash != hex.EncodeToString(hash[:]) || inv.SourceRevision < f.prepared.Run.Revision {
			t.Fatal("Resume invocation not exact original source hash")
		}
		if kind == "model" && (inv.Status != agent.ModelInvocationPending || inv.CompletedAt != nil || !ea1RecoveryJSONEqual(inv.Response, model.Response{})) {
			t.Fatal("missing Model receipt fabricated as completed")
		}
		if kind == "tool" {
			var marker struct {
				ToolDispatchID string `json:"toolDispatchID"`
			}
			if json.Unmarshal(before.State, &marker) != nil || marker.ToolDispatchID != "original-tool-call" {
				t.Fatal("original unresolved Tool intent missing")
			}
		}
		if inv.Provider != f.physical.ProviderName() || inv.BindingFingerprint != f.physical.BindingFingerprintForRequest(inv.Request) || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
			t.Fatal("original request provider/attempt lost")
		}
		if kind == "tool" {
			ea1StartSettlementJSONEqual(t, "prior consumed Model response", inv.Response, f.physical.response)
		}
	}
	delay := time.Until(lease.ExpiresAt.Add(150 * time.Millisecond))
	if delay > 5*time.Second {
		t.Fatal("unbounded native lease fixture")
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	f.physical.rt = freshRT
	// Feature claim/RetryAfter time is advanced independently; native expiry
	// above was awaited against the real authoritative Store clock.
	f.clockOffset = 3 * time.Minute
	next := f.runner(freshRT, "fresh-owner", true)
	if e = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("Store native TTL not expired: %v", e)
	}
	for range 2 {
		_, e = next.Resume(t.Context(), f.request.ID, before.Run.Revision)
		want := agent.ErrRootOutcomeUnknown
		if kind == "model" {
			want = agent.ErrModelOutcomeUnknown
		} else if kind == "tool" {
			want = agent.ErrToolOutcomeUnknown
		}
		if !errors.Is(e, want) {
			t.Fatalf("Resume wrong unknown boundary: %v want %v", e, want)
		}
		ea1CompBarrierUnchanged(t, freshRT, before)
		same, err := freshRT.LookupChildExecution(t.Context(), f.request.ID)
		if err != nil || same != g {
			t.Fatal("TTL recovery changed original scope")
		}
		c, err := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
		if err != nil || !ea1RecoveryJSONEqual(c, completion) {
			t.Fatal("TTL upgraded Host unknown")
		}
		a, b := f.physical.counts()
		if a != m || b != n || f.host.calls != 1 || (kind == "host" && f.host.sends != 1) {
			t.Fatal("unknown Resume replayed physical callback")
		}
		if err = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatal("recovery revived original epoch")
		}
	}
}
