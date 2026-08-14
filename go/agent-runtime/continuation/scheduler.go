package continuation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge/sdk/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

// ErrorReporter receives non-transactional scheduling failures for host observability.
type ErrorReporter func(error)

// Scheduler converts committed Kernel transitions into idempotent delivery Jobs.
type Scheduler struct {
	queue     Enqueuer
	relations interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
		ListAll(context.Context) ([]runrelation.Relation, error)
	}
	runs      SnapshotLoader
	report    ErrorReporter
	jobPolicy queuecore.Policy
	triggers  map[kernel.RunKind]SelfTriggerResolver
}

// SchedulerDependencies explicitly provide transition projection dependencies.
type SchedulerDependencies struct {
	Queue     Enqueuer
	Relations interface {
		GetByChild(context.Context, string) (runrelation.Relation, error)
		ListAll(context.Context) ([]runrelation.Relation, error)
	}
	Runs   SnapshotLoader
	Report ErrorReporter
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
		scheduleErr := scheduler.scheduleRelation(ctx, relation, child)
		if !errors.Is(scheduleErr, queuecore.ErrConflict) {
			result = errors.Join(result, scheduleErr)
		}
	}
	return result
}

// NewScheduler creates a committed-transition observer.
func NewScheduler(dependencies SchedulerDependencies, registrations ...TriggerRegistration) (*Scheduler, error) {
	if dependencies.Queue == nil || dependencies.Relations == nil || dependencies.Runs == nil {
		return nil, ErrInvalidInput
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
		queue: dependencies.Queue, relations: dependencies.Relations, runs: dependencies.Runs,
		report: dependencies.Report, triggers: triggers,
		jobPolicy: queuecore.Policy{
			MaxAttempts: 8, VisibilityTimeout: 2 * time.Minute,
			InitialBackoff: 250 * time.Millisecond, MaxBackoff: 30 * time.Second, BackoffMultiplier: 2,
		},
	}, nil
}

// ObserveTransition implements kernel.TransitionSink. Scheduling cannot roll back
// the transition; failures are reported and callers may reconcile from durable facts.
func (scheduler *Scheduler) ObserveTransition(ctx context.Context, transition kernel.Transition) {
	if scheduler == nil {
		return
	}
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if terminal(transition.Current.Run.Status) {
		scheduler.reportError(scheduler.scheduleOwningParent(deliveryCtx, transition.Current))
	}
	for _, event := range transition.Events {
		trigger, ok := scheduler.selfTrigger(transition.Current.Run.Kind, event)
		if !ok {
			continue
		}
		payload := Payload{
			SchemaVersion: SchemaVersion, RunID: transition.Current.Run.ID,
			ExpectedRevision: transition.Current.Run.Revision, Trigger: trigger,
			SourceRunID: transition.Current.Run.ID, SourceRevision: transition.Current.Run.Revision,
		}
		scheduler.reportError(scheduler.Schedule(deliveryCtx, payload))
	}
}

// Schedule creates or reuses one immutable continuation Job.
func (scheduler *Scheduler) Schedule(ctx context.Context, payload Payload) error {
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
	_, err = scheduler.queue.Enqueue(ctx, queuecore.EnqueueRequest{
		Queue: QueueName, ClientJobID: clientJobID(normalized), Kind: JobKind,
		Payload: encoded, Priority: triggerPriority(normalized.Trigger),
		AvailableAt: continuationAvailableAt(normalized.Trigger), Policy: scheduler.jobPolicy,
	})
	return err
}

func (scheduler *Scheduler) scheduleOwningParent(ctx context.Context, child kernel.Snapshot) error {
	relation, err := scheduler.relations.GetByChild(ctx, child.Run.ID)
	if errors.Is(err, runrelation.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return scheduler.scheduleRelation(ctx, relation, child)
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
	return scheduler.Schedule(ctx, Payload{
		SchemaVersion: SchemaVersion, RunID: parent.Run.ID,
		ExpectedRevision: parent.Run.Revision, Trigger: TriggerChildTerminal,
		SourceRunID: child.Run.ID, SourceRevision: child.Run.Revision,
	})
}

func (scheduler *Scheduler) reportError(err error) {
	if err != nil && scheduler.report != nil {
		scheduler.report(err)
	}
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
	case TriggerApprovalResolved, TriggerWaitResolved:
		return 10
	default:
		return 0
	}
}

func continuationAvailableAt(trigger Trigger) time.Time {
	if trigger == TriggerChildTerminal {
		// A child may finish inside its parent's synchronous call stack. A short grace
		// window lets that stack commit first; the frozen parent revision then makes
		// the queued delivery a harmless stale no-op.
		return time.Now().UTC().Add(250 * time.Millisecond)
	}
	return time.Time{}
}
