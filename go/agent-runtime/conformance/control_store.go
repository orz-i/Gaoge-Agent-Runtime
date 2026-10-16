package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type controlFastClock struct{ at time.Time }

func (c controlFastClock) Now() time.Time { return c.at }

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
		created, err := runtime.Create(t.Context(), kernel.CreateRequest{ExecutionRequired: true,
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
		// No lower-layer caller without a trusted, mutation-bound Feature
		// proof may transform a pending Tool approval into a running Run.
		forged := *checkpoint
		forged.Status = kernel.CheckpointResolved
		forged.Response = json.RawMessage(`{"decision":"approve"}`)
		forgedAt := time.Now().UTC()
		forged.ResolvedAt = &forgedAt
		forgedMutation := kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"unvalidated":true}`),
			Checkpoint: &forged,
		}
		if _, _, e := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: checkpoint.ID},
			forgedMutation, nil); !errors.Is(e, kernel.ErrInvalidInput) {
			t.Fatalf("unproven control bypassed Feature approval: %v", e)
		}
		resolve := func(checkpointID string) (kernel.Snapshot, error) {
			resolved := *checkpoint
			resolved.Status = kernel.CheckpointResolved
			resolved.ResolvedAt = &now
			resolved.Response = json.RawMessage(`{"decision":"approve"}`)
			proof := featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision,
				string(kernel.ControlApproval), checkpointID, waiting.State, &resolved)
			snap, revoked, applyErr := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
				kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: checkpointID},
				kernel.Mutation{Status: kernel.RunStatusRunning, State: waiting.State, Checkpoint: &resolved,
					Events: []kernel.EventDraft{{Type: "interaction.resolved", Wakeup: true}}},
				proof)
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
			kernel.Mutation{Status: kernel.RunStatusRunning, State: nextWaiting.State}, nil)
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
		if _, _, e := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: waiting.Run.ErrorCode},
			grant, nil); !errors.Is(e, kernel.ErrInvalidInput) {
			t.Fatalf("unvalidated allowance bypassed first-party limit checker: %v", e)
		}
		badProof := featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision,
			string(kernel.ControlAllowance), "agent.tool_allowance_exhausted", grant.State, grant.Checkpoint)
		_, _, err = rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: "agent.tool_allowance_exhausted"}, grant, badProof)
		if !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("wrong budget dimension accepted: %v", err)
		}
		grantProof := featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision,
			string(kernel.ControlAllowance), waiting.Run.ErrorCode, grant.State, grant.Checkpoint)
		granted, revoked, err := rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlAllowance, BlockedCode: waiting.Run.ErrorCode}, grant, grantProof)
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
		// Real PostgreSQL migration and row-lock setup can consume more than
		// a 150ms deadline under -race. Keep ample scheduling margin.
		future := time.Now().UTC().Add(750 * time.Millisecond)
		rt, created := create(t, "deadline", &future)
		_, bound := acquire(t, rt)
		early, expired, err := rt.Expire(t.Context(), created.Run.ID, created.Run.Revision)
		if err != nil || expired || early.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("premature expiration: %+v %v %v", early.Run, expired, err)
		}
		time.Sleep(850 * time.Millisecond)
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
	t.Run("bound-owner-cancel", func(t *testing.T) {
		rt, created := create(t, "owned cancellation", nil)
		_, bound := acquire(t, rt)
		stopped, err := bound.Cancel(t.Context(), created.Run.ID, created.Run.Revision, "terminate")
		if err != nil || stopped.Run.Status != kernel.RunStatusCancelled {
			t.Fatalf("bound owner cannot cancel: %+v %v", stopped.Run, err)
		}
		if err = bound.RequireExecution(t.Context(), created.Run.ID); !errors.Is(err, kernel.ErrTerminal) {
			t.Fatalf("terminal execution permitted: %v", err)
		}
	})
	t.Run("Store-deadline-gates-approval-and-allowance", func(t *testing.T) {
		for _, gate := range []kernel.ControlKind{kernel.ControlApproval, kernel.ControlAllowance} {
			t.Run(string(gate), func(t *testing.T) {
				deadline := time.Now().UTC().Add(750 * time.Millisecond)
				rt, created := create(t, "deadline before control", &deadline)
				_, bound := acquire(t, rt)
				pending := kernel.Mutation{Status: kernel.RunStatusWaitingInput, State: created.State}
				var cp *kernel.Checkpoint
				if gate == kernel.ControlApproval {
					cp = &kernel.Checkpoint{ID: "due-checkpoint", Kind: "tool_approval", Status: kernel.CheckpointPending,
						Payload: json.RawMessage(`{"toolCallID":"c"}`), CreatedAt: time.Now().UTC()}
					pending.Checkpoint = cp
				} else {
					pending.Status = kernel.RunStatusPausedBudget
					pending.ErrorCode = "agent.model_allowance_exhausted"
				}
				waiting, err := bound.Apply(t.Context(), created.Run.ID, created.Run.Revision, pending)
				if err != nil {
					t.Fatal(err)
				}
				time.Sleep(850 * time.Millisecond)
				guard := kernel.ControlGuard{Kind: gate}
				next := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"grant":1}`)}
				source := ""
				if gate == kernel.ControlApproval {
					guard.CheckpointID = cp.ID
					source = cp.ID
					resolved := *cp
					resolved.Status = kernel.CheckpointResolved
					now := time.Now().UTC()
					resolved.ResolvedAt = &now
					resolved.Response = json.RawMessage(`{"decision":"approve"}`)
					next.Checkpoint = &resolved
				} else {
					guard.BlockedCode = waiting.Run.ErrorCode
					source = waiting.Run.ErrorCode
				}
				proof := featurecontrol.Seal(waiting.Run.ID, waiting.Run.Revision, string(gate),
					source, next.State, next.Checkpoint)
				_, _, err = rt.ApplyControl(t.Context(), waiting.Run.ID, waiting.Run.Revision, guard, next, proof)
				if !errors.Is(err, kernel.ErrDeadline) {
					t.Fatalf("post-deadline %s control accepted: %v", gate, err)
				}
				stored, err := rt.Load(t.Context(), waiting.Run.ID)
				if err != nil || stored.Run.Revision != waiting.Run.Revision || stored.Run.Status != waiting.Run.Status {
					t.Fatalf("rejected control changed Run: %+v %v", stored.Run, err)
				}
			})
		}
	})
	t.Run("fast-caller-clock-does-not-report-false-expiry", func(t *testing.T) {
		store := factory(t)
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().UTC().Add(3 * time.Hour)
		created, err := rt.Create(t.Context(), kernel.CreateRequest{ExecutionRequired: true,
			ID: "future-deadline", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "clock skew", State: json.RawMessage(`{}`),
			DeadlineAt: &deadline,
		})
		if err != nil {
			t.Fatal(err)
		}
		skewed, err := kernel.New(kernel.Dependencies{Store: store, Clock: controlFastClock{at: deadline.Add(time.Hour)}})
		if err != nil {
			t.Fatal(err)
		}
		_, expired, e := skewed.Expire(t.Context(), created.Run.ID, created.Run.Revision)
		if !errors.Is(e, kernel.ErrDeadline) || expired {
			t.Fatalf("host fast clock lied about persisted expiration expired=%v err=%v", expired, e)
		}
		state, e := rt.Load(t.Context(), created.Run.ID)
		if e != nil || state.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("clock-skew control mutated Run: %+v %v", state.Run, e)
		}
	})

	t.Run("direct-Store-call-cannot-forge-a-Feature-proof", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		created, err := runtime.Create(ctx, kernel.CreateRequest{ExecutionRequired: true,
			ID: "direct-proof", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "guard", State: json.RawMessage(`{"approved":false}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		lease, err := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: created.Run.ID, OwnerID: "owner", LeaseDuration: time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		bound, err := runtime.WithExecution(lease)
		if err != nil {
			t.Fatal(err)
		}
		cp := &kernel.Checkpoint{ID: "tool-a", Kind: "tool_approval", Status: kernel.CheckpointPending,
			Payload: json.RawMessage(`{"toolCallID":"a"}`), CreatedAt: time.Now().UTC()}
		pending, err := bound.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusWaitingInput, State: created.State, Checkpoint: cp,
		})
		if err != nil {
			t.Fatal(err)
		}
		resolved := *cp
		resolved.Status = kernel.CheckpointResolved
		now := time.Now().UTC()
		resolved.ResolvedAt = &now
		resolved.Response = json.RawMessage(`{"decision":"approve"}`)
		record := kernel.Record{Run: pending.Run, State: json.RawMessage(`{"approved":true}`), Checkpoint: &resolved}
		record.Run.Revision++
		record.Run.Status = kernel.RunStatusRunning
		record.Run.UpdatedAt = now
		direct, ok := store.(kernel.ControlStore)
		if !ok {
			t.Fatal("Store is not ControlStore")
		}
		_, revoked, err := direct.ApplyControl(ctx, pending.Run.ID, pending.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: cp.ID},
			kernel.StoreMutation{Record: record})
		if !errors.Is(err, kernel.ErrInvalidInput) || revoked {
			t.Fatalf("unsealed direct Store call bypassed approval: revoked=%v err=%v", revoked, err)
		}
		if err = bound.RequireExecution(ctx, pending.Run.ID); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("unsealed call changed waiting gate: %v", err)
		}
	})

}
