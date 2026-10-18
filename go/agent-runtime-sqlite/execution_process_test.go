package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Helper acquires the same Run from a fresh OS process, carrying only SQLite
// state across process death. Its epoch change must NOT change Run revision.
func TestExecutionClaimChild(t *testing.T) {
	if os.Getenv("GAOGE_STAGE2_CLAIM_CHILD") != "1" {
		return
	}
	path := os.Getenv("GAOGE_STAGE2_CLAIM_DB")
	resultFile := os.Getenv("GAOGE_STAGE2_CLAIM_RESULT")
	store, err := sqlite.OpenExisting(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	acquired, err := store.AcquireExecution(context.Background(), kernel.ExecutionAcquireRequest{
		RunID: "execution-restart", OwnerID: "new-process", LeaseDuration: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(acquired)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(resultFile, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionEpochTakeoverAcrossOSProcesses(t *testing.T) {
	ctx := t.Context()
	base := privateDir(t)
	path := filepath.Join(base, "execution.sqlite")
	output := filepath.Join(base, "child-lease.json")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := rt.Create(ctx, kernel.CreateRequest{ExecutionRequired: true,
		ID: "execution-restart", Kind: "agent", Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "same-revision takeover", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	old, err := rt.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "old-process", LeaseDuration: 70 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(130 * time.Millisecond)
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecutionClaimChild$")
	cmd.Env = append(os.Environ(), "GAOGE_STAGE2_CLAIM_CHILD=1",
		"GAOGE_STAGE2_CLAIM_DB="+path, "GAOGE_STAGE2_CLAIM_RESULT="+output)
	result, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("separate process takeover: %s %v", result, err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var currentLease kernel.ExecutionLease
	if err = json.Unmarshal(data, &currentLease); err != nil {
		t.Fatal(err)
	}
	if currentLease.Epoch != old.Epoch+1 {
		t.Fatalf("no epoch bump on cross-process takeover: old=%+v new=%+v", old, currentLease)
	}
	reopened, err := sqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	currentRuntime, err := kernel.New(kernel.Dependencies{Store: reopened})
	if err != nil {
		t.Fatal(err)
	}
	state, err := currentRuntime.Load(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Run.Revision != created.Run.Revision {
		t.Fatalf("takeover changed revision %d != %d", state.Run.Revision, created.Run.Revision)
	}
	oldRuntime, err := currentRuntime.WithExecution(old)
	if err != nil {
		t.Fatal(err)
	}
	candidate := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"result":"old"}`)}
	if _, err = oldRuntime.Apply(ctx, created.Run.ID, created.Run.Revision, candidate); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("expired old owner committed after new process takeover: %v", err)
	}
	newRuntime, err := currentRuntime.WithExecution(currentLease)
	if err != nil {
		t.Fatal(err)
	}
	candidate.State = json.RawMessage(`{"result":"new"}`)
	applied, err := newRuntime.Apply(ctx, created.Run.ID, created.Run.Revision, candidate)
	if err != nil || applied.Run.Revision != created.Run.Revision+1 {
		t.Fatalf("valid new-process owner could not commit: %+v %v", applied.Run, err)
	}
}

// A failed ReadyWork projection must roll back the fenced Run revision,
// journal and commit proof together. The active execution lease survives.
func TestFencedApplyAtomicRollbackOnReadyWorkFailure(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "fenced-rollback.sqlite")
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
		ID: "fenced-rollback", Kind: "agent", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "atomic fence", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := runtime.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: created.Run.ID, OwnerID: "writer", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := runtime.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	_, err = raw.Exec(`CREATE TRIGGER block_ready BEFORE INSERT ON runtime_ready_work
        BEGIN SELECT RAISE(ABORT,'injected ready projection failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	mutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
		Events: []kernel.EventDraft{{Type: "next", Wakeup: true}}}
	if _, err = bound.Apply(ctx, created.Run.ID, created.Run.Revision, mutation); err == nil {
		t.Fatal("fenced commit unexpectedly succeeded without ReadyWork")
	}
	actual, err := runtime.Load(ctx, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Run.Revision != created.Run.Revision || actual.EventHead != created.EventHead {
		t.Fatalf("failed fenced apply leaked revision/events: %+v", actual)
	}
	var ready, proofs int
	if err = raw.QueryRow("SELECT count(*) FROM runtime_ready_work").Scan(&ready); err != nil {
		t.Fatal(err)
	}
	if err = raw.QueryRow("SELECT count(*) FROM runtime_commit_proofs").Scan(&proofs); err != nil {
		t.Fatal(err)
	}
	if ready != 0 || proofs != 1 {
		t.Fatalf("partial fenced transaction ready=%d proofs=%d", ready, proofs)
	}
	if _, err = raw.Exec("DROP TRIGGER block_ready"); err != nil {
		t.Fatal(err)
	}
	persisted, err := bound.Apply(ctx, created.Run.ID, created.Run.Revision, mutation)
	if err != nil || persisted.Run.Revision != created.Run.Revision+1 {
		t.Fatalf("corrected fenced CAS did not survive injected rollback: %+v %v", persisted.Run, err)
	}
}
