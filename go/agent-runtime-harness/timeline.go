package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func appendHostedToolStartedItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation Invocation,
	runID string,
	state hostedToolStreamState,
	call model.HostedToolCall,
) error {
	payload, err := json.Marshal(hostedToolTimelinePayload{
		CallID: state.CallID, ToolKey: state.ToolKey, Status: "in_progress",
	})
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: state.ItemID, TurnID: turn.ID, Kind: ItemTool, Status: ItemStarted, RunID: runID, InvocationID: invocation.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

type hostedToolStreamState struct {
	ItemID    string
	CallID    string
	ToolKey   string
	InputHash string
	Closed    bool
}

type hostedToolStreamEventPayload struct {
	ID      string `json:"id,omitempty"`
	ToolKey string `json:"toolKey"`
	Status  string `json:"status,omitempty"`
}

type hostedToolStreamTracker struct {
	turnID string
	states []hostedToolStreamState
}

func newHostedToolStreamTracker(turnID string) *hostedToolStreamTracker {
	return &hostedToolStreamTracker{turnID: strings.TrimSpace(turnID)}
}

func agentMessagePreviewEvent(event model.StreamEvent) (model.StreamEvent, bool) {
	event.HostedToolCall = nil
	return event, event.Delta != "" || event.Reasoning != nil || event.Usage != nil || strings.TrimSpace(event.ResponseID) != ""
}

func (middleware *ModelTimelineClient) recordHostedToolStreamEvent(
	ctx context.Context,
	turn Turn,
	invocation Invocation,
	runID string,
	tracker *hostedToolStreamTracker,
	call model.HostedToolCall,
) error {
	if strings.TrimSpace(call.ID) == "" {
		return nil
	}
	state, created := tracker.resolveStream(call)
	if created {
		if err := appendHostedToolStartedItem(ctx, middleware.store, middleware.clock, middleware.feed, turn, invocation, runID, state, call); err != nil {
			return err
		}
	}
	if middleware.feed != nil {
		raw, err := json.Marshal(hostedToolStreamEventPayload{
			ID: strings.TrimSpace(call.ID), ToolKey: strings.TrimSpace(call.ToolKey), Status: strings.TrimSpace(call.Status),
		})
		if err != nil {
			return err
		}
		_, _ = middleware.feed.Publish(ctx, turn.ID, TurnEventDraft{
			Type: EventItemDelta, ItemID: state.ItemID, ItemKind: ItemTool,
			Data: raw, Status: string(ItemStarted),
		})
	}
	tracker.markStreamStatus(state.ItemID, call.Status)
	return nil
}

func (tracker *hostedToolStreamTracker) resolveStream(call model.HostedToolCall) (hostedToolStreamState, bool) {
	callID := strings.TrimSpace(call.ID)
	toolKey := strings.TrimSpace(call.ToolKey)
	inputHash := hashTimelineBytes(call.Input)
	if index := tracker.stateIndexByCallID(callID); index >= 0 {
		state := &tracker.states[index]
		if state.InputHash == "" && inputHash != "" {
			state.InputHash = inputHash
		}
		return *state, false
	}
	state := hostedToolStreamState{
		ItemID: stableID("hihts", tracker.turnID, toolKey, callID),
		CallID: callID, ToolKey: toolKey, InputHash: inputHash,
	}
	tracker.states = append(tracker.states, state)
	return state, true
}

func (tracker *hostedToolStreamTracker) stateIndexByCallID(callID string) int {
	if callID == "" {
		return -1
	}
	for index := range tracker.states {
		if tracker.states[index].CallID == callID {
			return index
		}
	}
	return -1
}

func (tracker *hostedToolStreamTracker) markStreamStatus(itemID, status string) {
	if !hostedToolTerminalStatus(status) {
		return
	}
	for index := range tracker.states {
		if tracker.states[index].ItemID == itemID {
			tracker.states[index].Closed = true
			return
		}
	}
}

func (tracker *hostedToolStreamTracker) finalParent(call model.HostedToolCall) string {
	callID := strings.TrimSpace(call.ID)
	if index := tracker.stateIndexByCallID(callID); index >= 0 {
		return tracker.states[index].ItemID
	}
	return ""
}

func hostedToolTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

// ModelTimelineClient projects observations at the existing provider boundary.
// Projection failures never replace a physical provider response or error.
type ModelTimelineClient struct {
	store Store
	clock Clock
	feed  *TurnFeed
	client model.Client
}

func NewModelTimelineClient(store Store, clock Clock, feed *TurnFeed, client model.Client) (*ModelTimelineClient, error) {
	if store == nil || clock == nil || client == nil {
		return nil, ErrInvalidRequest
	}
	return &ModelTimelineClient{store: store, clock: clock, feed: feed, client: client}, nil
}

func (client *ModelTimelineClient) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return client.GenerateStream(ctx, request, nil)
}

func (middleware *ModelTimelineClient) GenerateStream(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
) (model.Response, error) {
	turn, invocation, harnessRun, err := timelineTurn(ctx, middleware.store, request.RunID)
	if err != nil || !harnessRun || invocation.ParentItemID != "" {
		return middleware.generate(ctx, request, emit)
	}
	messageItemID, hasMessageItem, err := activeAgentMessageItemID(ctx, middleware.store, turn)
	if err != nil {
		return middleware.generate(ctx, request, emit)
	}
	if !hasMessageItem {
		messageItemID = stableID("him", turn.ID, request.RunID, strconv.Itoa(len(request.Messages)))
		if middleware.feed != nil {
			_, _ = middleware.feed.Publish(ctx, turn.ID, TurnEventDraft{
				Type: EventItemStarted, ItemID: messageItemID, ItemKind: ItemAgentMessage, Status: string(ItemStarted),
			})
		}
	}
	hostedTools := newHostedToolStreamTracker(turn.ID)
	stream := func(event model.StreamEvent) error {
		if event.HostedToolCall != nil {
			_ = middleware.recordHostedToolStreamEvent(ctx, turn, invocation, request.RunID, hostedTools, *event.HostedToolCall)
		}
		if middleware.feed != nil {
			preview, ok := agentMessagePreviewEvent(event)
			if ok {
				raw, marshalErr := json.Marshal(preview)
				if marshalErr == nil {
					_, _ = middleware.feed.Publish(ctx, turn.ID, TurnEventDraft{
						Type: EventItemDelta, ItemID: messageItemID, ItemKind: ItemAgentMessage,
						Delta: preview.Delta, Data: raw, Status: string(ItemStarted),
					})
				}
			}
		}
		if emit != nil {
			return emit(event)
		}
		return nil
	}
	response, callErr := middleware.generate(ctx, request, stream)
	if callErr != nil {
		return response, callErr
	}
	_ = appendModelTimelineItems(
		ctx, middleware.store, middleware.clock, middleware.feed, turn, invocation, request, response, hostedTools,
	)
	return response, nil
}

func (client *ModelTimelineClient) generate(ctx context.Context, request model.Request, emit model.StreamSink) (model.Response, error) {
	if streaming, ok := client.client.(model.StreamingClient); ok {
		return streaming.GenerateStream(ctx, request, emit)
	}
	return client.client.Generate(ctx, request)
}

type toolTimelinePayload struct {
	CallID        string `json:"callID"`
	ToolKey       string `json:"toolKey"`
	ArgumentsHash string `json:"argumentsHash"`
	ContentHash   string `json:"contentHash,omitempty"`
	ExecutionID   string `json:"executionID,omitempty"`
	Disposition   string `json:"disposition,omitempty"`
}

type modelTimelinePayload struct {
	ContentHash  string `json:"contentHash"`
	ContentBytes int    `json:"contentBytes"`
}

type artifactTimelinePayload struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType,omitempty"`
	Name      string `json:"name,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
}

type hostedToolTimelinePayload struct {
	CallID     string `json:"callID,omitempty"`
	ToolKey    string `json:"toolKey"`
	Status     string `json:"status,omitempty"`
	InputHash  string `json:"inputHash,omitempty"`
	OutputHash string `json:"outputHash,omitempty"`
	ErrorHash  string `json:"errorHash,omitempty"`
}

func timelineTurn(ctx context.Context, store Store, executionRefID string) (Turn, Invocation, bool, error) {
	invocation, err := store.GetInvocationByExecutionRefID(ctx, strings.TrimSpace(executionRefID))
	if errors.Is(err, ErrNotFound) {
		return Turn{}, Invocation{}, false, nil
	}
	if err != nil {
		return Turn{}, Invocation{}, false, err
	}
	turn, err := loadVerifiedTurn(ctx, store, invocation.TurnID)
	return turn, invocation, err == nil, err
}

func appendModelTimelineItems(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation Invocation,
	request model.Request,
	response model.Response,
	hostedTools *hostedToolStreamTracker,
) error {
	for _, artifact := range response.Artifacts {
		if err := appendArtifactTimelineItem(ctx, store, clock, feed, turn, invocation, request.RunID, artifact); err != nil {
			return err
		}
	}
	for index, call := range response.HostedToolCalls {
		if err := appendHostedToolTimelineItem(ctx, store, clock, feed, turn, invocation, request, call, hostedTools, index); err != nil {
			return err
		}
	}
	return nil
}

func activeAgentMessageItemID(ctx context.Context, store Store, turn Turn) (string, bool, error) {
	itemID, _, err := activeAgentMessageBinding(ctx, store, turn)
	return itemID, itemID != "", err
}

func activeAgentMessageBinding(ctx context.Context, store Store, turn Turn) (string, *HostRef, error) {
	items, err := listAllItems(ctx, store, turn.ID)
	if err != nil {
		return "", nil, err
	}
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if item.Kind != ItemAgentMessage || item.Status != ItemStarted || strings.TrimSpace(item.ParentItemID) != "" {
			continue
		}
		var hostRef *HostRef
		if item.HostRef != nil {
			value := *item.HostRef
			hostRef = &value
		}
		return item.ID, hostRef, nil
	}
	return "", nil, nil
}

func appendArtifactTimelineItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation Invocation,
	runID string,
	artifact model.ArtifactRef,
) error {
	payload, err := json.Marshal(artifactTimelinePayload{
		ID: artifact.ID, Kind: artifact.Kind, MediaType: artifact.MediaType, Name: artifact.Name, SizeBytes: artifact.SizeBytes,
	})
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: stableID("hiaf", turn.ID, artifact.ID), TurnID: turn.ID,
		Kind: ItemArtifact, Status: ItemCompleted, RunID: runID, InvocationID: invocation.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func appendHostedToolTimelineItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation Invocation,
	request model.Request,
	call model.HostedToolCall,
	tracker *hostedToolStreamTracker,
	ordinal int,
) error {
	status := hostedToolItemStatus(call.Status)
	value := hostedToolTimelinePayload{
		CallID: call.ID, ToolKey: call.ToolKey, Status: call.Status,
		InputHash: hashTimelineBytes(call.Input), OutputHash: hashTimelineBytes(call.Output), ErrorHash: hashTimelineBytes(call.Error),
	}
	if status == ItemStarted {
		// A paused search may be observed again with fuller input/output in
		// a later invocation. Its started fact is immutable; terminal evidence
		// belongs only to the separate completed/failed fact.
		value = hostedToolTimelinePayload{CallID: call.ID, ToolKey: call.ToolKey, Status: "in_progress"}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	identity := strings.TrimSpace(call.ID)
	if identity == "" {
		identity = hashTimelineBytes(call.Input)
	}
	parentItemID := ""
	if tracker != nil {
		parentItemID = tracker.finalParent(call)
	}
	if identity == "" {
		identity = firstNonEmpty(parentItemID, strconv.Itoa(ordinal+1))
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: stableID("hiht", turn.ID, call.ToolKey, identity, string(status)), TurnID: turn.ID,
		Kind: ItemTool, Status: status, RunID: request.RunID, InvocationID: invocation.ID, ParentItemID: parentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func hostedToolItemStatus(status string) ItemStatus {
	if !model.HostedToolTerminalStatus(status) {
		return ItemStarted
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return ItemFailed
	case "cancelled", "canceled":
		return ItemCancelled
	default:
		return ItemCompleted
	}
}

func (runner *Runner) recordContextItem(ctx context.Context, turn Turn) error {
	if strings.TrimSpace(turn.ContextRef.ID) == "" {
		return nil
	}
	payload, err := json.Marshal(turn.ContextRef)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	invocation, _ := loadTopLevelInvocation(ctx, runner.store, turn.ID)
	item := Item{ID: stableID("hic", turn.ID, turn.ContextRef.ID), TurnID: turn.ID,
		Kind: ItemContext, Status: ItemCompleted, RunID: invocation.ExecutionRefID, InvocationID: invocation.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now}
	// A reused checkpoint is an immutable Turn fact, not a new Attempt.
	// Verify its historical owner rather than rewriting its original RunID.
	if invocation.Attempt > 1 {
		if original, e := runner.store.GetItem(ctx, item.ID); e == nil && original.RunID != item.RunID {
			items, err := listAllItems(ctx, runner.store, turn.ID)
			if err != nil {
				return err
			}
			ledger, err := runner.loadTurnBudget(ctx, turn)
			if err != nil {
				return err
			}
			item.RunID = original.RunID
			if _, ok := historicalRootForRun(invocation, original.RunID, items, ledger); !ok || original.Seq == 0 || !sameItemIdentity(original, item) {
				return ErrConflict
			}
			return nil
		} else if e != nil && !errors.Is(e, ErrNotFound) {
			return e
		}
	}
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, item)
	return err
}

func hashTimelineBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
