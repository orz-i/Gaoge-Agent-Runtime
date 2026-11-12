package sqlite_test

import (
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// An external Tool/Model callback may outlive the process that held its Grant.
// Reopening the physical database must NOT discard that Grant or allow parent
// terminal CAS to report successful cleanup of an unknown external effect.
func TestIssue107SQLiteGrantSurvivesReopenUntilOriginalSettlement(t *testing.T) {
	path := filepath.Join(privateDir(t), "child-grant.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: first})
	if err != nil {
		t.Fatal(err)
	}
	parentReq := sampleCreate("parent")
	parentReq.Kind = "workflow"
	parent, err := rt.Create(t.Context(), parentReq)
	if err != nil {
		t.Fatal(err)
	}
	childReq := sampleCreate("child")
	childReq.ParentRunID = parent.Run.ID
	child, err := rt.Create(t.Context(), childReq)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	recovered, err := kernel.New(kernel.Dependencies{Store: second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "after restart")
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("reopened Store lost original pending Grant: %v", err)
	}
	if _, err = recovered.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("restarted worker replayed unresolved Tool: %v", err)
	}
	original, lookupErr := recovered.LookupChildExecution(t.Context(), child.Run.ID)
	if lookupErr != nil || original != grant {
		t.Fatalf("original Grant not recoverable: %+v err=%v", original, lookupErr)
	}
	if err = second.FinishChildExecution(t.Context(), original); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("reopened pending Grant was unsafely retired: %v", err)
	}
	if err = second.MarkChildExecutionSettled(t.Context(), original, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("untrusted direct Store Mark authorized cleanup: %v", err)
	}
	if err = second.FinishChildExecution(t.Context(), original); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("unknown physical outcome released without receipt: %v", err)
	}
	if _, err = recovered.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "unknown after restart"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown physical grant lost its cancellation fence: %v", err)
	}
}
