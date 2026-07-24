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

	t.Run("continuation jobs are idempotent leased and reclaimable", func(t *testing.T) {
		const checkpointID = "checkpoint-1"
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		now := time.Now().UTC()
		job := domain.ContinuationJob{JobID: "continuation-1", SegmentKey: "segment-1", RunID: run.RunID, CheckpointID: checkpointID, Actor: actor, Source: "conformance", Status: domain.ContinuationJobQueued, ReservationAmountNanousd: 42, ReservationRefNo: "reservation-1", MaxAttempts: 3, AvailableAt: now}
		created, reused, err := store.CreateContinuationJob(ctx, &job)
		if err != nil || reused || created.Status != domain.ContinuationJobQueued || created.ReservationAmountNanousd != 42 || created.ReservationRefNo != "reservation-1" {
			t.Fatalf("create continuation = %+v,%t,%v", created, reused, err)
		}
		again, reused, err := store.CreateContinuationJob(ctx, &job)
		if err != nil || !reused || again.JobID != created.JobID {
			t.Fatalf("reuse continuation = %+v,%t,%v", again, reused, err)
		}
		leaseUntil := now.Add(time.Minute)
		claimed, err := store.ClaimNextContinuationJob(ctx, "worker-1", now, leaseUntil)
		if err != nil || claimed.Status != domain.ContinuationJobRunning || claimed.AttemptCount != 1 || claimed.LeaseOwner != "worker-1" {
			t.Fatalf("claim continuation = %+v,%v", claimed, err)
		}
		if _, err = store.ClaimNextContinuationJob(ctx, "worker-2", now.Add(30*time.Second), now.Add(90*time.Second)); !errors.Is(err, agentruntime.ErrNotFound) {
			t.Fatalf("unexpired continuation was reclaimed: %v", err)
		}
		heartbeatAt := now.Add(45 * time.Second)
		if err = store.HeartbeatContinuationJob(ctx, claimed.JobID, "worker-1", heartbeatAt, heartbeatAt.Add(time.Minute)); err != nil {
			t.Fatalf("heartbeat continuation: %v", err)
		}
		if _, err = store.ClaimNextContinuationJob(ctx, "worker-2", now.Add(75*time.Second), now.Add(135*time.Second)); !errors.Is(err, agentruntime.ErrNotFound) {
			t.Fatalf("heartbeat lease was ignored: %v", err)
		}
		reclaimedAt := heartbeatAt.Add(time.Minute)
		reclaimed, err := store.ClaimNextContinuationJob(ctx, "worker-2", reclaimedAt, reclaimedAt.Add(time.Minute))
		if err != nil || reclaimed.AttemptCount != 2 || reclaimed.LeaseOwner != "worker-2" {
			t.Fatalf("reclaim continuation = %+v,%v", reclaimed, err)
		}
		retryAt := reclaimedAt.Add(time.Second)
		if err = store.RetryContinuationJob(ctx, reclaimed.JobID, "worker-2", "transient", retryAt, false); err != nil {
			t.Fatalf("retry continuation: %v", err)
		}
		final, err := store.ClaimNextContinuationJob(ctx, "worker-3", retryAt, retryAt.Add(time.Minute))
		if err != nil || final.AttemptCount != 3 {
			t.Fatalf("final claim = %+v,%v", final, err)
		}
		if err = store.RetryContinuationJob(ctx, final.JobID, "worker-3", "exhausted", retryAt, true); err != nil {
			t.Fatalf("dead letter continuation: %v", err)
		}
		if _, err = store.ClaimNextContinuationJob(ctx, "worker-4", retryAt.Add(time.Hour), retryAt.Add(2*time.Hour)); !errors.Is(err, agentruntime.ErrNotFound) {
			t.Fatalf("dead letter continuation was claimable: %v", err)
		}

		exhausted := domain.ContinuationJob{JobID: "continuation-exhausted", SegmentKey: "segment-exhausted", RunID: run.RunID, CheckpointID: checkpointID, Actor: actor, Status: domain.ContinuationJobQueued, MaxAttempts: 1, AvailableAt: now}
		if _, _, err = store.CreateContinuationJob(ctx, &exhausted); err != nil {
			t.Fatalf("create exhausted continuation: %v", err)
		}
		crashed, err := store.ClaimNextContinuationJob(ctx, "crashed-worker", now, now.Add(time.Second))
		if err != nil || crashed.JobID != exhausted.JobID || crashed.AttemptCount != 1 {
			t.Fatalf("claim exhausted continuation = %+v,%v", crashed, err)
		}
		deadLettered, err := store.DeadLetterExpiredContinuationJob(ctx, now.Add(2*time.Second))
		if err != nil || deadLettered.JobID != exhausted.JobID || deadLettered.Status != domain.ContinuationJobDeadLetter {
			t.Fatalf("dead letter exhausted continuation = %+v,%v", deadLettered, err)
		}
		if _, err = store.ClaimNextContinuationJob(ctx, "recovery-worker", now.Add(2*time.Second), now.Add(time.Minute)); !errors.Is(err, agentruntime.ErrNotFound) {
			t.Fatalf("dead-lettered continuation was reclaimed: %v", err)
		}
		exhaustedView, err := store.GetContinuationJob(ctx, exhausted.JobID)
		if err != nil || exhaustedView.Status != domain.ContinuationJobDeadLetter {
			t.Fatalf("exhausted continuation = %+v,%v", exhaustedView, err)
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
