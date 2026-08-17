package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

type ToolTimelineMiddleware struct {
	store Store
	clock Clock
	feed  *TurnFeed
}

func appendHostedToolStartedItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	runID string,
	state hostedToolStreamState,
	call model.HostedToolCall,
) error {
	payload, err := json.Marshal(hostedToolTimelinePayload{
		CallID: state.CallID, ToolKey: state.ToolKey, Status: strings.TrimSpace(call.Status),
		InputHash: state.InputHash, OutputHash: hashTimelineBytes(call.Output), ErrorHash: hashTimelineBytes(call.Error),
	})
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: state.ItemID, TurnID: turn.ID, Kind: ItemTool, Status: ItemStarted, RunID: runID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

type hostedToolStreamState struct {
	ItemID     string
	CallID     string
	ToolKey    string
	InputHash  string
	Closed     bool
}

type hostedToolStreamTracker struct {
	turnID string
	next   int
	states []hostedToolStreamState
}

func newHostedToolStreamTracker(turnID string) *hostedToolStreamTracker {
	return &hostedToolStreamTracker{turnID: strings.TrimSpace(turnID)}
}

func agentMessagePreviewEvent(event model.StreamEvent) (model.StreamEvent, bool) {
	event.HostedToolCall = nil
	return event, event.Delta != "" || event.Reasoning != nil || event.Usage != nil || strings.TrimSpace(event.ResponseID) != ""
}

func (middleware *ModelTimelineMiddleware) recordHostedToolStreamEvent(
	ctx context.Context,
	turn Turn,
	runID string,
	tracker *hostedToolStreamTracker,
	call model.HostedToolCall,
) error {
	state, created := tracker.resolveStream(call)
	if created {
		if err := appendHostedToolStartedItem(ctx, middleware.store, middleware.clock, middleware.feed, turn, runID, state, call); err != nil {
			return err
		}
	}
	if middleware.feed != nil {
		raw, err := json.Marshal(call)
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
	if index := tracker.findStreamState(callID, toolKey, inputHash); index >= 0 {
		state := &tracker.states[index]
		if state.CallID == "" && callID != "" {
			state.CallID = callID
		}
		if state.InputHash == "" && inputHash != "" {
			state.InputHash = inputHash
		}
		return *state, false
	}
	tracker.next++
	identity := firstNonEmpty(callID, inputHash, strconv.Itoa(tracker.next))
	state := hostedToolStreamState{
		ItemID: stableID("hihts", tracker.turnID, toolKey, identity),
		CallID: callID, ToolKey: toolKey, InputHash: inputHash,
	}
	tracker.states = append(tracker.states, state)
	return state, true
}

func (tracker *hostedToolStreamTracker) findStreamState(callID, toolKey, inputHash string) int {
	if index := tracker.stateIndexByCallID(callID); index >= 0 {
		return index
	}
	if index := tracker.stateIndexByInput(toolKey, inputHash); index >= 0 {
		return index
	}
	return tracker.uniqueOpenStateIndex(toolKey)
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

func (tracker *hostedToolStreamTracker) stateIndexByInput(toolKey, inputHash string) int {
	if inputHash == "" {
		return -1
	}
	for index := range tracker.states {
		state := tracker.states[index]
		if state.ToolKey == toolKey && state.InputHash == inputHash {
			return index
		}
	}
	return -1
}

func (tracker *hostedToolStreamTracker) uniqueOpenStateIndex(toolKey string) int {
	candidate := -1
	for index := len(tracker.states) - 1; index >= 0; index-- {
		state := tracker.states[index]
		if state.ToolKey != toolKey || state.Closed {
			continue
		}
		if candidate >= 0 {
			return -1
		}
		candidate = index
	}
	return candidate
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
	toolKey := strings.TrimSpace(call.ToolKey)
	inputHash := hashTimelineBytes(call.Input)
	if index := tracker.stateIndexByCallID(callID); index >= 0 {
		return tracker.states[index].ItemID
	}
	if index := tracker.stateIndexByInput(toolKey, inputHash); index >= 0 {
		return tracker.states[index].ItemID
	}
	return tracker.uniqueItemIDForTool(toolKey)
}

func (tracker *hostedToolStreamTracker) uniqueItemIDForTool(toolKey string) string {
	parent := ""
	for _, state := range tracker.states {
		if state.ToolKey != toolKey {
			continue
		}
		if parent != "" {
			return ""
		}
		parent = state.ItemID
	}
	return parent
}

func hostedToolTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func NewToolTimelineMiddleware(store Store, clock Clock, feed *TurnFeed) (*ToolTimelineMiddleware, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ToolTimelineMiddleware{store: store, clock: clock, feed: feed}, nil
}

func (*ToolTimelineMiddleware) Name() string { return "harness.timeline.tool" }

func (middleware *ToolTimelineMiddleware) Tool(
	ctx context.Context,
	invocation plugin.ToolInvocation,
	next plugin.ToolNext,
) (tools.ExecutionResult, error) {
	turn, harnessRun, err := timelineTurn(ctx, middleware.store, invocation.Run.ID)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	if !harnessRun {
		return next(ctx)
	}
	startedID, err := appendToolTimelineItem(
		ctx, middleware.store, middleware.clock, middleware.feed, turn, invocation, tools.ExecutionResult{}, ItemStarted, "",
	)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	result, executeErr := next(ctx)
	status := ItemCompleted
	if executeErr != nil {
		status = ItemFailed
	}
	_, itemErr := appendToolTimelineItem(
		ctx, middleware.store, middleware.clock, middleware.feed, turn, invocation, result, status, startedID,
	)
	return result, errors.Join(executeErr, itemErr)
}

type ModelTimelineMiddleware struct {
	store Store
	clock Clock
	feed  *TurnFeed
}

func NewModelTimelineMiddleware(store Store, clock Clock, feed *TurnFeed) (*ModelTimelineMiddleware, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ModelTimelineMiddleware{store: store, clock: clock, feed: feed}, nil
}

func (*ModelTimelineMiddleware) Name() string { return "harness.timeline.model" }

func (middleware *ModelTimelineMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	turn, harnessRun, err := timelineTurn(ctx, middleware.store, request.RunID)
	if err != nil {
		return model.Response{}, err
	}
	if !harnessRun {
		return next(ctx, request, emit)
	}
	messageItemID, hasMessageItem, err := activeAgentMessageItemID(ctx, middleware.store, turn)
	if err != nil {
		return model.Response{}, err
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
			if streamErr := middleware.recordHostedToolStreamEvent(ctx, turn, request.RunID, hostedTools, *event.HostedToolCall); streamErr != nil {
				return streamErr
			}
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
	response, callErr := next(ctx, request, stream)
	if callErr != nil {
		return response, callErr
	}
	if err = appendModelTimelineItems(
		ctx, middleware.store, middleware.clock, middleware.feed, turn, request, response, hostedTools,
	); err != nil {
		return response, err
	}
	return response, nil
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

func timelineTurn(ctx context.Context, store Store, rootRunID string) (Turn, bool, error) {
	turn, err := store.GetTurnByRootRunID(ctx, strings.TrimSpace(rootRunID))
	if errors.Is(err, ErrNotFound) {
		return Turn{}, false, nil
	}
	return turn, err == nil, err
}

func appendToolTimelineItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation plugin.ToolInvocation,
	result tools.ExecutionResult,
	status ItemStatus,
	parentItemID string,
) (string, error) {
	payload, err := json.Marshal(toolTimelinePayload{
		CallID: invocation.Request.Call.ID, ToolKey: invocation.Definition.Key,
		ArgumentsHash: hashTimelineBytes(invocation.Request.Call.Arguments),
		ContentHash:   hashTimelineBytes(result.Content), ExecutionID: result.Receipt.ExecutionID,
		Disposition: result.Receipt.Disposition,
	})
	if err != nil {
		return "", err
	}
	itemID := stableID("hit", turn.ID, invocation.Request.Call.ID, string(status))
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: itemID, TurnID: turn.ID, Kind: ItemTool, Status: status, RunID: invocation.Run.ID,
		ParentItemID: parentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return itemID, err
}

func appendModelTimelineItems(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	request model.Request,
	response model.Response,
	hostedTools *hostedToolStreamTracker,
) error {
	for _, artifact := range response.Artifacts {
		if err := appendArtifactTimelineItem(ctx, store, clock, feed, turn, request.RunID, artifact); err != nil {
			return err
		}
	}
	for index, call := range response.HostedToolCalls {
		if err := appendHostedToolTimelineItem(ctx, store, clock, feed, turn, request, call, hostedTools, index); err != nil {
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
	items, err := store.ListItems(ctx, turn.ID, 0, defaultItemListLimit)
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
		Kind: ItemArtifact, Status: ItemCompleted, RunID: runID,
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
	request model.Request,
	call model.HostedToolCall,
	tracker *hostedToolStreamTracker,
	ordinal int,
) error {
	payload, err := json.Marshal(hostedToolTimelinePayload{
		CallID: call.ID, ToolKey: call.ToolKey, Status: call.Status,
		InputHash: hashTimelineBytes(call.Input), OutputHash: hashTimelineBytes(call.Output), ErrorHash: hashTimelineBytes(call.Error),
	})
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
	status := hostedToolItemStatus(call.Status)
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: stableID("hiht", turn.ID, call.ToolKey, identity, string(status)), TurnID: turn.ID,
		Kind: ItemTool, Status: status, RunID: request.RunID, ParentItemID: parentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func hostedToolItemStatus(status string) ItemStatus {
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
	_, err = appendItemFact(ctx, runner.store, runner.turnFeed, Item{
		ID: stableID("hic", turn.ID, turn.ContextRef.ID), TurnID: turn.ID,
		Kind: ItemContext, Status: ItemCompleted, RunID: turn.RootRunID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func hashTimelineBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
