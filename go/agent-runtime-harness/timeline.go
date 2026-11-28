package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func appendHostedToolStartedItem(
	ctx context.Context,
	store Store,
	clock Clock,
	feed *TurnFeed,
	turn Turn,
	invocation Invocation,
	runID string,
	call model.HostedToolCall,
) error {
	payload, err := json.Marshal(hostedToolTimelinePayload{
		CallID: strings.TrimSpace(call.ID), ToolKey: strings.TrimSpace(call.ToolKey), Status: "in_progress",
	})
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{
		ID: hostedStreamItemID(turn.ID, call), TurnID: turn.ID, Kind: ItemTool, Status: ItemStarted, RunID: runID, InvocationID: invocation.ID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

type hostedToolStreamEventPayload struct {
	ID      string `json:"id,omitempty"`
	ToolKey string `json:"toolKey"`
	Status  string `json:"status,omitempty"`
}

func hostedStreamItemID(turnID string, call model.HostedToolCall) string {
	return stableID("hihts", turnID, strings.TrimSpace(call.ToolKey), strings.TrimSpace(call.ID))
}

func agentMessagePreviewEvent(event model.StreamEvent) (model.StreamEvent, bool) {
	event.HostedToolCall = nil
	return event, event.Delta != "" || event.Reasoning != nil || event.Usage != nil || strings.TrimSpace(event.ResponseID) != ""
}

// ModelTimelineObserver projects live facts and committed Model receipts using
// the existing observer boundary. It never wraps or changes the Provider.
type ModelTimelineObserver struct {
	store   Store
	runtime *kernel.Runtime
	clock   Clock
	feed    *TurnFeed
}

func NewModelTimelineObserver(store Store, runtime *kernel.Runtime, clock Clock, feed *TurnFeed) (*ModelTimelineObserver, error) {
	if store == nil || runtime == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ModelTimelineObserver{store: store, runtime: runtime, clock: clock, feed: feed}, nil
}
func (*ModelTimelineObserver) Name() string { return "harness.timeline.model" }

func (p *ModelTimelineObserver) Observe(ctx context.Context, event plugin.Event) {
	if event.Type != agent.EventModelStarted && event.Type != agent.EventModelDelta && event.Type != agent.EventModelCompleted {
		return
	}
	turn, invocation, active, err := timelineTurn(ctx, p.store, event.RunID)
	if err != nil || !active || invocation.ParentItemID != "" || event.Message == "" {
		return
	}
	if event.Type == agent.EventModelDelta {
		var stream model.StreamEvent
		if json.Unmarshal(event.Data, &stream) != nil {
			return
		}
		if call := stream.HostedToolCall; call != nil && strings.TrimSpace(call.ID) != "" {
			// The immutable started fact, not an in-memory tracker, links
			// observed progress to the eventual original receipt.
			if err := appendHostedToolStartedItem(ctx, p.store, p.clock, p.feed, turn, invocation, event.RunID, *call); err == nil && p.feed != nil {
				raw, err := json.Marshal(hostedToolStreamEventPayload{ID: strings.TrimSpace(call.ID), ToolKey: strings.TrimSpace(call.ToolKey), Status: strings.TrimSpace(call.Status)})
				if err == nil {
					_, _ = p.feed.Publish(ctx, turn.ID, TurnEventDraft{Type: EventItemDelta, ItemID: hostedStreamItemID(turn.ID, *call), ItemKind: ItemTool, Data: raw, Status: string(ItemStarted)})
				}
			}
		}
		preview, ok := agentMessagePreviewEvent(stream)
		if p.feed != nil && ok {
			messageID, _, err := activeAgentMessageItemID(ctx, p.store, turn)
			if err != nil {
				return
			}
			if messageID == "" {
				messageID = stableID("him", turn.ID, event.RunID, event.Message)
			}
			if raw, err := json.Marshal(preview); err == nil {
				_, _ = p.feed.Publish(ctx, turn.ID, TurnEventDraft{Type: EventItemDelta, ItemID: messageID, ItemKind: ItemAgentMessage, Delta: preview.Delta, Data: raw, Status: string(ItemStarted)})
			}
		}
		return
	}
	snapshot, err := p.runtime.Load(ctx, event.RunID)
	if err != nil {
		return
	}
	view, err := agent.ViewState(snapshot)
	if err != nil || len(view.ModelInvocations) == 0 {
		return
	}
	original := view.ModelInvocations[len(view.ModelInvocations)-1]
	if original.ID != event.Message || original.Purpose == agent.ModelInvocationContextSummary {
		return
	}
	if event.Type == agent.EventModelStarted {
		if p.feed != nil {
			_, found, err := activeAgentMessageItemID(ctx, p.store, turn)
			if err == nil && !found {
				_, _ = p.feed.Publish(ctx, turn.ID, TurnEventDraft{Type: EventItemStarted, ItemID: stableID("him", turn.ID, event.RunID, event.Message), ItemKind: ItemAgentMessage, Status: string(ItemStarted)})
			}
		}
		return
	}
	if original.Status != agent.ModelInvocationCompleted || original.ErrorCode != "" {
		return
	}
	_ = appendModelTimelineItems(ctx, p.store, p.clock, p.feed, turn, invocation, original.Request, original.Response)
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
) error {
	for _, artifact := range response.Artifacts {
		if err := appendArtifactTimelineItem(ctx, store, clock, feed, turn, invocation, request.RunID, artifact); err != nil {
			return err
		}
	}
	for index, call := range response.HostedToolCalls {
		if err := appendHostedToolTimelineItem(ctx, store, clock, feed, turn, invocation, request, call, index); err != nil {
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
	if strings.TrimSpace(call.ID) != "" {
		id := hostedStreamItemID(turn.ID, call)
		started, err := store.GetItem(ctx, id)
		if err == nil {
			if started.TurnID != turn.ID || started.RunID != request.RunID || started.InvocationID != invocation.ID || started.Kind != ItemTool || started.Status != ItemStarted {
				return ErrConflict
			}
			parentItemID = id
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
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
