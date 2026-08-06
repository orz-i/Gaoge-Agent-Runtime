// Package conformance exposes reusable adapter contract suites.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const conformanceThreadID = "thread"

// KernelStoreFactory creates one empty isolated Store for each test.
type KernelStoreFactory func(testing.TB) kernel.Store

// RunKernelStoreSuite validates the minimal atomic Kernel persistence contract.
func RunKernelStoreSuite(t *testing.T, factory KernelStoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("kernel store factory is required")
	}
	t.Run("create-load-isolation", func(t *testing.T) { testCreateLoadIsolation(t, factory(t)) })
	t.Run("duplicate-create", func(t *testing.T) { testDuplicateCreate(t, factory(t)) })
	t.Run("cas-and-events", func(t *testing.T) { testCASAndEvents(t, factory(t)) })
	t.Run("concurrent-cas", func(t *testing.T) { testConcurrentCAS(t, factory(t)) })
}

func testCreateLoadIsolation(t *testing.T, store kernel.Store) {
	t.Helper()
	record := conformanceRecord("run_isolation")
	created, err := store.Create(context.Background(), record, []kernel.EventDraft{{Type: "run.created"}})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	record.State[1] = 'x'
	*record.Run.DeadlineAt = record.Run.DeadlineAt.Add(time.Hour)
	created.State[1] = 'y'
	created.Events[0].Data = json.RawMessage(`{"mutated":true}`)
	loaded, err := store.Load(context.Background(), record.Run.ID)
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if string(loaded.State) != `{"value":1}` || loaded.Run.DeadlineAt == nil ||
		!loaded.Run.DeadlineAt.Equal(time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("store did not isolate input/output: %#v", loaded)
	}
	loaded.State[1] = 'z'
	again, err := store.Load(context.Background(), record.Run.ID)
	if err != nil || string(again.State) != `{"value":1}` {
		t.Fatalf("load result leaked mutation: %#v, %v", again, err)
	}
}

func testDuplicateCreate(t *testing.T, store kernel.Store) {
	t.Helper()
	record := conformanceRecord("run_duplicate")
	if _, err := store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("create first record: %v", err)
	}
	if _, err := store.Create(context.Background(), record, nil); !errors.Is(err, kernel.ErrAlreadyExists) {
		t.Fatalf("expected duplicate create error, got %v", err)
	}
}

func testCASAndEvents(t *testing.T, store kernel.Store) {
	t.Helper()
	record := conformanceRecord("run_cas")
	created, err := store.Create(context.Background(), record, []kernel.EventDraft{{Type: "one"}})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	next := record
	next.Run.Revision = 2
	next.Run.UpdatedAt = next.Run.UpdatedAt.Add(time.Second)
	next.State = json.RawMessage(`{"value":2}`)
	applied, err := store.Apply(context.Background(), record.Run.ID, created.Run.Revision, kernel.StoreMutation{
		Record: next, Events: []kernel.EventDraft{{Type: "two"}, {Type: "three"}},
	})
	if err != nil {
		t.Fatalf("apply record: %v", err)
	}
	assertAppliedSnapshot(t, applied)
	assertStaleApplyDoesNotMutate(t, store, record, next)
}

func assertAppliedSnapshot(t *testing.T, applied kernel.Snapshot) {
	t.Helper()
	if applied.Run.Revision != 2 || string(applied.State) != `{"value":2}` || len(applied.Events) != 3 ||
		applied.Events[0].Seq != 1 || applied.Events[1].Seq != 2 || applied.Events[2].Seq != 3 {
		t.Fatalf("unexpected applied snapshot: %#v", applied)
	}
}

func assertStaleApplyDoesNotMutate(t *testing.T, store kernel.Store, record kernel.Record, next kernel.Record) {
	t.Helper()
	stale := next
	stale.Run.Revision = 3
	stale.State = json.RawMessage(`{"value":3}`)
	if _, err := store.Apply(context.Background(), record.Run.ID, 1, kernel.StoreMutation{Record: stale}); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("expected stale conflict, got %v", err)
	}
	loaded, err := store.Load(context.Background(), record.Run.ID)
	if err != nil || loaded.Run.Revision != 2 || string(loaded.State) != `{"value":2}` {
		t.Fatalf("conflict mutated store: %#v, %v", loaded, err)
	}
}

func testConcurrentCAS(t *testing.T, store kernel.Store) {
	t.Helper()
	record := conformanceRecord("run_concurrent")
	if _, err := store.Create(context.Background(), record, nil); err != nil {
		t.Fatalf("create record: %v", err)
	}
	const workers = 16
	results := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			next := record
			next.Run.Revision = 2
			next.State = json.RawMessage(`{"winner":true}`)
			_, err := store.Apply(context.Background(), record.Run.ID, 1, kernel.StoreMutation{
				Record: next, Events: []kernel.EventDraft{{Type: "winner"}},
			})
			results <- err
		}()
	}
	group.Wait()
	close(results)
	winners := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, kernel.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected CAS result: %v", err)
		}
	}
	if winners != 1 || conflicts != workers-1 {
		t.Fatalf("unexpected CAS outcomes: winners=%d conflicts=%d", winners, conflicts)
	}
}

func conformanceRecord(runID string) kernel.Record {
	now := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	return kernel.Record{
		Run: kernel.Run{
			ID: runID, Kind: kernel.RunKindText,
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: conformanceThreadID},
			Goal:   "test", Status: kernel.RunStatusRunning, Revision: 1,
			DeadlineAt: &deadline, CreatedAt: now, UpdatedAt: now,
		},
		State: json.RawMessage(`{"value":1}`),
	}
}
