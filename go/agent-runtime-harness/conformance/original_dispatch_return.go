package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type dispatchReturnInterrupted struct{}

// A reopened Host wrapper must not erase persisted native execution authority.
type wrappedReturnAgent struct{ *agent.Runner }

type returnedDispatchKernelCut struct {
	noEntryKernelPorts
	cut   string
	grant kernel.ChildExecutionGrant
}

var errApprovalControlCut = errors.New("original approval control interrupted")

func (s *returnedDispatchKernelCut) CheckOwnedPhysicalEntry(ctx context.Context, g kernel.ChildExecutionGrant, l kernel.ExecutionLease, v kernel.Snapshot) (kernel.OwnedPhysicalEntryWindow, error) {
	if s.cut == "approval-no-entry" {
		return kernel.OwnedPhysicalEntryWindow{}, errApprovalControlCut
	}
	return s.noEntryKernelPorts.CheckOwnedPhysicalEntry(ctx, g, l, v)
}

func (s *returnedDispatchKernelCut) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Control != nil {
		g := *commitAuth.Control

		if g.Kind == kernel.ControlApproval && s.cut == "approval-control-before" {
			return kernel.Snapshot{}, false, errApprovalControlCut
		}
		v, revoked, err := s.noEntryKernelPorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && g.Kind == kernel.ControlApproval && s.cut == "approval-control-after" {
			return kernel.Snapshot{}, false, errApprovalControlCut
		}
		return v, revoked, err
	}
	return s.noEntryKernelPorts.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

type substitutedApprovalHandler struct{ plugin.ApprovalHandler }

func (h substitutedApprovalHandler) ResolveToolApproval(c *kernel.Checkpoint, r plugin.ApprovalResponse) (*kernel.Checkpoint, error) {
	r.Comment = "substituted"
	return h.ApprovalHandler.ResolveToolApproval(c, r)
}

func (s *returnedDispatchKernelCut) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	if s.cut != "" {
		s.grant = g
	}
	if s.cut == "return-before" {
		panic(dispatchReturnInterrupted{})
	}
	err := s.noEntryKernelPorts.RecordPhysicalCallbackReturn(ctx, g, rev, o, p)
	if err == nil && s.cut == "return-after" {
		panic(dispatchReturnInterrupted{})
	}
	return err
}
func (s *returnedDispatchKernelCut) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	if s.cut == "finish-before" {
		panic(dispatchReturnInterrupted{})
	}
	err := s.noEntryKernelPorts.FinishChildExecution(ctx, g, p)
	if err == nil && s.cut == "finish-after" {
		panic(dispatchReturnInterrupted{})
	}
	return err
}

type returnedDispatchHostCut struct {
	harness.Store
	cut string
}

func (s *returnedDispatchHostCut) AppendItem(ctx context.Context, i harness.Item) (harness.Item, bool, error) {
	hit := s.cut != "" && i.Status == harness.ItemCompleted && (i.Kind == harness.ItemApproval || i.Kind == harness.ItemDiagnostic && string(i.Payload) == `{"boundary":"physical_dispatch_returned"}`)
	if hit && s.cut == "host-before" {
		panic(dispatchReturnInterrupted{})
	}
	out, fresh, err := s.Store.AppendItem(ctx, i)
	if err == nil && hit && s.cut == "host-after" {
		panic(dispatchReturnInterrupted{})
	}
	return out, fresh, err
}

func (s *returnedDispatchHostCut) UpdateInvocation(ctx context.Context, v harness.Invocation, rev uint64) (harness.Invocation, error) {
	terminal := v.Status == harness.InvocationCompleted || v.Status == harness.InvocationFailed || v.Status == harness.InvocationCancelled
	if terminal && s.cut == "projection-before" {
		panic(dispatchReturnInterrupted{})
	}
	out, e := s.Store.UpdateInvocation(ctx, v, rev)
	if terminal && s.cut == "projection-after" && e == nil {
		panic(dispatchReturnInterrupted{})
	}
	return out, e
}

type originalDispatchModel struct {
	phase    string
	rejected bool
	calls    int
}

const dispatchReturnTool = "return.fixture"

func (m *originalDispatchModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	if m.phase == "resume" && m.calls == 1 {
		return model.Response{Content: "next", Continue: true}, nil
	}
	if m.phase == "approval" && m.calls == 1 {
		return model.Response{ToolCalls: []tools.Call{{ID: "original-write", ToolKey: dispatchReturnTool, Arguments: json.RawMessage(`{}`)}}}, nil
	}
	if m.rejected {
		return model.Response{ResponseID: "original-rejected", Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, model.NewResponseRejectedError(model.NewClassifiedError(errors.New("fixture rejection"), "model.upstream_request_invalid"))
	}
	return model.Response{Content: "original result"}, nil
}

// OriginalDispatchReturn uses the exact public Harness and native Agent
// entrypoints with Store-backed cuts. It reopens both Stores, never fabricates
// a completion Item or invokes a new physical callback to close the old one.
func OriginalDispatchReturn(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	for _, phase := range []string{"start", "resume", "approval"} {
		cuts := []string{"return-before", "return-after", "finish-before", "finish-after"}
		if phase != "approval" {
			cuts = append(cuts, "projection-before", "projection-after")
		} else {
			cuts = append(cuts, "host-before", "host-after")
		}
		for _, cut := range cuts {
			t.Run(phase+"/"+cut, func(t *testing.T) { exerciseOriginalDispatchReturn(t, factory, phase, cut, false) })
		}
		t.Run(phase+"/rejected", func(t *testing.T) { exerciseOriginalDispatchReturn(t, factory, phase, "return-after", true) })
	}
	for _, cut := range []string{"approval-no-entry", "approval-not-applied", "approval-control-before", "approval-control-after", "approval-user-rejected", "approval-conflict-admission"} {
		t.Run("approval/"+cut, func(t *testing.T) { exerciseOriginalDispatchReturn(t, factory, "approval", cut, false) })
	}
}
func exerciseOriginalDispatchReturn(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store)), phase, cut string, rejected bool) {
	t.Helper()
	ctx := t.Context()
	backing, host, reopen := factory(t)
	ports, ok := backing.(noEntryKernelPorts)
	if !ok {
		t.Fatal("missing native retirement ports")
	}
	kcut := &returnedDispatchKernelCut{noEntryKernelPorts: ports}
	hcut := &returnedDispatchHostCut{Store: host}
	provider := &originalDispatchModel{phase: phase, rejected: rejected}
	writes := 0
	reg, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: dispatchReturnTool, Name: "return fixture", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
			writes++
			return tools.ExecutionResult{Content: json.RawMessage(`{"result":"ok"}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	makeRunner := func(k kernel.Store, h harness.Store) (*harness.Runner, *kernel.Runtime, *agent.Runner) {
		t.Helper()
		rt, e := kernel.New(kernel.Dependencies{Store: k})
		if e != nil {
			t.Fatal(e)
		}
		approvals, e := interaction.New(rt)
		if e != nil {
			t.Fatal(e)
		}
		policy, e := agent.NewBoundedToolApproval(func(context.Context, tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
			return plugin.ApprovalRequired, nil
		})
		if e != nil {
			t.Fatal(e)
		}
		var approvalHandler plugin.ApprovalHandler = interactionadapter.New(approvals)
		if k == kcut && cut == "approval-not-applied" {
			approvalHandler = substitutedApprovalHandler{approvalHandler}
		}
		native, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: reg, Executor: reg, Approvals: approvalHandler, ApprovalPolicies: []plugin.ApprovalPolicy{policy}})
		if e != nil {
			t.Fatal(e)
		}
		r, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: h, Catalog: reg})
		if e != nil {
			t.Fatal(e)
		}
		return r, rt, native
	}
	r, rt, _ := makeRunner(kcut, hcut)
	req := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "return-thread"}, HostTurn: harness.HostRef{Kind: "turn", ID: "return-turn"},
		Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "return-thread"}, Goal: "return original result",
		Config: harness.ConfigSnapshot{Environment: harness.VersionRef{ID: "test", Revision: 1}, AgentSpec: harness.AgentSpec{Model: "fixture", ToolKeys: []string{dispatchReturnTool}}, ToolPolicies: []harness.ToolPolicySnapshot{{Key: dispatchReturnTool, DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write"}}},
	}
	sessionID, e := harness.SessionID(req.HostThread)
	if e != nil {
		t.Fatal(e)
	}
	turnID, e := harness.TurnID(sessionID, req.HostTurn)
	if e != nil {
		t.Fatal(e)
	}
	var initial harness.Snapshot
	var checkpoint string
	if phase != "start" {
		initial, e = r.StartAgentTurn(ctx, req)
		if e != nil {
			t.Fatal(e)
		}
		if phase == "approval" {
			root, ok := harness.TopLevelInvocation(initial)
			if !ok {
				t.Fatal("missing root")
			}
			s, e := rt.Load(ctx, root.ExecutionRefID)
			if e != nil || s.Checkpoint == nil {
				t.Fatalf("missing approval: %v", e)
			}
			checkpoint = s.Checkpoint.ID
		}
	}
	if cut == "approval-conflict-admission" {
		root, _ := harness.TopLevelInvocation(initial)
		parentID := fixtureStableID("hia", turnID, checkpoint)
		parent, err := host.GetItem(ctx, parentID)
		if err != nil {
			t.Fatal(err)
		}
		fact := harness.Item{ID: fixtureStableID("hia", turnID, checkpoint, "decision"), TurnID: turnID, RunID: root.ExecutionRefID, InvocationID: root.ID, Kind: harness.ItemApproval, Status: harness.ItemCompleted, ParentItemID: parentID,
			Payload: json.RawMessage(`{"checkpointID":"` + checkpoint + `","decision":"reject","comment":"contradictory"}`), CreatedAt: parent.CreatedAt, UpdatedAt: parent.UpdatedAt}
		if _, _, err = host.AppendItem(ctx, fact); err != nil {
			t.Fatal(err)
		}
		_, err = r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "original decision"})
		if !errors.Is(err, harness.ErrConflict) || provider.calls != 1 || writes != 0 {
			t.Fatalf("contradictory prior decision admitted effects: %v %d/%d", err, provider.calls, writes)
		}
		items, err := host.ListItems(ctx, turnID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if phase, ok := harness.VerifiedExecutionDispatchGrant(item); ok && phase == "resolve-approval" {
				t.Fatal("conflicting decision acquired new dispatch")
			}
		}
		return
	}
	if cut == "approval-user-rejected" {
		// Same terminal-return fault, but the original Host explicitly
		// denied the write. No effect may be invented on replay.
		kcut.cut = "return-after"
	} else {
		kcut.cut = cut
	}
	hcut.cut = cut
	if cut == "approval-no-entry" || cut == "approval-not-applied" || cut == "approval-control-before" || cut == "approval-control-after" {
		_, err := r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "original decision"})
		if err == nil {
			t.Fatal("approval failure cut missed")
		}
		root, _ := harness.TopLevelInvocation(initial)
		native, err := rt.Load(ctx, root.ExecutionRefID)
		if err != nil {
			t.Fatal(err)
		}
		items, err := host.ListItems(ctx, turnID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		wantApplied := cut == "approval-control-after"
		wantDecision := 0
		if wantApplied {
			wantDecision = 1
		}
		assertOriginalApprovalDecision(t, items, checkpoint, "approve", "original decision", wantDecision)
		if provider.calls != 1 || writes != 0 || native.Checkpoint == nil || native.Checkpoint.ID != checkpoint {
			t.Fatal("approval control cut dispatched or changed the original gate")
		}
		decisions, err := rt.NativeApprovalDecisions(ctx, native)
		if err != nil || len(decisions) != wantDecision || wantApplied && decisions[0] != (kernel.ManagedApprovalDecision{CheckpointID: checkpoint, Decision: "approve", Comment: "original decision"}) {
			t.Fatalf("wrong native approval marker: %+v %v", decisions, err)
		}
		if harness.PendingExecutionDispatch(items, root.ID) {
			t.Fatal("known returned control did not close")
		}
		backing, host = reopen()
		r, _, _ = makeRunner(backing, host)
		if wantApplied {
			if native.Checkpoint.Status != kernel.CheckpointResolved {
				t.Fatal("original approval not committed")
			}
			if _, err = r.Refresh(ctx, turnID); err != nil {
				t.Fatal(err)
			}
			initial, err = r.ResumeTurn(ctx, turnID)
		} else {
			if native.Checkpoint.Status != kernel.CheckpointPending {
				t.Fatal("unapplied approval resolved the gate")
			}
			initial, err = r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "original decision"})
		}
		if err != nil || initial.Turn.Status != harness.TurnCompleted || provider.calls != 2 || writes != 1 {
			t.Fatalf("original control did not finish exactly once: %v %s %d/%d", err, initial.Turn.Status, provider.calls, writes)
		}
		assertOriginalApprovalDecision(t, initial.Items, checkpoint, "approve", "original decision", 1)
		return
	}
	func() {
		defer func() {
			if _, ok := recover().(dispatchReturnInterrupted); !ok {
				t.Fatal("cut not reached")
			}
		}()
		switch phase {
		case "start":
			_, _ = r.StartAgentTurn(ctx, req)
		case "resume":
			_, _ = r.ResumeTurn(ctx, turnID)
		case "approval":
			decision := harness.ApprovalApprove
			if cut == "approval-user-rejected" {
				decision = harness.ApprovalReject
			}
			_, _ = r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: decision, Comment: "original decision"})
		}
	}()
	pending, e := r.Load(ctx, turnID)
	if e != nil {
		t.Fatal(e)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing root")
	}
	source, e := rt.Load(ctx, root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	wantNative := kernel.RunStatusCompleted
	wantTurn := harness.TurnCompleted
	if rejected {
		wantNative, wantTurn = kernel.RunStatusFailed, harness.TurnFailed
	}
	if source.Run.Status != wantNative {
		t.Fatalf("cut missed terminal native source: %s", source.Run.Status)
	}
	wantCalls := 1
	if phase != "start" {
		wantCalls = 2
	}
	wantWrites := 0
	if phase == "approval" && cut != "approval-user-rejected" {
		wantWrites = 1
	}
	if provider.calls != wantCalls || writes != wantWrites {
		t.Fatalf("wrong original effects: %d/%d", provider.calls, writes)
	}
	items, e := host.ListItems(ctx, turnID, 0, 1000)
	if e != nil {
		t.Fatal(e)
	}
	var dispatch harness.Item
	expectedPhase := phase
	if phase == "approval" {
		expectedPhase = "resolve-approval"
	}
	for _, i := range items {
		if p, valid := harness.VerifiedExecutionDispatchGrant(i); valid && p == expectedPhase {
			if dispatch.ID != "" {
				t.Fatal("duplicate original dispatch")
			}
			dispatch = i
		}
	}
	if dispatch.ID != "" || harness.PendingExecutionDispatch(items, root.ID) {
		t.Fatal("native command still created Host dispatch")
	}
	originalDecision := "approve"
	if cut == "approval-user-rejected" {
		originalDecision = "reject"
	}
	if phase == "approval" {
		want := 0
		if cut == "host-after" {
			want = 1
		}
		assertOriginalApprovalDecision(t, items, checkpoint, originalDecision, "original decision", want)
		decisions, err := rt.NativeApprovalDecisions(ctx, source)
		if err != nil || len(decisions) != 1 || decisions[0] != (kernel.ManagedApprovalDecision{CheckpointID: checkpoint, Decision: originalDecision, Comment: "original decision"}) {
			t.Fatalf("native applied decision missing: %+v %v", decisions, err)
		}
	}
	op := plugin.RunStart
	switch phase {
	case "resume":
		op = plugin.RunResume
	case "approval":
		op = plugin.RunResolveApproval
	}
	backing, host = reopen()
	r, rt, native := makeRunner(backing, host)
	if phase != "approval" && cut == "return-before" {
		wrapped, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: &wrappedReturnAgent{native}, Store: host, Catalog: reg})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = wrapped.Refresh(ctx, turnID); !errors.Is(err, harness.ErrExecutionOutcomeUnknown) {
			t.Fatalf("reopened wrapper bypassed native retirement: %v", err)
		}
	}
	if kind, e := native.ReconcileOriginalPhysicalDispatch(ctx, root.ExecutionRefID, op, dispatch.ID+"-foreign"); e != nil || kind != agent.PhysicalCallUnknown {
		t.Fatalf("foreign dispatch accepted: %v %v", kind, e)
	}
	if phase == "start" {
		if ok, e := native.ReconcileUnenteredPhysicalDispatch(ctx, root.ExecutionRefID, op, "nativecmd_foreign"); e != nil || ok {
			t.Fatalf("foreign no-entry proof accepted: %v %v", ok, e)
		}
	}
	var done harness.Snapshot
	// Both observation and continuation must consume the same durable fact.
	// Neither can dispatch another callback for an already terminal source.
	if cut == "finish-before" {
		done, e = r.ResumeTurn(ctx, turnID)
	} else {
		done, e = r.Refresh(ctx, turnID)
	}
	if cut == "return-before" {
		if phase == "approval" {
			facts, err := host.ListItems(ctx, turnID, 0, 1000)
			if err != nil {
				t.Fatal(err)
			}
			assertOriginalApprovalDecision(t, facts, checkpoint, originalDecision, "original decision", 0)
		}
		if !errors.Is(e, harness.ErrExecutionOutcomeUnknown) {
			t.Fatalf("unknown callback allowed terminal projection: %v", e)
		}
		if g, e := rt.LookupChildExecution(ctx, root.ExecutionRefID); e != nil || g != kcut.grant {
			t.Fatalf("unknown grant lost: %v", e)
		}
		return
	}
	if e != nil || done.Turn.Status != wantTurn {
		t.Fatalf("original dispatch could not close: %s %v", done.Turn.Status, e)
	}
	receipt, e := rt.LoadOwnedRetirement(ctx, kcut.grant.ID)
	if e != nil || !receipt.Valid() || receipt.Evidence.Grant != kcut.grant ||
		!receipt.Evidence.Completion.HostCompleted || kernel.ManagedDigest(receipt.Evidence.Snapshot) != kernel.ManagedDigest(source) {
		t.Fatalf("original retirement not preserved: %v %+v", e, receipt)
	}
	again, e := r.ResumeTurn(ctx, turnID)
	if e != nil || again.Turn.Status != wantTurn || again.Turn.Revision != done.Turn.Revision {
		t.Fatalf("terminal replay changed original: %v", e)
	}
	if phase == "approval" {
		if _, e = r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove}); !errors.Is(e, harness.ErrConflict) {
			t.Fatalf("stale approval accepted: %v", e)
		}
	}
	items, e = host.ListItems(ctx, turnID, 0, 1000)
	if phase == "approval" {
		assertOriginalApprovalDecision(t, items, checkpoint, originalDecision, "original decision", 1)
	}
	for _, i := range items {
		if p, ok := harness.VerifiedExecutionDispatchGrant(i); ok && p == expectedPhase {
			t.Fatal("recovery created a Host grant")
		}
	}
	if e != nil || harness.PendingExecutionDispatch(items, root.ID) || provider.calls != wantCalls || writes != wantWrites {
		t.Fatalf("original native effects mismatch: %v %d/%d", e, provider.calls, writes)
	}
}

func assertOriginalApprovalDecision(t *testing.T, items []harness.Item, checkpoint, decision, comment string, want int) {
	t.Helper()
	count := 0
	for _, item := range items {
		if item.Kind != harness.ItemApproval || item.Status != harness.ItemCompleted {
			continue
		}
		var fact struct {
			CheckpointID string
			Decision     string
			Comment      string
		}
		if json.Unmarshal(item.Payload, &fact) != nil || fact.CheckpointID != checkpoint || fact.Decision != decision || fact.Comment != comment {
			t.Fatalf("wrong decision: %s", item.Payload)
		}
		count++
	}
	if count != want {
		t.Fatalf("original decision count=%d want=%d", count, want)
	}
}
