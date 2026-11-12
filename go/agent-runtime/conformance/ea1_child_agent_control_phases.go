package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1ChildAgentControlPhases measures only actual ResolveApproval.
// Child native approval does not acquire a Root-owned lease. Default child
// approval can continue the original Tool inside this same public call.
func RunEA1ChildAgentControlPhases(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store)) {
	for _, v := range []string{"missing", "stale", "not-waiting", "missing-preflight", "preflight-rejected", "parent-wins", "invalid-decision", "foreign-checkpoint"} {
		t.Run("P0/"+v, func(t *testing.T) { ea1ChildControlPreflight(t, factory, v) })
	}
	for _, native := range []bool{false, true} {
		name := map[bool]string{false: "default", true: "native"}[native]
		t.Run("P1/"+name, func(t *testing.T) { ea1ChildControlBarrier(t, factory, native) })
	}
	for _, v := range []string{"host", "handler", "tool"} {
		t.Run("P2/"+v, func(t *testing.T) { ea1ChildControlUnknown(t, factory, v) })
	}
}

type ea1ChildControlHost struct {
	f            *ea1ChildControlFixture
	before       func()
	failure      error
	calls, sends int
}

func (*ea1ChildControlHost) Name() string { return "ea1.actual.child.approval" }
func (h *ea1ChildControlHost) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	r := h.f.request
	if i.Operation != plugin.RunResolveApproval || i.RunID != r.ID || i.ParentRunID != r.ParentRunID || i.Actor != r.Actor || i.Thread != r.Thread || i.Goal != r.Goal {
		h.f.t.Fatal("actual child approval source changed")
	}
	h.calls++
	if h.before != nil {
		h.before()
	}
	if h.failure != nil {
		h.sends++
		return kernel.Snapshot{}, h.failure
	}
	return next(ctx)
}

// Default Child control has no bound native lease: its real receipt CAS
// reaches ordinary Apply, with the original opaque PhysicalPermit unchanged.
type ea1ChildControlStore struct {
	*ea1ChildStartStore
	faultRun string
	fault    error
	failures int
}

func (s *ea1ChildControlStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if s.fault != nil && s.failures == 0 && id == s.faultRun && m.Record.Run.ID == id &&
		m.Record.Run.Status == kernel.RunStatusCompleted && m.Record.Result != nil &&
		m.Record.Result.ContentType == "application/json" {
		for _, e := range m.Events {
			if e.Type == "tool.completed" {
				s.failures++
				return kernel.Snapshot{}, s.fault
			}
		}
	}
	return s.Store.Apply(ctx, id, rev, m)
}

type ea1ChildControlFixture struct {
	*ea1ChildStartFixture
	native            bool
	controlStore      *ea1ChildControlStore
	approval          *ea1ControlApproval
	controlHost       *ea1ChildControlHost
	waiting, approved kernel.Snapshot
	setupLease        kernel.ExecutionLease
	preflight         func(context.Context, kernel.Snapshot) error
}

func ea1NewChildControl(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), native bool) *ea1ChildControlFixture {
	f := &ea1ChildControlFixture{ea1ChildStartFixture: ea1NewChildStart(t, factory, true, false), native: native}
	f.controlStore = &ea1ChildControlStore{ea1ChildStartStore: f.store}
	f.rt = ea1StartRuntime(t, f.controlStore)
	f.physical.rt = f.rt
	approvals, e := interaction.New(f.rt)
	if e != nil {
		t.Fatal(e)
	}
	f.approval = &ea1ControlApproval{base: interactionadapter.New(approvals), t: t}
	f.controlHost = &ea1ChildControlHost{f: f}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", InputSchema: []byte(`{"type":"object"}`), Terminal: true}
	f.catalog, e = tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		s, e := f.rt.Load(ctx, f.request.ID)
		if e != nil {
			t.Fatal(e)
		}
		f.assertResolved(s)
		f.approved = s
		return f.physical.execute(ctx, r)
	})}})
	if e != nil {
		t.Fatal(e)
	}
	setup := f.runner(f.rt, false)
	f.waiting, e = setup.StartRun(t.Context(), f.request)
	if e != nil {
		t.Fatal(e)
	}
	if f.waiting.Run.Status != kernel.RunStatusWaitingInput || f.waiting.Checkpoint == nil || f.waiting.Checkpoint.Status != kernel.CheckpointPending || !reflect.DeepEqual(f.waiting.Checkpoint, f.approval.checkpoint) {
		t.Fatal("real Start did not preserve actual original checkpoint")
	}
	if f.approval.calls != 0 || f.controlHost.calls != 0 || len(f.physical.requests) != 1 || len(f.physical.toolRequests) != 0 {
		t.Fatal("setup entered measured approval/Tool")
	}
	f.receipts(f.waiting, true)
	ea1StartSettlementJSONEqual(t, "approval full original call", f.approval.call, tools.Call{ID: "original-call", ToolKey: "effect", Name: "Original Effect", Arguments: json.RawMessage(`{"source":"original"}`)})
	ea1StartSettlementJSONEqual(t, "approval original definition", f.approval.definition, definition)
	if _, e = f.rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("setup grant not retired: %v", e)
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	f.preflight = func(_ context.Context, s kernel.Snapshot) error {
		if !reflect.DeepEqual(s, f.waiting) {
			return agent.ErrInvalidRequest
		}
		return nil
	}
	f.setupLease = f.store.acquired
	f.store.admissions, f.store.marks = 0, 0
	return f
}
func (f *ea1ChildControlFixture) runner(rt *kernel.Runtime, host bool) *agent.Runner {
	d := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.catalog, Executor: f.catalog, Approvals: f.approval, ApprovalPolicies: []plugin.ApprovalPolicy{ea1ControlPolicy{}}, ApprovalControlPreflight: f.preflight, Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}, Tool: agent.CallPolicy{Timeout: 500 * time.Millisecond, MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond}}}
	if f.native {
		d.RunExecution = &agent.RunExecution{OwnerID: "child-approval-native", LeaseDuration: 2 * time.Second}
	}
	if host {
		d.RunMiddleware = []plugin.RunMiddleware{f.controlHost}
	}
	r, e := agent.NewRunner(d)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ChildControlFixture) assertResolved(s kernel.Snapshot) {
	t := f.t
	cp := s.Checkpoint
	original := f.waiting.Checkpoint
	actual := f.approval.resolved
	// This assertion is used only on resolved Running snapshots before
	// Tool receipt consumption. Terminal snapshots legitimately clear both
	// Checkpoint and pendingApproval and are checked by terminal Result instead.
	if s.Run.Status != kernel.RunStatusRunning || s.Run.ID != f.request.ID || s.Run.ID != f.waiting.Run.ID ||
		s.Run.ParentRunID != f.waiting.Run.ParentRunID || s.Run.Actor != f.waiting.Run.Actor ||
		s.Run.Thread != f.waiting.Run.Thread || s.Run.RequestID != f.waiting.Run.RequestID {
		t.Fatal("resolved checkpoint belongs to a different original Run")
	}
	var before, current struct {
		PendingApproval json.RawMessage `json:"pendingApproval"`
	}
	if json.Unmarshal(f.waiting.State, &before) != nil || json.Unmarshal(s.State, &current) != nil ||
		len(before.PendingApproval) == 0 || string(before.PendingApproval) == "null" {
		t.Fatal("original approval binding absent")
	}
	ea1StartSettlementJSONEqual(t, "original complete pending approval binding", current.PendingApproval, before.PendingApproval)
	var binding struct {
		CheckpointID string `json:"checkpointID"`
		CallID       string `json:"callID"`
		ToolKey      string `json:"toolKey"`
	}
	if json.Unmarshal(current.PendingApproval, &binding) != nil || binding.CheckpointID != original.ID ||
		binding.CallID != f.approval.call.ID || binding.ToolKey != f.approval.call.ToolKey {
		t.Fatal("resolved checkpoint detached from original approved Call")
	}
	var payload interaction.ApprovalRequest
	if json.Unmarshal(original.Payload, &payload) != nil {
		t.Fatal("original approval payload invalid")
	}
	ea1StartSettlementJSONEqual(t, "full original approval payload", payload, interaction.ApprovalRequest{
		ToolCallID: f.approval.call.ID, ToolKey: f.approval.call.ToolKey, ToolName: f.approval.call.Name, Arguments: f.approval.call.Arguments,
	})
	if cp == nil || actual == nil || cp.Status != kernel.CheckpointResolved || cp.ResolvedAt == nil || cp.ID != original.ID || cp.Kind != original.Kind || !cp.CreatedAt.Equal(original.CreatedAt) || !reflect.DeepEqual(cp.Payload, original.Payload) {
		t.Fatal("approval checkpoint original identity/payload changed")
	}
	ea1StartSettlementJSONEqual(t, "actual full resolved checkpoint", cp, actual)
	ea1StartSettlementJSONEqual(t, "actual approval response", cp.Response, json.RawMessage(`{"decision":"approve"}`))
}
func (f *ea1ChildControlFixture) scope() kernel.ChildExecutionGrant {
	g, e := f.peer.LookupChildExecution(f.t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ExpectedRevision != f.waiting.Run.Revision || g.ChildRunID != f.request.ID {
		f.t.Fatalf("wrong actual child approval grant: %+v %v", g, e)
	}
	return g
}
func (f *ea1ChildControlFixture) fence() {
	t := f.t
	ctx := t.Context()
	s := f.load(f.peer)
	g := f.scope()
	for _, a := range append(append([]kernel.Snapshot{}, f.ancestors...), s) {
		if _, e := f.peer.Cancel(ctx, a.Run.ID, a.Run.Revision, "competing approval cancel"); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("approval Cancel %s: %v", a.Run.ID, e)
		}
		f.unchanged(f.peer, s, g)
		for _, status := range []kernel.RunStatus{kernel.RunStatusRunning, kernel.RunStatusFailed} {
			_, e := f.peer.Apply(ctx, a.Run.ID, a.Run.Revision, kernel.Mutation{Status: status, State: a.State, Checkpoint: a.Checkpoint})
			want := kernel.ErrChildExecutionBusy
			if f.native && a.Run.ID == s.Run.ID {
				want = kernel.ErrExecutionRequired
			}
			if !errors.Is(e, want) {
				t.Fatalf("approval Apply %s: %v want %v", a.Run.ID, e, want)
			}
			f.unchanged(f.peer, s, g)
		}
	}
	if f.store.acquired != f.setupLease {
		t.Fatal("child approval unexpectedly acquired new native owner")
	}
	if f.native {
		if e := f.peerStore.(kernel.ExecutionStore).CheckExecution(ctx, s.Run.ID, &f.store.acquired); !errors.Is(e, kernel.ErrExecutionRequired) {
			t.Fatalf("waiting approval allowed native dispatch: %v", e)
		}
	}
	if len(f.physical.requests) != 1 || len(f.physical.toolRequests) != 0 {
		t.Fatal("P1 already dispatched original Tool")
	}
}
func ea1ChildControlPreflight(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), v string) {
	f := ea1NewChildControl(t, factory, true)
	ctx := t.Context()
	id, rev := f.request.ID, f.waiting.Run.Revision
	before := f.waiting
	response := plugin.ApprovalResponse{Decision: plugin.ApprovalApprove}
	expected := kernel.ErrConflict
	callback := false
	switch v {
	case "missing":
		id = "missing"
		expected = kernel.ErrNotFound
	case "stale":
		rev++
	case "not-waiting":
		var e error
		before, e = f.peer.Cancel(ctx, id, rev, "before approval")
		if e != nil {
			t.Fatal(e)
		}
		rev = before.Run.Revision
		expected = agent.ErrApprovalRequired
	case "missing-preflight":
		f.preflight = nil
		expected = agent.ErrApprovalRequired
	case "preflight-rejected":
		expected = errors.New("original approval admission denied")
		f.preflight = func(context.Context, kernel.Snapshot) error { return expected }
	case "parent-wins":
		expected = kernel.ErrExecutionConflict
		f.store.beforeAdmission = func(kernel.ChildExecutionGrant) {
			a, e := f.peer.Cancel(ctx, f.ancestors[0].Run.ID, f.ancestors[0].Run.Revision, "ancestor wins admission")
			if e != nil {
				t.Fatal(e)
			}
			f.ancestors[0] = a
		}
	case "invalid-decision":
		response.Decision = "invalid"
		expected = interaction.ErrInvalidApproval
		callback = true
	case "foreign-checkpoint":
		f.approval.foreign = true
		expected = agent.ErrApprovalRequired
		callback = true
	}
	_, e := f.runner(f.rt, true).ResolveApproval(ctx, id, rev, response)
	if !errors.Is(e, expected) {
		t.Fatalf("%s: %v want %v", v, e, expected)
	}
	if !callback && v != "parent-wins" && !errors.Is(e, agent.ErrControlNoEffect) {
		t.Fatalf("preflight lost exact no-effect classification: %v", e)
	}
	ea1CompBarrierUnchanged(t, f.peer, before)
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	if len(f.physical.requests) != 1 || len(f.physical.toolRequests) != 0 {
		t.Fatal("denied approval sent Tool/Model")
	}
	if callback {
		if f.approval.calls != 1 || f.controlHost.calls != 1 || !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatal("handler refusal lost unknown original scope")
		}
		f.scope()
	} else {
		if f.approval.calls != 0 || f.controlHost.calls != 0 || f.store.admissions != 0 {
			t.Fatal("preflight invoked arbitrary callback")
		}
		if _, e = f.peer.LookupChildExecution(ctx, f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("preflight created scope")
		}
	}
}
func ea1ChildControlBarrier(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), native bool) {
	f := ea1NewChildControl(t, factory, native)
	cuts := 0
	check := func() { cuts++; f.fence() }
	f.controlHost.before = check
	f.approval.before = check
	out, e := f.runner(f.rt, true).ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if e != nil || cuts != 2 || f.approval.calls != 1 || f.controlHost.calls != 1 {
		t.Fatalf("real child approval failed: %v cuts %d", e, cuts)
	}
	if native {
		if out.Run.Status != kernel.RunStatusRunning || len(f.physical.toolRequests) != 0 {
			t.Fatal("native approval unexpectedly dispatched")
		}
		f.assertResolved(out)
	} else {
		if len(f.physical.toolRequests) != 1 {
			t.Fatal("default approval did not execute original Tool")
		}
		f.receipts(out, true)
		ea1ControlTerminalResult(t, out, f.physical.receipts[0])
	}
	for _, a := range f.ancestors {
		ea1CompBarrierUnchanged(t, f.peer, a)
	}
	if _, e = f.peer.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("actual approval completion retained grant: %v", e)
	}
}
func ea1ChildControlUnknown(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store, func() kernel.Store), kind string) {
	f := ea1NewChildControl(t, factory, kind != "tool")
	stop := errors.New("original child approval callback unknown")
	switch kind {
	case "host":
		f.controlHost.failure = stop
	case "handler":
		f.approval.failure = stop
	case "tool":
		f.controlStore.faultRun = f.request.ID
		f.controlStore.fault = stop
	}
	_, e := f.runner(f.rt, true).ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if !errors.Is(e, stop) || !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("actual child approval unknown cut: %v", e)
	}
	s, g := f.load(f.peer), f.scope()
	if kind == "tool" {
		if f.controlStore.failures != 1 || len(f.physical.toolRequests) != 1 || len(f.physical.receipts) != 1 {
			t.Fatal("actual approved Tool receipt fault missed")
		}
		f.assertResolved(s)
		f.receipts(s, true)
	} else if !reflect.DeepEqual(s, f.waiting) || len(f.physical.toolRequests) != 0 {
		t.Fatal("unknown handler changed checkpoint/source or sent Tool")
	}
	if f.store.admissions != 1 || f.store.marks != 0 {
		t.Fatal("unknown approval scope retired")
	}
	f.unchanged(f.peer, s, g)
	fresh := f.reopen()
	rt := ea1StartRuntime(t, fresh)
	f.physical.rt = rt
	r := f.runner(rt, true)
	f.unchanged(rt, s, g)
	if e = r.ReconcileChildPhysicalGrant(t.Context(), s.Run.ID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown approval callback invented settlement: %v", e)
	}
	if kind == "tool" {
		if _, e = r.Resume(t.Context(), s.Run.ID, s.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) {
			t.Fatalf("unknown approved Tool replay: %v", e)
		}
		f.receipts(s, true)
	} else {
		if _, e = r.ResolveApproval(t.Context(), s.Run.ID, s.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove}); !errors.Is(e, kernel.ErrChildExecutionBusy) {
			t.Fatalf("unknown original approval replay: %v", e)
		}
	}
	f.unchanged(rt, s, g)
	if f.controlHost.calls != 1 || f.approval.calls != map[bool]int{true: 0, false: 1}[kind == "host"] {
		t.Fatal("approval Host/handler replayed")
	}
	// No new native lease is acquired by Child ResolveApproval; do not
	// describe this durable reopen check as a Root lease-expiry proof.
}
