package agentruntime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	continuationWorkerCount      = 4
	continuationPollInterval     = 5 * time.Second
	continuationLeaseDuration    = 45 * time.Second
	continuationHeartbeatPeriod  = 15 * time.Second
	continuationHeartbeatTimeout = 5 * time.Second
)

const (
	continuationRecoveryReady       = "ready"
	continuationRecoveryNotDead     = "not_dead_letter"
	continuationRecoveryRunTerminal = "run_terminal"
	continuationRecoveryRunMissing  = "run_missing"
)

type ContinuationJobInspection struct {
	Job                 model.ContinuationJob
	RunStatus           string
	Recoverable         bool
	RecoveryBlockReason string
}

type ContinuationJobInspectionPage struct {
	Items []ContinuationJobInspection
	Total int64
}

type RequeueDeadLetterContinuationInput struct {
	Actor     model.ActorRef
	JobID     string
	Reason    string
	RequestID string
}

func (s *Engine) ListContinuationJobs(ctx context.Context, filter model.ContinuationJobFilter) (ContinuationJobInspectionPage, error) {
	if s == nil || s.repo == nil {
		return ContinuationJobInspectionPage{}, ErrInvalidInput
	}
	page, err := s.repo.ListContinuationJobs(ctx, filter)
	if err != nil {
		return ContinuationJobInspectionPage{}, err
	}
	result := ContinuationJobInspectionPage{Items: make([]ContinuationJobInspection, 0, len(page.Items)), Total: page.Total}
	for _, job := range page.Items {
		result.Items = append(result.Items, s.inspectContinuationJob(ctx, job))
	}
	return result, nil
}

func (s *Engine) inspectContinuationJob(ctx context.Context, job model.ContinuationJob) ContinuationJobInspection {
	result := ContinuationJobInspection{Job: job, RecoveryBlockReason: continuationRecoveryNotDead}
	run, err := s.repo.GetRun(ctx, job.Actor, job.RunID)
	if err != nil || run == nil {
		result.RecoveryBlockReason = continuationRecoveryRunMissing
		return result
	}
	result.RunStatus = run.Status
	if job.Status != model.ContinuationJobDeadLetter {
		return result
	}
	if continuationRunDoesNotExecute(*run) {
		result.RecoveryBlockReason = continuationRecoveryRunTerminal
		return result
	}
	result.Recoverable = true
	result.RecoveryBlockReason = continuationRecoveryReady
	return result
}

func (s *Engine) RequeueDeadLetterContinuationJob(ctx context.Context, input RequeueDeadLetterContinuationInput) (*ContinuationJobInspection, error) {
	input.JobID = strings.TrimSpace(input.JobID)
	input.Reason = strings.TrimSpace(input.Reason)
	if s == nil || s.repo == nil || !validActorRef(input.Actor) || input.JobID == "" || len([]rune(input.Reason)) < 3 || len([]rune(input.Reason)) > 500 {
		return nil, ErrInvalidInput
	}
	job, err := s.repo.RequeueDeadLetterContinuationJob(ctx, input.JobID, time.Now())
	if err != nil {
		return nil, err
	}
	if s.auditWriter != nil {
		s.auditWriter.Write(ctx, strings.TrimSpace(input.RequestID), input.Actor, "agent_runtime.continuation_requeued", model.ThreadRef{Kind: "run", ID: job.RunID}, "", "", map[string]interface{}{
			"job_id":        job.JobID,
			"checkpoint_id": job.CheckpointID,
			"reason":        input.Reason,
		})
	}
	s.wakeContinuationJobs()
	inspection := s.inspectContinuationJob(ctx, *job)
	return &inspection, nil
}

func (s *Engine) createContinuationJob(ctx context.Context, run model.Run, checkpoint model.Checkpoint, source string, reservation *UsageBalanceReservation) error {
	continuation, err := decodeRunContinuation(checkpoint)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(continuation.SegmentKey))
	job := &model.ContinuationJob{
		JobID:        "continuation_" + fmt.Sprintf("%x", digest[:16]),
		SegmentKey:   continuation.SegmentKey,
		RunID:        run.RunID,
		CheckpointID: checkpoint.CheckpointID,
		Actor:        run.Actor,
		Source:       strings.TrimSpace(source),
		Status:       model.ContinuationJobQueued,
		MaxAttempts:  5,
		AvailableAt:  time.Now(),
	}
	if reservation != nil {
		job.ReservationAmountNanousd = reservation.AmountNanousd
		job.ReservationRefNo = strings.TrimSpace(reservation.RefNo)
	}
	_, _, err = s.repo.CreateContinuationJob(ctx, job)
	return err
}

func (s *Engine) failDeadLetterContinuation(ctx context.Context, job model.ContinuationJob, cause error) {
	run, err := s.repo.GetRun(ctx, job.Actor, job.RunID)
	if err != nil || run == nil || continuationRunDoesNotExecute(*run) {
		return
	}
	if cause == nil {
		cause = ErrContinuationAttemptsExhausted
	}
	message := fmt.Errorf("%w: job=%s checkpoint=%s attempts=%d: %w", ErrContinuationDeadLetter, job.JobID, job.CheckpointID, job.AttemptCount, cause)
	s.failTextRun(ctx, *run, run.CurrentStepID, message)
}

func continuationRunDoesNotExecute(run model.Run) bool {
	if run.EndedAt != nil {
		return true
	}
	switch run.Status {
	case model.RunStatusWaitingInput, model.RunStatusWaitingHandoff, model.RunStatusSuspended, model.RunStatusCompleted, model.RunStatusFailed, model.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func (s *Engine) wakeContinuationJobs() {
	if s == nil || s.continuationWake == nil {
		return
	}
	select {
	case s.continuationWake <- struct{}{}:
	default:
	}
}

func (s *Engine) startContinuationWorkers(ctx context.Context) {
	if s == nil || s.repo == nil || s.continuationWake == nil {
		return
	}
	workerGroup := "continuation-" + uuid.NewString()
	for index := 0; index < continuationWorkerCount; index++ {
		owner := fmt.Sprintf("%s-%d", workerGroup, index+1)
		s.startWorker(ctx, func(workerCtx context.Context) {
			s.continuationWorkerLoop(workerCtx, owner)
		})
	}
	s.wakeContinuationJobs()
}

func (s *Engine) continuationWorkerLoop(ctx context.Context, owner string) {
	ticker := time.NewTicker(continuationPollInterval)
	defer ticker.Stop()
	for {
		for s.runNextContinuationJob(ctx, owner) {
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.continuationWake:
		}
	}
}

func (s *Engine) runNextContinuationJob(ctx context.Context, owner string) bool {
	now := time.Now()
	if handled, stop := s.reconcileExpiredContinuation(ctx, now); handled || stop {
		return handled
	}
	job, ok := s.claimContinuationJob(ctx, owner, now)
	if !ok {
		return false
	}
	return s.executeClaimedContinuationJob(ctx, owner, *job)
}

func (s *Engine) reconcileExpiredContinuation(ctx context.Context, now time.Time) (handled, stop bool) {
	exhausted, err := s.repo.DeadLetterExpiredContinuationJob(ctx, now)
	if err == nil && exhausted != nil {
		cause := fmt.Errorf("%w: %s", ErrContinuationAttemptsExhausted, strings.TrimSpace(exhausted.LastError))
		s.failDeadLetterContinuation(context.WithoutCancel(ctx), *exhausted, cause)
		return true, false
	}
	if errors.Is(err, ErrNotFound) {
		return false, false
	}
	if err != nil && s.logger != nil {
		s.logger.Error("continuation_job_dead_letter_reconcile_failed", Error(err))
	}
	return false, err != nil
}

func (s *Engine) claimContinuationJob(ctx context.Context, owner string, now time.Time) (*model.ContinuationJob, bool) {
	job, err := s.repo.ClaimNextContinuationJob(ctx, owner, now, now.Add(continuationLeaseDuration))
	if errors.Is(err, ErrNotFound) || ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Error("continuation_job_claim_failed", Error(err))
		}
		return nil, false
	}
	return job, true
}

func (s *Engine) executeClaimedContinuationJob(ctx context.Context, owner string, job model.ContinuationJob) bool {
	err := s.processContinuationJob(ctx, owner, job)
	if ctx.Err() != nil {
		return false
	}
	if err == nil {
		s.completeContinuationJob(ctx, owner, job.JobID)
		return true
	}
	return s.retryContinuationJob(ctx, owner, job, err)
}

func (s *Engine) completeContinuationJob(ctx context.Context, owner, jobID string) {
	if err := s.repo.CompleteContinuationJob(ctx, jobID, owner, time.Now()); err != nil && s.logger != nil {
		s.logger.Error("continuation_job_complete_failed", String("job_id", jobID), Error(err))
	}
}

func (s *Engine) retryContinuationJob(ctx context.Context, owner string, job model.ContinuationJob, cause error) bool {
	deadLetter := job.AttemptCount >= job.MaxAttempts
	nextAttempt := time.Now().Add(continuationRetryDelay(job.AttemptCount))
	if err := s.repo.RetryContinuationJob(ctx, job.JobID, owner, cause.Error(), nextAttempt, deadLetter); err != nil {
		if s.logger != nil {
			s.logger.Error("continuation_job_retry_failed", String("job_id", job.JobID), Error(err))
		}
		return false
	}
	if deadLetter {
		s.failDeadLetterContinuation(context.WithoutCancel(ctx), job, cause)
	}
	if s.logger != nil {
		s.logger.Warn("continuation_job_execution_failed", String("job_id", job.JobID), String("run_id", job.RunID), Int("attempt", job.AttemptCount), Bool("dead_letter", deadLetter), Error(cause))
	}
	return true
}

func (s *Engine) processContinuationJob(ctx context.Context, owner string, job model.ContinuationJob) (resultErr error) {
	defer recoverContinuationWorkerPanic(&resultErr)
	jobCtx, heartbeat := s.startContinuationHeartbeat(ctx, owner, job.JobID)
	defer heartbeat.stop(&resultErr)
	return s.executeContinuationJob(jobCtx, owner, job)
}

type continuationHeartbeat struct {
	cancel context.CancelFunc
	err    <-chan error
	done   <-chan struct{}
}

func (s *Engine) startContinuationHeartbeat(ctx context.Context, owner, jobID string) (context.Context, continuationHeartbeat) {
	jobCtx, cancel := context.WithCancel(ctx)
	heartbeatErr := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go s.heartbeatContinuationJob(jobCtx, cancel, owner, jobID, heartbeatErr, heartbeatDone)
	return jobCtx, continuationHeartbeat{cancel: cancel, err: heartbeatErr, done: heartbeatDone}
}

func (h continuationHeartbeat) stop(resultErr *error) {
	h.cancel()
	<-h.done
	select {
	case heartbeatFailure := <-h.err:
		if *resultErr == nil || errors.Is(*resultErr, context.Canceled) {
			*resultErr = heartbeatFailure
		}
	default:
	}
}

func recoverContinuationWorkerPanic(resultErr *error) {
	if recovered := recover(); recovered != nil {
		*resultErr = fmt.Errorf("%w: %v", ErrContinuationWorkerPanic, recovered)
	}
}

func (s *Engine) executeContinuationJob(ctx context.Context, owner string, job model.ContinuationJob) error {
	run, err := s.repo.GetRun(ctx, job.Actor, job.RunID)
	if err != nil {
		return err
	}
	if continuationRunDoesNotExecute(*run) {
		return nil
	}
	checkpoint, err := s.repo.GetRunCheckpoint(ctx, job.Actor, job.RunID, job.CheckpointID)
	if err != nil {
		return err
	}
	continuation, err := decodeRunContinuation(*checkpoint)
	if err != nil || continuation.SegmentKey != job.SegmentKey {
		return ErrRunSnapshotIncompatible
	}
	ctx = context.WithValue(ctx, runHandoffJoinContextKey{}, continuation.HandoffJoin)
	effective, err := s.loadResumeTextRunRuntime(ctx, *run)
	if err != nil {
		return err
	}
	root, err := s.runRootStep(ctx, run.RunID)
	if err != nil {
		return err
	}
	reservation, err := s.ensureContinuationJobReservation(ctx, owner, job, *run, effective)
	if err != nil {
		return err
	}
	segmentCtx := context.WithValue(ctx, runSegmentKeyContextKey{}, job.SegmentKey)
	s.executeRunContinuation(segmentCtx, *run, root, effective, reservation, *checkpoint, job.Source)
	return ctx.Err()
}

func (s *Engine) ensureContinuationJobReservation(ctx context.Context, owner string, job model.ContinuationJob, run model.Run, effective effectiveTextRunConfig) (*UsageBalanceReservation, error) {
	reservationValue, hasReservation, err := continuationReservation(job, run.Actor)
	if err != nil {
		return nil, err
	}
	if hasReservation {
		return &reservationValue, nil
	}
	reservation, _, err := s.ReserveRunUsageBalance(ctx, RunBillingInput{
		Actor: run.Actor, Thread: run.Thread, PlatformModelName: effective.PlatformModelName, ClientRunID: job.SegmentKey,
	})
	if err != nil || reservation == nil {
		return reservation, err
	}
	saved, _, persistErr := s.repo.SetContinuationJobReservation(ctx, job.JobID, owner, reservation.AmountNanousd, reservation.RefNo, time.Now())
	if persistErr != nil {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Continuation Job 预扣回执持久化失败退回预扣")
		return nil, persistErr
	}
	if saved == nil || saved.ReservationRefNo != reservation.RefNo || saved.ReservationAmountNanousd != reservation.AmountNanousd {
		_ = s.ReleaseRunUsageReservation(context.WithoutCancel(ctx), reservation, "Continuation Job 预扣回执不一致退回预扣")
		return nil, ErrRunSnapshotIncompatible
	}
	return reservation, nil
}

func continuationReservation(job model.ContinuationJob, actor model.ActorRef) (UsageBalanceReservation, bool, error) {
	refNo := strings.TrimSpace(job.ReservationRefNo)
	if job.ReservationAmountNanousd == 0 && refNo == "" {
		return UsageBalanceReservation{}, false, nil
	}
	if job.ReservationAmountNanousd < 0 || refNo == "" {
		return UsageBalanceReservation{}, false, ErrRunSnapshotIncompatible
	}
	return UsageBalanceReservation{Actor: actor, AmountNanousd: job.ReservationAmountNanousd, RefNo: refNo}, true, nil
}

func (s *Engine) heartbeatContinuationJob(ctx context.Context, cancel context.CancelFunc, owner, jobID string, errOut chan<- error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(continuationHeartbeatPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case heartbeatAt := <-ticker.C:
			heartbeatCtx, heartbeatCancel := context.WithTimeout(context.WithoutCancel(ctx), continuationHeartbeatTimeout)
			err := s.repo.HeartbeatContinuationJob(heartbeatCtx, jobID, owner, heartbeatAt, heartbeatAt.Add(continuationLeaseDuration))
			heartbeatCancel()
			if err != nil {
				select {
				case errOut <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func continuationRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<(min(attempt, 6)-1))
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
