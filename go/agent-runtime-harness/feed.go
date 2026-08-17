package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

const (
	EventTurnStarted      = "turn.started"
	EventTurnWaitingInput = "turn.waiting_input"
	EventTurnCompleted    = "turn.completed"
	EventTurnFailed       = "turn.failed"
	EventTurnCancelled    = "turn.cancelled"
	EventItemStarted      = "item.started"
	EventItemDelta        = "item.delta"
	EventItemCompleted    = "item.completed"
)

// TurnEvent is the product-facing live projection of one Harness Turn. Durable
// Items remain authoritative; delta events are short-lived previews only.
type TurnEvent struct {
	Seq       int64           `json:"seq"`
	TurnID    string          `json:"turnID"`
	Type      string          `json:"type"`
	ItemID    string          `json:"itemID,omitempty"`
	ItemKind  ItemKind        `json:"itemKind,omitempty"`
	Delta     string          `json:"delta,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Status    string          `json:"status,omitempty"`
	Terminal  bool            `json:"terminal,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type TurnEventDraft struct {
	Type     string
	ItemID   string
	ItemKind ItemKind
	Delta    string
	Message  string
	Data     json.RawMessage
	Status   string
	Terminal bool
}

type turnFeedEnvelope struct {
	ItemID   string          `json:"itemID,omitempty"`
	ItemKind ItemKind        `json:"itemKind,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// TurnFeed reuses the Runtime replay transport while replacing root Run
// identity with the stable Harness Turn identity and semantic Item events.
type TurnFeed struct{ feed *runfeed.Feed }

func NewTurnFeed(feed *runfeed.Feed) (*TurnFeed, error) {
	if feed == nil {
		return nil, ErrInvalidRequest
	}
	return &TurnFeed{feed: feed}, nil
}

func (feed *TurnFeed) Publish(ctx context.Context, turnID string, draft TurnEventDraft) (TurnEvent, error) {
	turnID = strings.TrimSpace(turnID)
	draft.Type = strings.TrimSpace(draft.Type)
	draft.ItemID = strings.TrimSpace(draft.ItemID)
	draft.Status = strings.TrimSpace(draft.Status)
	if feed == nil || feed.feed == nil || turnID == "" || !validTurnEventDraft(draft) {
		return TurnEvent{}, ErrInvalidRequest
	}
	envelope, err := json.Marshal(turnFeedEnvelope{
		ItemID: draft.ItemID, ItemKind: draft.ItemKind, Data: append(json.RawMessage(nil), draft.Data...),
	})
	if err != nil {
		return TurnEvent{}, err
	}
	event, err := feed.feed.Publish(ctx, turnID, runfeed.Draft{
		Type: draft.Type, Delta: draft.Delta, Message: draft.Message, Data: envelope,
		Status: draft.Status, Terminal: draft.Terminal,
	})
	if err != nil {
		return TurnEvent{}, err
	}
	return turnEventFromRunFeed(event)
}

func (feed *TurnFeed) Replay(ctx context.Context, turnID string, afterSeq int64) ([]TurnEvent, error) {
	if feed == nil || feed.feed == nil {
		return nil, ErrInvalidRequest
	}
	events, err := feed.feed.Replay(ctx, strings.TrimSpace(turnID), afterSeq)
	if err != nil {
		return nil, err
	}
	return turnEventsFromRunFeed(events)
}

type TurnSubscription struct {
	Replay []TurnEvent
	Events <-chan TurnEvent
	Errors <-chan error
	inner  *runfeed.Subscription
}

func (subscription *TurnSubscription) Close() {
	if subscription != nil && subscription.inner != nil {
		subscription.inner.Close()
	}
}

func (feed *TurnFeed) Subscribe(ctx context.Context, turnID string, afterSeq int64) (*TurnSubscription, error) {
	if feed == nil || feed.feed == nil {
		return nil, ErrInvalidRequest
	}
	inner, err := feed.feed.Subscribe(ctx, strings.TrimSpace(turnID), afterSeq)
	if err != nil {
		return nil, err
	}
	replay, err := turnEventsFromRunFeed(inner.Replay)
	if err != nil {
		inner.Close()
		return nil, err
	}
	events := make(chan TurnEvent, 32)
	errorsChannel := make(chan error, 1)
	go translateTurnSubscription(ctx, inner, events, errorsChannel)
	return &TurnSubscription{Replay: replay, Events: events, Errors: errorsChannel, inner: inner}, nil
}

func translateTurnSubscription(
	ctx context.Context,
	inner *runfeed.Subscription,
	events chan<- TurnEvent,
	errorsChannel chan<- error,
) {
	defer close(events)
	defer close(errorsChannel)
	for {
		select {
		case event, open := <-inner.Events:
			if !open {
				return
			}
			projected, err := turnEventFromRunFeed(event)
			if err != nil {
				sendTurnSubscriptionError(errorsChannel, err)
				return
			}
			if !sendTurnSubscriptionEvent(ctx, events, projected) {
				return
			}
		case err, open := <-inner.Errors:
			if open {
				sendTurnSubscriptionError(errorsChannel, err)
			}
			return
		case <-ctx.Done():
			return
		}
	}
}

func sendTurnSubscriptionEvent(ctx context.Context, events chan<- TurnEvent, event TurnEvent) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func sendTurnSubscriptionError(errorsChannel chan<- error, err error) {
	if err == nil {
		return
	}
	select {
	case errorsChannel <- err:
	default:
	}
}

func turnEventsFromRunFeed(events []runfeed.Event) ([]TurnEvent, error) {
	result := make([]TurnEvent, 0, len(events))
	for _, event := range events {
		projected, err := turnEventFromRunFeed(event)
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	return result, nil
}

func turnEventFromRunFeed(event runfeed.Event) (TurnEvent, error) {
	var envelope turnFeedEnvelope
	if len(event.Data) > 0 {
		if err := json.Unmarshal(event.Data, &envelope); err != nil {
			return TurnEvent{}, errors.Join(ErrConflict, err)
		}
	}
	return TurnEvent{
		Seq: event.Seq, TurnID: event.RunID, Type: event.Type,
		ItemID: envelope.ItemID, ItemKind: envelope.ItemKind,
		Delta: event.Delta, Message: event.Message, Data: append(json.RawMessage(nil), envelope.Data...),
		Status: event.Status, Terminal: event.Terminal, CreatedAt: event.CreatedAt,
	}, nil
}

func validTurnEventDraft(draft TurnEventDraft) bool {
	switch draft.Type {
	case EventTurnStarted, EventTurnWaitingInput, EventTurnCompleted, EventTurnFailed, EventTurnCancelled:
		return draft.ItemID == "" && draft.ItemKind == ""
	case EventItemStarted, EventItemDelta, EventItemCompleted:
		return draft.ItemID != "" && validItemKind(draft.ItemKind)
	default:
		return false
	}
}

func turnEventTypeForItem(item Item) string {
	switch item.Status {
	case ItemCompleted, ItemFailed, ItemCancelled:
		return EventItemCompleted
	default:
		return EventItemStarted
	}
}

func itemLifecycleID(item Item) string {
	if parent := strings.TrimSpace(item.ParentItemID); parent != "" {
		return parent
	}
	return strings.TrimSpace(item.ID)
}

func appendItemFact(
	ctx context.Context,
	store Store,
	feed *TurnFeed,
	item Item,
) (Item, error) {
	createdItem, created, err := store.AppendItem(ctx, item)
	if err != nil || !created || feed == nil {
		return createdItem, err
	}
	raw, marshalErr := json.Marshal(createdItem)
	if marshalErr == nil {
		_, _ = feed.Publish(ctx, createdItem.TurnID, TurnEventDraft{
			Type: turnEventTypeForItem(createdItem), ItemID: itemLifecycleID(createdItem), ItemKind: createdItem.Kind,
			Data: raw, Status: string(createdItem.Status),
		})
	}
	return createdItem, nil
}
