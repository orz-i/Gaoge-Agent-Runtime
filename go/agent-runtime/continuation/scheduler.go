package continuation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// ErrorReporter receives non-transactional scheduling failures for host observability.
type ErrorReporter func(error)

// ReconcileGuard is an optional feature-owned predicate for a terminal child.
// Returning false means the parent has already consumed this child in its
// durable state, so a new parent Revision must not re-enqueue that old fact.
// Unknown feature kinds should return true to preserve recovery safety.
type ReconcileGuard func(context.Context, kernel.Snapshot, runrelation.Relation) (bool, error)

// Scheduler converts committed Kernel transitions into idempotent delivery Jobs.
type Scheduler struct {
	outbox    kernel.TransitionOutbox
	queue     Enqueuer
	relations interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
		ListAll(context.Context) ([]runrelation.Relation, error)
	}
	runs      SnapshotLoader
	clock     kernel.Clock
	workerID  string
	jobPolicy queuecore.Policy
	triggers  map[kernel.RunKind]SelfTriggerResolver
	guard     ReconcileGuard

	// The optional pager bounds recovery work per tick. Cursor state is local
	// and disposable; durable per-relation handoff watermarks live in the Store.
	reconcilePager    runrelation.ReconciliationPager
	reconcileMu       sync.Mutex
	reconcileCursor   string
	reconcileFailures map[string]runrelation.ReconciliationCandidate
	reconcileRetryIDs []string
}

// SchedulerDependencies explicitly provide transition projection dependencies.
type SchedulerDependencies struct {
	Outbox    kernel.TransitionOutbox
	Queue     Enqueuer
	Relations interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
		ListAll(context.Context) ([]runrelation.Relation, error)
	}
	Runs           SnapshotLoader
	Clock          kernel.Clock
	ProjectorID    string
	ReconcileGuard ReconcileGuard
	// RequireDurableQueue rejects volatile queues for production reconciliation.
	// Redis persistence and retention still need host-side configuration.
	RequireDurableQueue bool
}

const (
	maxReconcileRetryBacklog   = 128
	maxReconcileRetriesPerTick = 16
)

var errReconcileRetryBacklogFull = errors.New("continuation reconciliation retry backlog is full")

// Reconcile repairs terminal child handoffs. Built-in adapters use bounded
// keyset pages. Progress advances after queue acceptance or feature-verified
// consumption; unknown kinds are conservatively enqueued.
// Legacy host-defined relation adapters without a pager retain ListAll fallback.
func (scheduler *Scheduler) Reconcile(ctx context.Context) error {
	if scheduler == nil {
		return ErrInvalidInput
	}
	if scheduler.reconcilePager == nil {
		return scheduler.reconcileLegacy(ctx)
	}
	scheduler.reconcileMu.Lock()
	defer scheduler.reconcileMu.Unlock()
	result := scheduler.retryReconcileFailures(ctx)
	if err := ctx.Err(); err != nil {
		return errors.Join(result, err)
	}
	page, err := scheduler.reconcilePager.ListReconcilePage(
		ctx, scheduler.reconcileCursor, runrelation.MaxReconcilePageSize,
	)
	if err != nil {
		return errors.Join(result, err)
	}
	lastProcessed := scheduler.reconcileCursor
	for _, candidate := range page.Candidates {
		if err = ctx.Err(); err != nil {
			scheduler.reconcileCursor = lastProcessed
			return errors.Join(result, err)
		}
		id := candidate.Relation.ChildRunID
		if _, pending := scheduler.reconcileFailures[id]; pending {
			lastProcessed = id
			continue
		}
		candidateErr := scheduler.reconcileCandidate(ctx, candidate)
		if err = ctx.Err(); err != nil {
			// An interrupted candidate must be retried on the next tick.
			scheduler.reconcileCursor = lastProcessed
			return errors.Join(result, candidateErr, err)
		}
		if candidateErr != nil {
			if len(scheduler.reconcileFailures) >= maxReconcileRetryBacklog {
				// The relation remains unsettled in the durable pager. Do not
				// stall every later candidate behind a full in-memory retry
				// backlog; a future cursor wrap will revisit this item.
				result = errors.Join(result, candidateErr, errReconcileRetryBacklogFull)
				lastProcessed = id
				continue
			}
			if scheduler.reconcileFailures == nil {
				scheduler.reconcileFailures = make(map[string]runrelation.ReconciliationCandidate)
			}
			scheduler.reconcileFailures[id] = candidate
			scheduler.reconcileRetryIDs = append(scheduler.reconcileRetryIDs, id)
			result = errors.Join(result, candidateErr)
		}
		lastProcessed = id
	}
	if page.HasMore {
		scheduler.reconcileCursor = page.NextCursor
	} else {
		scheduler.reconcileCursor = ""
	}
	return result
}

// retryReconcileFailures rotates a bounded, in-memory retry queue so a single
// failing relation does not block all later pages. Missing watermarks remain
// durable; on process restart the cursor resets and old failures are rescanned.
func (scheduler *Scheduler) retryReconcileFailures(ctx context.Context) error {
	var result error
	count := min(maxReconcileRetriesPerTick, len(scheduler.reconcileRetryIDs))
	for index := 0; index < count; index++ {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		id := scheduler.reconcileRetryIDs[0]
		scheduler.reconcileRetryIDs = scheduler.reconcileRetryIDs[1:]
		candidate := scheduler.reconcileFailures[id]
		err := scheduler.reconcileCandidate(ctx, candidate)
		if err != nil {
			scheduler.reconcileRetryIDs = append(scheduler.reconcileRetryIDs, id)
			result = errors.Join(result, err)
		} else {
			delete(scheduler.reconcileFailures, id)
		}
	}
	return result
}

func (scheduler *Scheduler) reconcileCandidate(
	ctx context.Context, candidate runrelation.ReconciliationCandidate,
) error {
	child, err := scheduler.runs.Load(ctx, candidate.Relation.ChildRunID)
	if errors.Is(err, kernel.ErrNotFound) || (err == nil && !terminal(child.Run.Status)) {
		return nil
	}
	if err != nil {
		return err
	}
	parent, err := scheduler.runs.Load(ctx, candidate.Relation.ParentRunID)
	if errors.Is(err, kernel.ErrNotFound) || (err == nil && parent.Run.Status != kernel.RunStatusRunning) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameRelationTenant(parent, child) {
		return fmt.Errorf("%w: cross-tenant run relation", ErrInvalidInput)
	}
	if candidate.LastParentRevision >= parent.Run.Revision &&
		candidate.LastChildRevision >= child.Run.Revision {
		return nil
	}
	if scheduler.guard != nil {
		needed, guardErr := scheduler.guard(ctx, parent, candidate.Relation)
		if guardErr != nil {
			return guardErr
		}
		if !needed {
			// A feature-verified consumed child is a settled fact, not a new
			// delivery. Advance the observed revisions to suppress replay.
			return scheduler.reconcilePager.MarkObserved(
				ctx, child.Run.ID, parent.Run.Revision, child.Run.Revision,
			)
		}
	}
	if err = scheduler.Schedule(ctx, Payload{
		SchemaVersion: SchemaVersion, RunID: parent.Run.ID,
		ExpectedRevision: parent.Run.Revision, Trigger: TriggerChildTerminal,
		SourceRunID: child.Run.ID, SourceRevision: child.Run.Revision,
	}); err != nil {
		return err
	}
	return scheduler.reconcilePager.MarkEnqueued(
		ctx, child.Run.ID, parent.Run.Revision, child.Run.Revision,
	)
}

func (scheduler *Scheduler) reconcileLegacy(ctx context.Context) error {
	relations, err := scheduler.relations.ListAll(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, relation := range relations {
		child, loadErr := scheduler.runs.Load(ctx, relation.ChildRunID)
		if errors.Is(loadErr, kernel.ErrNotFound) || (loadErr == nil && !terminal(child.Run.Status)) {
			continue
		}
		if loadErr != nil {
			result = errors.Join(result, loadErr)
			continue
		}
		result = errors.Join(result, scheduler.scheduleRelation(ctx, relation, child))
	}
	return result
}

// Project drains the durable committed-transition outbox. Queue identity makes
// retry after enqueue-before-ack idempotent: a duplicate queue conflict is a
// successful logical handoff and the outbox record can then be acknowledged.
func (scheduler *Scheduler) Project(ctx context.Context) error {
	if scheduler == nil || scheduler.outbox == nil {
		return ErrInvalidInput
	}
	now := scheduler.clock.Now().UTC()
	claims, err := scheduler.outbox.ClaimTransitions(ctx, kernel.TransitionClaimRequest{
		WorkerID: scheduler.workerID, Limit: 32, LeaseDuration: 30 * time.Second, Now: now,
	})
	if err != nil {
		return err
	}
	var result error
	for _, claim := range claims {
		projectErr := scheduler.projectTransition(ctx, claim.Transition)
		lease := kernel.TransitionLeaseRequest{
			TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
		}
		if projectErr == nil {
			result = errors.Join(result, scheduler.outbox.AckTransition(ctx, lease))
			continue
		}
		retryErr := scheduler.outbox.RetryTransition(ctx, kernel.TransitionRetryRequest{
			TransitionLeaseRequest: lease,
			AvailableAt:            now.Add(projectionBackoff(claim.Transition.Attempts)),
		})
		result = errors.Join(result, projectErr, retryErr)
	}
	return result
}

// NewScheduler creates a durable committed-transition projector.
func NewScheduler(dependencies SchedulerDependencies, registrations ...TriggerRegistration) (*Scheduler, error) {
	if dependencies.Outbox == nil || dependencies.Queue == nil || dependencies.Relations == nil || dependencies.Runs == nil {
		return nil, ErrInvalidInput
	}
	if dependencies.RequireDurableQueue {
		durable, ok := dependencies.Queue.(interface{ DurableQueue() bool })
		if !ok || !durable.DurableQueue() {
			return nil, ErrDurableQueueRequired
		}
	}
	if dependencies.Clock == nil {
		dependencies.Clock = schedulerClock{}
	}
	if strings.TrimSpace(dependencies.ProjectorID) == "" {
		dependencies.ProjectorID = randomWorkerID() + "-projector"
	}
	triggers := make(map[kernel.RunKind]SelfTriggerResolver, len(registrations))
	for _, registration := range registrations {
		if !validRegistrationKind(registration.kind) || registration.resolver == nil {
			return nil, ErrInvalidInput
		}
		if _, duplicate := triggers[registration.kind]; duplicate {
			return nil, ErrInvalidInput
		}
		triggers[registration.kind] = registration.resolver
	}
	var pager runrelation.ReconciliationPager
	if provider, ok := dependencies.Relations.(runrelation.ReconciliationProvider); ok {
		pager, _ = provider.ReconciliationPager()
	}
	return &Scheduler{
		outbox: dependencies.Outbox, queue: dependencies.Queue, relations: dependencies.Relations, runs: dependencies.Runs,
		clock: dependencies.Clock, workerID: strings.TrimSpace(dependencies.ProjectorID), triggers: triggers,
		guard:          dependencies.ReconcileGuard,
		reconcilePager: pager,
		jobPolicy: queuecore.Policy{
			MaxAttempts: 12, VisibilityTimeout: 2 * time.Minute,
			InitialBackoff: 250 * time.Millisecond, MaxBackoff: 30 * time.Second, BackoffMultiplier: 2,
		},
	}, nil
}

func (scheduler *Scheduler) projectTransition(ctx context.Context, transition kernel.CommittedTransition) error {
	var result error
	if terminal(transition.Status) {
		result = errors.Join(result, scheduler.scheduleOwningParent(ctx, transition))
	}
	for _, event := range transition.Events {
		if !event.Wakeup {
			continue
		}
		trigger := TriggerRunReady
		if registered, ok := scheduler.selfTrigger(transition.Kind, event); ok {
			trigger = registered
		}
		result = errors.Join(result, scheduler.schedule(ctx, Payload{
			SchemaVersion: SchemaVersion, RunID: transition.RunID,
			ExpectedRevision: transition.Revision, Trigger: trigger,
			SourceRunID: transition.RunID, SourceRevision: transition.Revision,
			WaitGeneration: event.WakeupGeneration,
		}, event.WakeupAt))
	}
	return result
}

// Schedule creates or reuses one immutable continuation Job.
func (scheduler *Scheduler) Schedule(ctx context.Context, payload Payload) error {
	return scheduler.schedule(ctx, payload, nil)
}

func (scheduler *Scheduler) schedule(ctx context.Context, payload Payload, wakeupAt *time.Time) error {
	if scheduler == nil || scheduler.queue == nil {
		return ErrInvalidInput
	}
	normalized, err := normalizePayload(payload)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return errors.Join(ErrInvalidInput, err)
	}
	availableAt := scheduler.continuationAvailableAt(normalized.Trigger)
	if wakeupAt != nil && wakeupAt.After(availableAt) {
		availableAt = wakeupAt.UTC()
	}
	_, err = scheduler.queue.Enqueue(ctx, queuecore.EnqueueRequest{
		Queue: QueueName, ClientJobID: clientJobID(normalized), Kind: JobKind,
		Payload: encoded, Priority: triggerPriority(normalized.Trigger),
		AvailableAt: availableAt, Policy: scheduler.jobPolicy,
	})
	return err
}

func (scheduler *Scheduler) scheduleOwningParent(ctx context.Context, transition kernel.CommittedTransition) error {
	relation, err := scheduler.relations.GetByChild(ctx, transition.RunID)
	if errors.Is(err, runrelation.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	child, err := scheduler.runs.Load(ctx, transition.RunID)
	if err != nil {
		return err
	}
	return scheduler.scheduleRelation(ctx, relation, child)
}

func sameRelationTenant(parent, child kernel.Snapshot) bool {
	return parent.Run.Actor.TenantID != "" && parent.Run.Actor.TenantID == child.Run.Actor.TenantID
}

func (scheduler *Scheduler) scheduleRelation(
	ctx context.Context,
	relation runrelation.Relation,
	child kernel.Snapshot,
) error {
	parent, err := scheduler.runs.Load(ctx, relation.ParentRunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil || terminal(parent.Run.Status) {
		return err
	}
	// A persisted relation is an ownership hint, not a grant to cross a
	// tenant boundary. Keep invalid work retryable for host diagnostics.
	if child.Run.ID != relation.ChildRunID || !sameRelationTenant(parent, child) {
		return fmt.Errorf("%w: cross-tenant run relation", ErrInvalidInput)
	}
	return scheduler.Schedule(ctx, Payload{
		SchemaVersion: SchemaVersion, RunID: parent.Run.ID,
		ExpectedRevision: parent.Run.Revision, Trigger: TriggerChildTerminal,
		SourceRunID: child.Run.ID, SourceRevision: child.Run.Revision,
	})
}

func (scheduler *Scheduler) selfTrigger(kind kernel.RunKind, event kernel.EventDraft) (Trigger, bool) {
	resolver, ok := scheduler.triggers[kind]
	if !ok {
		return "", false
	}
	return resolver(event)
}

func clientJobID(payload Payload) string {
	if payload.Trigger == TriggerChildTerminal {
		return fmt.Sprintf("%s:%s:%s:%d:%d", payload.Trigger, payload.RunID, payload.SourceRunID, payload.SourceRevision, payload.ExpectedRevision)
	}
	return fmt.Sprintf("%s:%s:%s:%d", payload.Trigger, payload.RunID, payload.SourceRunID, payload.SourceRevision)
}

func triggerPriority(trigger Trigger) int {
	switch trigger {
	case TriggerChildTerminal:
		return 20
	case TriggerApprovalResolved, TriggerModelReady, TriggerWaitResolved, TriggerToolWaitDeadline, TriggerToolResultReady:
		return 10
	case TriggerSegmentYielded, TriggerRunReady:
		return 0
	default:
		return 0
	}
}

func (scheduler *Scheduler) continuationAvailableAt(trigger Trigger) time.Time {
	if trigger == TriggerChildTerminal {
		// A child may finish inside its parent's synchronous call stack. A short grace
		// window lets that stack commit first; the frozen parent revision then makes
		// the queued delivery a harmless stale no-op.
		return scheduler.clock.Now().UTC().Add(250 * time.Millisecond)
	}
	return time.Time{}
}

func projectionBackoff(attempt uint32) time.Duration {
	backoff := 250 * time.Millisecond
	for index := uint32(1); index < attempt && backoff < 30*time.Second; index++ {
		backoff *= 2
	}
	if backoff > 30*time.Second {
		return 30 * time.Second
	}
	return backoff
}

type schedulerClock struct{}

func (schedulerClock) Now() time.Time { return time.Now() }
