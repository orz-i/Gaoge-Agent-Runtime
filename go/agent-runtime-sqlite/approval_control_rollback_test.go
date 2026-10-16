package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestVerifiedApprovalRollbackRetainsEpochAndWait(t *testing.T) {
	ctx := context.Background()
	base := privateDir(t)
	path := filepath.Join(base, "approval-atomic.sqlite")
	marker := filepath.Join(base, "side-effects.txt")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	testAdapter := &controlTestFencedCreateStore{Store: store, execution: store}
	starting, err := kernel.New(kernel.Dependencies{Store: testAdapter})
	if err != nil {
		t.Fatal(err)
	}
	first, err := controlTestRunner(starting, marker, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := first.StartRun(ctx, agent.StartRequest{
		ID: "atomic-approval", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "approval fault recovery",
		Model: "fixture", ToolKeys: []string{"publish"},
	})
	if err != nil || pending.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("pending %+v %v", pending.Run, err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(`CREATE TRIGGER fail_approved_ready BEFORE INSERT ON runtime_ready_work
  BEGIN SELECT RAISE(ABORT,'fault injected after approval Run CAS'); END`)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	approver, err := controlTestRunner(plain, marker, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = approver.ResolveApproval(ctx, pending.Run.ID, pending.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err == nil {
		t.Fatal("injected fault should abort guarded approval")
	}
	stored, err := plain.Load(ctx, pending.Run.ID)
	if err != nil || stored.Run.Revision != pending.Run.Revision ||
		stored.Run.Status != kernel.RunStatusWaitingInput || stored.Checkpoint.ID != pending.Checkpoint.ID {
		t.Fatalf("failed approval leaked new state: %+v %+v %v", stored.Run, stored.Checkpoint, err)
	}
	if _, err = plain.RenewExecution(ctx, kernel.ExecutionRenewRequest{
		Lease: testAdapter.lease, LeaseDuration: 5 * time.Second,
	}); err != nil {
		t.Fatalf("failed approval revoked original lease: %v", err)
	}
	if _, err = raw.Exec("DROP TRIGGER fail_approved_ready"); err != nil {
		t.Fatal(err)
	}
	resolved, err := approver.ResolveApproval(ctx, pending.Run.ID, pending.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || resolved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("approved retry unsuccessful: %+v %v", resolved.Run, err)
	}
	if _, err = plain.RenewExecution(ctx, kernel.ExecutionRenewRequest{
		Lease: testAdapter.lease, LeaseDuration: time.Second,
	}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("old lease not revoked by control: %v", err)
	}
	marks, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(marks) != "model\n" {
		t.Fatalf("approval unexpectedly dispatched Tool without a new claim: %q", marks)
	}
}
