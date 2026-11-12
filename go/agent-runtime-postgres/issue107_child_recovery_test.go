package postgres

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIssue107PostgresChildGrantSurvivesNewStoreInstance(t *testing.T) {
	db := openKernelStoreTestDB(t)
	one := NewKernelStore(db)
	rt, err := kernel.New(kernel.Dependencies{Store: one})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "p", Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	child, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "c", Kind: "agent", ParentRunID: parent.Run.ID,
		Actor: actor, Thread: thread, Goal: "child", State: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "concurrent owner after restart")
	if !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("new Store instance lost durable Grant: %v", err)
	}
	original, lookupErr := other.LookupChildExecution(t.Context(), child.Run.ID)
	if lookupErr != nil || original != grant {
		t.Fatalf("original Grant not recoverable: %+v err=%v", original, lookupErr)
	}
	if err = NewKernelStore(db).MarkChildExecutionSettled(t.Context(), original, kernel.ChildExecutionSettlement{}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("untrusted direct Store Mark authorized cleanup: %v", err)
	}
	if err = NewKernelStore(db).FinishChildExecution(t.Context(), original); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("reopened pending grant was incorrectly released: %v", err)
	}
	if _, err = other.Cancel(t.Context(), parent.Run.ID, parent.Run.Revision, "still unknown"); !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown Grant did not fence cancellation after restart: %v", err)
	}
}
