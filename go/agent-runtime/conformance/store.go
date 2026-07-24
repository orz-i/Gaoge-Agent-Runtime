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
		store, actor, thread, run := seeded(t, factory)
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
		page, err := store.ListContinuationJobs(ctx, domain.ContinuationJobFilter{RunID: run.RunID, Status: domain.ContinuationJobDeadLetter, Limit: 1})
		if err != nil || page.Total != 2 || len(page.Items) != 1 {
			t.Fatalf("list dead-letter continuations = %+v,%v", page, err)
		}
		requeuedAt := now.Add(3 * time.Second)
		requeued, err := store.RequeueDeadLetterContinuationJob(ctx, exhausted.JobID, requeuedAt)
		if err != nil || requeued.Status != domain.ContinuationJobQueued || requeued.AttemptCount != 0 || !requeued.AvailableAt.Equal(requeuedAt) || requeued.LastError != "" {
			t.Fatalf("requeue continuation = %+v,%v", requeued, err)
		}
		recoveryClaim, err := store.ClaimNextContinuationJob(ctx, "recovery-worker", requeuedAt, requeuedAt.Add(time.Minute))
		if err != nil || recoveryClaim.JobID != exhausted.JobID || recoveryClaim.AttemptCount != 1 {
			t.Fatalf("claim requeued continuation = %+v,%v", recoveryClaim, err)
		}
		if err = store.RetryContinuationJob(ctx, recoveryClaim.JobID, "recovery-worker", "still failing", requeuedAt.Add(time.Second), true); err != nil {
			t.Fatalf("dead-letter requeued continuation: %v", err)
		}
		if _, _, _, err = store.FinalizeRun(ctx, domain.TerminalIntent{Actor: actor, Thread: thread, RunID: run.RunID, Outcome: domain.TerminalFailed, CurrentStepID: "step-1", Summary: "terminal"}); err != nil {
			t.Fatalf("finalize continuation run: %v", err)
		}
		if _, err = store.RequeueDeadLetterContinuationJob(ctx, exhausted.JobID, requeuedAt.Add(2*time.Second)); !errors.Is(err, agentruntime.ErrContinuationRunTerminal) {
			t.Fatalf("terminal continuation requeue error = %v", err)
		}
	})

	t.Run("agent manifests are revisioned and handoffs are replay safe", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		manifest := domain.AgentManifest{
			ManifestID: "agent-research", TenantID: actor.TenantID, Name: "Research agent", Description: "Collect bounded evidence",
			Instructions: "Return a concise evidence summary.", Status: domain.AgentManifestStatusActive, ExecutionMode: "direct",
			ToolKeys: []string{"search"}, SkillRefs: []domain.ResourceRef{{Kind: "skill", ID: "research"}}, MaxChildRuns: 2, MaxDepth: 2,
			CreatedBy: actor, RequestID: "manifest-request-1", RequestFingerprint: "manifest-fp-1",
		}
		first, reused, err := store.CreateAgentManifestRevision(ctx, &manifest, 0)
		if err != nil || reused || first.Revision != 1 {
			t.Fatalf("create manifest = %+v,%t,%v", first, reused, err)
		}
		again, reused, err := store.CreateAgentManifestRevision(ctx, &manifest, 0)
		if err != nil || !reused || again.Revision != first.Revision {
			t.Fatalf("reuse manifest = %+v,%t,%v", again, reused, err)
		}
		conflict := manifest
		conflict.RequestFingerprint = "different"
		if _, _, err = store.CreateAgentManifestRevision(ctx, &conflict, 0); !errors.Is(err, agentruntime.ErrAgentManifestConflict) {
			t.Fatalf("manifest request conflict = %v", err)
		}
		secondInput := manifest
		secondInput.Name = "Research specialist"
		secondInput.RequestID = "manifest-request-2"
		secondInput.RequestFingerprint = "manifest-fp-2"
		second, reused, err := store.CreateAgentManifestRevision(ctx, &secondInput, 1)
		if err != nil || reused || second.Revision != 2 {
			t.Fatalf("revise manifest = %+v,%t,%v", second, reused, err)
		}
		if _, _, err = store.CreateAgentManifestRevision(ctx, &secondInput, 1); err != nil {
			t.Fatalf("replay revised manifest: %v", err)
		}
		latest, err := store.GetAgentManifest(ctx, actor, domain.ResourceRef{Kind: domain.AgentManifestKind, ID: manifest.ManifestID})
		if err != nil || latest.Revision != 2 || latest.Name != secondInput.Name {
			t.Fatalf("latest manifest = %+v,%v", latest, err)
		}
		stable, err := store.GetAgentManifest(ctx, actor, domain.ResourceRef{Kind: domain.AgentManifestKind, ID: manifest.ManifestID, Revision: "1"})
		if err != nil || stable.Revision != 1 || stable.Name != manifest.Name {
			t.Fatalf("stable manifest revision = %+v,%v", stable, err)
		}
		page, err := store.ListAgentManifests(ctx, actor, domain.AgentManifestFilter{Status: domain.AgentManifestStatusActive})
		if err != nil || page.Total != 1 || len(page.Results) != 1 || page.Results[0].Revision != 2 {
			t.Fatalf("manifest page = %+v,%v", page, err)
		}

		handoff := domain.RunHandoff{
			HandoffID: "handoff-1", ClientHandoffID: "client-handoff-1", RequestFingerprint: "handoff-fp-1", Actor: actor,
			RootRunID: run.RunID, ParentRunID: run.RunID, ChildRunID: "run-child-1", AgentManifest: second.Ref(), AgentName: second.Name,
			Goal: "Research one bounded question", Status: domain.RunHandoffStatusQueued, Depth: 1,
		}
		created, reused, err := store.CreateRunHandoffWithinLimit(ctx, &handoff, 1)
		if err != nil || reused || created.ChildRunID != handoff.ChildRunID {
			t.Fatalf("create handoff = %+v,%t,%v", created, reused, err)
		}
		replayed, reused, err := store.CreateRunHandoffWithinLimit(ctx, &handoff, 1)
		if err != nil || !reused || replayed.HandoffID != created.HandoffID {
			t.Fatalf("replay handoff = %+v,%t,%v", replayed, reused, err)
		}
		secondHandoff := handoff
		secondHandoff.HandoffID = "handoff-2"
		secondHandoff.ClientHandoffID = "client-handoff-2"
		secondHandoff.RequestFingerprint = "handoff-fp-2"
		secondHandoff.ChildRunID = "run-child-2"
		if _, _, err = store.CreateRunHandoffWithinLimit(ctx, &secondHandoff, 1); !errors.Is(err, agentruntime.ErrRunHandoffLimit) {
			t.Fatalf("handoff child limit = %v", err)
		}
		handoffConflict := handoff
		handoffConflict.RequestFingerprint = "different"
		if _, _, err = store.CreateRunHandoff(ctx, &handoffConflict); !errors.Is(err, agentruntime.ErrRunHandoffConflict) {
			t.Fatalf("handoff conflict = %v", err)
		}
		completedAt := time.Now().UTC()
		completed, reused, err := store.CompleteRunHandoff(ctx, actor, handoff.ChildRunID, domain.RunHandoffCompletion{
			Status: domain.RunHandoffStatusCompleted, ResultSummary: "Evidence collected", ResultOutputIDs: []string{"output-1"}, CompletedAt: completedAt,
		})
		if err != nil || reused || completed.Status != domain.RunHandoffStatusCompleted || len(completed.ResultOutputIDs) != 1 {
			t.Fatalf("complete handoff = %+v,%t,%v", completed, reused, err)
		}
		completed, reused, err = store.CompleteRunHandoff(ctx, actor, handoff.ChildRunID, domain.RunHandoffCompletion{Status: domain.RunHandoffStatusCompleted})
		if err != nil || !reused || completed.Status != domain.RunHandoffStatusCompleted {
			t.Fatalf("reuse handoff completion = %+v,%t,%v", completed, reused, err)
		}
		handoffs, err := store.ListRunHandoffs(ctx, actor, domain.RunHandoffFilter{RootRunID: run.RunID})
		if err != nil || handoffs.Total != 1 || len(handoffs.Results) != 1 || handoffs.Results[0].ResultSummary != "Evidence collected" {
			t.Fatalf("handoff page = %+v,%v", handoffs, err)
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
