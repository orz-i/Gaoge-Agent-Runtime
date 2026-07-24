package agentruntime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testContinuationRunRunning = "run-running"
	testContinuationRunFailed  = "run-failed"
	testContinuationRunRequeue = "run-requeue"
)

type continuationAdminStore struct {
	Store
	jobs     []model.ContinuationJob
	runs     map[string]model.Run
	requeued *model.ContinuationJob
	err      error
}

func (s *continuationAdminStore) ListContinuationJobs(context.Context, model.ContinuationJobFilter) (model.ContinuationJobPage, error) {
	return model.ContinuationJobPage{Items: append([]model.ContinuationJob(nil), s.jobs...), Total: int64(len(s.jobs))}, s.err
}

func (s *continuationAdminStore) GetRun(_ context.Context, actor model.ActorRef, runID string) (*model.Run, error) {
	run, ok := s.runs[runID]
	if !ok || run.Actor != actor {
		return nil, ErrNotFound
	}
	return &run, nil
}

func (s *continuationAdminStore) RequeueDeadLetterContinuationJob(context.Context, string, time.Time) (*model.ContinuationJob, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.requeued == nil {
		return nil, ErrNotFound
	}
	item := *s.requeued
	return &item, nil
}

type continuationAuditRecord struct {
	requestID string
	actor     model.ActorRef
	action    string
	thread    model.ThreadRef
	metadata  interface{}
}

type continuationAuditWriter struct{ records []continuationAuditRecord }

func (w *continuationAuditWriter) Write(_ context.Context, requestID string, actor model.ActorRef, action string, thread model.ThreadRef, _, _ string, metadata interface{}) {
	w.records = append(w.records, continuationAuditRecord{requestID: requestID, actor: actor, action: action, thread: thread, metadata: metadata})
}

func TestListContinuationJobsReportsRecoverabilityWithoutExposingSegmentKey(t *testing.T) {
	actor := model.ActorRef{TenantID: "tenant-admin", ActorID: "actor-admin"}
	now := time.Now()
	store := &continuationAdminStore{
		jobs: []model.ContinuationJob{
			{JobID: "recoverable", RunID: testContinuationRunRunning, Actor: actor, Status: model.ContinuationJobDeadLetter, UpdatedAt: now},
			{JobID: "terminal", RunID: testContinuationRunFailed, Actor: actor, Status: model.ContinuationJobDeadLetter, UpdatedAt: now},
			{JobID: "queued", RunID: testContinuationRunRunning, Actor: actor, Status: model.ContinuationJobQueued, UpdatedAt: now},
			{JobID: "missing", RunID: "run-missing", Actor: actor, Status: model.ContinuationJobDeadLetter, UpdatedAt: now},
		},
		runs: map[string]model.Run{
			testContinuationRunRunning: {RunID: testContinuationRunRunning, Actor: actor, Status: model.RunStatusRunning},
			testContinuationRunFailed:  {RunID: testContinuationRunFailed, Actor: actor, Status: model.RunStatusFailed, EndedAt: &now},
		},
	}
	engine := &Engine{repo: store}
	page, err := engine.ListContinuationJobs(t.Context(), model.ContinuationJobFilter{})
	if err != nil || page.Total != 4 || len(page.Items) != 4 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	assertContinuationInspection(t, page.Items[0], true, continuationRecoveryReady, model.RunStatusRunning)
	assertContinuationInspection(t, page.Items[1], false, continuationRecoveryRunTerminal, model.RunStatusFailed)
	assertContinuationInspection(t, page.Items[2], false, continuationRecoveryNotDead, model.RunStatusRunning)
	assertContinuationInspection(t, page.Items[3], false, continuationRecoveryRunMissing, "")
}

func assertContinuationInspection(t *testing.T, inspection ContinuationJobInspection, recoverable bool, reason, status string) {
	t.Helper()
	if inspection.Recoverable != recoverable || inspection.RecoveryBlockReason != reason || inspection.RunStatus != status {
		t.Fatalf("inspection=%#v want recoverable=%t reason=%q status=%q", inspection, recoverable, reason, status)
	}
}

func TestRequeueDeadLetterContinuationRequiresReasonAndWritesAudit(t *testing.T) {
	actor := model.ActorRef{TenantID: "tenant-admin", ActorID: "operator"}
	job := &model.ContinuationJob{JobID: "continuation-requeue", RunID: testContinuationRunRequeue, Actor: actor, Status: model.ContinuationJobQueued}
	store := &continuationAdminStore{requeued: job, runs: map[string]model.Run{testContinuationRunRequeue: {RunID: testContinuationRunRequeue, Actor: actor, Status: model.RunStatusRunning}}}
	audit := &continuationAuditWriter{}
	engine := &Engine{repo: store, auditWriter: audit, continuationWake: make(chan struct{}, 1)}
	if _, err := engine.RequeueDeadLetterContinuationJob(t.Context(), RequeueDeadLetterContinuationInput{Actor: actor, JobID: job.JobID, Reason: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short reason error=%v", err)
	}
	inspection, err := engine.RequeueDeadLetterContinuationJob(t.Context(), RequeueDeadLetterContinuationInput{Actor: actor, JobID: job.JobID, Reason: "retry after provider recovery", RequestID: "request-admin"})
	if err != nil || inspection.Job.JobID != job.JobID || inspection.Recoverable {
		t.Fatalf("inspection=%#v err=%v", inspection, err)
	}
	assertContinuationRequeueAudit(t, audit.records, job.RunID)
	assertContinuationWake(t, engine.continuationWake)
}

func assertContinuationRequeueAudit(t *testing.T, records []continuationAuditRecord, runID string) {
	t.Helper()
	if len(records) != 1 || records[0].requestID != "request-admin" || records[0].action != "agent_runtime.continuation_requeued" || records[0].thread.ID != runID {
		t.Fatalf("audit records=%#v", records)
	}
}

func assertContinuationWake(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
	default:
		t.Fatal("continuation workers were not woken")
	}
}

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
