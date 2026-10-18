package sqlite_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestKernelQuarantinePersistsAcrossSQLiteReopen(t *testing.T) {
	path := filepath.Join(privateDir(t), "quarantined.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: first})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(t.Context(), kernel.CreateRequest{
		ExecutionRequired: true, ID: "quarantine-reopen", Kind: "agent",
		Actor:  kernel.ActorRef{TenantID: "profile", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "preserve quarantine", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "first", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	block := kernel.ExecutionQuarantine{RunID: created.Run.ID,
		Revision: created.Run.Revision, Generation: 19, Reason: "provider_unknown"}
	if err = rt.QuarantineExecution(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	second, err := kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	if err = second.RequireExecution(t.Context(), created.Run.ID); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("reopened Store dropped execution guard: %v", err)
	}
	if _, err = second.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "second", LeaseDuration: time.Minute,
	}); !errors.Is(err, kernel.ErrExecutionQuarantined) {
		t.Fatalf("new process stole quarantined work: %v", err)
	}
	if err = second.RepairExecutionQuarantine(t.Context(), block); err != nil {
		t.Fatal(err)
	}
	newLease, err := second.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "repaired", LeaseDuration: time.Minute,
	})
	if err != nil || newLease.Epoch <= lease.Epoch {
		t.Fatalf("reopened repair failed to fence old Owner: %+v err=%v", newLease, err)
	}
}
