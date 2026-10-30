package harnesshttp

import (
	stdhttp "net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
)

// TurnStateResponse is the committed Harness status, not a claim that a
// host-bound assistant message has completed product-side projection.
// Clients requiring host-output acknowledgement use the full Turn endpoint.
type TurnStateResponse struct {
	Turn TurnResponse `json:"turn"`
}

type TimelinePageResponse struct {
	Items        []ItemResponse `json:"items"`
	NextAfterSeq uint64         `json:"nextAfterSeq"`
}

// authorizedState checks the Host actor before reading the complete history.
// The underlying Session Actor is immutable across revisions.
func (handler *Handler) authorizedState(context *gin.Context) (harness.StateSnapshot, bool) {
	if handler == nil || handler.runner == nil || handler.shared == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "harness.unavailable", "Harness runtime is unavailable")
		return harness.StateSnapshot{}, false
	}
	turnID := strings.TrimSpace(context.Param("turn_id"))
	if turnID == "" {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.invalid_request", "Harness Turn id is required")
		return harness.StateSnapshot{}, false
	}
	actor, err := handler.shared.ActorRef(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", "authenticated principal is required")
		return harness.StateSnapshot{}, false
	}
	state, err := handler.runner.LoadState(context.Request.Context(), turnID)
	if err != nil {
		writeHarnessError(context, err)
		return harness.StateSnapshot{}, false
	}
	if state.Session.Actor != actor {
		runtimehttp.WriteError(context, stdhttp.StatusNotFound, "harness.not_found", "Harness Turn not found")
		return harness.StateSnapshot{}, false
	}
	return state, true
}

// GetTurnState exposes the lightweight persisted status for polling without
// traversing every Timeline Item or reconstructing complete feature state.
func (handler *Handler) GetTurnState(context *gin.Context) {
	state, ok := handler.authorizedState(context)
	if !ok {
		return
	}
	turn := state.Turn
	context.JSON(stdhttp.StatusOK, TurnStateResponse{Turn: TurnResponse{
		ID: turn.ID, HostTurn: turn.HostTurn, Status: turn.Status,
		Revision: turn.Revision, ErrorCode: runtimehttp.PublicFailureCode(turn.ErrorCode),
		ErrorDetail: runtimehttp.PublicFailureDetail(turn.ErrorCode, turn.ErrorDetail),
		CreatedAt:   turn.CreatedAt, UpdatedAt: turn.UpdatedAt,
	}})
}

// GetTurnItems supports owner-authorized, bounded historical reads. Same public
// payload filtering as the full snapshot path; never serializes private Run IDs.
func (handler *Handler) GetTurnItems(context *gin.Context) {
	state, ok := handler.authorizedState(context)
	if !ok {
		return
	}
	afterSeq := uint64(0)
	if raw := strings.TrimSpace(context.Query("afterSeq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.invalid_request", "invalid Timeline cursor")
			return
		}
		afterSeq = parsed
	}
	limit := 100
	if raw := strings.TrimSpace(context.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "harness.invalid_request", "invalid Timeline page limit")
			return
		}
		limit = parsed
	}
	page, err := handler.runner.ListTimeline(context.Request.Context(), state.Turn.ID, afterSeq, limit)
	if err != nil {
		writeHarnessError(context, err)
		return
	}
	items := make([]ItemResponse, 0, len(page))
	for _, item := range page {
		public, err := itemResponse(item)
		if err != nil {
			writeHarnessError(context, err)
			return
		}
		items = append(items, public)
		afterSeq = item.Seq
	}
	context.JSON(stdhttp.StatusOK, TimelinePageResponse{Items: items, NextAfterSeq: afterSeq})
}
