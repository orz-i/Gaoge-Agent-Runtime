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
}

func NewToolTimelineMiddleware(store Store, clock Clock) (*ToolTimelineMiddleware, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ToolTimelineMiddleware{store: store, clock: clock}, nil
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
		ctx, middleware.store, middleware.clock, turn, invocation, tools.ExecutionResult{}, ItemStarted, "",
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
		ctx, middleware.store, middleware.clock, turn, invocation, result, status, startedID,
	)
	return result, errors.Join(executeErr, itemErr)
}

type ModelTimelineMiddleware struct {
	store Store
	clock Clock
}

func NewModelTimelineMiddleware(store Store, clock Clock) (*ModelTimelineMiddleware, error) {
	if store == nil || clock == nil {
		return nil, ErrInvalidRequest
	}
	return &ModelTimelineMiddleware{store: store, clock: clock}, nil
}

func (*ModelTimelineMiddleware) Name() string { return "harness.timeline.model" }

func (middleware *ModelTimelineMiddleware) Model(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	next plugin.ModelNext,
) (model.Response, error) {
	response, callErr := next(ctx, request, emit)
	if callErr != nil {
		return response, callErr
	}
	turn, harnessRun, err := timelineTurn(ctx, middleware.store, request.RunID)
	if err != nil || !harnessRun {
		return response, err
	}
	if err = appendModelTimelineItems(ctx, middleware.store, middleware.clock, turn, request, response); err != nil {
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
	ContentHash   string `json:"contentHash"`
	ContentBytes  int    `json:"contentBytes"`
	CitationCount int    `json:"citationCount"`
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
	_, _, err = store.AppendItem(ctx, Item{
		ID: itemID, TurnID: turn.ID, Kind: ItemTool, Status: status, RunID: invocation.Run.ID,
		ParentItemID: parentItemID, Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return itemID, err
}

func appendModelTimelineItems(
	ctx context.Context,
	store Store,
	clock Clock,
	turn Turn,
	request model.Request,
	response model.Response,
) error {
	if content := strings.TrimSpace(response.Content); content != "" {
		if err := appendAgentMessageTimelineItem(ctx, store, clock, turn, request, response, content); err != nil {
			return err
		}
	}
	for _, artifact := range response.Artifacts {
		if err := appendArtifactTimelineItem(ctx, store, clock, turn, request.RunID, artifact); err != nil {
			return err
		}
	}
	for _, call := range response.HostedToolCalls {
		if err := appendHostedToolTimelineItem(ctx, store, clock, turn, request, call); err != nil {
			return err
		}
	}
	return nil
}

func appendAgentMessageTimelineItem(
	ctx context.Context,
	store Store,
	clock Clock,
	turn Turn,
	request model.Request,
	response model.Response,
	content string,
) error {
	payload, err := json.Marshal(modelTimelinePayload{
		ContentHash: hashTimelineString(content), ContentBytes: len(content), CitationCount: len(response.Citations),
	})
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	itemID := stableID("him", turn.ID, request.RunID, strconv.Itoa(len(request.Messages)), hashTimelineString(content))
	_, _, err = store.AppendItem(ctx, Item{
		ID: itemID, TurnID: turn.ID, Kind: ItemAgentMessage, Status: ItemCompleted, RunID: request.RunID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func appendArtifactTimelineItem(
	ctx context.Context,
	store Store,
	clock Clock,
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
	_, _, err = store.AppendItem(ctx, Item{
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
	turn Turn,
	request model.Request,
	call model.HostedToolCall,
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
	now := clock.Now().UTC()
	_, _, err = store.AppendItem(ctx, Item{
		ID: stableID("hiht", turn.ID, call.ToolKey, identity), TurnID: turn.ID,
		Kind: ItemTool, Status: ItemCompleted, RunID: request.RunID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
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
	_, _, err = runner.store.AppendItem(ctx, Item{
		ID: stableID("hic", turn.ID, turn.ContextRef.ID), TurnID: turn.ID,
		Kind: ItemContext, Status: ItemCompleted, RunID: turn.RootRunID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	return err
}

func hashTimelineString(value string) string { return hashTimelineBytes([]byte(value)) }

func hashTimelineBytes(value []byte) string {
	if len(value) == 0 {
		return ""
	}
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}
