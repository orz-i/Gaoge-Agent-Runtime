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

func TestGuardedCancelRollbackDoesNotRevokeOwner(t *testing.T) {
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
	created, err := runtime.Create(ctx, kernel.CreateRequest{ExecutionRequired: true,
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
	apply := func() (kernel.Snapshot, error) {
		return runtime.Cancel(ctx, waiting.Run.ID, waiting.Run.Revision, "operator requested")
	}
	if _, e := apply(); e == nil {
		t.Fatal("injected projection failure unexpectedly committed cancellation")
	}
	snap, e := runtime.Load(ctx, created.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	if snap.Run.Revision != waiting.Run.Revision || snap.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("failed guarded cancellation leaked state: %+v", snap.Run)
	}
	if _, e = runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: 5 * time.Second}); e != nil {
		t.Fatalf("failed cancellation revoked incumbent lease: %v", e)
	}
	if _, err = raw.Exec("DROP TRIGGER block_approval_ready"); err != nil {
		t.Fatal(err)
	}
	cancelled, e := apply()
	if e != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("guarded cancellation failed after trigger dropped: %+v %v", cancelled.Run, e)
	}
	if _, err = runtime.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: lease, LeaseDuration: time.Second}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("revoked lease renewed after cancelled control: %v", err)
	}
}
