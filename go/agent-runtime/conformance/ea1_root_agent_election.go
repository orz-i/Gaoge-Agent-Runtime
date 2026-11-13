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

// RunEA1RootAgentElectionSuite measures AR-S/R/Q P5 through distinct real
// public entries. Roots have no ancestors; these APIs expose neither Workflow
// compensation nor a shared-budget election. Full Budget/Usage remains part of
// every denial snapshot, and physical unknown never acquires retry authority.
func RunEA1RootAgentElectionSuite(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, entry := range []string{"start", "resume"} {
		for _, cut := range []string{"host", "model", "tool", "deadline", "unknown-model", "unknown-tool"} {
			t.Run(entry+"/"+cut, func(t *testing.T) { ea1AgentElectionEntry(t, factory, entry, cut) })
		}
	}
	for _, kind := range []string{"model", "tool"} {
		for _, mode := range []string{"known", "unknown-ttl", "competing-receipt", "deadline-known"} {
			t.Run("recovery/"+kind+"/"+mode, func(t *testing.T) { ea1AgentElectionRecovery(t, factory, kind, mode) })
		}
	}
}

type ea1ElectionStore struct {
	*ea1AgentReceiptFault
	created          func(kernel.Snapshot)
	acquired         kernel.ExecutionLease
	receiptCommitted func(kernel.Snapshot)
}

func (s *ea1ElectionStore) Create(ctx context.Context, r kernel.Record, e []kernel.EventDraft) (kernel.Snapshot, error) {
	out, err := s.Store.Create(ctx, r, e)
	if err == nil && s.created != nil {
		s.created(out)
	}
	return out, err
}
func (s *ea1ElectionStore) AcquireExecution(ctx context.Context, r kernel.ExecutionAcquireRequest) (kernel.ExecutionLease, error) {
	out, err := s.ExecutionStore.AcquireExecution(ctx, r)
	if err == nil {
		s.acquired = out
	}
	return out, err
}

// Observe only an actual successful Store receipt CAS, before later terminal
// consumption removes its internal replay record. Never mint or alter proof.
func (s *ea1ElectionStore) ApplyOwnedPhysicalRecovery(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, m kernel.StoreMutation, p kernel.OwnedPhysicalRecovery) (kernel.Snapshot, error) {
	out, err := s.OwnedPhysicalExecutionStore.ApplyOwnedPhysicalRecovery(ctx, g, rev, m, p)
	if err == nil && s.receiptCommitted != nil {
		s.receiptCommitted(out)
	}
	return out, err
}

type ea1ElectionHost struct {
	operation plugin.RunOperation
	hook      func()
	calls     int
}

func (*ea1ElectionHost) Name() string { return "ea1.election.original" }
func (h *ea1ElectionHost) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	if inv.Operation != h.operation || inv.ParentRunID != "" {
		return kernel.Snapshot{}, errors.New("wrong original election entry")
	}
	h.calls++
	if h.hook != nil {
		h.hook()
	}
	return next(ctx)
}

type ea1ElectionPhysical struct {
	*ea1StartSettlementPhysical
	modelHook, toolHook func()
}

func (p *ea1ElectionPhysical) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	out, err := p.ea1StartSettlementPhysical.Generate(ctx, r)
	if err == nil && p.modelHook != nil {
		p.modelHook()
	}
	return out, err
}
func (p *ea1ElectionPhysical) execute(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
	out, err := p.ea1StartSettlementPhysical.execute(ctx, r)
	if err == nil && p.toolHook != nil {
		p.toolHook()
	}
	return out, err
}

type ea1ElectionFixture struct {
	t             *testing.T
	store         *ea1ElectionStore
	rt, peer      *kernel.Runtime
	peerStore     kernel.Store
	reopen        func() kernel.Store
	physical      *ea1ElectionPhysical
	catalog       *tools.Registry
	host          *ea1ElectionHost
	request       agent.StartRequest
	created       kernel.Snapshot
	modelVerifier agent.ModelOutcomeVerifier
	toolVerifier  agent.ToolOutcomeVerifier
}

func ea1NewElection(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), withTool bool) *ea1ElectionFixture {
	backing, peer, reopen := factory(t)
	fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), injected: errors.New("original physical receipt not committed")}
	f := &ea1ElectionFixture{t: t, store: &ea1ElectionStore{ea1AgentReceiptFault: fault}, peer: ea1StartRuntime(t, peer), peerStore: peer, reopen: reopen, host: &ea1ElectionHost{operation: plugin.RunStart}}
	f.rt = ea1StartRuntime(t, f.store)
	f.physical = &ea1ElectionPhysical{ea1StartSettlementPhysical: &ea1StartSettlementPhysical{rt: f.rt, tool: withTool}}
	var err error
	f.catalog, err = tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "Original Effect", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true}, Handler: tools.HandlerFunc(f.physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	f.request = agent.StartRequest{ID: "root-election", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original election", Instructions: "Preserve source", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		f.request.ToolKeys = []string{"effect"}
	}
	f.store.created = func(s kernel.Snapshot) {
		f.created = s
		if s.Run.ParentRunID != "" || f.request.OriginFingerprint != "" || !f.runner(f.rt, "source-verifier", false).VerifyFrozenStartRequest(s, f.request) {
			t.Fatal("actual Create source changed")
		}
	}
	return f
}
func (f *ea1ElectionFixture) runner(rt *kernel.Runtime, owner string, host bool) *agent.Runner {
	deps := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, ModelOutcomeVerifier: f.modelVerifier, ToolOutcomeVerifier: f.toolVerifier, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Second}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if host {
		deps.RunMiddleware = []plugin.RunMiddleware{f.host}
	}
	r, e := agent.NewRunner(deps)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ElectionFixture) snapshot() kernel.Snapshot {
	f.t.Helper()
	s, e := f.peer.Load(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	return s
}
func (f *ea1ElectionFixture) grant() kernel.ChildExecutionGrant {
	f.t.Helper()
	g, e := f.peer.LookupChildExecution(f.t.Context(), f.request.ID)
	if e != nil {
		f.t.Fatal(e)
	}
	return g
}
func (f *ea1ElectionFixture) unchanged(s kernel.Snapshot, g kernel.ChildExecutionGrant) {
	ea1ControlUnchanged(f.t, f.peer, s, g)
}
func (f *ea1ElectionFixture) compete(s kernel.Snapshot, g kernel.ChildExecutionGrant, live bool) {
	t := f.t
	ctx := t.Context()
	lease := f.store.acquired
	if g.ExecutionOwnerID != lease.OwnerID || g.ExecutionLeaseID != lease.LeaseID || g.ExecutionEpoch != lease.Epoch || g.AdmissionSourceHash == "" {
		t.Fatal("original native authority/source mismatch")
	}
	_, e := f.peer.Cancel(ctx, s.Run.ID, s.Run.Revision, "competing cancel")
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Cancel escaped physical election: %v", e)
	}
	f.unchanged(s, g)
	_, e = f.peer.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{RunID: s.Run.ID, OwnerID: "foreign-owner", LeaseDuration: 2 * time.Second})
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("takeover escaped physical election: %v", e)
	}
	f.unchanged(s, g)
	e = f.peer.ReleaseExecution(ctx, lease)
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Release escaped physical election: %v", e)
	}
	f.unchanged(s, g)
	e = f.peerStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &lease)
	if live && e != nil {
		t.Fatalf("competitor revoked live original owner: %v", e)
	}
	if !live && !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatalf("real TTL did not expire owner: %v", e)
	}
}
func (f *ea1ElectionFixture) expireWhileOwned(s kernel.Snapshot, g kernel.ChildExecutionGrant) {
	t := f.t
	ea1ControlWaitUntil(*s.Run.DeadlineAt)
	until := time.Now().Add(time.Second)
	for {
		_, applied, e := f.peer.Expire(t.Context(), s.Run.ID, s.Run.Revision)
		f.unchanged(s, g)
		if !applied && errors.Is(e, kernel.ErrChildExecutionBusy) {
			return
		}
		if applied || !errors.Is(e, kernel.ErrDeadline) || time.Now().After(until) {
			t.Fatalf("real deadline failed physical election: applied=%v err=%v", applied, e)
		}
		time.Sleep(time.Millisecond)
	}
}
func (f *ea1ElectionFixture) originalReceipt(s kernel.Snapshot, completed bool) {
	t := f.t
	t.Helper()
	p := f.physical.ea1StartSettlementPhysical
	if len(p.requests) != 1 || len(p.intents) != 1 || len(p.responses) != 1 {
		t.Fatal("physical Model replay/missing source")
	}
	inv := p.intents[0]
	req := model.CloneRequest(p.requests[0])
	ea1StartSettlementJSONEqual(t, "original request", inv.Request, req)
	req.InvocationID = ""
	encoded, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{req, inv.CatalogFingerprints, inv.Provider, inv.BindingFingerprint})
	sum := sha256.Sum256(encoded)
	if e != nil || inv.RequestHash != hex.EncodeToString(sum[:]) || inv.Provider != p.ProviderName() || inv.BindingFingerprint != strings.Repeat("b", 64) || inv.ExecutionAttempt != 1 || inv.DispatchAttempt != 1 {
		t.Fatal("original request/provider/attempt tuple changed")
	}
	v, e := agent.ViewState(s)
	if e != nil || len(v.ModelInvocations) != 1 {
		t.Fatal("durable original invocation missing")
	}
	got := v.ModelInvocations[0]
	if got.ID != inv.ID || got.RequestHash != inv.RequestHash || got.SourceRevision != inv.SourceRevision || got.Provider != inv.Provider || got.BindingFingerprint != inv.BindingFingerprint || got.ExecutionAttempt != inv.ExecutionAttempt || got.DispatchAttempt != inv.DispatchAttempt {
		t.Fatal("durable original tuple changed")
	}
	if len(p.toolRequests) > 0 {
		want := tools.ExecutionRequest{RunID: f.request.ID, Call: tools.Call{ID: "original-call", ToolKey: "effect", Name: "Original Effect", Arguments: json.RawMessage(`{"source":"original"}`)}}
		if len(p.toolRequests) != 1 || len(p.receipts) != 1 {
			t.Fatal("original physical Tool replayed")
		}
		ea1StartSettlementJSONEqual(t, "full original Tool request", p.toolRequests[0], want)
		if p.receipts[0].Receipt.ExecutionID != want.Call.ID || p.receipts[0].Receipt.Disposition != "committed" {
			t.Fatal("actual Tool result not original receipt")
		}
	}
	if completed {
		ea1StartSettlementJSONEqual(t, "full durable provider response", got.Response, p.responses[0])
	}
	if s.Run.Actor != f.request.Actor || s.Run.Thread != f.request.Thread || s.Run.RequestID != f.request.RequestID || s.Run.ParentRunID != "" {
		t.Fatal("original Root identity changed")
	}
	ea1StartSettlementJSONEqual(t, "original budget limits", v.Budget.Limits, f.request.Limits)
}
func ea1AgentElectionEntry(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), entry, cut string) {
	withTool := cut == "tool" || cut == "unknown-tool"
	f := ea1NewElection(t, factory, withTool)
	ctx := t.Context()
	if cut == "deadline" {
		deadline := time.Now().UTC().Add(time.Second)
		f.request.DeadlineAt = &deadline
	}
	var prepared kernel.Snapshot
	if entry == "resume" {
		var e error
		prepared, e = f.runner(f.rt, "prepare-owner", false).PrepareRun(ctx, f.request)
		if e != nil {
			t.Fatal(e)
		}
		if len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatal("Prepare dispatched")
		}
		if _, e = f.rt.LookupChildExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatalf("Prepare scope retained: %v", e)
		}
		f.host.operation = plugin.RunResume
	}
	unknown := strings.HasPrefix(cut, "unknown-")
	if unknown {
		f.store.event = "agent.model_invocation.completed"
		if withTool {
			f.store.event = "tool.completed"
		}
	}
	checks := 0
	barrier := func() {
		checks++
		s := f.snapshot()
		g := f.grant()
		if g.AdmissionOperation != entry || (entry == "resume" && g.ExpectedRevision != prepared.Run.Revision) || (entry == "start" && g.ExpectedRevision != f.created.Run.Revision) {
			t.Fatal("wrong measured entry/source revision")
		}
		f.compete(s, g, true)
		competitor := f.runner(f.peer, "duplicate-owner", false)
		if entry == "start" {
			_, e := competitor.StartRun(ctx, f.request)
			if !errors.Is(e, kernel.ErrAlreadyExists) {
				t.Fatalf("duplicate actual Start: %v", e)
			}
		} else {
			_, e := competitor.Resume(ctx, s.Run.ID, s.Run.Revision)
			expected := agent.ErrRootOutcomeUnknown
			if cut == "model" {
				expected = agent.ErrModelOutcomeUnknown
			}
			if cut == "tool" {
				expected = agent.ErrToolOutcomeUnknown
			}
			if !errors.Is(e, expected) {
				t.Fatalf("duplicate actual Resume: %v want %v", e, expected)
			}
		}
		f.unchanged(s, g)
		if cut == "deadline" {
			f.expireWhileOwned(s, g)
		}
	}
	switch cut {
	case "host", "deadline":
		f.host.hook = barrier
	case "model":
		f.physical.modelHook = barrier
	case "tool":
		f.physical.toolHook = barrier
	}
	runner := f.runner(f.rt, "original-owner", !unknown)
	var out kernel.Snapshot
	var err error
	if entry == "start" {
		out, err = runner.StartRun(ctx, f.request)
	} else {
		out, err = runner.Resume(ctx, f.request.ID, prepared.Run.Revision)
	}
	f.host.hook = nil
	f.physical.modelHook = nil
	f.physical.toolHook = nil
	if unknown {
		if !errors.Is(err, f.store.injected) || f.store.failures != 1 {
			t.Fatalf("actual unknown cut missing: %v", err)
		}
		before := f.snapshot()
		g := f.grant()
		f.originalReceipt(before, withTool)
		ea1ControlWaitUntil(f.store.acquired.ExpiresAt.Add(150 * time.Millisecond))
		f.compete(before, g, false)
		peer := f.runner(f.peer, "retry-after-expiry", false)
		if entry == "start" {
			_, err = peer.StartRun(ctx, f.request)
			if !errors.Is(err, kernel.ErrAlreadyExists) {
				t.Fatalf("expired duplicate Start: %v", err)
			}
		} else {
			_, err = peer.Resume(ctx, before.Run.ID, before.Run.Revision)
			expected := agent.ErrModelOutcomeUnknown
			if withTool {
				expected = agent.ErrToolOutcomeUnknown
			}
			if !errors.Is(err, expected) {
				t.Fatalf("expired retry adopted unknown: %v", err)
			}
		}
		f.unchanged(before, g)
		f.originalReceipt(f.snapshot(), withTool)
		return
	}
	if checks != 1 {
		t.Fatalf("actual cutpoint not hit exactly once: %d", checks)
	}
	if cut == "deadline" {
		if !errors.Is(err, kernel.ErrDeadline) || len(f.physical.requests) != 0 || len(f.physical.toolRequests) != 0 {
			t.Fatalf("due new dispatch executed: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	f.originalReceipt(out, true)
	if withTool {
		if len(f.physical.receipts) != 1 || len(f.physical.toolRequests) != 1 {
			t.Fatal("physical Tool replayed")
		}
		ea1ControlTerminalResult(t, out, f.physical.receipts[0])
	}
}

func ea1AgentElectionRecovery(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind, mode string) {
	f := ea1NewElection(t, factory, kind == "tool")
	ctx := t.Context()
	f.store.event = "agent.model_invocation.completed"
	if kind == "tool" {
		f.store.event = "tool.completed"
	}
	if mode == "deadline-known" {
		deadline := time.Now().UTC().Add(time.Second)
		f.request.DeadlineAt = &deadline
	}
	// No arbitrary Host callback can turn a failed receipt write into unknown
	// Host work. Actual first-party whole-callback return is persisted by Start.
	starter := f.runner(f.rt, "original-owner", false)
	_, err := starter.StartRun(ctx, f.request)
	if !errors.Is(err, f.store.injected) || f.store.failures != 1 {
		t.Fatalf("original physical unknown not reached: %v", err)
	}
	before := f.snapshot()
	grant := f.grant()
	f.originalReceipt(before, kind == "tool")
	completion, err := f.rt.LookupOwnedPhysicalCallbackReturn(ctx, grant)
	if err != nil || !completion.HostCompleted {
		t.Fatalf("actual callback return missing: %v", err)
	}
	var receiptWrites []kernel.Snapshot
	observeReceipt := func(s kernel.Snapshot) { receiptWrites = append(receiptWrites, s) }
	f.store.receiptCommitted = observeReceipt
	// The peer has its own Store/connection; observe its winning CAS through
	// the same faithful port, not through a fabricated post-hoc receipt.
	originalPeer := f.peerStore
	peerObserver := &ea1ElectionStore{ea1AgentReceiptFault: &ea1AgentReceiptFault{Store: originalPeer, ExecutionStore: originalPeer.(kernel.ExecutionStore), ControlStore: originalPeer.(kernel.ControlStore), ChildExecutionStore: originalPeer.(kernel.ChildExecutionStore), ExecutionQuarantineStore: originalPeer.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: originalPeer.(kernel.OwnedPhysicalExecutionStore)}, receiptCommitted: observeReceipt}
	f.peer = ea1StartRuntime(t, peerObserver)
	available := mode != "unknown-ttl"
	lookups := 0
	var verifyHook func()
	f.modelVerifier = agent.ModelOutcomeVerifierFunc(func(_ context.Context, r model.Request) (model.Response, error) {
		lookups++
		ea1StartSettlementJSONEqual(t, "verified original Model request", r, f.physical.requests[0])
		if verifyHook != nil {
			verifyHook()
		}
		if !available {
			return model.Response{}, agent.ErrModelOutcomeUnknown
		}
		return model.CloneResponse(f.physical.responses[0]), nil
	})
	f.toolVerifier = agent.ToolOutcomeVerifierFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		lookups++
		if len(f.physical.toolRequests) != 1 {
			t.Fatal("missing original Tool send")
		}
		ea1StartSettlementJSONEqual(t, "verified original Tool request", r, f.physical.toolRequests[0])
		if verifyHook != nil {
			verifyHook()
		}
		if !available {
			return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
		}
		return tools.CloneExecutionResult(f.physical.receipts[0]), nil
	})
	recover := func(r *agent.Runner, rev uint64) (kernel.Snapshot, error) {
		if kind == "model" {
			return r.ReconcileModelOutcome(ctx, f.request.ID, rev)
		}
		return r.ReconcileToolOutcome(ctx, f.request.ID, rev, "original-call")
	}
	runner := f.runner(f.rt, "recovery-original", false)
	verifyHook = func() {
		f.compete(before, grant, true)
		if mode == "deadline-known" {
			f.expireWhileOwned(before, grant)
		}
	}
	var winner kernel.Snapshot
	if mode == "competing-receipt" {
		verifyHook = func() {
			f.compete(before, grant, true)
			verifyHook = nil
			var e error
			winner, e = recover(f.runner(f.peer, "receipt-winner", false), before.Run.Revision)
			if e != nil {
				t.Fatalf("peer original receipt did not win: %v", e)
			}
		}
	}
	out, err := recover(runner, before.Run.Revision)
	verifyHook = nil
	if mode == "unknown-ttl" {
		expected := agent.ErrModelOutcomeUnknown
		if kind == "tool" {
			expected = agent.ErrToolOutcomeUnknown
		}
		if !errors.Is(err, expected) || lookups != 1 {
			t.Fatalf("unknown recovery contract: %v", err)
		}
		f.unchanged(before, grant)
		ea1ControlWaitUntil(f.store.acquired.ExpiresAt.Add(150 * time.Millisecond))
		f.compete(before, grant, false)
		fresh := f.reopen()
		freshRT := ea1StartRuntime(t, fresh)
		f.physical.rt = freshRT
		retry := f.runner(freshRT, "after-expiry", false)
		_, err = recover(retry, before.Run.Revision)
		if !errors.Is(err, expected) {
			t.Fatalf("TTL granted unknown replay: %v", err)
		}
		ea1ControlUnchanged(t, freshRT, before, grant)
		if e := retry.ReconcileRootPhysicalExecution(ctx, f.request.ID); !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("unknown retired after TTL: %v", e)
		}
		f.originalReceipt(before, kind == "tool")
		if lookups != 2 || len(f.physical.requests) != 1 || kind == "tool" && len(f.physical.toolRequests) != 1 {
			t.Fatal("unknown retry sent physical work")
		}
		return
	}
	if mode == "competing-receipt" {
		// Runtime receipt CAS reloads revision before entering the Store. The peer
		// has committed the exact original receipt and retired its scope already.
		if !errors.Is(err, kernel.ErrConflict) || lookups != 2 {
			t.Fatalf("losing exact receipt CAS: %v", err)
		}
		ea1CompBarrierUnchanged(t, f.peer, winner)
		out = winner
	} else if err != nil {
		t.Fatalf("known original receipt failed: %v", err)
	}
	if len(receiptWrites) != 1 {
		t.Fatalf("original receipt CAS did not elect one winner: %d", len(receiptWrites))
	}
	if kind == "tool" {
		var state struct {
			RecoveredTools []struct {
				Call   tools.Call            `json:"call"`
				Result tools.ExecutionResult `json:"result"`
			} `json:"recoveredTools"`
		}
		if e := json.Unmarshal(receiptWrites[0].State, &state); e != nil || len(state.RecoveredTools) != 1 {
			t.Fatalf("committed original receipt not observed: %v", e)
		}
		ea1StartSettlementJSONEqual(t, "full committed Tool receipt before consumption", state.RecoveredTools[0].Result, f.physical.receipts[0])
		ea1StartSettlementJSONEqual(t, "committed original Tool Call", state.RecoveredTools[0].Call, f.physical.toolRequests[0].Call)
	}
	f.originalReceipt(out, true)
	if kind == "tool" {
		ea1ControlTerminalResult(t, out, f.physical.receipts[0])
	} else {
		v, e := agent.ViewState(out)
		if e != nil || v.ModelInvocations[0].Status != agent.ModelInvocationCompleted {
			t.Fatal("known Model receipt not completed")
		}
	}
	if _, e := f.peer.LookupChildExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("original recovered scope retained: %v", e)
	}
	freshStore := f.reopen()
	freshRT := ea1StartRuntime(t, freshStore)
	f.physical.rt = freshRT
	persisted, e := freshRT.Load(ctx, f.request.ID)
	if e != nil || !reflect.DeepEqual(persisted, out) {
		t.Fatal("complete original receipt changed across reopen")
	}
	fresh := f.runner(freshRT, "fresh-after-receipt", false)
	if e = fresh.ReconcileRootPhysicalExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("duplicate retirement invented scope: %v", e)
	}
	if kind == "model" {
		// Continue=true is the original durable yield, not a request to resend.
		consumed, consumeErr := fresh.Resume(ctx, persisted.Run.ID, persisted.Run.Revision)
		if consumeErr != nil {
			t.Fatalf("known Model receipt consumption failed: %v", consumeErr)
		}
		f.originalReceipt(consumed, true)
		if mode == "deadline-known" {
			_, dispatchErr := fresh.Resume(ctx, consumed.Run.ID, consumed.Run.Revision)
			if !errors.Is(dispatchErr, kernel.ErrDeadline) {
				t.Fatalf("new physical request ignored due deadline: %v", dispatchErr)
			}
		}
	}
	if len(f.physical.requests) != 1 || kind == "tool" && len(f.physical.toolRequests) != 1 {
		t.Fatal("known recovery/continuation sent new physical work")
	}
}
