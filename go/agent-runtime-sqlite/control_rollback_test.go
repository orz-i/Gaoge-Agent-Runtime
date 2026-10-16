package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestGuardedApprovalRollbackDoesNotRevokeOwner(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "control-rollback.sqlite")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(ctx, kernel.CreateRequest{
		ID: "control-rollback", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "atomic control", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "current", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := runtime.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	checkpoint := &kernel.Checkpoint{ID: "approval-checkpoint", Kind: "tool_approval", Status: kernel.CheckpointPending,
		Payload: json.RawMessage(`{"callID":"one"}`), CreatedAt: now}
	waiting, err := bound.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusWaitingInput, State: created.State, Checkpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(`CREATE TRIGGER block_approval_ready BEFORE INSERT ON runtime_ready_work
   BEGIN SELECT RAISE(ABORT,'reject ready during approval'); END`)
	if err != nil {
		t.Fatal(err)
	}
	resolved := *checkpoint
	resolved.Status = kernel.CheckpointResolved
	resolved.ResolvedAt = &now
	resolved.Response = json.RawMessage(`{"decision":"approve"}`)
	apply := func() (kernel.Snapshot, bool, error) {
		return runtime.ApplyControl(ctx, waiting.Run.ID, waiting.Run.Revision,
			kernel.ControlGuard{Kind: kernel.ControlApproval, CheckpointID: checkpoint.ID},
			kernel.Mutation{Status: kernel.RunStatusRunning, State: waiting.State, Checkpoint: &resolved,
				Events: []kernel.EventDraft{{Type: "interaction.resolved", Wakeup: true}}},
		)
	}
	if _, revoked, e := apply(); e == nil || revoked {
		t.Fatalf("injected projection failure wrongly committed/revoked: revoked=%v err=%v", revoked, e)
	}
	snap, err := runtime.Load(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Run.Revision != waiting.Run.Revision || snap.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("failed guarded control leaked state: %+v", snap.Run)
	}
	if _, err = runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 5 * time.Second}); err != nil {
		t.Fatalf("failed guarded control incorrectly revoked incumbent lease: %v", err)
	}
	if _, err = raw.Exec("DROP TRIGGER block_approval_ready"); err != nil {
		t.Fatal(err)
	}
	granted, revoked, err := apply()
	if err != nil || !revoked || granted.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("guarded control did not recover after rollback: %+v revoked=%v err=%v", granted.Run, revoked, err)
	}
	if _, err = runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: time.Second}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("revoked lease renewed after approved control: %v", err)
	}
}
