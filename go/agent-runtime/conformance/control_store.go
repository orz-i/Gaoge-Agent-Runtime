package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunKernelControlSuite exercises the single guarded Feature-owned CAS
// without introducing a separate command queue or progress schema.
func RunKernelControlSuite(t *testing.T, factory ExecutionStoreFactory) {
	t.Helper()
	create := func(t *testing.T, goal string, deadline *time.Time) (*kernel.Runtime, kernel.Snapshot) {
		t.Helper()
		runtime, err := kernel.New(kernel.Dependencies{Store: factory(t)})
		if err != nil {
			t.Fatal(err)
		}
		created, err := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: "gate-run", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: goal, State: json.RawMessage(`{"step":0}`),
			DeadlineAt: deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime, created
	}
	acquire := func(t *testing.T, runtime *kernel.Runtime) (kernel.ExecutionLease, *kernel.Runtime) {
		t.Helper()
		lease, err := runtime.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
			RunID: "gate-run", OwnerID: "executor", LeaseDuration: 3 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		bound, err := runtime.WithExecution(lease)
		if err != nil {
			t.Fatal(err)
		}
		return lease, bound
	}
	t.Run("approval-exact-checkpoint-and-revocation", func(t *testing.T) {
		rt, created := create(t, "approval", nil)
		old, bound := acquire(t, rt)
		now := time.Now().UTC()
		checkpoint := &kernel.Checkpoint{ID: "first-checkpoint", Kind: "tool_approval", Status: kernel.CheckpointPending,
			Payload: json.RawMessage(`{"toolCallID":"first-call","arguments":{}}`), CreatedAt: now}
		waiting, err := bound.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusWaitingInput, State: created.State, Checkpoint: checkpoint,
			Events: []kernel.EventDraft{{Type: "approval.pending", Wakeup: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		resolve := func(checkpointID string) (kernel.Snapshot, error) {
			resolved := *checkpoint
			resolved.Status = kernel.CheckpointResolved
			resolved.ResolvedAt = &now
			resolved.Response = json.RawMessage(`{"decision":"approve"}`)
			snap, revoked, applyErr := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
				kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: checkpointID},
				kernel.Mutation{Status: kernel.RunStatusRunning, State: waiting.State, Checkpoint: &resolved,
					Events: []kernel.EventDraft{{Type: "interaction.resolved", Wakeup: true}}},
			)
			if applyErr == nil && !revoked {
				t.Fatal("existing execution fence was not revoked by approved control")
			}
			return snap, applyErr
		}
		if _, err = rt.Apply(t.Context(), waiting.Run.ID, waiting.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: waiting.State}); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("plain Apply bypassed approval: %v", err)
		}
		if _, err = resolve("wrong-checkpoint"); !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("wrong checkpoint accepted: %v", err)
		}
		current, err := rt.Load(t.Context(), waiting.Run.ID)
		if err != nil || current.Run.Revision != waiting.Run.Revision {
			t.Fatalf("rejected guard changed Run: %+v %v", current.Run, err)
		}
		granted, err := resolve(checkpoint.ID)
		if err != nil || granted.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("approved control failed: %+v %v", granted.Run, err)
		}
		if granted.Run.Revision != waiting.Run.Revision+1 {
			t.Fatalf("control Run revision=%d", granted.Run.Revision)
		}
		if _, err = bound.Apply(t.Context(), granted.Run.ID, granted.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: granted.State,
		}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("old owner committed after gate: %v", err)
		}
		if _, err = resolve(checkpoint.ID); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("stale approval replay: %v", err)
		}
		nextLease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: granted.Run.ID, OwnerID: "next-owner", LeaseDuration: time.Second})
		if err != nil || nextLease.Epoch <= old.Epoch+1 {
			t.Fatalf("gate did not revoke old epoch %+v %v", nextLease, err)
		}
		next, err := rt.WithExecution(nextLease)
		if err != nil {
			t.Fatal(err)
		}
		nextWaiting, err := next.Apply(t.Context(), granted.Run.ID, granted.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusWaitingInput, State: granted.State,
			Checkpoint: &kernel.Checkpoint{ID: "second-checkpoint", Kind: "tool_approval", Status: kernel.CheckpointPending,
				Payload: json.RawMessage(`{"toolCallID":"second-call","arguments":{}}`), CreatedAt: time.Now().UTC()},
		})
		if err != nil {
			t.Fatal(err)
		}
		// An earlier checkpoint/decision must not unlock the next Tool.
		_, _, err = rt.ApplyControl(t.Context(), nextWaiting.Run.ID, nextWaiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: "first-checkpoint"},
			kernel.Mutation{Status: kernel.RunStatusRunning, State: nextWaiting.State})
		if !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("stale checkpoint authorized next Tool: %v", err)
		}
	})
	t.Run("allowance-dimension-and-revocation", func(t *testing.T) {
		rt, created := create(t, "allowance", nil)
		_, bound := acquire(t, rt)
		waiting, err := bound.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusPausedBudget, State: created.State, ErrorCode: "agent.model_allowance_exhausted",
		})
		if err != nil {
			t.Fatal(err)
		}
		grant := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
			Events: []kernel.EventDraft{{Type: "allowance.granted", Wakeup: true}}}
		_, _, err = rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: "agent.tool_allowance_exhausted"}, grant)
		if !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("wrong budget dimension accepted: %v", err)
		}
		granted, revoked, err := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: waiting.Run.ErrorCode}, grant)
		if err == nil && !revoked {
			t.Fatal("existing execution fence was not revoked by budget control")
		}
		if err != nil || granted.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("budget not resumed: %+v %v", granted.Run, err)
		}
		if _, err = bound.Apply(t.Context(), granted.Run.ID, granted.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: granted.State,
		}); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("old owner wrote after budget grant: %v", err)
		}
	})
	t.Run("cancel-revokes-active-owner", func(t *testing.T) {
		rt, created := create(t, "cancel", nil)
		_, bound := acquire(t, rt)
		stopped, err := rt.Cancel(t.Context(), created.Run.ID, created.Run.Revision, "operator stop")
		if err != nil || stopped.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("cancel failed %+v %v", stopped.Run, err)
		}
		if _, err = bound.Apply(t.Context(), stopped.Run.ID, stopped.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: stopped.State,
		}); !errors.Is(err, kernel.ErrTerminal) && !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("cancelled worker wrote: %v", err)
		}
		if _, err = rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{RunID: stopped.Run.ID, OwnerID: "revival", LeaseDuration: time.Second}); err == nil {
			t.Fatal("terminal revival")
		}
	})
	t.Run("deadline-checks-store-time", func(t *testing.T) {
		future := time.Now().UTC().Add(150 * time.Millisecond)
		rt, created := create(t, "deadline", &future)
		_, bound := acquire(t, rt)
		early, expired, err := rt.Expire(t.Context(), created.Run.ID, created.Run.Revision)
		if err != nil || expired || early.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("premature expiration: %+v %v %v", early.Run, expired, err)
		}
		time.Sleep(190 * time.Millisecond)
		failed, expired, err := rt.Expire(t.Context(), created.Run.ID, created.Run.Revision)
		if err != nil || !expired || failed.Run.Status != kernel.RunStatusFailed {
			t.Fatalf("deadline failed: %+v expired=%v err=%v", failed.Run, expired, err)
		}
		if _, err = bound.Apply(t.Context(), failed.Run.ID, failed.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: failed.State,
		}); !errors.Is(err, kernel.ErrTerminal) && !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("expired worker committed: %v", err)
		}
	})
}
