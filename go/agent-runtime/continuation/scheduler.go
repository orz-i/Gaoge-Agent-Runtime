package continuation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// ErrorReporter receives non-transactional scheduling failures for host observability.
type ErrorReporter func(error)

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
}

// SchedulerDependencies explicitly provide transition projection dependencies.
type SchedulerDependencies struct {
	Outbox    kernel.TransitionOutbox
	Queue     Enqueuer
	Relations interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
		ListAll(context.Context) ([]runrelation.Relation, error)
	}
	Runs        SnapshotLoader
	Clock       kernel.Clock
	ProjectorID string
}

// Reconcile reprojects terminal children from durable relations. Existing queue
// identity conflicts mean that source revision was already delivered or dead-lettered.
func (scheduler *Scheduler) Reconcile(ctx context.Context) error {
	if scheduler == nil {
		return ErrInvalidInput
	}
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
		result = errors.Join(result, scheduler.scheduleRelation(ctx, relation, child.Run.ID, child.Run.Revision))
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
	return &Scheduler{
		outbox: dependencies.Outbox, queue: dependencies.Queue, relations: dependencies.Relations, runs: dependencies.Runs,
		clock: dependencies.Clock, workerID: strings.TrimSpace(dependencies.ProjectorID), triggers: triggers,
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
		trigger, ok := scheduler.selfTrigger(transition.Kind, event)
		if !ok {
			continue
		}
		result = errors.Join(result, scheduler.schedule(ctx, Payload{
			SchemaVersion: SchemaVersion, RunID: transition.RunID,
			ExpectedRevision: transition.Revision, Trigger: trigger,
			SourceRunID: transition.RunID, SourceRevision: transition.Revision,
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

func (scheduler *Scheduler) scheduleOwningParent(ctx context.Context, child kernel.CommittedTransition) error {
	relation, err := scheduler.relations.GetByChild(ctx, child.RunID)
	if errors.Is(err, runrelation.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return scheduler.scheduleRelation(ctx, relation, child.RunID, child.Revision)
}

func (scheduler *Scheduler) scheduleRelation(
	ctx context.Context,
	relation runrelation.Relation,
	childRunID string,
	childRevision uint64,
) error {
	parent, err := scheduler.runs.Load(ctx, relation.ParentRunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil || terminal(parent.Run.Status) {
		return err
	}
	return scheduler.Schedule(ctx, Payload{
		SchemaVersion: SchemaVersion, RunID: parent.Run.ID,
		ExpectedRevision: parent.Run.Revision, Trigger: TriggerChildTerminal,
		SourceRunID: childRunID, SourceRevision: childRevision,
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
	return fmt.Sprintf("%s:%s:%s:%d", payload.Trigger, payload.RunID, payload.SourceRunID, payload.SourceRevision)
}

func triggerPriority(trigger Trigger) int {
	switch trigger {
	case TriggerChildTerminal:
		return 20
	case TriggerApprovalResolved, TriggerModelReady, TriggerWaitResolved:
		return 10
	case TriggerSegmentYielded:
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
