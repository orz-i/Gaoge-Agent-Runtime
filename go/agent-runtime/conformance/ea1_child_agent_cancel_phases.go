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

// AC-C P0/P2/P3/P4 only. Kernel Cancel has no caller-actor/route argument;
// Harness CancelSubtask has independent Host authorization and budget evidence.
func RunEA1ChildAgentCancelPhases(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	for _, v := range []string{"missing", "stale", "prepared", "native-pregrant"} {
		t.Run("P0/"+v, func(t *testing.T) { ea1ChildCancelPreflight(t, factory, v) })
	}
	for _, native := range []bool{false, true} {
		mode := map[bool]string{false: "default-child", true: "explicit-native"}[native]
		for _, kind := range []string{"host", "model", "tool"} {
			t.Run("P2/"+mode+"/"+kind, func(t *testing.T) { ea1ChildCancelUnknown(t, factory, native, kind) })
		}
		for _, phase := range []string{"P3", "P4"} {
			for _, tool := range []bool{false, true} {
				name := map[bool]string{false: "yield", true: "terminal-tool"}[tool]
				t.Run(phase+"/"+mode+"/"+name, func(t *testing.T) { ea1ChildCancelSettlement(t, factory, native, phase, tool) })
			}
		}
	}
	t.Run("P4/actual-child-Cancel-CAS-ACK", func(t *testing.T) { ea1ChildCancelACK(t, factory) })
}

type ea1ChildCancelStore struct {
	*ea1AgentAdmissionStore
	runID, event, mode                                                        string
	fault                                                                     error
	begins, marks, finishes, markAttempts, applyHits, nativeHits, controlHits int
}

func (s *ea1ChildCancelStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	e := s.ChildExecutionStore.BeginChildExecution(ctx, g)
	if e == nil {
		s.begins++
	}
	return e
}
func (s *ea1ChildCancelStore) matched(id string, m kernel.StoreMutation, event string) bool {
	if id != s.runID || m.Record.Run.ID != s.runID || event == "" {
		return false
	}
	for _, e := range m.Events {
		if e.Type == event {
			return true
		}
	}
	return false
}
func (s *ea1ChildCancelStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.matched(id, m, s.event) {
		s.applyHits++
		return kernel.Snapshot{}, s.fault
	}
	return s.Store.Apply(ctx, id, rev, m)
}
func (s *ea1ChildCancelStore) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.matched(l.RunID, m, s.event) {
		s.nativeHits++
		return kernel.Snapshot{}, s.fault
	}
	return s.ExecutionStore.ApplyExecution(ctx, l, rev, m)
}
func (s *ea1ChildCancelStore) ApplyControl(ctx context.Context, id string, rev uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	out, revoked, e := s.ControlStore.ApplyControl(ctx, id, rev, g, m)
	if e == nil && s.mode == "cancel-ack" && g.Kind == kernel.ControlCancel && s.matched(id, m, "run.cancelled") {
		s.controlHits++
		return kernel.Snapshot{}, false, s.fault
	}
	return out, revoked, e
}
func (s *ea1ChildCancelStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if g.ChildRunID != s.runID {
		return s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
	}
	s.markAttempts++
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
func (s *ea1ChildCancelStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	e := s.ChildExecutionStore.FinishChildExecution(ctx, g)
	if e == nil {
		s.finishes++
	}
	return e
}

type ea1ChildCancelHost struct {
	request  agent.StartRequest
	physical *ea1StartSettlementPhysical
	fault    error
	sends    int
}

func (*ea1ChildCancelHost) Name() string { return "ea1.original.child.cancel.unknown" }
func (h *ea1ChildCancelHost) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if i.Operation != plugin.RunResume || i.RunID != h.request.ID || i.ParentRunID != h.request.ParentRunID || i.Actor != h.request.Actor || i.Thread != h.request.Thread || i.Goal != h.request.Goal {
		return kernel.Snapshot{}, agent.ErrInvalidRequest
	}
	h.physical.mu.Lock()
	h.physical.hostCalls++
	h.physical.mu.Unlock()
	if h.fault != nil {
		h.sends++
		return kernel.Snapshot{}, h.fault
	}
	return next(ctx)
}

type ea1ChildCancelFixture struct {
	native                                 bool
	t                                      *testing.T
	store                                  *ea1ChildCancelStore
	rt                                     *kernel.Runtime
	reopen                                 func() (kernel.Store, kernel.Store)
	request                                agent.StartRequest
	parent, grandparent, created, prepared kernel.Snapshot
	physical                               *ea1StartSettlementPhysical
	host                                   *ea1ChildCancelHost
	definition                             tools.Definition
	registry                               *tools.Registry
}

func ea1NewChildCancelFixture(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), tool, native bool) *ea1ChildCancelFixture {
	backing, reopen := factory(t)
	st := &ea1ChildCancelStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), fault: errors.New("actual original Child cancellation fixture cut")}
	rt := ea1StartRuntime(t, st)
	f := &ea1ChildCancelFixture{t: t, native: native, store: st, rt: rt, reopen: reopen}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}
	var e error
	f.grandparent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "grandparent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "original grandparent", State: json.RawMessage("{}")})
	if e != nil {
		t.Fatal(e)
	}
	f.parent, e = rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", ParentRunID: f.grandparent.Run.ID, Kind: "workflow", Actor: actor, Thread: thread, Goal: "original parent", State: json.RawMessage("{}")})
	if e != nil {
		t.Fatal(e)
	}
	f.request = agent.StartRequest{ID: "child-cancel-source", RequestID: "original-child-cancel-source", ParentRunID: f.parent.Run.ID, OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child-cancel-source","context":"isolated"}`)), Actor: actor, Thread: thread, Goal: "original child cancellation source", Instructions: "Preserve original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if tool {
		f.request.ToolKeys = []string{"effect"}
	}
	f.physical = &ea1StartSettlementPhysical{rt: rt, tool: tool}
	f.definition = tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	f.registry, e = tools.NewRegistry([]tools.Registration{{Definition: f.definition, Handler: tools.HandlerFunc(f.physical.execute)}})
	if e != nil {
		t.Fatal(e)
	}
	st.runID = f.request.ID
	f.host = &ea1ChildCancelHost{request: f.request, physical: f.physical}
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
	if len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || f.physical.hostCalls != 0 || st.begins != 1 || st.marks != 1 || st.finishes != 1 {
		t.Fatal("Prepare did not finish its separate zero-send callback")
	}
	st.begins, st.marks, st.finishes, st.markAttempts = 0, 0, 0, 0
	ea1CompBarrierUnchanged(t, rt, f.parent)
	ea1CompBarrierUnchanged(t, rt, f.grandparent)
	return f
}
func (f *ea1ChildCancelFixture) runner(rt *kernel.Runtime, owner string, host bool) *agent.Runner {
	var middleware []plugin.RunMiddleware
	if host {
		middleware = []plugin.RunMiddleware{f.host}
	}
	deps := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.registry, Executor: f.registry, RunMiddleware: middleware, ToolMiddleware: []plugin.ToolMiddleware{f.physical},
		Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if f.native {
		deps.RunExecution = &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}
	}
	r, e := agent.NewRunner(deps)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}

func ea1ChildCancelCommitted(t *testing.T, rt *kernel.Runtime, before, after kernel.Snapshot) {
	t.Helper()
	expected := before.Run
	expected.Status, expected.Revision = kernel.RunStatusCancelled, before.Run.Revision+1
	expected.ErrorCode, expected.ErrorDetail = "run.cancelled", "original cancel"
	expected.UpdatedAt, expected.EndedAt = after.Run.UpdatedAt, after.Run.EndedAt
	if !ea1RecoveryJSONEqual(expected, after.Run) || after.Run.EndedAt == nil || after.Run.UpdatedAt.Before(before.Run.UpdatedAt) || !ea1RecoveryJSONEqual(before.State, after.State) || after.Checkpoint != nil || after.Result != nil || after.EventHead != before.EventHead+1 {
		t.Fatal("child Cancel changed immutable source/state/budget or transition")
	}
	events, e := rt.ListEvents(t.Context(), after.Run.ID, before.EventHead, 10)
	if e != nil || len(events) != 1 || events[0].Type != "run.cancelled" || events[0].Message != "original cancel" || events[0].Seq != after.EventHead {
		t.Fatal("child Cancel journal not exactly once", e)
	}
	ea1CompBarrierUnchanged(t, rt, after)
	ea1CancelRejected(t, rt, after, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
}
func ea1ChildCancelPreflight(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), v string) {
	f := ea1NewChildCancelFixture(t, factory, false, true)
	if v == "missing" || v == "stale" {
		id, rev, want := f.request.ID, f.prepared.Run.Revision+1, kernel.ErrConflict
		if v == "missing" {
			id = "missing-child"
			rev = f.prepared.Run.Revision
			want = kernel.ErrNotFound
		}
		_, e := f.rt.Cancel(t.Context(), id, rev, "original cancel")
		if !errors.Is(e, want) || !kernel.IsControlNoEffect(e) {
			t.Fatal("child Cancel preflight priority", e)
		}
		ea1CompBarrierUnchanged(t, f.rt, f.prepared)
	} else {
		var lease kernel.ExecutionLease
		if v == "native-pregrant" {
			var e error
			lease, e = f.rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "original-cancel-candidate", LeaseDuration: 2 * time.Second})
			if e != nil {
				t.Fatal(e)
			}
		}
		out, e := f.rt.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
		if e != nil {
			t.Fatal(e)
		}
		ea1ChildCancelCommitted(t, f.rt, f.prepared, out)
		if v == "native-pregrant" {
			_, e = f.rt.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 2 * time.Second})
			if !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatal("successful Cancel failed to revoke original epoch", e)
			}
		}
		_, e = f.runner(f.rt, "late-owner", true).Resume(t.Context(), f.request.ID, out.Run.Revision)
		if !errors.Is(e, agent.ErrRunTerminal) {
			t.Fatal("cancelled child dispatched", e)
		}
	}
	ea1CompBarrierUnchanged(t, f.rt, f.parent)
	ea1CompBarrierUnchanged(t, f.rt, f.grandparent)
	if len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || f.physical.hostCalls != 0 {
		t.Fatal("preflight Cancel dispatched callback")
	}
}
func ea1ChildCancelUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native bool, kind string) {
	f := ea1NewChildCancelFixture(t, factory, kind == "tool", native)
	switch kind {
	case "host":
		f.host.fault = f.store.fault
	case "model":
		f.store.event = "agent.model_invocation.completed"
	default:
		f.store.event = "tool.completed"
	}
	_, e := f.runner(f.rt, "original-unknown-owner", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, f.store.fault) || !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatal("actual unknown child cut missing", e)
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ChildRunID != f.request.ID || g.ExpectedRevision != f.prepared.Run.Revision {
		t.Fatal("unknown original child scope missing", e)
	}
	if kind == "host" {
		if f.host.sends != 1 || f.store.applyHits+f.store.nativeHits != 0 || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatal("host send fixture substituted receipt")
		}
	} else {
		wantApply, wantNative := 1, 0
		if native {
			wantApply, wantNative = 0, 1
		}
		if f.store.applyHits != wantApply || f.store.nativeHits != wantNative {
			t.Fatal("receipt fault missed actual ordinary/native Store port")
		}
		ea1ChildCancelUnknownSource(t, f, before, kind)
	}
	ea1ChildRecoveryPhaseControls(t, f.rt, before, f.parent, f.grandparent, g)
	// Explicit native Resume acquires before Host. Default Child never claims
	// a Root-native lease; only explicit mode makes a real Store TTL assertion.
	if native {
		lease := f.store.acquired
		if lease.RunID != f.request.ID || lease.OwnerID != "original-unknown-owner" {
			t.Fatal("unknown missing original native owner")
		}
		if e = f.store.CheckExecution(t.Context(), f.request.ID, &lease); e != nil {
			t.Fatal("rejected Cancel revoked live owner", e)
		}
		ea1AwaitControlExpiry(t, lease)
	} else if f.store.acquireCalls != 0 {
		t.Fatal("default child fabricated Root native owner")
	}
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	for range 2 {
		ea1ChildRecoveryPhaseControls(t, ar, before, f.parent, f.grandparent, g)
	}
	if native {
		lease := f.store.acquired
		if e = a.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &lease); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("original native token revived", e)
		}
	}
	if e = f.runner(ar, "recovery-only", true).ReconcileChildPhysicalGrant(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatal("unknown child retired by recovery", e)
	}
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	if kind != "host" {
		ea1ChildCancelUnknownSource(t, f, before, kind)
	} else if f.host.sends != 1 {
		t.Fatal("unknown host replayed")
	}
	if f.physical.hostCalls != 1 || f.store.marks != 0 || f.store.finishes != 0 {
		t.Fatal("unknown callback was replayed or marked")
	}
}
func ea1ChildCancelUnknownSource(t *testing.T, f *ea1ChildCancelFixture, s kernel.Snapshot, kind string) {
	t.Helper()
	p := f.physical
	if len(p.requests) != 1 || len(p.responses) != 1 || len(p.intents) != 1 || p.hostCalls != 1 {
		t.Fatal("original Model source count changed")
	}
	captured := p.intents[0]
	actual := p.requests[0]
	canonical := model.CloneRequest(captured.Request)
	canonical.InvocationID = ""
	raw, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{canonical, captured.CatalogFingerprints, captured.Provider, captured.BindingFingerprint})
	hash := sha256.Sum256(raw)
	view, viewErr := agent.ViewState(s)
	if e != nil || viewErr != nil || len(view.ModelInvocations) != 1 {
		t.Fatal("original pending source missing")
	}
	inv := view.ModelInvocations[0]
	if captured.RequestHash != hex.EncodeToString(hash[:]) || !ea1RecoveryJSONEqual(actual, captured.Request) || inv.ID != captured.ID || inv.RequestHash != captured.RequestHash || inv.Provider != captured.Provider || inv.BindingFingerprint != captured.BindingFingerprint || inv.ExecutionAttempt != captured.ExecutionAttempt || inv.DispatchAttempt != captured.DispatchAttempt || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 || inv.SourceRevision != captured.SourceRevision || inv.SourceRevision < f.prepared.Run.Revision {
		t.Fatal("unknown source tuple/attempt changed")
	}
	var state struct {
		OriginFingerprint string `json:"originFingerprint"`
		ToolDispatchID    string `json:"toolDispatchID"`
	}
	if json.Unmarshal(s.State, &state) != nil || state.OriginFingerprint != f.request.OriginFingerprint || s.Run.ParentRunID != f.parent.Run.ID || s.Run.Actor != f.request.Actor || s.Run.Thread != f.request.Thread || s.Run.RequestID != f.request.RequestID || s.Run.Goal != f.request.Goal || !ea1RecoveryJSONEqual(view.Budget.Limits, f.request.Limits) {
		t.Fatal("unknown child original origin changed")
	}
	if kind == "model" {
		if inv.Status != agent.ModelInvocationPending || inv.CompletedAt != nil || len(p.toolRequests) != 0 || !ea1RecoveryJSONEqual(inv.Response, model.Response{}) {
			t.Fatal("missing Model receipt fabricated")
		}
	} else {
		if inv.Status != agent.ModelInvocationConsumed || len(p.toolRequests) != 1 || len(p.receipts) != 1 || state.ToolDispatchID != "original-call" {
			t.Fatal("unknown Tool lost original dispatch")
		}
		ea1StartSettlementJSONEqual(t, "original consumed Model response", inv.Response, p.responses[0])
		want := tools.ExecutionRequest{RunID: f.request.ID, Call: tools.Call{ID: "original-call", ToolKey: "effect", Name: f.definition.Name, Arguments: json.RawMessage(`{"source":"original"}`)}}
		if !ea1RecoveryJSONEqual(p.toolRequests[0], want) || len(p.toolInvocations) != 1 || !ea1RecoveryJSONEqual(p.toolInvocations[0].Definition, f.definition) || !ea1RecoveryJSONEqual(p.toolInvocations[0].Request, want) {
			t.Fatal("unknown actual Tool original source changed")
		}
		result := tools.ExecutionResult{Content: json.RawMessage(`{"committed":"original"}`), Receipt: tools.Receipt{ExecutionID: "original-call", Disposition: "committed"}}
		ea1StartSettlementJSONEqual(t, "actual Tool return before failed receipt", p.receipts[0], result)
	}
}
func ea1ChildCancelSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native bool, phase string, tool bool) {
	f := ea1NewChildCancelFixture(t, factory, tool, native)
	f.store.mode = phase
	f.store.markAttempts = 0
	_, e := f.runner(f.rt, "original-settlement-owner", true).Resume(t.Context(), f.request.ID, f.prepared.Run.Revision)
	if !errors.Is(e, f.store.fault) || !errors.Is(e, kernel.ErrChildExecutionBusy) || f.store.markAttempts != 1 || f.store.finishes != 0 {
		t.Fatal("actual original Mark cut not reached exactly once", e)
	}
	if (phase == "P3" && f.store.marks != 0) || (phase == "P4" && f.store.marks != 1) {
		t.Fatal("wrong original marker commit")
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ExpectedRevision != f.prepared.Run.Revision {
		t.Fatal("original child settlement scope absent", e)
	}
	ea1AssertChildRecoveryPhaseReceipt(t, f.physical, f.request, f.definition, before, tool)
	ea1ChildRecoveryPhaseControls(t, f.rt, before, f.parent, f.grandparent, g)
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	ea1ChildRecoveryPhaseControls(t, ar, before, f.parent, f.grandparent, g)
	e = f.runner(ar, "fresh-recovery", true).ReconcileChildPhysicalGrant(t.Context(), f.request.ID)
	if phase == "P3" {
		if !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("original receipt fabricated missing marker", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	} else {
		if e != nil {
			t.Fatal("original committed marker could not recover", e)
		}
		if _, lookupErr := ar.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(lookupErr, kernel.ErrNotFound) {
			t.Fatal("original marker recovery did not retire scope", lookupErr)
		}
		if replayErr := f.runner(ar, "second-inspection", true).ReconcileChildPhysicalGrant(t.Context(), f.request.ID); !errors.Is(replayErr, kernel.ErrNotFound) {
			t.Fatal("original marker replayed", replayErr)
		}
		if tool {
			ea1CancelRejected(t, ar, before, kernel.ChildExecutionGrant{}, kernel.ErrTerminal)
		} else {
			out, e := ar.Cancel(t.Context(), f.request.ID, before.Run.Revision, "original cancel")
			if e != nil {
				t.Fatal(e)
			}
			ea1ChildCancelCommitted(t, ar, before, out)
		}
		ea1CompBarrierUnchanged(t, ar, f.parent)
		ea1CompBarrierUnchanged(t, ar, f.grandparent)
	}
	ea1AssertChildRecoveryPhaseReceipt(t, f.physical, f.request, f.definition, before, tool)
}
func ea1ChildCancelACK(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	f := ea1NewChildCancelFixture(t, factory, false, true)
	lease, e := f.rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "original-cancel-owner", LeaseDuration: 2 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	f.store.mode = "cancel-ack"
	_, e = f.rt.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
	if !errors.Is(e, f.store.fault) || kernel.IsControlNoEffect(e) || f.store.controlHits != 1 {
		t.Fatal("actual Child Cancel CAS/ACK cut absent", e)
	}
	committed, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	ea1ChildCancelCommitted(t, f.rt, f.prepared, committed)
	ea1CompBarrierUnchanged(t, f.rt, f.parent)
	ea1CompBarrierUnchanged(t, f.rt, f.grandparent)
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	_, e = ar.Cancel(t.Context(), f.request.ID, f.prepared.Run.Revision, "original cancel")
	if !errors.Is(e, kernel.ErrConflict) {
		t.Fatal("stale original Cancel retry accepted", e)
	}
	ea1ChildCancelCommitted(t, ar, f.prepared, committed)
	_, e = ar.RenewExecution(t.Context(), kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 2 * time.Second})
	if !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatal("original cancelled native token revived", e)
	}
	ea1CompBarrierUnchanged(t, ar, f.parent)
	ea1CompBarrierUnchanged(t, ar, f.grandparent)
	if f.store.controlHits != 1 || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || f.physical.hostCalls != 0 {
		t.Fatal("Cancel replayed mutation or physical callbacks")
	}
}
