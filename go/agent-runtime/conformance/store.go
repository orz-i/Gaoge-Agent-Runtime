// Package conformance provides reusable black-box contracts for Store adapters.
package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// StoreFactory creates a fresh Store for one isolated contract test.
type StoreFactory func(testing.TB) agentruntime.Store

// RunStore exercises durable identity, event ordering, CAS, rollback and
// aggregate reads shared by all production-grade Store implementations.
func RunStore(t *testing.T, factory StoreFactory) {
	t.Helper()
	t.Run("run bundle and event idempotency", func(t *testing.T) {
		store, actor, thread, run := seeded(t, factory)
		ctx := context.Background()
		event := domain.Event{EventID: "event-2", RunID: run.RunID, EventType: "message.completed", Actor: actor, Thread: thread}
		first, created, err := store.AppendRunEvent(ctx, &event)
		if err != nil || !created || first.Seq != 2 {
			t.Fatalf("first append = (%+v,%t,%v)", first, created, err)
		}
		second, created, err := store.AppendRunEvent(ctx, &event)
		if err != nil || created || second.Seq != first.Seq {
			t.Fatalf("idempotent append = (%+v,%t,%v)", second, created, err)
		}
		items, err := store.ListRunEventsAfter(ctx, actor, run.RunID, 0, 10)
		if err != nil || len(items) != 2 || items[0].Seq != 1 || items[1].Seq != 2 {
			t.Fatalf("events = %+v, %v", items, err)
		}
	})

	t.Run("conditional append is atomic", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		items := []domain.Event{{EventID: "event-cas", RunID: run.RunID, EventType: "message.completed", Actor: actor}}
		if _, changed, err := store.AppendRunEventsIfCurrent(ctx, run.RunID, domain.RunStatusRunning, 99, items); err != nil || changed {
			t.Fatalf("stale CAS = %t, %v", changed, err)
		}
		events, _ := store.ListRunEventsAfter(ctx, actor, run.RunID, 0, 10)
		if len(events) != 1 {
			t.Fatalf("CAS leaked events: %+v", events)
		}
	})

	t.Run("evidence and queue revisions", func(t *testing.T) {
		store, actor, thread, _ := seeded(t, factory)
		ctx := context.Background()
		evidence := domain.Evidence{EvidenceID: "evidence-1", Actor: actor, SourceKind: "output", SourceID: "output-1", ContentHash: "hash"}
		if err := store.CreateEvidence(ctx, &evidence); err != nil {
			t.Fatal(err)
		}
		found, err := store.GetEvidenceByIDs(ctx, actor, []string{evidence.EvidenceID})
		if err != nil || len(found) != 1 {
			t.Fatalf("evidence = %+v, %v", found, err)
		}
		queueThread := domain.ThreadRef{Kind: thread.Kind, ID: "queue-thread-1"}
		queue := domain.QueueItem{QueueID: "queue-1", ClientQueueID: "client-1", RequestFingerprint: "fp", Actor: actor, Thread: queueThread, Status: domain.QueueQueued}
		created, reused, err := store.CreateRunQueueItem(ctx, &queue)
		if err != nil || reused || created.Revision != 1 {
			t.Fatalf("create queue = %+v,%t,%v", created, reused, err)
		}
		created.Status = domain.QueueFailed
		if err = store.UpdateRunQueueItem(ctx, created, 99); !errors.Is(err, agentruntime.ErrRunQueueConflict) {
			t.Fatalf("stale revision = %v", err)
		}
		claimed, err := store.ClaimNextRunQueueItem(ctx, time.Now())
		if err != nil || claimed.Status != domain.QueueDispatching {
			t.Fatalf("claim = %+v,%v", claimed, err)
		}
	})

	t.Run("snapshots are copied", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		loaded, err := store.GetRun(ctx, actor, run.RunID)
		if err != nil {
			t.Fatal(err)
		}
		loaded.Goal = "mutated"
		again, err := store.GetRun(ctx, actor, run.RunID)
		if err != nil || again.Goal == "mutated" {
			t.Fatalf("store leaked mutable state: %+v, %v", again, err)
		}
	})
}

func seeded(t testing.TB, factory StoreFactory) (agentruntime.Store, domain.ActorRef, domain.ThreadRef, domain.Run) {
	t.Helper()
	store := factory(t)
	ctx := context.Background()
	now := time.Now().UTC()
	actor := domain.ActorRef{TenantID: "tenant-1", ActorID: "actor-1"}
	thread := domain.ThreadRef{Kind: "thread", ID: "thread-1"}
	run := domain.Run{RunID: "run-1", Actor: actor, Thread: thread, Goal: "goal", Status: domain.RunStatusRunning, CreatedAt: now, UpdatedAt: now}
	step := domain.Step{StepID: "step-1", RunID: run.RunID, CreatedAt: now, UpdatedAt: now}
	snapshot := domain.ContextSnapshot{SnapshotID: "snapshot-1", RunID: run.RunID, Actor: actor, Thread: thread, CreatedAt: now, UpdatedAt: now}
	checkpoint := domain.Checkpoint{CheckpointID: "checkpoint-1", RunID: run.RunID, Status: domain.CheckpointReady, CreatedAt: now, UpdatedAt: now}
	event := domain.Event{EventID: "event-1", RunID: run.RunID, EventType: "run.started", Actor: actor, Thread: thread, CreatedAt: now}
	if _, err := store.CreateRunStartBundle(ctx, &run, &step, &snapshot, nil, &checkpoint, []domain.Event{event}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return store, actor, thread, run
}
