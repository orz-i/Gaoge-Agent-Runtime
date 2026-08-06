package queue

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

// Dependencies are the only requirements of the in-memory Queue.
type Dependencies struct {
	Clock kernel.Clock
}

// Memory is a thread-safe reference Queue state machine.
type Memory struct {
	mu    sync.Mutex
	clock kernel.Clock
	jobs  map[string]Job
}

// NewMemory creates the reference Queue implementation.
func NewMemory(dependencies Dependencies) *Memory {
	clock := dependencies.Clock
	if clock == nil {
		clock = systemClock{}
	}
	return &Memory{clock: clock, jobs: make(map[string]Job)}
}

// Descriptor declares the Queue delivery capability.
func (queue *Memory) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "queue", Provides: []kernel.Capability{CapabilityQueue}}
}

// Enqueue creates or reuses one stable Job identity.
func (queue *Memory) Enqueue(ctx context.Context, request EnqueueRequest) (EnqueueResult, error) {
	if err := ctx.Err(); err != nil {
		return EnqueueResult{}, err
	}
	now := queue.now()
	normalized, err := normalizeEnqueueRequest(request, now)
	if err != nil {
		return EnqueueResult{}, err
	}
	jobID, fingerprint, err := jobIdentity(normalized)
	if err != nil {
		return EnqueueResult{}, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if existing, ok := queue.jobs[jobID]; ok {
		if existing.Fingerprint != fingerprint {
			return EnqueueResult{}, ErrConflict
		}
		return EnqueueResult{Job: cloneJob(existing), Reused: true}, nil
	}
	job := Job{
		ID: jobID, Queue: normalized.Queue, ClientJobID: normalized.ClientJobID,
		Fingerprint: fingerprint, Kind: normalized.Kind, Payload: cloneJSON(normalized.Payload),
		Priority: normalized.Priority, Policy: normalized.Policy,
		Status: StatusQueued, AvailableAt: normalized.AvailableAt.UTC(), CreatedAt: now, UpdatedAt: now,
	}
	queue.jobs[job.ID] = job
	return EnqueueResult{Job: cloneJob(job)}, nil
}

// Claim leases eligible Jobs in deterministic priority order.
func (queue *Memory) Claim(ctx context.Context, request ClaimRequest) ([]Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request.Queue = strings.TrimSpace(request.Queue)
	request.WorkerID = strings.TrimSpace(request.WorkerID)
	if request.Limit <= 0 {
		request.Limit = 1
	}
	if request.Queue == "" || request.WorkerID == "" || request.Limit > 100 {
		return nil, ErrInvalidInput
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	now := queue.now()
	queue.reapExpiredLocked(now, request.Queue)
	eligible := queue.eligibleLocked(request.Queue, now)
	if len(eligible) > request.Limit {
		eligible = eligible[:request.Limit]
	}
	deliveries := make([]Delivery, 0, len(eligible))
	for _, jobID := range eligible {
		job := queue.jobs[jobID]
		job.Attempt++
		job.Generation++
		lease := Lease{
			ID:       stableID("lease", job.ID, strconv.FormatUint(job.Generation, 10)),
			WorkerID: request.WorkerID, Generation: job.Generation, Attempt: job.Attempt,
			ClaimedAt: now, ExpiresAt: now.Add(job.Policy.VisibilityTimeout),
		}
		job.Status = StatusLeased
		job.Lease = &lease
		job.UpdatedAt = now
		queue.jobs[job.ID] = job
		deliveries = append(deliveries, Delivery{Job: cloneJob(job), Lease: lease})
	}
	return deliveries, nil
}

// Renew extends only the current, non-expired Lease generation.
func (queue *Memory) Renew(ctx context.Context, request LeaseRequest) (Delivery, error) {
	if err := ctx.Err(); err != nil {
		return Delivery{}, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	now := queue.now()
	job, err := queue.currentLeaseLocked(request, now)
	if err != nil {
		return Delivery{}, err
	}
	lease := *job.Lease
	lease.ExpiresAt = now.Add(job.Policy.VisibilityTimeout)
	job.Lease = &lease
	job.UpdatedAt = now
	queue.jobs[job.ID] = job
	return Delivery{Job: cloneJob(job), Lease: lease}, nil
}

// Ack completes only the current, non-expired Lease generation.
func (queue *Memory) Ack(ctx context.Context, request LeaseRequest) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	now := queue.now()
	job, err := queue.currentLeaseLocked(request, now)
	if err != nil {
		return Job{}, err
	}
	job.Status = StatusCompleted
	job.Lease = nil
	job.UpdatedAt = now
	completedAt := now
	job.CompletedAt = &completedAt
	queue.jobs[job.ID] = job
	return cloneJob(job), nil
}

// Nack releases only the current Lease and schedules retry or dead-lettering.
func (queue *Memory) Nack(ctx context.Context, request NackRequest) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	request.ErrorCode = strings.TrimSpace(request.ErrorCode)
	request.Error = truncate(request.Error, 1_024)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	now := queue.now()
	job, err := queue.currentLeaseLocked(request.LeaseRequest, now)
	if err != nil {
		return Job{}, err
	}
	job.LastErrorCode = request.ErrorCode
	job.LastError = request.Error
	queue.releaseForRetryLocked(&job, now)
	queue.jobs[job.ID] = job
	return cloneJob(job), nil
}

// Reap expires current leases and returns the number of transitioned Jobs.
func (queue *Memory) Reap(ctx context.Context, queueName string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return 0, ErrInvalidInput
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.reapExpiredLocked(queue.now(), queueName), nil
}

// Get returns one immutable Job snapshot.
func (queue *Memory) Get(ctx context.Context, jobID string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	jobID = strings.TrimSpace(jobID)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	job, ok := queue.jobs[jobID]
	if !ok {
		return Job{}, ErrNotFound
	}
	return cloneJob(job), nil
}

// List returns Queue jobs in deterministic creation order filtered by Status when provided.
func (queue *Memory) List(ctx context.Context, queueName string, status Status) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return nil, ErrInvalidInput
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	result := make([]Job, 0)
	for _, job := range queue.jobs {
		if job.Queue == queueName && (status == "" || job.Status == status) {
			result = append(result, cloneJob(job))
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

// RequeueDeadLetter explicitly resets one terminal Job for operator replay.
func (queue *Memory) RequeueDeadLetter(ctx context.Context, jobID string) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	jobID = strings.TrimSpace(jobID)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	job, ok := queue.jobs[jobID]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.Status != StatusDeadLetter {
		return Job{}, ErrJobTerminal
	}
	now := queue.now()
	job.Status = StatusQueued
	job.Attempt = 0
	job.AvailableAt = now
	job.Lease = nil
	job.LastErrorCode = ""
	job.LastError = ""
	job.DeadLetterAt = nil
	job.UpdatedAt = now
	queue.jobs[job.ID] = job
	return cloneJob(job), nil
}

func (queue *Memory) currentLeaseLocked(request LeaseRequest, now time.Time) (Job, error) {
	request.JobID = strings.TrimSpace(request.JobID)
	request.LeaseID = strings.TrimSpace(request.LeaseID)
	request.WorkerID = strings.TrimSpace(request.WorkerID)
	job, ok := queue.jobs[request.JobID]
	if !ok {
		return Job{}, ErrNotFound
	}
	if job.Status == StatusCompleted || job.Status == StatusDeadLetter {
		return Job{}, ErrJobTerminal
	}
	if job.Status != StatusLeased || job.Lease == nil ||
		job.Lease.ID != request.LeaseID || job.Lease.WorkerID != request.WorkerID {
		return Job{}, ErrLeaseLost
	}
	if !job.Lease.ExpiresAt.After(now) {
		queue.releaseForRetryLocked(&job, now)
		queue.jobs[job.ID] = job
		return Job{}, ErrLeaseExpired
	}
	return job, nil
}

func (queue *Memory) eligibleLocked(queueName string, now time.Time) []string {
	result := make([]string, 0)
	for jobID, job := range queue.jobs {
		if job.Queue == queueName && job.Status == StatusQueued && !job.AvailableAt.After(now) {
			result = append(result, jobID)
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		leftJob := queue.jobs[result[left]]
		rightJob := queue.jobs[result[right]]
		if leftJob.Priority != rightJob.Priority {
			return leftJob.Priority > rightJob.Priority
		}
		if !leftJob.AvailableAt.Equal(rightJob.AvailableAt) {
			return leftJob.AvailableAt.Before(rightJob.AvailableAt)
		}
		if !leftJob.CreatedAt.Equal(rightJob.CreatedAt) {
			return leftJob.CreatedAt.Before(rightJob.CreatedAt)
		}
		return leftJob.ID < rightJob.ID
	})
	return result
}

func (queue *Memory) reapExpiredLocked(now time.Time, queueName string) int {
	transitioned := 0
	for jobID, job := range queue.jobs {
		if job.Queue != queueName || job.Status != StatusLeased || job.Lease == nil || job.Lease.ExpiresAt.After(now) {
			continue
		}
		queue.releaseForRetryLocked(&job, now)
		queue.jobs[jobID] = job
		transitioned++
	}
	return transitioned
}

func (queue *Memory) releaseForRetryLocked(job *Job, now time.Time) {
	job.Lease = nil
	job.UpdatedAt = now
	if job.Attempt >= job.Policy.MaxAttempts {
		job.Status = StatusDeadLetter
		deadLetterAt := now
		job.DeadLetterAt = &deadLetterAt
		return
	}
	job.Status = StatusQueued
	job.AvailableAt = now.Add(retryBackoff(job.Policy, job.Attempt))
}

func retryBackoff(policy Policy, attempt int) time.Duration {
	delay := policy.InitialBackoff
	for index := 1; index < attempt && delay < policy.MaxBackoff; index++ {
		if delay > policy.MaxBackoff/time.Duration(policy.BackoffMultiplier) {
			return policy.MaxBackoff
		}
		delay *= time.Duration(policy.BackoffMultiplier)
	}
	if delay > policy.MaxBackoff {
		return policy.MaxBackoff
	}
	return delay
}

func (queue *Memory) now() time.Time {
	return queue.clock.Now().UTC()
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 {
		return ""
	}
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
