package harnesshttp

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge/sdk/go/agent-runtime-harness"
	runtimehttp "github.com/orz-i/Gaoge/sdk/go/agent-runtime-http"
)

type TurnResponse struct {
	ID          string             `json:"id"`
	HostTurn    harness.HostRef    `json:"hostTurn"`
	Status      harness.TurnStatus `json:"status"`
	Revision    uint64             `json:"revision"`
	ErrorCode   string             `json:"errorCode,omitempty"`
	ErrorDetail string             `json:"errorDetail,omitempty"`
	CreatedAt   time.Time          `json:"createdAt"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

func (handler *Handler) ResolveApproval(context *gin.Context) {
	snapshot, ok := handler.authorizedTurn(context)
	if !ok {
		return
	}
	var request ResolveApprovalRequest
	if err := context.ShouldBindJSON(&request); err != nil ||
		(request.Decision != harness.ApprovalApprove && request.Decision != harness.ApprovalReject) {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.approval_invalid_request", "invalid Harness approval decision")
		return
	}
	resolved, err := handler.runner.ResolveApproval(context.Request.Context(), snapshot.Turn.ID, harness.ResolveApprovalRequest{
		Decision: request.Decision, Comment: strings.TrimSpace(request.Comment),
	})
	if err != nil {
		writeHarnessError(context, err)
		return
	}
	response, err := snapshotResponse(resolved)
	if err != nil {
		writeHarnessError(context, err)
		return
	}
	context.JSON(stdhttp.StatusOK, response)
}

func snapshotResponse(snapshot harness.Snapshot) (SnapshotResponse, error) {
	items := make([]ItemResponse, 0, len(snapshot.Items))
	for _, item := range snapshot.Items {
		projected, err := itemResponse(item)
		if err != nil {
			return SnapshotResponse{}, err
		}
		items = append(items, projected)
	}
	status := snapshot.Turn.Status
	errorCode := snapshot.Turn.ErrorCode
	errorDetail := snapshot.Turn.ErrorDetail
	output := snapshot.Output
	if terminalHarnessTurn(status) && !harness.TerminalFeedReady(snapshot) {
		status = harness.TurnRunning
		errorCode = ""
		errorDetail = ""
		output = nil
	}
	return SnapshotResponse{
		Turn: TurnResponse{
			ID: snapshot.Turn.ID, HostTurn: snapshot.Turn.HostTurn, Status: status,
			Revision: snapshot.Turn.Revision, ErrorCode: errorCode, ErrorDetail: errorDetail,
			CreatedAt: snapshot.Turn.CreatedAt, UpdatedAt: snapshot.Turn.UpdatedAt,
		},
		Items:  items,
		Output: output,
	}, nil
}

func itemResponse(item harness.Item) (ItemResponse, error) {
	payload, err := publicItemPayload(item.Kind, item.Payload)
	if err != nil {
		return ItemResponse{}, err
	}
	var hostRef *harness.HostRef
	if item.HostRef != nil {
		value := *item.HostRef
		hostRef = &value
	}
	return ItemResponse{
		ID: item.ID, TurnID: item.TurnID, Seq: item.Seq, Kind: item.Kind, Status: item.Status,
		HostRef: hostRef, ParentItemID: item.ParentItemID, Payload: payload,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}, nil
}

func publicItemPayload(kind harness.ItemKind, payload json.RawMessage) (json.RawMessage, error) {
	if len(payload) == 0 || kind != harness.ItemDelegation {
		return append(json.RawMessage(nil), payload...), nil
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, errors.Join(harness.ErrConflict, err)
	}
	delete(value, "childRunID")
	return json.Marshal(value)
}

func turnEventResponse(event harness.TurnEvent) (harness.TurnEvent, error) {
	result := event
	result.Data = append(json.RawMessage(nil), event.Data...)
	if event.Type != harness.EventItemStarted && event.Type != harness.EventItemCompleted || len(event.Data) == 0 {
		return result, nil
	}
	var item harness.Item
	if err := json.Unmarshal(event.Data, &item); err != nil {
		return harness.TurnEvent{}, errors.Join(harness.ErrConflict, err)
	}
	projected, err := itemResponse(item)
	if err != nil {
		return harness.TurnEvent{}, err
	}
	result.Data, err = json.Marshal(projected)
	return result, err
}

type ItemResponse struct {
	ID           string             `json:"id"`
	TurnID       string             `json:"turnID"`
	Seq          uint64             `json:"seq"`
	Kind         harness.ItemKind   `json:"kind"`
	Status       harness.ItemStatus `json:"status"`
	HostRef      *harness.HostRef   `json:"hostRef,omitempty"`
	ParentItemID string             `json:"parentItemID,omitempty"`
	Payload      json.RawMessage    `json:"payload,omitempty"`
	CreatedAt    time.Time          `json:"createdAt"`
	UpdatedAt    time.Time          `json:"updatedAt"`
}

type SnapshotResponse struct {
	Turn   TurnResponse    `json:"turn"`
	Items  []ItemResponse  `json:"items"`
	Output *harness.Output `json:"output,omitempty"`
}

type ResolveApprovalRequest struct {
	Decision harness.ApprovalDecision `json:"decision" binding:"required"`
	Comment  string                   `json:"comment,omitempty"`
}

type Dependencies struct {
	Runner *harness.Runner
	Shared *runtimehttp.Shared
}

type Handler struct {
	runner *harness.Runner
	shared *runtimehttp.Shared
}

func NewHandler(dependencies Dependencies) *Handler {
	return &Handler{runner: dependencies.Runner, shared: dependencies.Shared}
}

type Module struct{ Handler *Handler }

func NewModule(handler *Handler) *Module { return &Module{Handler: handler} }

func (module *Module) RegisterRoutes(routes *gin.RouterGroup) {
	if module == nil || module.Handler == nil || routes == nil {
		return
	}
	routes.GET("/harness/turns/:turn_id", module.Handler.GetTurn)
	routes.GET("/harness/turns/:turn_id/feed", module.Handler.StreamTurnFeed)
	routes.POST("/harness/turns/:turn_id/approval", module.Handler.ResolveApproval)
}

func (handler *Handler) GetTurn(context *gin.Context) {
	snapshot, ok := handler.authorizedTurn(context)
	if !ok {
		return
	}
	response, err := snapshotResponse(snapshot)
	if err != nil {
		writeHarnessError(context, err)
		return
	}
	context.JSON(stdhttp.StatusOK, response)
}

func (handler *Handler) StreamTurnFeed(context *gin.Context) {
	snapshot, ok := handler.authorizedTurn(context)
	if !ok {
		return
	}
	afterSeq, err := turnFeedAfterSeq(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.feed_invalid_request", "invalid Harness Turn feed cursor")
		return
	}
	subscription, err := handler.runner.SubscribeTurnFeed(context.Request.Context(), snapshot.Turn.ID, afterSeq)
	if err != nil {
		writeHarnessError(context, err)
		return
	}
	defer subscription.Close()
	beginTurnFeedStream(context)
	lastSeq, terminal := writeTurnFeedReplay(context, subscription.Replay, afterSeq)
	if terminal {
		return
	}
	if terminalHarnessTurn(snapshot.Turn.Status) && harness.TerminalFeedReady(snapshot) {
		_ = writeTurnFeedEvent(context, terminalTurnSnapshotEvent(snapshot, lastSeq+1))
		return
	}
	followTurnFeed(context, subscription)
}

func (handler *Handler) authorizedTurn(context *gin.Context) (harness.Snapshot, bool) {
	if handler == nil || handler.runner == nil || handler.shared == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "harness.unavailable", "Harness runtime is unavailable")
		return harness.Snapshot{}, false
	}
	turnID := strings.TrimSpace(context.Param("turn_id"))
	if turnID == "" {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.invalid_request", "Harness Turn id is required")
		return harness.Snapshot{}, false
	}
	snapshot, err := handler.runner.Load(context.Request.Context(), turnID)
	if err != nil {
		writeHarnessError(context, err)
		return harness.Snapshot{}, false
	}
	actor, err := handler.shared.ActorRef(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", err.Error())
		return harness.Snapshot{}, false
	}
	if snapshot.Session.Actor != actor {
		runtimehttp.WriteError(context, stdhttp.StatusNotFound, "harness.not_found", "Harness Turn not found")
		return harness.Snapshot{}, false
	}
	return snapshot, true
}

func writeHarnessError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, harness.ErrInvalidRequest):
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.invalid_request", "invalid Harness request")
	case errors.Is(err, harness.ErrNotFound):
		runtimehttp.WriteError(context, stdhttp.StatusNotFound, "harness.not_found", "Harness Turn not found")
	case errors.Is(err, harness.ErrConflict):
		runtimehttp.WriteError(context, stdhttp.StatusConflict, "harness.conflict", "Harness Turn state conflicts")
	default:
		runtimehttp.WriteError(context, stdhttp.StatusInternalServerError, "harness.internal", "Harness runtime request failed")
	}
}

func turnFeedAfterSeq(context *gin.Context) (int64, error) {
	raw := strings.TrimSpace(context.Query("afterSeq"))
	if raw == "" {
		raw = strings.TrimSpace(context.GetHeader("Last-Event-ID"))
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, harness.ErrInvalidRequest
	}
	return value, nil
}

func beginTurnFeedStream(context *gin.Context) {
	context.Header("Content-Type", "text/event-stream; charset=utf-8")
	context.Header("Cache-Control", "no-cache, no-transform")
	context.Header("Connection", "keep-alive")
	context.Header("X-Accel-Buffering", "no")
	context.Status(stdhttp.StatusOK)
	context.Writer.Flush()
}

func writeTurnFeedReplay(context *gin.Context, replay []harness.TurnEvent, afterSeq int64) (int64, bool) {
	lastSeq := afterSeq
	for _, event := range replay {
		if writeTurnFeedEvent(context, event) != nil {
			return lastSeq, true
		}
		lastSeq = event.Seq
		if event.Terminal {
			return lastSeq, true
		}
	}
	return lastSeq, false
}

func followTurnFeed(context *gin.Context, subscription *harness.TurnSubscription) {
	events := subscription.Events
	errorsChannel := subscription.Errors
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			if writeTurnFeedEvent(context, event) != nil || event.Terminal {
				return
			}
		case _, open := <-errorsChannel:
			if open {
				return
			}
			errorsChannel = nil
		case <-context.Request.Context().Done():
			return
		}
	}
}

func writeTurnFeedEvent(context *gin.Context, event harness.TurnEvent) error {
	projected, err := turnEventResponse(event)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(projected)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(context.Writer, "id: %d\ndata: %s\n\n", event.Seq, encoded); err != nil {
		return err
	}
	context.Writer.Flush()
	return nil
}

func terminalHarnessTurn(status harness.TurnStatus) bool {
	return status == harness.TurnCompleted || status == harness.TurnFailed || status == harness.TurnCancelled
}

func terminalTurnSnapshotEvent(snapshot harness.Snapshot, seq int64) harness.TurnEvent {
	eventType := harness.EventTurnFailed
	switch snapshot.Turn.Status {
	case harness.TurnCompleted:
		eventType = harness.EventTurnCompleted
	case harness.TurnCancelled:
		eventType = harness.EventTurnCancelled
	}
	return harness.TurnEvent{
		Seq: seq, TurnID: snapshot.Turn.ID, Type: eventType,
		Message: snapshot.Turn.ErrorDetail, Status: string(snapshot.Turn.Status), Terminal: true,
		CreatedAt: snapshot.Turn.UpdatedAt,
	}
}
