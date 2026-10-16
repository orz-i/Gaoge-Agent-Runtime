package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestCancelledCreateDoesNotPublishAndCanRetry(t *testing.T) {
	path := filepath.Join(privateDir(t), "fresh.sqlite")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sqlite.CreateNew(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled initialization = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled initial setup published a database: %v", err)
	}
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatalf("retry did not publish a complete v1 schema: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("development schema should remain v1: version=%d err=%v", version, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+filepath.Base(path)+".init-") {
			t.Fatalf("unpublished temporary database file leaked: %s", entry.Name())
		}
	}
}

func TestConcurrentFirstCreateNeverOverwrites(t *testing.T) {
	path := filepath.Join(privateDir(t), "winner.sqlite")
	const racers = 2
	start := make(chan struct{})
	outcomes := make(chan error, racers)
	var group sync.WaitGroup
	for range racers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			store, err := sqlite.CreateNew(context.Background(), path)
			if err == nil {
				err = store.Close()
			}
			outcomes <- err
		}()
	}
	close(start)
	group.Wait()
	close(outcomes)
	successes, alreadyExists := 0, 0
	for err := range outcomes {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, os.ErrExist):
			alreadyExists++
		default:
			t.Fatalf("unexpected concurrent create error: %v", err)
		}
	}
	if successes != 1 || alreadyExists != racers-1 {
		t.Fatalf("concurrent create outcomes successes=%d exists=%d", successes, alreadyExists)
	}
	db, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatalf("published database not reopenable: %v", err)
	}
	defer func() { _ = db.Close() }()
}

func TestSameDevelopmentV1WithObsoleteLayoutIsRejected(t *testing.T) {
	path := filepath.Join(privateDir(t), "obsolete-v1.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("ALTER TABLE runtime_ready_work DROP COLUMN payload_hash"); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("test expects unchanged schema v1: %d %v", version, err)
	}
	if _, err = sqlite.OpenExisting(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("old dev layout accepted despite missing integrity field: %v", err)
	}
}

func TestTerminalRunPreservesQuarantineAndAllUnprojectedFacts(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "quarantined.sqlite")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("quarantine-term")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
	created, err := runtime.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.Exec("UPDATE runtime_ready_work SET events_json=x'7b' WHERE run_id=?", req.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: "projector", Now: time.Now().Add(time.Minute),
		LeaseDuration: time.Minute, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err := store.ListQuarantinedTransitions(ctx, 10)
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("missing quarantine diagnosis %#v %v", quarantine, err)
	}
	completed, err := runtime.Apply(ctx, req.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"phase":"done"}`),
		Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"done"`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	quarantine, err = store.ListQuarantinedTransitions(ctx, 10)
	if err != nil || len(quarantine) != 1 || quarantine[0].FailureCode != "invalid_events" {
		t.Fatalf("terminal cleanup lost quarantine diagnosis: %#v %v", quarantine, err)
	}
	claims, err := store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: "terminal-projector", Now: time.Now().Add(2 * time.Minute),
		LeaseDuration: time.Minute, Limit: 10,
	})
	if err != nil || len(claims) != 1 || claims[0].Transition.Revision != completed.Run.Revision {
		t.Fatalf("terminal relation handoff lost: %#v %v", claims, err)
	}
	var count int
	if err = raw.QueryRow("SELECT count(*) FROM runtime_ready_work WHERE run_id=?", req.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("outbox must preserve quarantined+terminal records=%d %v", count, err)
	}
}

func TestWellFormedButModifiedReadyEventsAreQuarantined(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "changed-events.sqlite")
	store, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("changed")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
	if _, err = runtime.Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	// Syntactically valid JSON that removes the sole wakeup.
	if _, err = db.Exec("UPDATE runtime_ready_work SET events_json='[]' WHERE run_id=?", req.ID); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: "pump", Now: time.Now().Add(time.Minute), LeaseDuration: time.Minute, Limit: 5,
	})
	if err != nil || len(claims) != 0 {
		t.Fatalf("mutated projection delivered: %#v %v", claims, err)
	}
	quarantine, err := store.ListQuarantinedTransitions(ctx, 10)
	if err != nil || len(quarantine) != 1 || quarantine[0].FailureCode != "invalid_payload_integrity" {
		t.Fatalf("well formed mutation was not quarantined: %#v %v", quarantine, err)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM runtime_ready_work WHERE run_id=?", req.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid work was silently deleted: %d %v", count, err)
	}
}
