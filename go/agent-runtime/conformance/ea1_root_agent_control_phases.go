package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// AR-A P0/P1/P2 only. Caller authentication is a Host responsibility: these
// methods expose no caller-actor or caller-selected checkpoint ID parameter.
func RunEA1RootAgentControlPhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, v := range []string{"missing", "stale", "not-waiting", "invalid-decision", "foreign-checkpoint", "native-success", "default-success"} {
		t.Run("P0/approval/"+v, func(t *testing.T) { ea1ApprovalPreflight(t, factory, v) })
	}
	t.Run("P0/tool-wait-no-wait", func(t *testing.T) {
		f := ea1NewControlFixture(t, factory, false)
		r := f.runner(f.rt, true, false)
		out, e := r.ResumeToolWait(t.Context(), f.request.ID, f.waiting.Run.Revision, 1)
		if e != nil || !ea1RecoveryJSONEqual(out, f.waiting) || f.lookups != 0 || f.approval.calls != 0 {
			t.Fatal("no-wait public idempotent contract changed")
		}
		ea1CompBarrierUnchanged(t, f.observer, f.waiting)
	})
	t.Run("P1/approval", func(t *testing.T) { ea1ApprovalBarrier(t, factory) })
	for _, v := range []string{"handler", "tool"} {
		t.Run("P2/approval/"+v, func(t *testing.T) { ea1ApprovalUnknown(t, factory, v) })
	}
	for _, phase := range []string{"P0", "P1", "P2"} {
		t.Run(phase+"/tool-wait", func(t *testing.T) { ea1ToolWaitPhase(t, factory, phase) })
	}
}

type ea1ControlPolicy struct{}

func (ea1ControlPolicy) Name() string { return "ea1.original.approval.required" }
func (ea1ControlPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

type ea1ControlApproval struct {
	base         plugin.ApprovalHandler
	t            *testing.T
	call         tools.Call
	definition   tools.Definition
	checkpoint   *kernel.Checkpoint
	resolved     *kernel.Checkpoint
	calls, sends int
	before       func()
	failure      error
	foreign      bool
}

func (a *ea1ControlApproval) PrepareToolApproval(c tools.Call, d tools.Definition) (*kernel.Checkpoint, error) {
	a.call = tools.CloneCall(c)
	a.definition = tools.CloneDefinition(d)
	cp, e := a.base.PrepareToolApproval(c, d)
	a.checkpoint = cp
	return cp, e
}
func (a *ea1ControlApproval) ResolveToolApproval(cp *kernel.Checkpoint, r plugin.ApprovalResponse) (*kernel.Checkpoint, error) {
	a.calls++
	if !ea1RecoveryJSONEqual(cp, a.checkpoint) {
		a.t.Fatal("approval handler received foreign original checkpoint")
	}
	if a.before != nil {
		a.before()
	}
	if a.failure != nil {
		a.sends++
		return nil, a.failure
	}
	out, e := a.base.ResolveToolApproval(cp, r)
	if e == nil && a.foreign {
		out.Payload = json.RawMessage(`{"toolCallID":"foreign","toolKey":"effect","arguments":{}}`)
	}
	a.resolved = out
	return out, e
}

type ea1ControlHost struct {
	t       *testing.T
	request agent.StartRequest
	calls   int
}

func (*ea1ControlHost) Name() string { return "ea1.actual.approval.host" }
func (h *ea1ControlHost) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	h.calls++
	if i.Operation != plugin.RunResolveApproval || i.RunID != h.request.ID || i.ParentRunID != "" || i.Actor != h.request.Actor || i.Thread != h.request.Thread || i.Goal != h.request.Goal {
		h.t.Fatal("actual approval Host source mismatch")
	}
	return next(ctx)
}

type ea1ControlFixture struct {
	t                *testing.T
	peer             kernel.Store
	reopen           func() kernel.Store
	store            *ea1AgentAdmissionStore
	fault            *ea1AgentReceiptFault
	rt, observer     *kernel.Runtime
	physical         *ea1RootRecoveryPhysical
	registry         *tools.Registry
	definition       tools.Definition
	approval         *ea1ControlApproval
	host             *ea1ControlHost
	request          agent.StartRequest
	created, waiting kernel.Snapshot
	approved         kernel.Snapshot
	external         bool
	offset           time.Duration
	toolResult       tools.ExecutionResult
	verifiedResult   tools.ExecutionResult
	lookups          int
	verify           func()
	available        bool
}

func ea1NewControlFixture(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), external bool) *ea1ControlFixture {
	backing, peer, reopen := factory(t)
	s := ea1AgentAdmissionWrap(t, backing)
	fault := &ea1AgentReceiptFault{Store: s, ExecutionStore: s, ControlStore: s, ChildExecutionStore: s, ExecutionQuarantineStore: s, OwnedPhysicalExecutionStore: s}
	rt := ea1StartRuntime(t, fault)
	approvals, e := interaction.New(rt)
	if e != nil {
		t.Fatal(e)
	}
	f := &ea1ControlFixture{t: t, peer: peer, reopen: reopen, store: s, fault: fault, rt: rt, observer: ea1StartRuntime(t, peer), external: external}
	f.approval = &ea1ControlApproval{base: interactionadapter.New(approvals), t: t}
	f.physical = &ea1RootRecoveryPhysical{ea1UnknownAgentPhysical: &ea1UnknownAgentPhysical{rt: rt, tool: true}, binding: "original"}
	f.definition = tools.Definition{Key: "effect", Name: "Original terminal effect", InputSchema: json.RawMessage(`{"type":"object"}`), Terminal: true}
	f.registry, e = tools.NewRegistry([]tools.Registration{{Definition: f.definition, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		if !external {
			approved, loadErr := f.rt.Load(ctx, f.request.ID)
			if loadErr != nil || approved.Checkpoint == nil || !ea1RecoveryJSONEqual(approved.Checkpoint, f.approval.resolved) {
				t.Fatalf("actual Tool lacks durably resolved original approval: %v", loadErr)
			}
			f.approved = approved
		}
		result, err := f.physical.execute(ctx, r)
		if external {
			deadline := time.Now().UTC().Add(2 * time.Minute)
			result = tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: tools.ReceiptDispositionPending}, Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "original-operation", InputDigest: strings.Repeat("a", 64), DeadlineAt: &deadline}}
		}
		f.toolResult = tools.CloneExecutionResult(result)
		return result, err
	})}})
	if e != nil {
		t.Fatal(e)
	}
	f.request = agent.StartRequest{ID: "root-control", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original controlled effect", Instructions: "Use original call only.", Model: "fixture-model", ToolKeys: []string{"effect"}}
	f.host = &ea1ControlHost{t: t, request: f.request}
	setup := f.runner(rt, true, false)
	s.afterCreate = func(_ context.Context, v kernel.Snapshot) error {
		f.created = v
		if v.Run.ParentRunID != "" || f.request.OriginFingerprint != "" || !setup.VerifyFrozenStartRequest(v, f.request) {
			t.Fatal("Create source invalid")
		}
		return nil
	}
	f.waiting, e = setup.StartRun(t.Context(), f.request)
	if e != nil {
		t.Fatal(e)
	}
	m, n := f.physical.counts()
	if m != 1 || (!external && n != 0) || (external && n != 1) || f.host.calls != 0 || f.approval.calls != 0 {
		t.Fatal("setup dispatched measured control callback")
	}
	if !setup.VerifyFrozenStartRequest(f.waiting, f.request) {
		t.Fatal("waiting source changed")
	}
	view, e := agent.ViewState(f.waiting)
	if e != nil || len(view.ModelInvocations) != 1 {
		t.Fatal("original model source absent")
	}
	original := f.physical.intents[0]
	current := view.ModelInvocations[0]
	canonical := model.CloneRequest(original.Request)
	canonical.InvocationID = ""
	raw, marshalErr := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{canonical, original.CatalogFingerprints, original.Provider, original.BindingFingerprint})
	hash := sha256.Sum256(raw)
	if marshalErr != nil || original.RequestHash != hex.EncodeToString(hash[:]) || current.RequestHash != original.RequestHash || current.ID != original.ID || current.Provider != original.Provider || current.BindingFingerprint != original.BindingFingerprint || current.ExecutionAttempt != 1 || current.DispatchAttempt != 1 || current.Status != agent.ModelInvocationConsumed || !ea1RecoveryJSONEqual(model.CloneRequest(original.Request), model.CloneRequest(f.physical.models[0])) {
		t.Fatal("original full Model source/attempt mismatch")
	}
	if external {
		f.physical.assertOriginal(t, f.request, current)
	}
	ea1StartSettlementJSONEqual(t, "actual consumed Model receipt", view.ModelInvocations[0].Response, f.physical.response)
	if external {
		if f.waiting.Run.Status != kernel.RunStatusRunning {
			t.Fatal("external setup not Running")
		}
		f.assertWait(setup, f.waiting)
	} else {
		if f.waiting.Run.Status != kernel.RunStatusWaitingInput || !ea1RecoveryJSONEqual(f.waiting.Checkpoint, f.approval.checkpoint) || !ea1RecoveryJSONEqual(f.approval.definition, f.definition) || f.approval.call.ID != "original-tool-call" || f.approval.call.ToolKey != "effect" || string(f.approval.call.Arguments) != `{"source":"original"}` {
			t.Fatal("original approval checkpoint/call/definition lost")
		}
		if _, e = rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("waiting approval retained setup scope")
		}
	}
	return f
}
func (f *ea1ControlFixture) runner(rt *kernel.Runtime, native, host bool) *agent.Runner {
	d := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.registry, Executor: f.registry, Approvals: f.approval, Clock: ea1UnknownAgentClock{offset: f.offset}, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 100 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if native {
		d.RunExecution = &agent.RunExecution{OwnerID: "control-owner", LeaseDuration: 2 * time.Second}
	}
	if host {
		d.RunMiddleware = []plugin.RunMiddleware{f.host}
	}
	if !f.external {
		d.ApprovalPolicies = []plugin.ApprovalPolicy{ea1ControlPolicy{}}
	}
	d.ToolOutcomeVerifier = agent.ToolOutcomeVerifierFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		f.lookups++
		if len(f.physical.tools) != 1 || !ea1RecoveryJSONEqual(r, f.physical.tools[0]) {
			f.t.Fatal("lookup changed original Tool source")
		}
		if f.verify != nil {
			f.verify()
		}
		if !f.available {
			return tools.ExecutionResult{}, agent.ErrToolOutcomeUnknown
		}
		f.verifiedResult = tools.ExecutionResult{Content: json.RawMessage(`{"verified":"original"}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}
		return tools.CloneExecutionResult(f.verifiedResult), nil
	})
	r, e := agent.NewRunner(d)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ControlFixture) assertWait(r *agent.Runner, s kernel.Snapshot) *agent.PendingTool {
	f.t.Helper()
	w, e := r.QueryPendingTool(f.t.Context(), s.Run.ID, "original-tool-call")
	if e != nil || w == nil || w.Generation == 0 || w.CallID != f.physical.tools[0].Call.ID || w.ToolKey != f.definition.Key || !ea1RecoveryJSONEqual(w.PendingExecution, *f.toolResult.Pending) {
		f.t.Fatalf("original pending descriptor missing: %v", e)
	}
	return w
}
func (f *ea1ControlFixture) signal() agent.ExternalToolResultSignal {
	return agent.ExternalToolResultSignal{RunID: f.request.ID, CallID: "original-tool-call", OperationID: "original-operation", InputDigest: strings.Repeat("a", 64), SignalID: "original-ready"}
}
func ea1ApprovalPreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), vector string) {
	f := ea1NewControlFixture(t, factory, false)
	r := f.runner(f.rt, vector != "default-success", true)
	id, rev := f.request.ID, f.waiting.Run.Revision
	response := plugin.ApprovalResponse{Decision: plugin.ApprovalApprove}
	want := kernel.ErrConflict
	before := f.waiting
	switch vector {
	case "missing":
		id = "missing"
		want = kernel.ErrNotFound
	case "stale":
		rev++
	case "not-waiting":
		var e error
		before, e = f.observer.Cancel(t.Context(), id, rev, "cancel before approval")
		if e != nil {
			t.Fatal(e)
		}
		rev = before.Run.Revision
		want = agent.ErrApprovalRequired
	case "invalid-decision":
		response.Decision = "invalid"
		want = interaction.ErrInvalidApproval
	case "foreign-checkpoint":
		f.approval.foreign = true
		want = agent.ErrApprovalRequired
	}
	out, e := r.ResolveApproval(t.Context(), id, rev, response)
	if vector == "native-success" || vector == "default-success" {
		if e != nil || f.approval.calls != 1 || f.host.calls != 1 {
			t.Fatalf("real approval failed: %v", e)
		}
		m, n := f.physical.counts()
		if m != 1 {
			t.Fatal("approval repeated original Model")
		}
		if vector == "native-success" {
			if out.Run.Status != kernel.RunStatusRunning || n != 0 || out.Checkpoint == nil || out.Checkpoint.Status != kernel.CheckpointResolved || !ea1RecoveryJSONEqual(out.Checkpoint, f.approval.resolved) {
				t.Fatal("explicit native approval dispatched Tool")
			}
		} else {
			if out.Run.Status != kernel.RunStatusCompleted || n != 1 || out.Result == nil || out.Checkpoint != nil || f.approved.Run.Status != kernel.RunStatusRunning {
				t.Fatal("default approval did not complete original Tool")
			}
			ea1StartSettlementJSONEqual(t, "terminal actual Tool content", out.Result.Content, f.toolResult.Content)
			if !ea1RecoveryJSONEqual(f.physical.tools[0].Call, f.approval.call) {
				t.Fatal("approved original Call replaced")
			}
		}
		return
	}
	if !errors.Is(e, want) {
		t.Fatalf("wrong approval rejection: %v want %v", e, want)
	}
	ea1CompBarrierUnchanged(t, f.observer, before)
	m, n := f.physical.counts()
	if m != 1 || n != 0 {
		t.Fatal("rejected approval sent")
	}
	if vector == "invalid-decision" || vector == "foreign-checkpoint" {
		if f.approval.calls != 1 || f.host.calls != 1 {
			t.Fatal("handler validation not reached")
		}
	} else {
		if f.approval.calls != 0 || f.host.calls != 0 {
			t.Fatal("preflight entered callbacks")
		}
		if _, e = f.observer.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("preflight created scope")
		}
	}
}
func ea1ControlFence(t *testing.T, f *ea1ControlFixture, before kernel.Snapshot, g kernel.ChildExecutionGrant, lease kernel.ExecutionLease) {
	t.Helper()
	unchanged := func() {
		ea1CompBarrierUnchanged(t, f.observer, before)
		same, e := f.observer.LookupChildExecution(t.Context(), before.Run.ID)
		if e != nil || same != g {
			t.Fatal("control changed original grant/native binding")
		}
	}
	_, e := f.observer.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
	if !errors.Is(e, kernel.ErrExecutionRequired) {
		t.Fatalf("unbound write gate: %v", e)
	}
	unchanged()
	bound, e := f.observer.WithExecution(lease)
	if e != nil {
		t.Fatal(e)
	}
	_, e = bound.Apply(t.Context(), before.Run.ID, before.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: before.State, Checkpoint: before.Checkpoint})
	want := kernel.ErrChildExecutionBusy
	if before.Run.Status == kernel.RunStatusWaitingInput {
		want = kernel.ErrExecutionRequired
	}
	if !errors.Is(e, want) {
		t.Fatalf("native-only write gate: %v want %v", e, want)
	}
	unchanged()
	_, e = f.observer.Cancel(t.Context(), before.Run.ID, before.Run.Revision, "competing cancel")
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Cancel escaped: %v", e)
	}
	unchanged()
	_, e = f.observer.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: before.Run.ID, OwnerID: "foreign", LeaseDuration: 2 * time.Second})
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Acquire escaped: %v", e)
	}
	unchanged()
	e = f.observer.ReleaseExecution(t.Context(), lease)
	if !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("Release escaped: %v", e)
	}
	unchanged()
	e = f.peer.(kernel.ExecutionStore).CheckExecution(t.Context(), before.Run.ID, &lease)
	if before.Run.Status == kernel.RunStatusWaitingInput {
		if !errors.Is(e, kernel.ErrExecutionRequired) {
			t.Fatalf("waiting state allowed dispatch: %v", e)
		}
	} else if e != nil {
		t.Fatalf("original active lease revoked: %v", e)
	}
}
func ea1ApprovalBarrier(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	f := ea1NewControlFixture(t, factory, false)
	r := f.runner(f.rt, true, true)
	checks := 0
	var original kernel.ChildExecutionGrant
	check := func() {
		checks++
		g, e := f.observer.LookupChildExecution(t.Context(), f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		l := f.store.acquired
		if g.AdmissionOperation != "approval" || g.ExpectedRevision != f.waiting.Run.Revision || g.ExecutionEpoch != l.Epoch || g.ExecutionLeaseID != l.LeaseID || g.ExecutionOwnerID != l.OwnerID {
			t.Fatal("not original approval owner")
		}
		if original.ID == "" {
			original = g
		} else if original != g {
			t.Fatal("approval changed admitted scope")
		}
		ea1ControlFence(t, f, f.waiting, g, l)
		if _, e = f.observer.LookupOwnedPhysicalCallbackReturn(t.Context(), g); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("active approval fabricated callback return")
		}
		m, n := f.physical.counts()
		if m != 1 || n != 0 {
			t.Fatal("P1 already Tool-dispatched")
		}
	}
	f.store.afterOwned = func(context.Context, kernel.ChildExecutionGrant) error {
		if f.host.calls != 0 || f.approval.calls != 0 {
			t.Fatal("post-admission cut too late")
		}
		check()
		return nil
	}
	f.approval.before = check
	out, e := r.ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if e != nil || checks != 2 || f.host.calls != 1 || f.approval.calls != 1 || out.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("same original approval could not finish after rejected controls: %v", e)
	}
	if !ea1RecoveryJSONEqual(out.Checkpoint, f.approval.resolved) {
		t.Fatal("actual resolved checkpoint differs")
	}
	_, n := f.physical.counts()
	if n != 0 {
		t.Fatal("explicit native approval dispatched")
	}
}
func ea1AwaitControlExpiry(t *testing.T, lease kernel.ExecutionLease) {
	t.Helper()
	delay := time.Until(lease.ExpiresAt.Add(150 * time.Millisecond))
	if delay > 5*time.Second {
		t.Fatal("unexpected real native TTL")
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
}
func ea1ApprovalUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	f := ea1NewControlFixture(t, factory, false)
	failure := errors.New("actual approval external outcome unknown")
	rt := f.rt
	native := true
	if kind == "handler" {
		f.approval.failure = failure
	} else {
		f.fault.event = "tool.completed"
		f.fault.injected = failure
		lease, e := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: f.request.ID, OwnerID: "default-original-caller", LeaseDuration: 2 * time.Second})
		if e != nil {
			t.Fatal(e)
		}
		rt, e = rt.WithExecution(lease)
		if e != nil {
			t.Fatal(e)
		}
		native = false
	}
	r := f.runner(rt, native, true)
	_, e := r.ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if !errors.Is(e, failure) {
		t.Fatalf("actual unknown approval cut missing: %v", e)
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := f.store.acquired
	if g.AdmissionOperation != "approval" || g.ExpectedRevision != f.waiting.Run.Revision || g.ExecutionLeaseID != l.LeaseID || g.ExecutionEpoch != l.Epoch || g.ExecutionOwnerID != l.OwnerID {
		t.Fatal("unknown approval source changed")
	}
	c, e := f.rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || c.HostCompleted {
		t.Fatal("unknown approval Host falsely completed")
	}
	m, n := f.physical.counts()
	if m != 1 || f.approval.calls != 1 || f.host.calls != 1 || (kind == "handler" && (f.approval.sends != 1 || n != 0)) || (kind == "tool" && (n != 1 || f.fault.failures != 1)) {
		t.Fatal("unknown fixture substituted callback")
	}
	if kind == "handler" {
		ea1CompBarrierUnchanged(t, f.rt, f.waiting)
	} else {
		if !ea1RecoveryJSONEqual(before.Checkpoint, f.approval.resolved) || !ea1RecoveryJSONEqual(f.physical.tools[0].Call, f.approval.call) {
			t.Fatal("original approved Call/checkpoint changed")
		}
	}
	ea1AwaitControlExpiry(t, l)
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	f.physical.rt = freshRT
	next := f.runner(freshRT, true, true)
	_, e = next.ResolveApproval(t.Context(), f.request.ID, before.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	want := agent.ErrRootOutcomeUnknown
	if kind == "tool" {
		want = agent.ErrApprovalRequired
	}
	if !errors.Is(e, want) {
		t.Fatalf("approval retry wrong boundary: %v want %v", e, want)
	}
	ea1CompBarrierUnchanged(t, freshRT, before)
	same, e := freshRT.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || same != g {
		t.Fatal("TTL changed original approval scope")
	}
	after, e := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || !ea1RecoveryJSONEqual(c, after) {
		t.Fatal("TTL upgraded callback outcome")
	}
	a, b := f.physical.counts()
	if a != m || b != n || f.host.calls != 1 || f.approval.calls != 1 {
		t.Fatal("approval retry replayed")
	}
}
func ea1ToolWaitPhase(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), phase string) {
	f := ea1NewControlFixture(t, factory, true)
	r := f.runner(f.rt, true, false)
	wait := f.assertWait(r, f.waiting)
	before := f.waiting
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	l := f.store.acquired
	if g.AdmissionOperation != "start" || g.ChildRunID != f.request.ID || g.ExecutionOwnerID != l.OwnerID || g.ExecutionLeaseID != l.LeaseID || g.ExecutionEpoch != l.Epoch || g.AdmissionSourceHash == "" {
		t.Fatal("ToolWait lost original Start scope/native source")
	}
	c, e := f.rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
	if e != nil || !c.HostCompleted {
		t.Fatal("pending Tool lacks actual returned Host scope")
	}
	unchanged := func(rt *kernel.Runtime) {
		ea1CompBarrierUnchanged(t, rt, before)
		same, e := rt.LookupChildExecution(t.Context(), f.request.ID)
		if e != nil || same != g {
			t.Fatal("ToolWait changed original physical scope")
		}
		co, e := rt.LookupOwnedPhysicalCallbackReturn(t.Context(), g)
		if e != nil || !ea1RecoveryJSONEqual(co, c) {
			t.Fatal("ToolWait changed original callback fact")
		}
		m, n := f.physical.counts()
		if m != 1 || n != 1 {
			t.Fatal("ToolWait replayed physical operation")
		}
	}
	if phase == "P0" {
		_, e = r.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision+1, wait.Generation)
		if !errors.Is(e, kernel.ErrConflict) {
			t.Fatalf("stale wait revision: %v", e)
		}
		unchanged(f.rt)
		out, e := r.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision, wait.Generation+1)
		if e != nil || !ea1RecoveryJSONEqual(out, before) || f.lookups != 0 {
			t.Fatal("stale generation changed idempotent no-op contract")
		}
		unchanged(f.rt)
		signal := f.signal()
		signal.InputDigest = strings.Repeat("b", 64)
		_, e = r.NotifyExternalToolResultReady(t.Context(), signal)
		if !errors.Is(e, agent.ErrToolSignalConflict) {
			t.Fatalf("foreign signal accepted: %v", e)
		}
		unchanged(f.rt)
		// No-wait is separately a public no-op, not a new dispatch/admission.
		return
	}
	if phase == "P1" {
		before, e = r.NotifyExternalToolResultReady(t.Context(), f.signal())
		if e != nil {
			t.Fatal(e)
		}
		sameWait := f.assertWait(r, before)
		if sameWait.Generation != wait.Generation || sameWait.SignalID != "original-ready" {
			t.Fatal("ready signal changed original generation")
		}
		f.verify = func() { ea1ControlFence(t, f, before, g, l) }
		_, e = r.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision, wait.Generation)
		if !errors.Is(e, agent.ErrToolOutcomeUnknown) || f.lookups != 1 {
			t.Fatalf("actual ToolWait verifier boundary missing: %v", e)
		}
		unchanged(f.rt)
		return
	}
	_, e = r.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision, wait.Generation)
	if !errors.Is(e, agent.ErrToolOutcomeUnknown) || f.lookups != 0 {
		t.Fatal("unready ToolWait dispatched/queried")
	}
	unchanged(f.rt)
	before, e = r.NotifyExternalToolResultReady(t.Context(), f.signal())
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision, wait.Generation)
	if !errors.Is(e, agent.ErrToolOutcomeUnknown) || f.lookups != 1 {
		t.Fatalf("unknown verified wait accepted: %v", e)
	}
	unchanged(f.rt)
	ea1AwaitControlExpiry(t, l)
	fresh := f.reopen()
	freshRT := ea1StartRuntime(t, fresh)
	f.physical.rt = freshRT
	f.offset = 3 * time.Minute
	next := f.runner(freshRT, true, false)
	if e = fresh.(kernel.ExecutionStore).CheckExecution(t.Context(), f.request.ID, &l); !errors.Is(e, kernel.ErrExecutionConflict) {
		t.Fatal("real native lease not expired")
	}
	_, e = next.ResumeToolWait(t.Context(), f.request.ID, before.Run.Revision, wait.Generation)
	if !errors.Is(e, agent.ErrToolOutcomeUnknown) || !errors.Is(e, agent.ErrToolOutcomeOverdue) {
		t.Fatalf("overdue unresolved Tool lost exact contract: %v", e)
	}
	unchanged(freshRT)
	after := f.assertWait(next, before)
	if after.Generation != wait.Generation || after.SignalID != "original-ready" {
		t.Fatal("deadline erased original ready descriptor")
	}
}
