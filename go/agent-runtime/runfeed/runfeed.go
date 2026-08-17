// Package runfeed provides the replayable, non-authoritative live event stream for Runtime Runs.
package runfeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const (
	CapabilityFeed kernel.Capability = "runfeed.feed"

	EventRunStarted          = "run.started"
	EventRunWaitingInput     = "run.waiting_input"
	EventRunCompleted        = "run.completed"
	EventRunFailed           = "run.failed"
	EventRunCancelled        = "run.cancelled"
	EventModelStarted        = "model.started"
	EventModelDelta          = "model.delta"
	EventModelCompleted      = "model.completed"
	EventToolRequested       = "tool.requested"
	EventToolStarted         = "tool.started"
	EventToolCompleted       = "tool.completed"
	EventInteractionRequired = "interaction.required"
)

var (
	ErrInvalidInput   = errors.New("invalid run feed input")
	ErrCursorExpired  = errors.New("run feed cursor expired")
)

// CursorExpiredError reports that retained events no longer cover the
// requested cursor. HeadSeq is the current monotonic feed high-water mark;
// callers must restore durable state before continuing from that cursor.
type CursorExpiredError struct {
	AfterSeq int64
	HeadSeq  int64
}

func (err *CursorExpiredError) Error() string {
	if err == nil {
		return ErrCursorExpired.Error()
	}
	return fmt.Sprintf("%s: after=%d head=%d", ErrCursorExpired, err.AfterSeq, err.HeadSeq)
}

func (err *CursorExpiredError) Unwrap() error { return ErrCursorExpired }

// Draft is one provider-neutral live event before the Store assigns its sequence.
type Draft struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Message  string          `json:"message,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Revision uint64          `json:"revision,omitempty"`
	Status   string          `json:"status,omitempty"`
	Terminal bool            `json:"terminal,omitempty"`
}

// Event is one immutable, ordered Run Feed fact.
type Event struct {
	Seq       int64           `json:"seq"`
	RunID     string          `json:"runID"`
	Type      string          `json:"type"`
	Delta     string          `json:"delta,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Revision  uint64          `json:"revision,omitempty"`
	Status    string          `json:"status,omitempty"`
	Terminal  bool            `json:"terminal,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Store persists ordered feed events independently from Kernel CAS state.
type Store interface {
	Append(context.Context, string, Draft, time.Time, time.Duration) (Event, error)
	List(context.Context, string, int64, int) ([]Event, error)
}

// Publisher is the narrow write capability consumed by Runtime features.
type Publisher interface {
	Publish(context.Context, string, Draft) (Event, error)
}

// Options configure retention and subscription polling without changing event semantics.
type Options struct {
	Retention    time.Duration
	PollInterval time.Duration
	BatchSize    int
	BufferSize   int
	Clock        kernel.Clock
}

// Feed composes one Store into replay and live subscription capabilities.
type Feed struct {
	store        Store
	retention    time.Duration
	pollInterval time.Duration
	batchSize    int
	bufferSize   int
	clock        kernel.Clock
}

// New constructs a Run Feed over one persistence adapter.
func New(store Store, options Options) (*Feed, error) {
	if store == nil {
		return nil, ErrInvalidInput
	}
	if options.Retention <= 0 {
		options.Retention = 15 * time.Minute
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.BatchSize <= 0 {
		options.BatchSize = 128
	}
	if options.BufferSize <= 0 {
		options.BufferSize = 32
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Feed{
		store: store, retention: options.Retention, pollInterval: options.PollInterval,
		batchSize: options.BatchSize, bufferSize: options.BufferSize, clock: options.Clock,
	}, nil
}

// Descriptor declares the replayable Run Feed capability.
func (feed *Feed) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "runfeed", Provides: []kernel.Capability{CapabilityFeed}}
}

// Publish appends one best-effort live fact without mutating Kernel state.
func (feed *Feed) Publish(ctx context.Context, runID string, draft Draft) (Event, error) {
	runID = strings.TrimSpace(runID)
	draft = normalizeDraft(draft)
	if feed == nil || feed.store == nil || runID == "" || !validDraft(draft) {
		return Event{}, ErrInvalidInput
	}
	return feed.store.Append(ctx, runID, draft, feed.clock.Now().UTC(), feed.retention)
}

// Replay returns retained events strictly after afterSeq.
func (feed *Feed) Replay(ctx context.Context, runID string, afterSeq int64) ([]Event, error) {
	runID = strings.TrimSpace(runID)
	if feed == nil || feed.store == nil || runID == "" || afterSeq < 0 {
		return nil, ErrInvalidInput
	}
	return feed.store.List(ctx, runID, afterSeq, feed.batchSize)
}

// Subscription exposes an initial replay followed by a lossless polling stream.
type Subscription struct {
	Replay []Event
	Events <-chan Event
	Errors <-chan error
	stop   chan struct{}
	once   sync.Once
}

// Close stops only this subscriber; it never cancels the underlying Run.
func (subscription *Subscription) Close() {
	if subscription == nil || subscription.stop == nil {
		return
	}
	subscription.once.Do(func() { close(subscription.stop) })
}

// Subscribe returns retained events and follows newly appended events until terminal or cancellation.
func (feed *Feed) Subscribe(ctx context.Context, runID string, afterSeq int64) (*Subscription, error) {
	replay, err := feed.Replay(ctx, runID, afterSeq)
	if err != nil {
		return nil, err
	}
	events := make(chan Event, feed.bufferSize)
	errorsChannel := make(chan error, 1)
	stop := make(chan struct{})
	subscription := &Subscription{Replay: cloneEvents(replay), Events: events, Errors: errorsChannel, stop: stop}
	if len(replay) > 0 {
		afterSeq = replay[len(replay)-1].Seq
		if replay[len(replay)-1].Terminal {
			close(events)
			close(errorsChannel)
			return subscription, nil
		}
	}
	go feed.follow(ctx, stop, strings.TrimSpace(runID), afterSeq, events, errorsChannel)
	return subscription, nil
}

func (feed *Feed) follow(
	ctx context.Context,
	stop <-chan struct{},
	runID string,
	afterSeq int64,
	events chan<- Event,
	errorsChannel chan<- error,
) {
	defer close(events)
	defer close(errorsChannel)
	ticker := time.NewTicker(feed.pollInterval)
	defer ticker.Stop()
	for {
		nextSeq, terminal, stopped, err := feed.deliverAvailable(ctx, stop, runID, afterSeq, events)
		if err != nil {
			reportSubscriptionError(ctx, stop, errorsChannel, err)
			return
		}
		afterSeq = nextSeq
		if terminal || stopped || !waitForFeedPoll(ctx, stop, ticker.C) {
			return
		}
	}
}

func (feed *Feed) deliverAvailable(
	ctx context.Context,
	stop <-chan struct{},
	runID string,
	afterSeq int64,
	events chan<- Event,
) (int64, bool, bool, error) {
	items, err := feed.store.List(ctx, runID, afterSeq, feed.batchSize)
	if err != nil {
		return afterSeq, false, false, err
	}
	for _, event := range items {
		if !sendFeedEvent(ctx, stop, events, event) {
			return afterSeq, false, true, nil
		}
		afterSeq = event.Seq
		if event.Terminal {
			return afterSeq, true, false, nil
		}
	}
	return afterSeq, false, false, nil
}

func sendFeedEvent(ctx context.Context, stop <-chan struct{}, events chan<- Event, event Event) bool {
	select {
	case events <- cloneEvent(event):
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

func reportSubscriptionError(ctx context.Context, stop <-chan struct{}, errorsChannel chan<- error, err error) {
	select {
	case errorsChannel <- err:
	case <-ctx.Done():
	case <-stop:
	}
}

func waitForFeedPoll(ctx context.Context, stop <-chan struct{}, tick <-chan time.Time) bool {
	select {
	case <-tick:
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

func normalizeDraft(draft Draft) Draft {
	draft.Type = strings.TrimSpace(draft.Type)
	draft.Message = strings.TrimSpace(draft.Message)
	draft.Status = strings.TrimSpace(draft.Status)
	draft.Data = append(json.RawMessage(nil), draft.Data...)
	return draft
}

func validDraft(draft Draft) bool {
	return draft.Type != "" && (len(draft.Data) == 0 || json.Valid(draft.Data))
}

func cloneEvents(events []Event) []Event {
	result := make([]Event, len(events))
	for index, event := range events {
		result[index] = cloneEvent(event)
	}
	return result
}

func cloneEvent(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
