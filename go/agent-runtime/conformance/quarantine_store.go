package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunKernelQuarantineSuite exercises an actual Store, not an in-memory
// simulation of a PostgreSQL/SQLite run-level execution block.
func RunKernelQuarantineSuite(t *testing.T, factory ExecutionStoreFactory) {
	t.Helper()
	t.Run("current-work-quarantine-revokes-owner-and-requires-exact-repair", func(t *testing.T) {
		ctx := t.Context()
		store := factory(t)
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		if !rt.SupportsExecutionQuarantine() {
			t.Fatal("Store has no authoritative execution quarantine capability")
		}
		makeRun := func(id string) kernel.Snapshot {
			t.Helper()
			run, createErr := rt.Create(ctx, kernel.CreateRequest{
				ExecutionRequired: true, ID: id, Kind: "agent",
				Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
				Goal:   "current work", State: json.RawMessage(`{}`),
			})
			if createErr != nil {
				t.Fatal(createErr)
			}
			return run
		}
		main, healthy := makeRun("quarantine-main"), makeRun("quarantine-neighbor")
		a, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: main.Run.ID, OwnerID: "executor-A", LeaseDuration: 5 * time.Second,
		})
		if err != nil {
			t.Fatal(err)
		}
		boundA, err := rt.WithExecution(a)
		if err != nil {
			t.Fatal(err)
		}
		current, err := boundA.Apply(ctx, main.Run.ID, main.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		block := kernel.ExecutionQuarantine{
			RunID: current.Run.ID, Revision: current.Run.Revision,
			Generation: 7, Reason: "invalid_current_work",
		}
		stale := block
		stale.Revision--
		if err = rt.QuarantineExecution(ctx, stale); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("stale generation/revision poisoned a newer Run: %v", err)
		}
		if err = rt.QuarantineExecution(ctx, block); err != nil {
			t.Fatalf("current work quarantine not committed: %v", err)
		}
		if err = rt.QuarantineExecution(ctx, block); err != nil {
			t.Fatalf("identical quarantine was not idempotent: %v", err)
		}
		changedGeneration := block
		changedGeneration.Generation++
		if err = rt.QuarantineExecution(ctx, changedGeneration); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("different work generation replaced current diagnosis: %v", err)
		}
		// The same Feature wait might survive an unrelated guarded control
		// revision. Such a CAS must not age the quarantine out merely by
		// incrementing the Run revision: exact repair is required first.
		until := time.Now().UTC().Add(2 * time.Minute)
		guard := kernel.ControlGuard{Kind: kernel.ControlExternalSignal,
			WaitID: "feature-wait-generation-7", WaitDeadlineAt: &until}
		mutation := kernel.Mutation{Status: kernel.RunStatusRunning,
			State:  json.RawMessage(`{"step":1,"ready":true}`),
			Events: []kernel.EventDraft{{Type: "tool.external_result_ready", Wakeup: true, WakeupGeneration: 7}}}
		proof := featurecontrol.Seal(main.Run.ID, current.Run.Revision,
			string(kernel.ControlExternalSignal), guard.WaitID, mutation.State, nil)
		if _, _, err = rt.ApplyControl(ctx, main.Run.ID, current.Run.Revision, guard, mutation, proof); !errors.Is(err, kernel.ErrExecutionQuarantined) {
			t.Fatalf("unrelated control revision bypassed quarantined work: %v", err)
		}
		if err = rt.RequireExecution(ctx, main.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
			t.Fatalf("unbound/direct execution did not honor quarantine: %v", err)
		}
		if err = boundA.RequireExecution(ctx, main.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
			t.Fatalf("active owner pre-dispatch bypassed quarantine: %v", err)
		}
		if _, err = rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: main.Run.ID, OwnerID: "hosted-worker-B", LeaseDuration: 5 * time.Second,
		}); !errors.Is(err, kernel.ErrExecutionQuarantined) {
			t.Fatalf("another worker acquired a blocked Run: %v", err)
		}
		if _, err = boundA.Apply(ctx, main.Run.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"effect":"forbidden"}`),
		}); !errors.Is(err, kernel.ErrExecutionQuarantined) {
			t.Fatalf("incumbent owner committed while quarantined: %v", err)
		}
		unchanged, err := rt.Load(ctx, main.Run.ID)
		if err != nil || unchanged.Run.Revision != current.Run.Revision ||
			string(unchanged.State) != string(current.State) {
			t.Fatalf("quarantine changed Run progress: %+v err=%v", unchanged, err)
		}
		// Invalid repair cannot clear current WorkID/generation/reason.
		for _, invalid := range []kernel.ExecutionQuarantine{
			changedGeneration,
			{RunID: main.Run.ID, Revision: block.Revision, Generation: block.Generation, Reason: "other_reason"},
			stale,
		} {
			if err = rt.RepairExecutionQuarantine(ctx, invalid); !errors.Is(err, kernel.ErrConflict) {
				t.Fatalf("incorrect repair identity bypassed quarantine: %+v err=%v", invalid, err)
			}
		}
		// A different Run is never blocked by this diagnosis.
		neighborLease, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: healthy.Run.ID, OwnerID: "neighbor", LeaseDuration: 5 * time.Second,
		})
		if err != nil {
			t.Fatalf("other Run starved: %v", err)
		}
		neighbor, err := rt.WithExecution(neighborLease)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = neighbor.Apply(ctx, healthy.Run.ID, healthy.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"ok":true}`),
		}); err != nil {
			t.Fatalf("healthy Run was poisoned: %v", err)
		}
		if err = neighbor.QuarantineExecution(ctx, block); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("a bound different-Run executor poisoned the owner: %v", err)
		}
		if err = neighbor.RepairExecutionQuarantine(ctx, block); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("a bound different-Run executor repaired the owner: %v", err)
		}

		if err = rt.RepairExecutionQuarantine(ctx, block); err != nil {
			t.Fatalf("trusted exact repair failed: %v", err)
		}
		if err = rt.RepairExecutionQuarantine(ctx, block); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("repeated repair acted as an authorization grant: %v", err)
		}
		// The old pre-quarantine lease must stay fenced after repair.
		if err = boundA.RequireExecution(ctx, main.Run.ID); !errors.Is(err, kernel.ErrExecutionConflict) {
			t.Fatalf("old owner revived after repair: %v", err)
		}
		newLease, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
			RunID: main.Run.ID, OwnerID: "trusted-recovery", LeaseDuration: 5 * time.Second,
		})
		if err != nil || newLease.Epoch <= a.Epoch {
			t.Fatalf("repair did not permit a fresh, fenced claim: %+v err=%v", newLease, err)
		}
		authorized, err := rt.WithExecution(newLease)
		if err != nil {
			t.Fatal(err)
		}
		advanced, err := authorized.Apply(ctx, main.Run.ID, current.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"nextGeneration":true}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = rt.QuarantineExecution(ctx, block); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("old WorkID quarantined next Run revision: %v", err)
		}
		if err = authorized.RequireExecution(ctx, main.Run.ID); err != nil ||
			advanced.Run.Revision != block.Revision+1 {
			t.Fatalf("old block still prevented newer work: %+v err=%v", advanced.Run, err)
		}
	})
}
