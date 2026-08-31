// Package conformance exposes reusable adapter contract suites.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	conformanceThreadID  = "thread"
	conformanceValueJSON = `{"value":1}`
	conformanceRunKind   = kernel.RunKind("conformance")
)

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
	t.Run("checkpoint-result-isolation", func(t *testing.T) { testCheckpointResultIsolation(t, factory(t)) })
	t.Run("cas-and-event-journal", func(t *testing.T) { testCASAndEvents(t, factory(t)) })
	t.Run("concurrent-cas", func(t *testing.T) { testConcurrentCAS(t, factory(t)) })
	t.Run("transition-outbox", func(t *testing.T) { testTransitionOutbox(t, factory(t)) })
}

func testCheckpointResultIsolation(t *testing.T, store kernel.Store) {
	t.Helper()
	record := conformanceRecord("run_optional")
	record.Checkpoint = &kernel.Checkpoint{
		ID: "checkpoint_1", Kind: "approval", Status: kernel.CheckpointPending,
		Payload: json.RawMessage(`{"required":true}`), CreatedAt: record.Run.CreatedAt,
	}
	record.Result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"value":1}`)}
	created, err := store.Create(context.Background(), record, nil)
	if err != nil {
		t.Fatalf("create optional record: %v", err)
	}
	record.Checkpoint.Payload[1] = 'x'
	record.Result.Content[1] = 'x'
	created.Checkpoint.Payload[1] = 'y'
	created.Result.Content[1] = 'y'
	loaded, err := store.Load(context.Background(), record.Run.ID)
	if err != nil {
		t.Fatalf("load optional record: %v", err)
	}
	if loaded.Checkpoint == nil || string(loaded.Checkpoint.Payload) != `{"required":true}` ||
		loaded.Result == nil || string(loaded.Result.Content) != conformanceValueJSON {
		t.Fatalf("checkpoint/result mutation leaked: %#v", loaded)
	}
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
	loaded, err := store.Load(context.Background(), record.Run.ID)
	if err != nil {
		t.Fatalf("load record: %v", err)
	}
	if string(loaded.State) != conformanceValueJSON || loaded.EventHead != 1 || loaded.Run.DeadlineAt == nil ||
		!loaded.Run.DeadlineAt.Equal(time.Date(2026, 8, 6, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("store did not isolate input/output: %#v", loaded)
	}
	loaded.State[1] = 'z'
	again, err := store.Load(context.Background(), record.Run.ID)
	if err != nil || string(again.State) != conformanceValueJSON {
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
	events, err := store.ListEvents(context.Background(), record.Run.ID, 0, 2)
	if err != nil || len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("first journal page = %#v, %v", events, err)
	}
	events[0].Data = json.RawMessage(`{"mutated":true}`)
	last, err := store.ListEvents(context.Background(), record.Run.ID, events[1].Seq, 2)
	if err != nil || len(last) != 1 || last[0].Seq != 3 || last[0].Type != "three" {
		t.Fatalf("second journal page = %#v, %v", last, err)
	}
	again, err := store.ListEvents(context.Background(), record.Run.ID, 0, 1)
	if err != nil || len(again) != 1 || len(again[0].Data) != 0 {
		t.Fatalf("journal page leaked mutation = %#v, %v", again, err)
	}
	assertStaleApplyDoesNotMutate(t, store, record, next)
}

func assertAppliedSnapshot(t *testing.T, applied kernel.Snapshot) {
	t.Helper()
	if applied.Run.Revision != 2 || string(applied.State) != `{"value":2}` || applied.EventHead != 3 {
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

func testTransitionOutbox(t *testing.T, store kernel.Store) {
	t.Helper()
	ordinary := conformanceRecord("run_outbox_ordinary")
	if _, err := store.Create(context.Background(), ordinary, []kernel.EventDraft{{Type: "run.observed"}}); err != nil {
		t.Fatalf("create ordinary transition: %v", err)
	}
	if claims, err := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "ordinary-projector", Limit: 4, LeaseDuration: time.Minute, Now: ordinary.Run.UpdatedAt,
	}); err != nil || len(claims) != 0 {
		t.Fatalf("ordinary transition should not enter outbox: %#v, %v", claims, err)
	}

	record := conformanceRecord("run_outbox")
	wakeupAt := record.Run.UpdatedAt.Add(90 * time.Second)
	events := []kernel.EventDraft{{
		Type: "resume.ready", Data: json.RawMessage(`{"value":1}`), Wakeup: true, WakeupAt: &wakeupAt,
	}}
	if _, err := store.Create(context.Background(), record, events); err != nil {
		t.Fatalf("create outbox record: %v", err)
	}
	events[0].Data[1] = 'x'
	now := record.Run.UpdatedAt
	claims, err := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "projector-1", Limit: 4, LeaseDuration: time.Minute, Now: now,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim transition: %#v, %v", claims, err)
	}
	claim := claims[0]
	if claim.Transition.ID != "run_outbox:1" || claim.Transition.RunID != record.Run.ID ||
		claim.Transition.Revision != 1 || claim.Transition.Attempts != 1 || len(claim.Transition.Events) != 1 ||
		string(claim.Transition.Events[0].Data) != conformanceValueJSON || claim.Transition.Events[0].WakeupAt == nil ||
		!claim.Transition.Events[0].WakeupAt.Equal(wakeupAt) {
		t.Fatalf("unexpected committed transition: %#v", claim)
	}
	if again, claimErr := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "projector-2", Limit: 4, LeaseDuration: time.Minute, Now: now.Add(30 * time.Second),
	}); claimErr != nil || len(again) != 0 {
		t.Fatalf("active lease was claimed twice: %#v, %v", again, claimErr)
	}
	retryAt := now.Add(2 * time.Minute)
	lease := kernel.TransitionLeaseRequest{
		TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
	}
	if err = store.RetryTransition(context.Background(), kernel.TransitionRetryRequest{
		TransitionLeaseRequest: lease, AvailableAt: retryAt,
	}); err != nil {
		t.Fatalf("retry transition: %v", err)
	}
	if early, claimErr := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "projector-2", Limit: 4, LeaseDuration: time.Minute, Now: retryAt.Add(-time.Second),
	}); claimErr != nil || len(early) != 0 {
		t.Fatalf("transition became available early: %#v, %v", early, claimErr)
	}
	claims, err = store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "projector-2", Limit: 4, LeaseDuration: time.Minute, Now: retryAt,
	})
	if err != nil || len(claims) != 1 || claims[0].Transition.Attempts != 2 {
		t.Fatalf("reclaim transition: %#v, %v", claims, err)
	}
	claim = claims[0]
	if err = store.AckTransition(context.Background(), kernel.TransitionLeaseRequest{
		TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
	}); err != nil {
		t.Fatalf("ack transition: %v", err)
	}
	if afterAck, claimErr := store.ClaimTransitions(context.Background(), kernel.TransitionClaimRequest{
		WorkerID: "projector-3", Limit: 4, LeaseDuration: time.Minute, Now: retryAt.Add(2 * time.Minute),
	}); claimErr != nil || len(afterAck) != 0 {
		t.Fatalf("acknowledged transition was redelivered: %#v, %v", afterAck, claimErr)
	}
}

func conformanceRecord(runID string) kernel.Record {
	now := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	return kernel.Record{
		Run: kernel.Run{
			ID: runID, Kind: conformanceRunKind,
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: conformanceThreadID},
			Goal:   "test", Status: kernel.RunStatusRunning, Revision: 1,
			DeadlineAt: &deadline, CreatedAt: now, UpdatedAt: now,
		},
		State: json.RawMessage(conformanceValueJSON),
	}
}
