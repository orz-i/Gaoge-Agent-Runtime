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

type returnedDispatchKernelCut struct {
	noEntryKernelPorts
	cut   string
	grant kernel.ChildExecutionGrant
}

func (s *returnedDispatchKernelCut) RecordOwnedPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.OwnedPhysicalCallbackOutcome, p kernel.OwnedPhysicalCallbackReturn) error {
	if s.cut != "" {
		s.grant = g
	}
	if s.cut == "return-before" {
		panic(dispatchReturnInterrupted{})
	}
	err := s.noEntryKernelPorts.RecordOwnedPhysicalCallbackReturn(ctx, g, rev, o, p)
	if err == nil && s.cut == "return-after" {
		panic(dispatchReturnInterrupted{})
	}
	return err
}
func (s *returnedDispatchKernelCut) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant) error {
	if s.cut == "finish-before" {
		panic(dispatchReturnInterrupted{})
	}
	err := s.noEntryKernelPorts.FinishChildExecution(ctx, g)
	if err == nil && s.cut == "finish-after" {
		panic(dispatchReturnInterrupted{})
	}
	return err
}
func (s *returnedDispatchKernelCut) AcknowledgeOwnedRetirement(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.OwnedRetirementAcknowledgement) (kernel.OwnedRetirementAckResult, error) {
	result, err := s.noEntryKernelPorts.AcknowledgeOwnedRetirement(ctx, g, p)
	if err == nil && s.cut == "ack-after" {
		panic(dispatchReturnInterrupted{})
	}
	return result, err
}

type returnedDispatchHostCut struct {
	harness.Store
	cut string
}

func (s *returnedDispatchHostCut) AppendItem(ctx context.Context, i harness.Item) (harness.Item, bool, error) {
	hit := s.cut != "" && i.Kind == harness.ItemDiagnostic && i.Status == harness.ItemCompleted && string(i.Payload) == `{"boundary":"physical_dispatch_returned"}`
	if hit && s.cut == "host-before" {
		panic(dispatchReturnInterrupted{})
	}
	out, fresh, err := s.Store.AppendItem(ctx, i)
	if err == nil && hit && s.cut == "host-after" {
		panic(dispatchReturnInterrupted{})
	}
	return out, fresh, err
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
		for _, cut := range []string{"return-before", "return-after", "finish-before", "finish-after", "ack-after", "host-before", "host-after"} {
			t.Run(phase+"/"+cut, func(t *testing.T) { exerciseOriginalDispatchReturn(t, factory, phase, cut, false) })
		}
		t.Run(phase+"/rejected", func(t *testing.T) { exerciseOriginalDispatchReturn(t, factory, phase, "return-after", true) })
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
		native, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: reg, Executor: reg, Approvals: interactionadapter.New(approvals), ApprovalPolicies: []plugin.ApprovalPolicy{policy}})
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
	kcut.cut = cut
	hcut.cut = cut
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
			_, _ = r.ResolveApproval(ctx, turnID, harness.ResolveApprovalRequest{CheckpointID: checkpoint, Decision: harness.ApprovalApprove, Comment: "original decision"})
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
	if phase == "approval" {
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
	if dispatch.ID == "" || harness.PendingExecutionDispatch(items, root.ID) != (cut != "host-after") {
		t.Fatal("wrong original dispatch cut")
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
	if kind, e := native.ReconcileOriginalPhysicalDispatch(ctx, root.ExecutionRefID, op, dispatch.ID+"-foreign"); e != nil || kind != agent.PhysicalCallUnknown {
		t.Fatalf("foreign dispatch accepted: %v %v", kind, e)
	}
	if ok, e := native.ReconcileUnenteredPhysicalDispatch(ctx, root.ExecutionRefID, op, dispatch.ID); e != nil || ok {
		t.Fatalf("actual return reclassified as no-entry: %v %v", ok, e)
	}
	var done harness.Snapshot
	// Both observation and continuation must consume the same durable fact.
	// Neither can dispatch another callback for an already terminal source.
	if cut == "finish-before" || cut == "ack-after" {
		done, e = r.ResumeTurn(ctx, turnID)
	} else {
		done, e = r.Refresh(ctx, turnID)
	}
	if cut == "return-before" {
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
	if e != nil || !receipt.Valid() || receipt.Phase != "ACKNOWLEDGED" || receipt.Evidence.Grant != kcut.grant ||
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
	boundary, valid := harness.VerifiedExecutionDispatchReceipt(dispatch, items)
	if e != nil || harness.PendingExecutionDispatch(items, root.ID) || !valid || boundary != "physical_dispatch_returned" || provider.calls != wantCalls || writes != wantWrites {
		t.Fatalf("original Host receipt/effects mismatch: %q %t %v %d/%d", boundary, valid, e, provider.calls, writes)
	}
}
