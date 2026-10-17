package continuation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// WorkerOptions bound polling, lease consumption and error reporting.
type WorkerOptions struct {
	WorkerID     string
	PollInterval time.Duration
	// ClaimLimit bounds the number of just-in-time deliveries per poll. A
	// worker never pre-leases jobs that are waiting behind a slow handler.
	ClaimLimit        int
	Projector         Projector
	Reconciler        Reconciler
	ReconcileInterval time.Duration
	Report            ErrorReporter
	// TerminalQuarantine is supplied ONLY by a trusted host. It must
	// authenticate the work and atomically block the current Run/Generation
	// in the authoritative Store before returning nil. A protected Queue
	// Job cannot be sent to Dead Letter if this callback is absent/fails.
	TerminalQuarantine func(context.Context, Payload, error) error
}

// Worker owns the optional continuation background lifecycle.
type Worker struct {
	queue   DeliveryQueue
	handler Handler
	options WorkerOptions

	mu      sync.Mutex
	started bool
	closed  bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// NewWorker constructs a leased continuation consumer.
func NewWorker(queue DeliveryQueue, handler Handler, options WorkerOptions) (*Worker, error) {
	if queue == nil || handler == nil {
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(options.WorkerID) == "" {
		options.WorkerID = randomWorkerID()
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.ClaimLimit <= 0 || options.ClaimLimit > 100 {
		options.ClaimLimit = 8
	}
	if options.ReconcileInterval <= 0 {
		options.ReconcileInterval = 5 * time.Second
	}
	return &Worker{queue: queue, handler: handler, options: options}, nil
}

// Descriptor declares the explicitly composed background recovery graph.
func (worker *Worker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "continuation", Requires: []kernel.Capability{
			kernel.CapabilityRuntime, queuecore.CapabilityQueue, runrelation.CapabilityRelations,
		},
		Provides: []kernel.Capability{CapabilityDispatcher},
	}
}

// Start begins bounded polling until the host context or Close cancels it.
func (worker *Worker) Start(ctx context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.closed {
		return ErrClosed
	}
	if worker.started {
		return ErrAlreadyStarted
	}
	workerCtx, cancel := context.WithCancel(ctx)
	worker.cancel = cancel
	worker.done = make(chan struct{})
	worker.started = true
	go worker.loop(workerCtx, worker.done)
	return nil
}

// Close stops polling and waits for the active delivery batch.
func (worker *Worker) Close(ctx context.Context) error {
	worker.mu.Lock()
	if worker.closed {
		worker.mu.Unlock()
		return nil
	}
	worker.closed = true
	cancel := worker.cancel
	done := worker.done
	worker.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *Worker) loop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(worker.options.PollInterval)
	defer ticker.Stop()
	nextReconcile := time.Time{}
	for {
		if worker.options.Projector != nil {
			worker.report(worker.options.Projector.Project(ctx))
		}
		if worker.options.Reconciler != nil && !time.Now().Before(nextReconcile) {
			worker.report(worker.options.Reconciler.Reconcile(ctx))
			nextReconcile = time.Now().Add(worker.options.ReconcileInterval)
		}
		worker.consume(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (worker *Worker) consume(ctx context.Context) {
	for processed := 0; processed < worker.options.ClaimLimit && ctx.Err() == nil; processed++ {
		// Dispatch is synchronous: leasing a whole batch up front would allow
		// later jobs to expire before their handlers even begin.
		deliveries, err := worker.queue.Claim(ctx, queuecore.ClaimRequest{
			Queue: QueueName, WorkerID: worker.options.WorkerID, Limit: 1,
		})
		if err != nil {
			worker.report(err)
			return
		}
		if len(deliveries) == 0 {
			return
		}
		for _, delivery := range deliveries {
			if ctx.Err() != nil {
				return
			}
			worker.consumeDelivery(ctx, delivery)
		}
	}
}

// leaseRenewer is optional for host-defined queues, preserving the existing
// DeliveryQueue contract. Both built-in Memory and Redis queues implement it.
// A host queue without Renew must bound its handler below visibility timeout.
type leaseRenewer interface {
	Renew(context.Context, queuecore.LeaseRequest) (queuecore.Delivery, error)
}

func (worker *Worker) consumeDelivery(ctx context.Context, delivery queuecore.Delivery) {
	request := queuecore.LeaseRequest{
		JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: worker.options.WorkerID,
	}
	dispatchCtx, cancel := context.WithCancel(ctx)
	var leaseDone <-chan error
	if renewer, ok := worker.queue.(leaseRenewer); ok {
		done := make(chan error, 1)
		leaseDone = done
		go func() {
			done <- keepDeliveryLease(dispatchCtx, cancel, renewer, request, delivery.Job.Policy.VisibilityTimeout)
		}()
	}
	payload, err := decodePayload(delivery.Job.Payload)
	if err == nil && delivery.Job.Kind != JobKind {
		err = ErrInvalidInput
	}
	if err == nil {
		err = worker.handler.Dispatch(dispatchCtx, payload)
	}
	terminalFenced := false
	if err != nil {
		// A final Queue Nack is NOT authority to DLQ a Run. In particular,
		// Redis independently reclaims crashed/expired leases: those must
		// remain retryable until a PG Run quarantine has durably committed.
		if delivery.Job.Policy.RequireTerminalFence &&
			delivery.Job.Attempt >= delivery.Job.Policy.MaxAttempts {
			if payloadErr := validateTerminalQuarantinePayload(delivery.Job, payload); payloadErr != nil {
				err = errors.Join(err, payloadErr, ErrTerminalFenceRequired)
			} else if worker.options.TerminalQuarantine == nil {
				err = errors.Join(err, ErrTerminalFenceRequired)
			} else if fenceErr := worker.options.TerminalQuarantine(dispatchCtx, payload, err); fenceErr != nil {
				err = errors.Join(err, ErrTerminalFenceRequired, fenceErr)
			} else {
				terminalFenced = true
			}
		}
	}
	// Keep the Queue lease renewed THROUGH the cross-Store quarantine
	// commit. Cancelling renewal before a slow PG write opens an avoidable
	// expiry/reclaim window in which another Worker can observe the Run.
	cancel()
	if leaseDone != nil {
		if leaseErr := <-leaseDone; leaseErr != nil {
			// The handler may return context.Canceled after losing its lease;
			// report the actual renewal failure rather than hiding it.
			if worker.options.Report != nil {
				worker.options.Report(leaseErr)
			}
			err = errors.Join(err, leaseErr)
		}
	}
	if ctx.Err() != nil {
		// Shutdown leaves the lease for durable expiry recovery rather than
		// attempting an Ack/Nack using a cancelled worker context.
		worker.report(err)
		return
	}
	if err == nil {
		_, err = worker.queue.Ack(ctx, request)
	} else {
		_, nackErr := worker.queue.Nack(ctx, queuecore.NackRequest{
			LeaseRequest: request, ErrorCode: "continuation.dispatch_failed", Error: err.Error(),
			AllowDeadLetter: terminalFenced,
		})
		err = errors.Join(err, nackErr)
	}
	worker.report(err)
}

// Invalid payloads cannot identify an authoritative Run and are orphan
// diagnostics. Never grant final-DLQ permission for such work.
func validateTerminalQuarantinePayload(job queuecore.Job, payload Payload) error {
	if job.Kind != JobKind || payload.RunID == "" || payload.ExpectedRevision == 0 ||
		payload.SchemaVersion != SchemaVersion {
		return ErrInvalidInput
	}
	return nil
}

func keepDeliveryLease(
	ctx context.Context,
	cancel context.CancelFunc,
	renewer leaseRenewer,
	request queuecore.LeaseRequest,
	visibilityTimeout time.Duration,
) error {
	interval := visibilityTimeout / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := renewer.Renew(ctx, request); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// Stop cooperative execution when the lease can no longer be
				// proven current; the queue will fence stale Ack/Nack.
				cancel()
				return err
			}
		}
	}
}

func (worker *Worker) report(err error) {
	if err != nil && worker.options.Report != nil && !errors.Is(err, context.Canceled) {
		worker.options.Report(err)
	}
}

func randomWorkerID() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "continuation-worker"
	}
	return "continuation-" + hex.EncodeToString(value[:])
}
