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

	t.Run("continuation reservation receipts are lease bound and idempotent", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		now := time.Now().UTC()
		job := domain.ContinuationJob{
			JobID: "continuation-reservation", SegmentKey: "segment-reservation", RunID: run.RunID, CheckpointID: "checkpoint-1",
			Actor: actor, Source: "reservation-conformance", Status: domain.ContinuationJobQueued, MaxAttempts: 3, AvailableAt: now,
		}
		if _, _, err := store.CreateContinuationJob(ctx, &job); err != nil {
			t.Fatal(err)
		}
		claimed, err := store.ClaimNextContinuationJob(ctx, "reservation-worker", now, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		receipted, reused, err := store.SetContinuationJobReservation(ctx, claimed.JobID, "reservation-worker", 55, "reservation-late", now.Add(time.Second))
		if err != nil || reused || receipted.ReservationAmountNanousd != 55 || receipted.ReservationRefNo != "reservation-late" {
			t.Fatalf("set reservation = %+v,%t,%v", receipted, reused, err)
		}
		replayed, reused, err := store.SetContinuationJobReservation(ctx, claimed.JobID, "reservation-worker", 55, "reservation-late", now.Add(2*time.Second))
		if err != nil || !reused || replayed.ReservationRefNo != receipted.ReservationRefNo {
			t.Fatalf("replay reservation = %+v,%t,%v", replayed, reused, err)
		}
		if _, _, err = store.SetContinuationJobReservation(ctx, claimed.JobID, "reservation-worker", 56, "reservation-other", now.Add(3*time.Second)); !errors.Is(err, agentruntime.ErrContinuationJobConflict) {
			t.Fatalf("reservation conflict = %v", err)
		}
		if _, _, err = store.SetContinuationJobReservation(ctx, claimed.JobID, "other-worker", 55, "reservation-late", now.Add(3*time.Second)); !errors.Is(err, agentruntime.ErrContinuationJobConflict) {
			t.Fatalf("reservation owner conflict = %v", err)
		}
		deadLetterAt := now.Add(4 * time.Second)
		if err = store.RetryContinuationJob(ctx, claimed.JobID, "reservation-worker", "reservation attempt failed", deadLetterAt, true); err != nil {
			t.Fatalf("dead-letter receipted continuation: %v", err)
		}
		deadLettered, err := store.GetContinuationJob(ctx, claimed.JobID)
		if err != nil || deadLettered.ReservationAmountNanousd != 0 || deadLettered.ReservationRefNo != "" {
			t.Fatalf("retry retained stale reservation = %+v,%v", deadLettered, err)
		}
		requeued, err := store.RequeueDeadLetterContinuationJob(ctx, claimed.JobID, deadLetterAt.Add(time.Second))
		if err != nil || requeued.ReservationAmountNanousd != 0 || requeued.ReservationRefNo != "" {
			t.Fatalf("requeue retained stale reservation = %+v,%v", requeued, err)
		}
	})

	t.Run("agent manifests are revisioned and handoffs are replay safe", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		manifest := domain.AgentManifest{
			ManifestID: "agent-research", TenantID: actor.TenantID, Name: "Research agent", Description: "Collect bounded evidence",
			Instructions: "Return a concise evidence summary.", Status: domain.AgentManifestStatusActive, ExecutionMode: "direct",
			ToolKeys: []string{"search"}, SkillRefs: []domain.ResourceRef{{Kind: "skill", ID: "research"}}, MaxChildRuns: 2, MaxDepth: 2,
			MaxLLMCalls: 4, MaxToolCalls: 8,
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
		if err != nil || latest.Revision != 2 || latest.Name != secondInput.Name || latest.MaxLLMCalls != 4 || latest.MaxToolCalls != 8 {
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
		created, reused, err := store.CreateRunHandoffWithinLimit(ctx, &handoff, 2)
		if err != nil || reused || created.ChildRunID != handoff.ChildRunID {
			t.Fatalf("create handoff = %+v,%t,%v", created, reused, err)
		}
		replayed, reused, err := store.CreateRunHandoffWithinLimit(ctx, &handoff, 2)
		if err != nil || !reused || replayed.HandoffID != created.HandoffID {
			t.Fatalf("replay handoff = %+v,%t,%v", replayed, reused, err)
		}
		secondHandoff := handoff
		secondHandoff.HandoffID = "handoff-2"
		secondHandoff.ClientHandoffID = "client-handoff-2"
		secondHandoff.RequestFingerprint = "handoff-fp-2"
		secondHandoff.ChildRunID = "run-child-2"
		secondCreated, reused, err := store.CreateRunHandoffWithinLimit(ctx, &secondHandoff, 2)
		if err != nil || reused || secondCreated.HandoffID != secondHandoff.HandoffID {
			t.Fatalf("create second handoff = %+v,%t,%v", secondCreated, reused, err)
		}
		thirdHandoff := secondHandoff
		thirdHandoff.HandoffID = "handoff-3"
		thirdHandoff.ClientHandoffID = "client-handoff-3"
		thirdHandoff.RequestFingerprint = "handoff-fp-3"
		thirdHandoff.ChildRunID = "run-child-3"
		if _, _, err = store.CreateRunHandoffWithinLimit(ctx, &thirdHandoff, 2); !errors.Is(err, agentruntime.ErrRunHandoffLimit) {
			t.Fatalf("handoff child limit = %v", err)
		}
		handoffConflict := handoff
		handoffConflict.RequestFingerprint = "different"
		if _, _, err = store.CreateRunHandoff(ctx, &handoffConflict); !errors.Is(err, agentruntime.ErrRunHandoffConflict) {
			t.Fatalf("handoff conflict = %v", err)
		}
		joinBase := domain.RunHandoffJoin{
			Actor: actor, RootRunID: run.RunID, ParentRunID: run.RunID, HandoffIDs: []string{handoff.HandoffID, secondHandoff.HandoffID},
			Quorum: 1, FailurePolicy: domain.RunHandoffJoinFailureCollect, Status: domain.RunHandoffJoinStatusPending,
		}
		invalidJoin := joinBase
		invalidJoin.JoinID, invalidJoin.ClientJoinID, invalidJoin.RequestFingerprint, invalidJoin.Mode = "join-invalid", "client-join-invalid", "join-fp-invalid", domain.RunHandoffJoinModeAll
		invalidJoin.RootRunID = "other-root"
		if _, _, err = store.CreateRunHandoffJoin(ctx, &invalidJoin); !errors.Is(err, agentruntime.ErrRunHandoffJoinMember) {
			t.Fatalf("invalid join member error = %v", err)
		}
		allCollect := joinBase
		allCollect.JoinID, allCollect.ClientJoinID, allCollect.RequestFingerprint, allCollect.Mode = "join-all-collect", "client-join-all-collect", "join-fp-all-collect", domain.RunHandoffJoinModeAll
		createdJoin, reused, err := store.CreateRunHandoffJoin(ctx, &allCollect)
		if err != nil || reused || createdJoin.Status != domain.RunHandoffJoinStatusPending || createdJoin.PendingCount != 2 {
			t.Fatalf("create all collect join = %+v,%t,%v", createdJoin, reused, err)
		}
		replayedJoin, reused, err := store.CreateRunHandoffJoin(ctx, &allCollect)
		if err != nil || !reused || replayedJoin.JoinID != allCollect.JoinID {
			t.Fatalf("replay join = %+v,%t,%v", replayedJoin, reused, err)
		}
		joinConflict := allCollect
		joinConflict.RequestFingerprint = "different"
		if _, _, err = store.CreateRunHandoffJoin(ctx, &joinConflict); !errors.Is(err, agentruntime.ErrRunHandoffJoinConflict) {
			t.Fatalf("join conflict = %v", err)
		}
		anyCollect := joinBase
		anyCollect.JoinID, anyCollect.ClientJoinID, anyCollect.RequestFingerprint, anyCollect.Mode = "join-any-collect", "client-join-any-collect", "join-fp-any-collect", domain.RunHandoffJoinModeAny
		if _, _, err = store.CreateRunHandoffJoin(ctx, &anyCollect); err != nil {
			t.Fatalf("create any collect join: %v", err)
		}
		allFailFast := joinBase
		allFailFast.JoinID, allFailFast.ClientJoinID, allFailFast.RequestFingerprint, allFailFast.Mode = "join-all-fast", "client-join-all-fast", "join-fp-all-fast", domain.RunHandoffJoinModeAll
		allFailFast.FailurePolicy = domain.RunHandoffJoinFailureFailFast
		if _, _, err = store.CreateRunHandoffJoin(ctx, &allFailFast); err != nil {
			t.Fatalf("create fail-fast join: %v", err)
		}

		completedAt := time.Now().UTC()
		firstCompletion, err := store.CompleteRunHandoffWithJoins(ctx, actor, handoff.ChildRunID, domain.RunHandoffCompletion{
			Status: domain.RunHandoffStatusCompleted, ResultSummary: "Evidence collected", ResultOutputIDs: []string{"output-1"}, CompletedAt: completedAt,
		})
		completed := firstCompletion.Handoff
		if err != nil || firstCompletion.Reused || completed.Status != domain.RunHandoffStatusCompleted || len(completed.ResultOutputIDs) != 1 {
			t.Fatalf("complete handoff = %+v,%v", firstCompletion, err)
		}
		resolvedAny, found := handoffJoinByID(firstCompletion.ResolvedJoins, anyCollect.JoinID)
		if !found || resolvedAny.Status != domain.RunHandoffJoinStatusReady {
			t.Fatalf("first completion resolved joins = %+v", firstCompletion.ResolvedJoins)
		}
		anyReady, err := store.GetRunHandoffJoin(ctx, actor, anyCollect.JoinID)
		if err != nil || anyReady.Status != domain.RunHandoffJoinStatusReady || anyReady.CompletedCount != 1 || anyReady.PendingCount != 1 {
			t.Fatalf("any join after first success = %+v,%v", anyReady, err)
		}
		allPending, err := store.GetRunHandoffJoin(ctx, actor, allCollect.JoinID)
		if err != nil || allPending.Status != domain.RunHandoffJoinStatusPending || allPending.CompletedCount != 1 || allPending.PendingCount != 1 {
			t.Fatalf("all join after first success = %+v,%v", allPending, err)
		}
		replayedCompletion, reused, err := store.CompleteRunHandoff(ctx, actor, handoff.ChildRunID, domain.RunHandoffCompletion{Status: domain.RunHandoffStatusCompleted})
		if err != nil || !reused || replayedCompletion.Status != domain.RunHandoffStatusCompleted {
			t.Fatalf("reuse handoff completion = %+v,%t,%v", replayedCompletion, reused, err)
		}
		secondCompletion, err := store.CompleteRunHandoffWithJoins(ctx, actor, secondHandoff.ChildRunID, domain.RunHandoffCompletion{Status: domain.RunHandoffStatusFailed, ErrorCode: "child_failed"})
		failed := secondCompletion.Handoff
		if err != nil || secondCompletion.Reused || failed.Status != domain.RunHandoffStatusFailed {
			t.Fatalf("fail second handoff = %+v,%v", secondCompletion, err)
		}
		resolvedAll, found := handoffJoinByID(secondCompletion.ResolvedJoins, allCollect.JoinID)
		resolvedFast, fastFound := handoffJoinByID(secondCompletion.ResolvedJoins, allFailFast.JoinID)
		if !found || resolvedAll.Status != domain.RunHandoffJoinStatusReady || !fastFound || resolvedFast.Status != domain.RunHandoffJoinStatusFailed {
			t.Fatalf("second completion resolved joins = %+v", secondCompletion.ResolvedJoins)
		}
		allReady, err := store.GetRunHandoffJoin(ctx, actor, allCollect.JoinID)
		if err != nil || allReady.Status != domain.RunHandoffJoinStatusReady || allReady.CompletedCount != 1 || allReady.FailedCount != 1 || allReady.PendingCount != 0 {
			t.Fatalf("all collect join terminal = %+v,%v", allReady, err)
		}
		fastFailed, err := store.GetRunHandoffJoin(ctx, actor, allFailFast.JoinID)
		if err != nil || fastFailed.Status != domain.RunHandoffJoinStatusFailed || fastFailed.ErrorCode != "handoff_join_child_failed" {
			t.Fatalf("fail-fast join terminal = %+v,%v", fastFailed, err)
		}
		handoffs, err := store.ListRunHandoffs(ctx, actor, domain.RunHandoffFilter{RootRunID: run.RunID})
		completedHandoff, found := handoffByID(handoffs.Results, handoff.HandoffID)
		if err != nil || handoffs.Total != 2 || len(handoffs.Results) != 2 || !found || completedHandoff.ResultSummary != "Evidence collected" {
			t.Fatalf("handoff page = %+v,%v", handoffs, err)
		}
		joins, err := store.ListRunHandoffJoins(ctx, actor, domain.RunHandoffJoinFilter{ParentRunID: run.RunID})
		if err != nil || joins.Total != 3 || len(joins.Results) != 3 {
			t.Fatalf("join page = %+v,%v", joins, err)
		}
	})

	t.Run("handoff join wait bundle is atomic and replay safe", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		runHandoffJoinWaitBundleConformance(t, store, actor, run)
	})

	t.Run("parent cancellation makes pending joins terminal", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		manifest := domain.ResourceRef{Kind: domain.AgentManifestKind, ID: "agent-cancel", Revision: "1"}
		first := domain.RunHandoff{
			HandoffID: "handoff-cancel-1", ClientHandoffID: "client-cancel-1", RequestFingerprint: "cancel-fp-1", Actor: actor,
			RootRunID: run.RunID, ParentRunID: run.RunID, ChildRunID: "run-cancel-child-1", AgentManifest: manifest,
			AgentName: "Cancel child one", Goal: "Wait for cancellation", Status: domain.RunHandoffStatusQueued, Depth: 1,
		}
		second := first
		second.HandoffID, second.ClientHandoffID, second.RequestFingerprint, second.ChildRunID = "handoff-cancel-2", "client-cancel-2", "cancel-fp-2", "run-cancel-child-2"
		if _, _, err := store.CreateRunHandoff(ctx, &first); err != nil {
			t.Fatalf("create first cancellation handoff: %v", err)
		}
		if _, _, err := store.CreateRunHandoff(ctx, &second); err != nil {
			t.Fatalf("create second cancellation handoff: %v", err)
		}
		join := domain.RunHandoffJoin{
			JoinID: "join-cancel", ClientJoinID: "client-join-cancel", RequestFingerprint: "join-cancel-fp", Actor: actor,
			RootRunID: run.RunID, ParentRunID: run.RunID, HandoffIDs: []string{first.HandoffID, second.HandoffID},
			Mode: domain.RunHandoffJoinModeAll, Quorum: 1, FailurePolicy: domain.RunHandoffJoinFailureCollect, Status: domain.RunHandoffJoinStatusPending,
		}
		if _, _, err := store.CreateRunHandoffJoin(ctx, &join); err != nil {
			t.Fatalf("create cancellation join: %v", err)
		}
		cancelledAt := time.Now().UTC()
		cancelled, err := store.CancelPendingRunHandoffJoins(ctx, actor, run.RunID, cancelledAt, "parent_run_cancelled", "parent cancelled")
		if err != nil || len(cancelled) != 1 || cancelled[0].Status != domain.RunHandoffJoinStatusCancelled || cancelled[0].ResolvedAt == nil || cancelled[0].ErrorCode != "parent_run_cancelled" {
			t.Fatalf("cancel pending joins = %+v,%v", cancelled, err)
		}
		replayed, err := store.CancelPendingRunHandoffJoins(ctx, actor, run.RunID, cancelledAt.Add(time.Second), "parent_run_cancelled", "parent cancelled")
		if err != nil || len(replayed) != 0 {
			t.Fatalf("replay cancellation = %+v,%v", replayed, err)
		}
		completion, err := store.CompleteRunHandoffWithJoins(ctx, actor, first.ChildRunID, domain.RunHandoffCompletion{Status: domain.RunHandoffStatusCancelled})
		if err != nil || len(completion.ResolvedJoins) != 0 {
			t.Fatalf("late child cancellation resolved terminal join = %+v,%v", completion, err)
		}
		stable, err := store.GetRunHandoffJoin(ctx, actor, join.JoinID)
		if err != nil || stable.Status != domain.RunHandoffJoinStatusCancelled || stable.ErrorCode != "parent_run_cancelled" {
			t.Fatalf("cancelled join changed after late child = %+v,%v", stable, err)
		}
	})

	t.Run("handoff join deadlines expire once", func(t *testing.T) {
		store, actor, _, run := seeded(t, factory)
		ctx := context.Background()
		handoff := domain.RunHandoff{
			HandoffID: "handoff-timeout", ClientHandoffID: "client-timeout", RequestFingerprint: "timeout-fp", Actor: actor,
			RootRunID: run.RunID, ParentRunID: run.RunID, ChildRunID: "run-timeout-child",
			AgentManifest: domain.ResourceRef{Kind: domain.AgentManifestKind, ID: "agent-timeout", Revision: "1"},
			AgentName:     "Timeout child", Goal: "Exercise deadline", Status: domain.RunHandoffStatusQueued, Depth: 1,
		}
		if _, _, err := store.CreateRunHandoff(ctx, &handoff); err != nil {
			t.Fatalf("create timeout handoff: %v", err)
		}
		deadline := time.Now().UTC().Add(-time.Minute)
		join := domain.RunHandoffJoin{
			JoinID: "join-timeout", ClientJoinID: "client-join-timeout", RequestFingerprint: "join-timeout-fp", Actor: actor,
			RootRunID: run.RunID, ParentRunID: run.RunID, HandoffIDs: []string{handoff.HandoffID},
			Mode: domain.RunHandoffJoinModeAll, Quorum: 1, FailurePolicy: domain.RunHandoffJoinFailureCollect,
			TimeoutSeconds: 60, TimeoutPolicy: domain.RunHandoffJoinTimeoutLeaveRunning, DeadlineAt: &deadline,
			Status: domain.RunHandoffJoinStatusPending,
		}
		if _, _, err := store.CreateRunHandoffJoin(ctx, &join); err != nil {
			t.Fatalf("create timeout join: %v", err)
		}
		expiredAt := deadline.Add(time.Minute)
		expired, err := store.ExpireNextRunHandoffJoin(ctx, expiredAt)
		if err != nil || expired.Status != domain.RunHandoffJoinStatusFailed || expired.ErrorCode != "handoff_join_timeout" || expired.ResolvedAt == nil {
			t.Fatalf("expire timeout join = %+v,%v", expired, err)
		}
		if _, err = store.ExpireNextRunHandoffJoin(ctx, expiredAt.Add(time.Second)); !errors.Is(err, agentruntime.ErrNotFound) {
			t.Fatalf("expired join was claimed twice: %v", err)
		}
	})
}

func handoffJoinByID(items []domain.RunHandoffJoin, joinID string) (domain.RunHandoffJoin, bool) {
	for _, item := range items {
		if item.JoinID == joinID {
			return item, true
		}
	}
	return domain.RunHandoffJoin{}, false
}

func runHandoffJoinWaitBundleConformance(t *testing.T, store agentruntime.Store, actor domain.ActorRef, run domain.Run) {
	t.Helper()
	fixture := prepareRunHandoffJoinWaitFixture(t, store, actor, run)
	assertRunHandoffJoinWaitCreated(t, store, fixture)
	assertRunHandoffJoinWaitReplayed(t, store, fixture)
	assertStaleRunHandoffJoinWaitRejected(t, store, fixture)
}

type runHandoffJoinWaitFixture struct {
	ctx        context.Context
	actor      domain.ActorRef
	run        domain.Run
	current    domain.Run
	join       domain.RunHandoffJoin
	checkpoint domain.Checkpoint
	events     []domain.Event
}

func prepareRunHandoffJoinWaitFixture(t *testing.T, store agentruntime.Store, actor domain.ActorRef, run domain.Run) runHandoffJoinWaitFixture {
	t.Helper()
	ctx := context.Background()
	current, err := store.GetRun(ctx, actor, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	handoff := domain.RunHandoff{
		HandoffID: "handoff-wait", ClientHandoffID: "client-handoff-wait", RequestFingerprint: "handoff-wait-fp", Actor: actor,
		RootRunID: run.RunID, ParentRunID: run.RunID, ChildRunID: "run-child-wait",
		AgentManifest: domain.ResourceRef{Kind: domain.AgentManifestKind, ID: "agent-wait", Revision: "1"},
		AgentName:     "Wait agent", Goal: "Complete bounded work", Status: domain.RunHandoffStatusQueued, Depth: 1,
	}
	if _, _, err = store.CreateRunHandoff(ctx, &handoff); err != nil {
		t.Fatal(err)
	}
	checkpoint := domain.Checkpoint{
		CheckpointID: "checkpoint-join-wait", RunID: run.RunID, StepID: "step-1", Kind: "handoff_join_wait",
		Status: domain.CheckpointReady, ManifestHash: "manifest", ResumeStateJSON: `{}`,
	}
	join := domain.RunHandoffJoin{
		JoinID: "join-wait", ClientJoinID: "client-join-wait", RequestFingerprint: "join-wait-fp", Actor: actor,
		RootRunID: run.RunID, ParentRunID: run.RunID, HandoffIDs: []string{handoff.HandoffID}, ResumeCheckpointID: checkpoint.CheckpointID,
		Mode: domain.RunHandoffJoinModeAll, Quorum: 1, FailurePolicy: domain.RunHandoffJoinFailureCollect, Status: domain.RunHandoffJoinStatusPending,
	}
	events := []domain.Event{
		{EventID: "event-join-wait-step", RunID: run.RunID, StepID: "step-1", EventType: "step.waiting_handoff", Actor: actor},
		{EventID: "event-join-wait-run", RunID: run.RunID, StepID: "step-1", EventType: "run.waiting_handoff", Actor: actor},
	}
	return runHandoffJoinWaitFixture{ctx: ctx, actor: actor, run: run, current: *current, join: join, checkpoint: checkpoint, events: events}
}

func assertRunHandoffJoinWaitCreated(t *testing.T, store agentruntime.Store, fixture runHandoffJoinWaitFixture) {
	t.Helper()
	created, saved, reused, err := store.CreateRunHandoffJoinWaitBundle(fixture.ctx, &fixture.join, fixture.current.Status, fixture.current.LastEventSeq, &fixture.checkpoint, fixture.events)
	if err != nil || reused || created.JoinID != fixture.join.JoinID || len(saved) != 2 {
		t.Fatalf("create wait bundle = %+v,%+v,%t,%v", created, saved, reused, err)
	}
	waiting, err := store.GetRun(fixture.ctx, fixture.actor, fixture.run.RunID)
	if err != nil || waiting.Status != domain.RunStatusWaitingHandoff {
		t.Fatalf("waiting run = %+v,%v", waiting, err)
	}
	if _, err = store.GetRunCheckpoint(fixture.ctx, fixture.actor, fixture.run.RunID, fixture.checkpoint.CheckpointID); err != nil {
		t.Fatalf("wait checkpoint: %v", err)
	}
}

func assertRunHandoffJoinWaitReplayed(t *testing.T, store agentruntime.Store, fixture runHandoffJoinWaitFixture) {
	t.Helper()
	replayed, replayEvents, reused, err := store.CreateRunHandoffJoinWaitBundle(fixture.ctx, &fixture.join, fixture.current.Status, fixture.current.LastEventSeq, &fixture.checkpoint, fixture.events)
	if err != nil || !reused || replayed.JoinID != fixture.join.JoinID || len(replayEvents) != 0 {
		t.Fatalf("replay wait bundle = %+v,%+v,%t,%v", replayed, replayEvents, reused, err)
	}
}

func assertStaleRunHandoffJoinWaitRejected(t *testing.T, store agentruntime.Store, fixture runHandoffJoinWaitFixture) {
	t.Helper()
	staleJoin := fixture.join
	staleJoin.JoinID, staleJoin.ClientJoinID, staleJoin.RequestFingerprint = "join-stale", "client-join-stale", "join-stale-fp"
	staleCheckpoint := fixture.checkpoint
	staleCheckpoint.CheckpointID = "checkpoint-join-stale"
	staleJoin.ResumeCheckpointID = staleCheckpoint.CheckpointID
	if _, _, _, err := store.CreateRunHandoffJoinWaitBundle(fixture.ctx, &staleJoin, fixture.current.Status, fixture.current.LastEventSeq, &staleCheckpoint, fixture.events); !errors.Is(err, agentruntime.ErrDuplicate) {
		t.Fatalf("stale wait bundle error = %v", err)
	}
	if _, err := store.GetRunHandoffJoin(fixture.ctx, fixture.actor, staleJoin.JoinID); !errors.Is(err, agentruntime.ErrNotFound) {
		t.Fatalf("stale join leaked: %v", err)
	}
	if _, err := store.GetRunCheckpoint(fixture.ctx, fixture.actor, fixture.run.RunID, staleCheckpoint.CheckpointID); !errors.Is(err, agentruntime.ErrNotFound) {
		t.Fatalf("stale checkpoint leaked: %v", err)
	}
}

func handoffByID(items []domain.RunHandoff, handoffID string) (domain.RunHandoff, bool) {
	for _, item := range items {
		if item.HandoffID == handoffID {
			return item, true
		}
	}
	return domain.RunHandoff{}, false
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
