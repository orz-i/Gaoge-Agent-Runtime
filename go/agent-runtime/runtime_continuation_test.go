package agentruntime

import (
	"context"
	"sync"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestContinuationWorkersClaimJobsQueuedBeforeStartup(t *testing.T) {
	actor := model.ActorRef{TenantID: "tenant-restart", ActorID: "actor-restart"}
	store := &restartContinuationStore{
		run: model.Run{RunID: "run-restart", Actor: actor, Status: model.RunStatusWaitingInput},
		job: model.ContinuationJob{JobID: "continuation-restart", SegmentKey: "segment-restart", RunID: "run-restart", CheckpointID: "checkpoint-restart", Actor: actor, Status: model.ContinuationJobQueued, MaxAttempts: 3, AvailableAt: time.Now().Add(-time.Second)},
	}
	engine := &Engine{repo: store, continuationWake: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(t.Context())
	engine.startContinuationWorkers(ctx)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		status := store.job.Status
		store.mu.Unlock()
		if status == model.ContinuationJobCompleted {
			cancel()
			engine.workerWG.Wait()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	engine.workerWG.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	t.Fatalf("preexisting continuation status = %q, want %q", store.job.Status, model.ContinuationJobCompleted)
}

func (s *restartContinuationStore) DeadLetterExpiredContinuationJob(context.Context, time.Time) (*model.ContinuationJob, error) {
	return nil, ErrNotFound
}

type restartContinuationStore struct {
	Store
	mu  sync.Mutex
	run model.Run
	job model.ContinuationJob
}

func (s *restartContinuationStore) ClaimNextContinuationJob(_ context.Context, owner string, now, leaseUntil time.Time) (*model.ContinuationJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.Status != model.ContinuationJobQueued || s.job.AvailableAt.After(now) {
		return nil, ErrNotFound
	}
	s.job.Status = model.ContinuationJobRunning
	s.job.LeaseOwner = owner
	s.job.LeaseExpiresAt = &leaseUntil
	s.job.AttemptCount++
	claimed := s.job
	return &claimed, nil
}

func (s *restartContinuationStore) GetRun(_ context.Context, actor model.ActorRef, runID string) (*model.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if actor != s.run.Actor || runID != s.run.RunID {
		return nil, ErrNotFound
	}
	run := s.run
	return &run, nil
}

func (s *restartContinuationStore) CompleteContinuationJob(_ context.Context, jobID, owner string, completedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job.JobID != jobID || s.job.Status != model.ContinuationJobRunning || s.job.LeaseOwner != owner {
		return ErrContinuationJobConflict
	}
	s.job.Status = model.ContinuationJobCompleted
	s.job.LeaseOwner = ""
	s.job.LeaseExpiresAt = nil
	s.job.HeartbeatAt = &completedAt
	return nil
}
