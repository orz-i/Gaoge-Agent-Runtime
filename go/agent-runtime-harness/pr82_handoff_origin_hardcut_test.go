package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// A delegated child is owned by the Handoff route, not by a generic Agent
// capability. The latter must never acquire an execution grant on Retry.
type pr82UnexpectedNativeAgent struct {
	runtime *kernel.Runtime
	starts  int
	resumes int
}

func (starter *pr82UnexpectedNativeAgent) StartRun(ctx context.Context, request agent.StartRequest) (kernel.Snapshot, error) {
	starter.starts++
	return starter.runtime.Create(ctx, kernel.CreateRequest{
		ID: request.ID, Kind: agent.RunKind, Actor: request.Actor, Thread: request.Thread,
		RequestID: request.RequestID, Goal: request.Goal, State: []byte("{}"),
	})
}

func (*pr82UnexpectedNativeAgent) ResolveApproval(context.Context, string, uint64, plugin.ApprovalResponse) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, ErrInvalidRequest
}

func (starter *pr82UnexpectedNativeAgent) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	starter.resumes++
	return starter.runtime.Load(ctx, runID)
}

func TestPR82HandoffRetryCannotRotateOrStartNativeAgent(t *testing.T) {
	for _, mode := range []string{"member-only", "role"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			delegation := f.delegation()
			if mode == "member-only" {
				delegation.RoleID = ""
			}
			if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			child, err := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			child.Status = InvocationFailed
			child, err = f.store.UpdateInvocation(t.Context(), child, child.Revision)
			if err != nil {
				t.Fatal(err)
			}
			local := &pr82UnexpectedNativeAgent{runtime: f.runtime}
			f.runner.agent = local
			_, err = f.runner.RetryInvocation(t.Context(), child.ID)
			if !errors.Is(err, ErrConflict) {
				t.Errorf("Handoff retry must fail closed before changing its Attempt: %v", err)
			}
			after, loadErr := f.store.GetInvocation(t.Context(), child.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if after.Attempt != child.Attempt || after.ExecutionRefID != child.ExecutionRefID || after.Revision != child.Revision {
				t.Errorf("Handoff retry rotated frozen child identity: before=%+v after=%+v", child, after)
			}
			if local.starts != 0 || f.handoff.calls != 0 {
				t.Errorf("Handoff retry started a new physical execution: native=%d, handoff=%d", local.starts, f.handoff.calls)
			}
			items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if listErr != nil || PendingExecutionDispatch(items, "") {
				t.Errorf("rejected retry left a dispatch grant: listErr=%v pending=%t", listErr, PendingExecutionDispatch(items, ""))
			}
		})
	}
}

func TestPR82HandoffResumeNeverCreatesNativeDispatchGrant(t *testing.T) {
	for _, mode := range []string{"member-only", "role"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			delegation := f.delegation()
			if mode == "member-only" {
				delegation.RoleID = ""
			}
			if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			child, err := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.runtime.Create(t.Context(), kernel.CreateRequest{
				ID: child.ExecutionRefID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor,
				Thread: f.parentRun.Run.Thread, RequestID: "external-specialist", Goal: delegation.Goal,
				State: []byte("{}"),
			}); err != nil {
				t.Fatal(err)
			}
			_, err = f.runner.ResumeInvocation(t.Context(), child.ID)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Errorf("public ResumeInvocation must reject routed Handoff child: %v", err)
			}
			items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if listErr != nil || PendingExecutionDispatch(items, "") {
				t.Errorf("unsupported routed Handoff Resume opened a dispatch grant: err=%v pending=%t", listErr, PendingExecutionDispatch(items, ""))
			}
		})
	}
}

func TestPR82HandoffInternalContinuationNeverResumesNativeAgent(t *testing.T) {
	for _, mode := range []string{"member-only", "role"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			delegation := f.delegation()
			if mode == "member-only" {
				delegation.RoleID = ""
			}
			if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
				t.Fatal(err)
			}
			child, err := f.store.GetInvocationByExecutionRefID(t.Context(), delegation.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := f.runtime.Create(t.Context(), kernel.CreateRequest{
				ID: child.ExecutionRefID, Kind: agent.RunKind, Actor: f.parentRun.Run.Actor,
				Thread: f.parentRun.Run.Thread, RequestID: "external-specialist", Goal: delegation.Goal,
				State: []byte("{}"),
			})
			if err != nil {
				t.Fatal(err)
			}
			local := &pr82UnexpectedNativeAgent{runtime: f.runtime}
			f.runner.agent = local
			_, err = f.runner.resumeInvocationExecution(t.Context(), child, remote.Run.Revision)
			if !errors.Is(err, ErrInvalidRequest) || local.resumes != 0 {
				t.Errorf("routed Handoff child invoked native Agent continuation: err=%v resumes=%d", err, local.resumes)
			}
			items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if listErr != nil || PendingExecutionDispatch(items, "") {
				t.Errorf("routed Handoff continuation created grant: err=%v pending=%t", listErr, PendingExecutionDispatch(items, ""))
			}
		})
	}
}

func TestPR82HandoffResumedMemberAndRoleExcludeSupervisorContext(t *testing.T) {
	for _, mode := range []string{"member-only", "role"} {
		t.Run(mode, func(t *testing.T) {
			store, turn, _, root := contextRunMiddlewareFixture(t)
			delegation := handoff.Delegation{
				ID: "specialist-delegation", MemberID: "specialist", ChildRunID: "specialist-child",
				Goal: "independent specialist task", Status: handoff.StatusQueued,
			}
			if mode == "role" {
				delegation.RoleID = "specialist"
			}
			input, inputHash, err := marshalInvocationValue(delegation)
			if err != nil {
				t.Fatal(err)
			}
			input, inputHash, err = sealChildRootInput(input, inputHash, root)
			if err != nil {
				t.Fatal(err)
			}
			childID, err := InvocationID(turn.ID, "parent-delegation", CapabilityAgent, delegation.ID)
			if err != nil {
				t.Fatal(err)
			}
			child := root
			child.ID, child.ParentItemID, child.ExecutionRefID = childID, "parent-delegation", delegation.ChildRunID
			child.Input, child.InputHash = input, inputHash
			if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
				t.Fatal(err)
			}
			middleware, err := NewContextRunMiddleware(store, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = middleware.Run(t.Context(), plugin.RunInvocation{
				Operation: plugin.RunResume, Kind: agent.RunKind, RunID: child.ExecutionRefID,
			}, func(ctx context.Context) (kernel.Snapshot, error) {
				if _, hasCheckpoint := CurrentContextCheckpoint(ctx); hasCheckpoint {
					t.Error("supervisor checkpoint leaked into delegated child")
				}
				if _, hasBinding := CurrentContextWindowBinding(ctx); hasBinding {
					t.Error("supervisor Context Window binding leaked into delegated child")
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPR82HandoffReturnMustMatchFrozenMemberAndRoleIdentity(t *testing.T) {
	cases := []struct {
		name   string
		member bool
		alter  func(*handoff.Delegation)
	}{
		{name: "member-only-other-member", member: true, alter: func(d *handoff.Delegation) { d.MemberID = "other-member" }},
		{name: "member-only-other-member-revision", member: true, alter: func(d *handoff.Delegation) { d.MemberRevision = "other-revision" }},
		{name: "role-other-member", alter: func(d *handoff.Delegation) { d.MemberID = "other-member" }},
		{name: "role-other-member-revision", alter: func(d *handoff.Delegation) { d.MemberRevision = "other-revision" }},
		{name: "role-other-role-revision", alter: func(d *handoff.Delegation) { d.RoleRevision++ }},
		{name: "role-other-goal", alter: func(d *handoff.Delegation) { d.Goal = "unrelated goal" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			delegation := f.delegation()
			delegation.MemberRevision = "expected-member-revision"
			if tc.member {
				delegation.RoleID = ""
			} else {
				delegation.RoleRevision = 5
			}
			f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
				d.Status = handoff.StatusRunning
				tc.alter(&d)
				return d, handoff.ErrChildPending
			}
			dispatch := func() error {
				_, err := f.runner.executeDelegation(t.Context(), f.turn, f.parent, f.parentRun, delegation, f.startedItemID)
				return err
			}
			if err := dispatch(); !errors.Is(err, ErrExecutionOutcomeUnknown) {
				t.Errorf("mismatched Handoff identity must remain outcome-unknown: %v", err)
			}
			items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
			if err != nil || !PendingExecutionDispatch(items, "") {
				t.Errorf("wrong Handoff identity settled grant: %v", err)
			}
			if err := dispatch(); !errors.Is(err, ErrConflict) {
				t.Errorf("unknown Handoff outcome allowed replay: %v", err)
			}
			if f.handoff.calls != 1 {
				t.Errorf("Handoff dispatched %d times; expected exactly once", f.handoff.calls)
			}
		})
	}
}

func TestPR82HandoffMatchingMemberIdentityCanSettleGrant(t *testing.T) {
	f := newPR82HandoffFixture(t)
	delegation := f.delegation()
	delegation.RoleID = ""
	delegation.MemberRevision = "member-revision"
	f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
		d.Status = handoff.StatusRunning
		return d, handoff.ErrChildPending
	}
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, delegation, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	result, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, delegation, f.startedItemID)
	if !errors.Is(err, handoff.ErrChildPending) || result.MemberID != delegation.MemberID || result.MemberRevision != delegation.MemberRevision {
		t.Fatalf("matching Handoff receipt was rejected: result=%+v err=%v", result, err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil || PendingExecutionDispatch(items, "") {
		t.Fatalf("verified member receipt left pending grant: %v", err)
	}
}
