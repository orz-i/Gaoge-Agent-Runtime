package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Only AC-A P3/P4/P5, actual Child ResolveApproval. Explicit native approval
// yields Running/tool0; default Child authority may finish the approved Tool.
// This is not Root's automatic owned authority or a substitute ResumeToolWait.
func RunEA1ChildAgentApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store))) {
	for _, native := range []bool{true, false} {
		mode := map[bool]string{true: "native-checkpoint", false: "default-terminal-tool"}[native]
		for _, phase := range []string{"P3", "P4"} {
			t.Run(phase+"/"+mode, func(t *testing.T) { ea1ChildApprovalSettlement(t, factory, phase, native) })
		}
		t.Run("P5/"+mode+"/two-approvals", func(t *testing.T) { ea1ChildApprovalElection(t, factory, native, false) })
	}
	t.Run("P5/ancestor-deadline", func(t *testing.T) { ea1ChildApprovalElection(t, factory, false, true) })
}

type ea1ChildApprovalSettlementFixture struct {
	t                                               *testing.T
	native                                          bool
	store                                           *ea1ChildResumeSettlementStore
	rt                                              *kernel.Runtime
	reopen                                          func() (kernel.Store, kernel.Store)
	request                                         agent.StartRequest
	parent, grandparent, created, waiting, approved kernel.Snapshot
	physical                                        *ea1StartSettlementPhysical
	approval                                        *ea1ControlApproval
	definition                                      tools.Definition
	registry                                        *tools.Registry
	preflights                                      atomic.Int64
}

func ea1NewChildApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native, deadline bool) *ea1ChildApprovalSettlementFixture {
	backing, reopen := factory(t)
	store := &ea1ChildResumeSettlementStore{ea1AgentAdmissionStore: ea1AgentAdmissionWrap(t, backing), fault: errors.New("actual child approval settlement cut")}
	rt := ea1StartRuntime(t, store)
	f := &ea1ChildApprovalSettlementFixture{t: t, native: native, store: store, rt: rt, reopen: reopen}
	f.parent, f.grandparent = ea1ChildSettlementAncestors(t, rt, deadline)
	actor, thread := f.parent.Run.Actor, f.parent.Run.Thread
	var e error
	f.request = agent.StartRequest{ID: "child-approval-settlement", RequestID: "original-child-approval", ParentRunID: f.parent.Run.ID, OriginFingerprint: agent.FrozenOriginFingerprint([]byte(`{"source":"original-child-approval","context":"isolated"}`)), Actor: actor, Thread: thread, Goal: "original child approval", Instructions: "Preserve original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), ToolKeys: []string{"effect"}, Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	f.physical = &ea1StartSettlementPhysical{rt: rt, tool: true}
	f.definition = tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	interactions, e := interaction.New(rt)
	if e != nil {
		t.Fatal(e)
	}
	f.approval = &ea1ControlApproval{base: interactionadapter.New(interactions), t: t}
	f.registry, e = tools.NewRegistry([]tools.Registration{{Definition: f.definition, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		approved, err := f.rt.Load(ctx, f.request.ID)
		if err != nil || approved.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(approved.Checkpoint, f.approval.resolved) || !ea1RecoveryJSONEqual(r.Call, f.approval.call) {
			t.Fatal("physical Tool lacks original durable approval", err)
		}
		f.approved = approved
		return f.physical.execute(ctx, r)
	})}})
	if e != nil {
		t.Fatal(e)
	}
	setup := f.runner(rt, "setup-owner", false)
	store.afterCreate = func(_ context.Context, s kernel.Snapshot) error {
		f.created = s
		if !setup.VerifyFrozenStartRequest(s, f.request) || s.Run.ParentRunID != f.parent.Run.ID {
			t.Fatal("actual child Create source mismatch")
		}
		return nil
	}
	f.waiting, e = setup.StartRun(t.Context(), f.request)
	if e != nil || f.waiting.Run.Status != kernel.RunStatusWaitingInput || f.created.Run.ID != f.request.ID || !setup.VerifyFrozenStartRequest(f.waiting, f.request) {
		t.Fatal("legal original child approval setup failed", e)
	}
	if f.waiting.Checkpoint == nil || f.waiting.Checkpoint.Status != kernel.CheckpointPending || !ea1RecoveryJSONEqual(f.waiting.Checkpoint, f.approval.checkpoint) || !ea1RecoveryJSONEqual(f.approval.definition, f.definition) || f.approval.call.ID != "original-call" || f.approval.call.ToolKey != "effect" {
		t.Fatal("original pending checkpoint/call/catalog missing")
	}
	if len(f.physical.requests) != 1 || len(f.physical.toolRequests) != 0 || f.approval.calls != 0 || store.begins != 1 || store.marks != 1 || store.finishes != 1 {
		t.Fatal("setup callbacks or source scope count wrong")
	}
	if _, e = rt.LookupChildExecution(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatal("original Start scope not retired", e)
	}
	expectedClaims := 0
	if native {
		expectedClaims = 1
	}
	if store.acquireCalls != expectedClaims || store.releaseCalls != expectedClaims {
		t.Fatal("setup did not preserve explicit/default Child native policy")
	}
	store.begins, store.marks, store.finishes = 0, 0, 0
	ea1CompBarrierUnchanged(t, rt, f.parent)
	ea1CompBarrierUnchanged(t, rt, f.grandparent)
	return f
}
func (f *ea1ChildApprovalSettlementFixture) runner(rt *kernel.Runtime, owner string, control bool) *agent.Runner {
	deps := agent.Dependencies{Runtime: rt, Model: f.physical, Catalog: f.registry, Executor: f.registry, Approvals: f.approval, ApprovalPolicies: []plugin.ApprovalPolicy{ea1ControlPolicy{}}}
	if f.native {
		deps.RunExecution = &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}
	}
	if control {
		deps.ApprovalControlPreflight = func(ctx context.Context, s kernel.Snapshot) error {
			f.preflights.Add(1)
			if !ea1RecoveryJSONEqual(s, f.waiting) {
				return agent.ErrApprovalRequired
			}
			for _, original := range []kernel.Snapshot{f.parent, f.grandparent} {
				current, e := rt.Load(ctx, original.Run.ID)
				if e != nil {
					return e
				}
				if !ea1RecoveryJSONEqual(current, original) {
					return agent.ErrApprovalRequired
				}
			}
			return nil
		}
	}
	r, e := agent.NewRunner(deps)
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *ea1ChildApprovalSettlementFixture) assertReceipt(snapshot kernel.Snapshot) {
	f.t.Helper()
	if f.approval.calls != 1 || f.approval.resolved == nil || f.approval.resolved.ID != f.waiting.Checkpoint.ID || f.approval.resolved.Status != kernel.CheckpointResolved || !ea1RecoveryJSONEqual(f.approval.resolved.Payload, f.waiting.Checkpoint.Payload) {
		f.t.Fatal("actual approval replaced checkpoint identity/source")
	}
	expectedCheckpoint := *f.waiting.Checkpoint
	expectedCheckpoint.Status = kernel.CheckpointResolved
	expectedCheckpoint.Response = json.RawMessage(`{"decision":"approve","comment":"original decision"}`)
	expectedCheckpoint.ResolvedAt = f.approval.resolved.ResolvedAt
	if expectedCheckpoint.ResolvedAt == nil || expectedCheckpoint.ResolvedAt.Before(expectedCheckpoint.CreatedAt) || !ea1RecoveryJSONEqual(&expectedCheckpoint, f.approval.resolved) {
		f.t.Fatal("real handler changed full checkpoint or original decision")
	}
	originalView, originalErr := agent.ViewState(f.waiting)
	currentView, currentErr := agent.ViewState(snapshot)
	if originalErr != nil || currentErr != nil {
		f.t.Fatal("approval budget view missing")
	}
	expectedBudget := originalView.Budget
	if !f.native {
		expectedBudget.Usage.ToolCalls++
		if snapshot.Result == nil {
			f.t.Fatal("terminal Tool result absent")
		}
		expectedBudget.Usage.OutputBytes = len(snapshot.Result.Content)
	}
	if !ea1RecoveryJSONEqual(expectedBudget, currentView.Budget) {
		f.t.Fatal("approval changed budget beyond actual original Tool settlement")
	}
	ea1AssertStartSettlementReceipt(f.t, f.physical, f.request, f.definition, snapshot, !f.native)
	if f.native {
		if snapshot.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(snapshot.Checkpoint, f.approval.resolved) || len(f.physical.toolRequests) != 0 {
			f.t.Fatal("explicit native approval changed continuation timing")
		}
	} else if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Checkpoint != nil || f.approved.Run.Status != kernel.RunStatusRunning || !ea1RecoveryJSONEqual(f.approved.Checkpoint, f.approval.resolved) {
		f.t.Fatal("default child did not consume exact durable approval")
	}
}
func ea1ChildApprovalSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), phase string, native bool) {
	f := ea1NewChildApprovalSettlement(t, factory, native, false)
	f.store.mode = phase
	_, e := f.runner(f.rt, "original-approval-owner", true).ResolveApproval(t.Context(), f.request.ID, f.waiting.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
	if !errors.Is(e, f.store.fault) || !errors.Is(e, kernel.ErrChildExecutionBusy) || f.preflights.Load() != 1 {
		t.Fatal("actual approval cut not reached", e)
	}
	before, e := f.rt.Load(t.Context(), f.request.ID)
	if e != nil {
		t.Fatal(e)
	}
	g, e := f.rt.LookupChildExecution(t.Context(), f.request.ID)
	if e != nil || g.IsOwned() || g.ExpectedRevision != f.waiting.Run.Revision || g.ChildRunID != f.request.ID || f.store.begins != 1 {
		t.Fatal("original approval ChildGrant missing", e)
	}
	if (phase == "P3" && f.store.marks != 0) || (phase == "P4" && f.store.marks != 1) || f.store.finishes != 0 {
		t.Fatal("wrong original callback Mark/Finish boundary")
	}
	f.assertReceipt(before)
	ea1ChildRecoveryPhaseControls(t, f.rt, before, f.parent, f.grandparent, g)
	a, _ := f.reopen()
	ar := ea1StartRuntime(t, a)
	fresh := f.runner(ar, "fresh-recovery", true)
	ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID)
	if phase == "P3" {
		if !errors.Is(e, kernel.ErrChildExecutionBusy) || !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatal("receipt alone invented missing approval marker", e)
		}
		ea1ChildRecoveryPhaseUnchanged(t, ar, before, f.parent, f.grandparent, g)
	} else {
		if e != nil {
			t.Fatal("original marker ACK recovery failed", e)
		}
		ea1CompBarrierUnchanged(t, ar, before)
		ea1CompBarrierUnchanged(t, ar, f.parent)
		ea1CompBarrierUnchanged(t, ar, f.grandparent)
		if e = fresh.ReconcileChildPhysicalGrant(t.Context(), f.request.ID); !errors.Is(e, kernel.ErrNotFound) {
			t.Fatal("original approval marker replayed", e)
		}
	}
	_, e = fresh.ResolveApproval(t.Context(), f.request.ID, before.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove, Comment: "original decision"})
	if !errors.Is(e, agent.ErrApprovalRequired) || f.preflights.Load() != 1 {
		t.Fatal("completed approval replay passed preflight", e)
	}
	ea1CompBarrierUnchanged(t, ar, before)
	f.assertReceipt(before)
	expectedClaims := 0
	if native {
		expectedClaims = 1
	}
	if f.store.acquireCalls != expectedClaims || f.store.releaseCalls != expectedClaims {
		t.Fatal("approval/recovery fabricated native acquisition")
	}
}
func ea1ChildApprovalElection(t *testing.T, factory func(*testing.T) (kernel.Store, func() (kernel.Store, kernel.Store)), native, deadline bool) {
	f := ea1NewChildApprovalSettlement(t, factory, native, deadline)
	ea1ChildSettlementElection(t, f.reopen, f.request, f.waiting, f.parent, f.grandparent, f.physical, f.definition, f.runner, f, deadline)
}
