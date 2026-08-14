package continuation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge/sdk/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

// WorkerOptions bound polling, lease consumption and error reporting.
type WorkerOptions struct {
	WorkerID          string
	PollInterval      time.Duration
	ClaimLimit        int
	Reconciler        Reconciler
	ReconcileInterval time.Duration
	Report            ErrorReporter
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
	deliveries, err := worker.queue.Claim(ctx, queuecore.ClaimRequest{
		Queue: QueueName, WorkerID: worker.options.WorkerID, Limit: worker.options.ClaimLimit,
	})
	if err != nil {
		worker.report(err)
		return
	}
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}
		worker.consumeDelivery(ctx, delivery)
	}
}

func (worker *Worker) consumeDelivery(ctx context.Context, delivery queuecore.Delivery) {
	payload, err := decodePayload(delivery.Job.Payload)
	if err == nil && delivery.Job.Kind != JobKind {
		err = ErrInvalidInput
	}
	if err == nil {
		err = worker.handler.Dispatch(ctx, payload)
	}
	request := queuecore.LeaseRequest{
		JobID: delivery.Job.ID, LeaseID: delivery.Lease.ID, WorkerID: worker.options.WorkerID,
	}
	if err == nil {
		_, err = worker.queue.Ack(ctx, request)
	} else {
		_, nackErr := worker.queue.Nack(ctx, queuecore.NackRequest{
			LeaseRequest: request, ErrorCode: "continuation.dispatch_failed", Error: err.Error(),
		})
		err = errors.Join(err, nackErr)
	}
	worker.report(err)
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
