package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1ChildAgentStartPhases covers only AC-S P0/P1/P2/P3/P5, not the
// previously signed-off P4. An actual child Start is admitted under every
// ancestor; its legacy ChildGrant is not a Root-owned native lease binding.
// Compensation/shared-budget election are not parameters of this entry.
func RunEA1ChildAgentStartPhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, v := range []string{"origin", "actor", "thread", "missing-parent", "terminal-parent", "preflight", "duplicate", "stale-admission", "parent-wins"} {
		t.Run("P0/"+v, func(t *testing.T) { ea1ChildStartPreflight(t, factory, v) })
	}
	t.Run("P1/host-before-next", func(t *testing.T) { ea1ChildStartPhase(t, factory, "P1", "host") })
	for _, kind := range []string{"host", "model", "tool"} {
		t.Run("P2/"+kind, func(t *testing.T) { ea1ChildStartPhase(t, factory, "P2", kind) })
	}
	for _, kind := range []string{"model", "tool"} {
		t.Run("P3/"+kind, func(t *testing.T) { ea1ChildStartPhase(t, factory, "P3", kind) })
	}
	for _, kind := range []string{"host", "model", "tool", "deadline"} {
		t.Run("P5/"+kind, func(t *testing.T) { ea1ChildStartPhase(t, factory, "P5", kind) })
	}
}

type ea1ChildStartStore struct {
	*ea1ElectionStore
	beforeAdmission   func(kernel.ChildExecutionGrant)
	beforeMark        func(kernel.ChildExecutionGrant)
	admissions, marks int
}

func (s *ea1ChildStartStore) BeginChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.beforeAdmission != nil {
		s.beforeAdmission(g)
	}
	err := s.ChildExecutionStore.BeginChildExecution(ctx, g)
	if err == nil {
		s.admissions++
	}
	return err
}
func (s *ea1ChildStartStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.beforeMark != nil {
		s.beforeMark(g)
	}
	err := s.ChildExecutionStore.MarkChildExecutionSettled(ctx, g, p)
	if err == nil {
		s.marks++
	}
	return err
}

type ea1ChildStartHost struct {
	request      agent.StartRequest
	calls, sends int
	hook         func()
	failure      error
	sendUnknown  bool
}

func (*ea1ChildStartHost) Name() string { return "ea1.actual.child.start" }
func (h *ea1ChildStartHost) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if inv.Operation != plugin.RunStart || inv.RunID != h.request.ID || inv.ParentRunID != h.request.ParentRunID || inv.Actor != h.request.Actor || inv.Thread != h.request.Thread {
		return kernel.Snapshot{}, agent.ErrInvalidRequest
	}
	h.calls++
	if h.hook != nil {
		h.hook()
	}
	if h.failure != nil {
		if h.sendUnknown {
			h.sends++
		}
		return kernel.Snapshot{}, h.failure
	}
	return next(ctx)
}

type ea1ChildStartFixture struct {
	t            *testing.T
	store        *ea1ChildStartStore
	rt, peer     *kernel.Runtime
	peerStore    kernel.Store
	reopen       func() kernel.Store
	physical     *ea1ElectionPhysical
	catalog      *tools.Registry
	host         *ea1ChildStartHost
	request      agent.StartRequest
	ancestors    []kernel.Snapshot
	created      kernel.Snapshot
	preflightErr error
}

func ea1NewChildStart(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), withTool, deadline bool) *ea1ChildStartFixture {
	backing, peer, reopen := factory(t)
	fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), injected: errors.New("original child receipt unavailable")}
	f := &ea1ChildStartFixture{t: t, store: &ea1ChildStartStore{ea1ElectionStore: &ea1ElectionStore{ea1AgentReceiptFault: fault}}, peer: ea1StartRuntime(t, peer), peerStore: peer, reopen: reopen}
	f.rt = ea1StartRuntime(t, f.store)
	f.request = agent.StartRequest{ID: "actual-start-child", ParentRunID: "middle", OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child"}`)), RequestID: "original-child-effect", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original Child Start", Instructions: "Preserve child source", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if deadline {
		due := time.Now().UTC().Add(time.Second)
		f.request.DeadlineAt = &due
	}
	if withTool {
		f.request.ToolKeys = []string{"effect"}
	}
	parentID := ""
	for _, id := range []string{"root", "middle"} {
		s, e := f.rt.Create(t.Context(), kernel.CreateRequest{ID: id, ParentRunID: parentID, Kind: "workflow", Actor: f.request.Actor, Thread: f.request.Thread, Goal: "original " + id, State: json.RawMessage(`{"original":true}`), DeadlineAt: f.request.DeadlineAt})
		if e != nil {
			t.Fatal(e)
		}
		f.ancestors = append(f.ancestors, s)
		parentID = id
	}
	f.physical = &ea1ElectionPhysical{ea1StartSettlementPhysical: &ea1StartSettlementPhysical{rt: f.rt, tool: withTool}}
	var err error
	f.catalog, err = tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "Original Effect", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true}, Handler: tools.HandlerFunc(f.physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	f.host = &ea1ChildStartHost{request: f.request}
	f.store.created = func(s kernel.Snapshot) {
		if s.Run.ID == f.request.ID {
			f.created = s
			if !f.runner(f.rt, "source-check", false).VerifyFrozenStartRequest(s, f.request) {
				t.Fatal("actual child Create changed frozen source")
			}
		}
	}
	return f
}
func (f *ea1ChildStartFixture) runner(rt *kernel.Runtime, owner string, host bool) *agent.Runner {
	d := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if host {
		d.RunMiddleware = []plugin.RunMiddleware{f.host}
		d.StartControlPreflight = func(ctx context.Context, r agent.StartRequest) error {
			if f.preflightErr != nil {
				return f.preflightErr
			}
			if !reflect.DeepEqual(r, f.request) {
				return agent.ErrInvalidRequest
			}
			for _, want := range f.ancestors {
				got, e := rt.Load(ctx, want.Run.ID)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(got, want) {
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
func (f *ea1ChildStartFixture) load(rt *kernel.Runtime) kernel.Snapshot {
	f.t.Helper()
	s, e := rt.Load(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	return s
}
func (f *ea1ChildStartFixture) grant(rt *kernel.Runtime) kernel.ChildExecutionGrant {
	f.t.Helper()
	g, e := rt.LookupChildExecution(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	if g.IsOwned() || g.ChildRunID != f.request.ID || g.ExpectedRevision != f.created.Run.Revision {
		f.t.Fatal("child original grant replaced by Root authority")
	}
	return g
}
func (f *ea1ChildStartFixture) unchanged(rt *kernel.Runtime, s kernel.Snapshot, g kernel.ChildExecutionGrant) {
	ea1ControlUnchanged(f.t, rt, s, g)
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(f.t, rt, a)
	}
}
func (f *ea1ChildStartFixture) fence(s kernel.Snapshot, g kernel.ChildExecutionGrant, live bool) {
	t := f.t
	ctx := t.Context()
	for _, current := range append(append([]kernel.Snapshot{}, f.ancestors...), s) {
		_, e := f.peer.Cancel(ctx, current.Run.ID, current.Run.Revision, "competing cancel")
		expected := kernel.ErrChildExecutionBusy
		terminal := current.Run.Status == kernel.RunStatusCompleted || current.Run.Status == kernel.RunStatusFailed || current.Run.Status == kernel.RunStatusCancelled
		if terminal {
			expected = kernel.ErrTerminal
		}
		if !errors.Is(e, expected) {
			t.Fatalf("%s Cancel: %v want %v", current.Run.ID, e, expected)
		}
		f.unchanged(f.peer, s, g)
		for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusFailed} {
			_, e = f.peer.Apply(ctx, current.Run.ID, current.Run.Revision, kernel.Mutation{Status: status, State: current.State, Checkpoint: current.Checkpoint})
			expected = kernel.ErrChildExecutionBusy
			if current.Run.ID == s.Run.ID {
				expected = kernel.ErrExecutionRequired
			}
			if terminal {
				expected = kernel.ErrTerminal
			}
			if !errors.Is(e, expected) {
				t.Fatalf("%s Apply %s: %v want %v", current.Run.ID, status, e, expected)
			}
			f.unchanged(f.peer, s, g)
		}
	}
	lease := f.store.acquired
	if live && lease.LeaseID != "" && s.Run.Status == kernel.RunStatusRunning {
		before := f.peerStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &lease)
		if before != nil {
			t.Fatalf("original child native owner not active: %v", before)
		}
		bound, e := f.peer.WithExecution(lease)
		if e != nil {
			t.Fatal(e)
		}
		_, e = bound.Apply(ctx, s.Run.ID, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: s.State, Checkpoint: s.Checkpoint})
		if !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("native only child Apply: %v", e)
		}
		f.unchanged(f.peer, s, g)
		if e = f.peerStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &lease); e != nil {
			t.Fatalf("denial revoked original child epoch: %v", e)
		}
	}
}
func (f *ea1ChildStartFixture) receipts(s kernel.Snapshot, modelCommitted bool) {
	t := f.t
	p := f.physical.ea1StartSettlementPhysical
	if len(p.requests) != 1 || len(p.intents) != 1 || len(p.responses) != 1 {
		t.Fatal("original Model replay/source missing")
	}
	original := p.intents[0]
	ea1StartSettlementJSONEqual(t, "original child full request", original.Request, p.requests[0])
	req := model.CloneRequest(p.requests[0])
	req.InvocationID = ""
	raw, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{req, original.CatalogFingerprints, original.Provider, original.BindingFingerprint})
	sum := sha256.Sum256(raw)
	v, e2 := agent.ViewState(s)
	if e != nil || e2 != nil || len(v.ModelInvocations) != 1 {
		t.Fatal("original durable child invocation absent")
	}
	got := v.ModelInvocations[0]
	if original.RequestHash != hex.EncodeToString(sum[:]) || got.ID != original.ID || got.RequestHash != original.RequestHash || got.SourceRevision != original.SourceRevision || got.ExecutionAttempt != 1 || got.DispatchAttempt != 1 || got.Provider != p.ProviderName() || got.BindingFingerprint != strings.Repeat("b", 64) {
		t.Fatal("child provider/source/attempt changed")
	}
	if modelCommitted {
		ea1StartSettlementJSONEqual(t, "full child durable Model response", got.Response, p.responses[0])
	}
	if s.Run.ParentRunID != f.request.ParentRunID || s.Run.Actor != f.request.Actor || s.Run.Thread != f.request.Thread || s.Run.RequestID != f.request.RequestID {
		t.Fatal("child identity changed")
	}
	var state struct {
		OriginFingerprint string `json:"originFingerprint"`
	}
	if json.Unmarshal(s.State, &state) != nil || state.OriginFingerprint != f.request.OriginFingerprint {
		t.Fatal("child frozen origin changed")
	}
	ea1StartSettlementJSONEqual(t, "child limits", v.Budget.Limits, f.request.Limits)
	if len(p.toolRequests) > 0 {
		want := tools.ExecutionRequest{RunID: f.request.ID, Call: tools.Call{ID: "original-call", ToolKey: "effect", Name: "Original Effect", Arguments: json.RawMessage(`{"source":"original"}`)}}
		if len(p.toolRequests) != 1 || len(p.receipts) != 1 {
			t.Fatal("Tool replayed")
		}
		ea1StartSettlementJSONEqual(t, "full original child Tool request", p.toolRequests[0], want)
		if s.Run.Status == kernel.RunStatusCompleted {
			ea1ControlTerminalResult(t, s, p.receipts[0])
		}
	}
}

func ea1ChildStartPreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), vector string) {
	f := ea1NewChildStart(t, factory, false, false)
	ctx := t.Context()
	req := f.request
	expected := agent.ErrInvalidRequest
	created := false
	var retained kernel.Snapshot
	useHost := false
	switch vector {
	case "origin":
		req.OriginFingerprint = "foreign"
	case "actor":
		req.Actor.ActorID = "foreign"
		expected = kernel.ErrExecutionConflict
	case "thread":
		req.Thread.ID = "foreign"
		expected = kernel.ErrExecutionConflict
	case "missing-parent":
		req.ParentRunID = "missing"
		expected = kernel.ErrNotFound
	case "terminal-parent":
		next, e := f.peer.Cancel(ctx, f.ancestors[1].Run.ID, f.ancestors[1].Run.Revision, "before child admission")
		if e != nil {
			t.Fatal(e)
		}
		f.ancestors[1] = next
		expected = kernel.ErrExecutionConflict
	case "preflight":
		f.preflightErr = errors.New("exact Host preflight refusal")
		expected = f.preflightErr
		useHost = true
	case "duplicate":
		prepared, e := f.runner(f.rt, "setup", false).PrepareRun(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		retained = prepared
		created = true
		expected = kernel.ErrAlreadyExists
	case "stale-admission":
		created = true
		expected = kernel.ErrExecutionConflict
		f.store.beforeAdmission = func(g kernel.ChildExecutionGrant) {
			lease, e := f.peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: g.ChildRunID, OwnerID: "pre-admission-winner", LeaseDuration: time.Minute})
			if e != nil {
				t.Fatal(e)
			}
			bound, e := f.peer.WithExecution(lease)
			if e != nil {
				t.Fatal(e)
			}
			snap := f.load(f.peer)
			_, e = bound.Apply(ctx, snap.Run.ID, snap.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: snap.State})
			if e != nil {
				t.Fatal(e)
			}
			if e = f.peer.ReleaseExecution(ctx, lease); e != nil {
				t.Fatal(e)
			}
			retained = f.load(f.peer)
		}
	case "parent-wins":
		created = true
		expected = kernel.ErrExecutionConflict
		f.store.beforeAdmission = func(kernel.ChildExecutionGrant) {
			retained = f.load(f.peer)
			next, e := f.peer.Cancel(ctx, f.ancestors[0].Run.ID, f.ancestors[0].Run.Revision, "parent wins admission")
			if e != nil {
				t.Fatal(e)
			}
			f.ancestors[0] = next
		}
	}
	beforeAdmissions := f.store.admissions
	_, err := f.runner(f.rt, "original-child-owner", useHost).StartRun(ctx, req)
	if !errors.Is(err, expected) {
		t.Fatalf("%s public preflight: %v want %v", vector, err, expected)
	}
	if f.host.calls != 0 || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 || f.store.admissions != beforeAdmissions {
		t.Fatal("denied child Start dispatched/admitted")
	}
	snap, e := f.peer.Load(ctx, req.ID)
	if created {
		if e != nil || snap.Run.ID != req.ID || !f.runner(f.peer, "source", false).VerifyFrozenStartRequest(snap, f.request) {
			t.Fatalf("created child erased after admission denial: %v", e)
		}
		if retained.Run.ID != req.ID || !reflect.DeepEqual(snap, retained) {
			t.Fatal("denied admission changed full retained child snapshot/Budget")
		}
	} else if !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("pre-Create denial persisted child: %v", e)
	}
	if _, e = f.peer.LookupChildExecution(ctx, req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("denial created physical grant: %v", e)
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
}

func ea1ChildStartPhase(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), phase, kind string) {
	f := ea1NewChildStart(t, factory, kind == "tool", kind == "deadline")
	ctx := t.Context()
	cuts := 0
	stop := errors.New("test child callback stop before Next")
	barrier := func() {
		cuts++
		s := f.load(f.peer)
		g := f.grant(f.peer)
		f.fence(s, g, phase != "P3" && (kind == "model" || kind == "tool"))
		if phase == "P5" {
			duplicate := f.runner(f.peer, "duplicate-child", false)
			if _, e := duplicate.StartRun(ctx, f.request); !errors.Is(e, kernel.ErrAlreadyExists) {
				t.Fatalf("duplicate child Start: %v", e)
			}
			f.unchanged(f.peer, s, g)
			expected := kernel.ErrChildExecutionBusy
			if kind == "model" {
				expected = agent.ErrModelOutcomeUnknown
			}
			if kind == "tool" {
				expected = agent.ErrToolOutcomeUnknown
			}
			if _, e := duplicate.Resume(ctx, s.Run.ID, s.Run.Revision); !errors.Is(e, expected) {
				t.Fatalf("competing child Resume: %v want %v", e, expected)
			}
			f.unchanged(f.peer, s, g)
			if f.store.acquired.LeaseID != "" {
				if _, e := f.peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: s.Run.ID, OwnerID: "foreign-native", LeaseDuration: 2 * time.Second}); !errors.Is(e, kernel.ErrExecutionConflict) {
					t.Fatalf("live child native takeover: %v", e)
				}
				f.unchanged(f.peer, s, g)
			}
			if kind == "deadline" {
				ea1ControlWaitUntil(*s.Run.DeadlineAt)
				for _, current := range append(append([]kernel.Snapshot{}, f.ancestors...), s) {
					until := time.Now().Add(time.Second)
					for {
						_, applied, e := f.peer.Expire(ctx, current.Run.ID, current.Run.Revision)
						f.unchanged(f.peer, s, g)
						if !applied && errors.Is(e, kernel.ErrChildExecutionBusy) {
							break
						}
						if applied || !errors.Is(e, kernel.ErrDeadline) || time.Now().After(until) {
							t.Fatalf("ancestor/self deadline %s: %v", current.Run.ID, e)
						}
						time.Sleep(time.Millisecond)
					}
				}
			}
		}
	}
	switch phase {
	case "P1":
		f.host.hook = barrier
		f.host.failure = stop
	case "P2":
		if kind == "host" {
			f.host.sendUnknown = true
			f.host.failure = stop
		} else {
			f.store.event = "agent.model_invocation.completed"
			if kind == "tool" {
				f.store.event = "tool.completed"
			}
		}
	case "P3":
		f.store.beforeMark = func(g kernel.ChildExecutionGrant) {
			barrier()
			s := f.load(f.peer)
			f.receipts(s, true)
			if e := f.runner(f.peer, "pre-mark-recovery", false).ReconcileChildPhysicalGrant(ctx, s.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("clean pre-Mark receipt invented retirement: %v", e)
			}
			f.unchanged(f.peer, s, g)
			wrong := g
			wrong.ID += "-foreign"
			if e := f.peerStore.(kernel.ChildExecutionStore).FinishChildExecution(ctx, wrong); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("foreign grant retired callback: %v", e)
			}
			f.unchanged(f.peer, s, g)
			// Return to the SAME original first-party Mark call, carrying its real
			// opaque proof. No recovery path synthesizes a missing completion marker.
		}
	case "P5":
		switch kind {
		case "host", "deadline":
			f.host.hook = barrier
		case "model":
			f.physical.modelHook = barrier
		case "tool":
			f.physical.toolHook = barrier
		}
	}
	useHost := phase == "P1" || kind == "host" || kind == "deadline"
	runner := f.runner(f.rt, "original-child-owner", useHost)
	out, err := runner.StartRun(ctx, f.request)
	f.host.hook = nil
	f.physical.modelHook = nil
	f.physical.toolHook = nil
	f.store.beforeMark = nil
	if phase == "P1" || phase == "P2" {
		expected := f.store.injected
		if kind == "host" {
			expected = stop
		}
		if !errors.Is(err, expected) || !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("actual child unknown cut: %v", err)
		}
		if phase == "P1" && (cuts != 1 || len(f.physical.requests) != 0) {
			t.Fatal("P1 entered physical Model")
		}
		s := f.load(f.peer)
		g := f.grant(f.peer)
		f.unchanged(f.peer, s, g)
		if kind != "host" {
			if f.store.failures != 1 {
				t.Fatal("receipt fault not reached")
			}
			f.receipts(s, kind == "tool")
			ea1ControlWaitUntil(f.store.acquired.ExpiresAt.Add(150 * time.Millisecond))
		}
		freshStore := f.reopen()
		freshRT := ea1StartRuntime(t, freshStore)
		f.physical.rt = freshRT
		f.unchanged(freshRT, s, g)
		recovery := f.runner(freshRT, "fresh-child", useHost)
		if e := recovery.ReconcileChildPhysicalGrant(ctx, s.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unknown child retired: %v", e)
		}
		expected = kernel.ErrChildExecutionBusy
		if kind == "model" {
			expected = agent.ErrModelOutcomeUnknown
		}
		if kind == "tool" {
			expected = agent.ErrToolOutcomeUnknown
		}
		if _, e := recovery.Resume(ctx, s.Run.ID, s.Run.Revision); !errors.Is(e, expected) {
			t.Fatalf("unknown child resumed after TTL: %v", e)
		}
		f.unchanged(freshRT, s, g)
		if kind != "host" {
			f.receipts(s, kind == "tool")
			if e := freshStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &f.store.acquired); !errors.Is(e, kernel.ErrExecutionConflict) {
				t.Fatalf("original child lease did not expire: %v", e)
			}
		} else if f.host.calls != 1 || (phase == "P2" && f.host.sends != 1) || (phase == "P1" && f.host.sends != 0) || len(f.physical.requests) != 0 {
			t.Fatal("unknown Host replayed")
		}
		return
	}
	if cuts != 1 {
		t.Fatalf("actual child cut not reached once: %d", cuts)
	}
	if kind == "deadline" {
		if !errors.Is(err, kernel.ErrDeadline) || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatalf("due child sent new physical work: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	f.receipts(out, true)
	if f.store.marks != 1 {
		t.Fatal("original callback Mark did not commit exactly once")
	}
	if _, e := f.peer.LookupChildExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("original child not retired after genuine Mark: %v", e)
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	persisted, e := freshRT.Load(ctx, f.request.ID)
	if e != nil || !reflect.DeepEqual(persisted, out) {
		t.Fatal("actual original receipt lost across reopen")
	}
	f.receipts(persisted, true)
}
