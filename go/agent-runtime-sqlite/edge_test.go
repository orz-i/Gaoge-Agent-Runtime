package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestRejectNewerSchemaAndCorruptedState(t *testing.T) {
	path := filepath.Join(privateDir(t), "version.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Create(t.Context(), sampleCreate("must-survive")); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal(err)
	}
	_, err = sqlite.Open(t.Context(), path)
	if !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("future schema accepted: %v", err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM runtime_runs WHERE run_id='must-survive'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("unsupported schema was modified count=%d err=%v", count, err)
	}
	if _, err = db.Exec("PRAGMA user_version=1"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE runtime_runs SET state_json=x'7b' WHERE run_id='must-survive'"); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := reopened.Load(t.Context(), "must-survive"); !errors.Is(err, sqlite.ErrInvalidRecord) {
		t.Fatalf("corrupted state was accepted: %v", err)
	}
}

func TestCreateRollbackWhenReadyInsertFails(t *testing.T) {
	path := filepath.Join(privateDir(t), "create-rollback.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TRIGGER fail_ready BEFORE INSERT ON runtime_ready_work
		BEGIN SELECT RAISE(ABORT, 'ready insert failure'); END`); err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("rollback-create")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
	if _, err := runtime.Create(t.Context(), req); err == nil {
		t.Fatal("created Run without mandatory ready work")
	}
	if _, err := runtime.Load(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("failed create leaked Run: %v", err)
	}
	for _, table := range []string{"runtime_runs", "runtime_events", "runtime_ready_work", "runtime_commit_proofs"} {
		var count int
		if err := raw.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s partially committed: count=%d err=%v", table, count, err)
		}
	}
}

func TestReopenReadyClaimFencingAcrossConnections(t *testing.T) {
	path := filepath.Join(privateDir(t), "claims.sqlite")
	first, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: first})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("recover-claim")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
	if _, err = runtime.Create(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	request := kernel.TransitionClaimRequest{WorkerID: "first", Limit: 1, LeaseDuration: time.Minute, Now: now}
	claims, err := first.ClaimTransitions(t.Context(), request)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claim=%v err=%v", claims, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	request.WorkerID = "second"
	request.Now = now.Add(30 * time.Second)
	if pending, err := next.ClaimTransitions(t.Context(), request); err != nil || len(pending) != 0 {
		t.Fatalf("leased work claimed too early=%v err=%v", pending, err)
	}
	request.Now = now.Add(time.Minute)
	reclaimed, err := next.ClaimTransitions(t.Context(), request)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].Transition.Attempts != 2 {
		t.Fatalf("lost lease not reclaimed=%v err=%v", reclaimed, err)
	}
	old := kernel.TransitionLeaseRequest{
		TransitionID: claims[0].Transition.ID, LeaseID: claims[0].LeaseID, WorkerID: claims[0].WorkerID,
	}
	if err := next.AckTransition(t.Context(), old); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale lease ack succeeded: %v", err)
	}
	current := kernel.TransitionLeaseRequest{
		TransitionID: reclaimed[0].Transition.ID,
		LeaseID:      reclaimed[0].LeaseID, WorkerID: reclaimed[0].WorkerID,
	}
	if err := next.AckTransition(t.Context(), current); err != nil {
		t.Fatal(err)
	}
}

func TestExternalStaleCASAcrossTwoSQLiteStores(t *testing.T) {
	path := filepath.Join(privateDir(t), "stale.sqlite")
	one, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = one.Close() }()
	two, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = two.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: one})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(t.Context(), sampleCreate("stale"))
	if err != nil {
		t.Fatal(err)
	}
	// Two owners may observe the same revision, but only one CAS can land.
	next := kernel.StoreMutation{Record: kernel.Record{
		Run: created.Run, State: json.RawMessage(`{"step":1}`),
	}}
	next.Record.Run.Revision++
	next.Record.Run.UpdatedAt = next.Record.Run.UpdatedAt.Add(time.Second)
	if _, err := one.Apply(context.Background(), "stale", created.Run.Revision, next); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Apply(context.Background(), "stale", created.Run.Revision, next); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("stale CAS accepted by independent SQLite opener: %v", err)
	}
}

// Busy is a retryable storage condition, never proof that a Run revision
// changed. Kernel.ErrConflict may be ACKed as stale by continuation callers.
func TestBusyWriterDoesNotBecomeRunConflict(t *testing.T) {
	path := filepath.Join(privateDir(t), "busy.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	created, err := runtime.Create(t.Context(), sampleCreate("busy"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	conn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = runtime.Apply(ctx, created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`),
	})
	if err == nil {
		t.Fatal("write unexpectedly succeeded while another writer holds lock")
	}
	if errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("storage contention was mistaken for consumed/stale Run revision: %v", err)
	}
}
