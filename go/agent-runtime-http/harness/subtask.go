package harnesshttp

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
)

func (handler *Handler) CancelSubtask(ctx *gin.Context) {
	snapshot, ok := handler.authorizedTurn(ctx)
	if !ok {
		return
	}
	var request struct {
		Reason string `json:"reason" binding:"max=2000"`
	}
	if ctx.ShouldBindJSON(&request) != nil || strings.TrimSpace(ctx.Param("subtask_id")) == "" {
		runtimehttp.WriteError(ctx, http.StatusBadRequest, "harness.subtask_invalid_request", "invalid subtask cancellation")
		return
	}
	result, err := handler.runner.CancelSubtask(ctx.Request.Context(), snapshot.Turn.ID, ctx.Param("subtask_id"), request.Reason)
	handler.writeSubtaskSnapshot(ctx, result, err)
}

func (handler *Handler) ResolveSubtaskApproval(ctx *gin.Context) {
	snapshot, ok := handler.authorizedTurn(ctx)
	if !ok {
		return
	}
	var request struct {
		CheckpointID string                   `json:"checkpointID" binding:"required,max=128"`
		Decision     harness.ApprovalDecision `json:"decision" binding:"required,oneof=approve reject"`
		Comment      string                   `json:"comment" binding:"max=2000"`
	}
	if ctx.ShouldBindJSON(&request) != nil || strings.TrimSpace(ctx.Param("subtask_id")) == "" {
		runtimehttp.WriteError(ctx, http.StatusBadRequest, "harness.subtask_invalid_request", "invalid subtask approval")
		return
	}
	result, err := handler.runner.ResolveSubtaskApproval(ctx.Request.Context(), snapshot.Turn.ID, ctx.Param("subtask_id"), request.CheckpointID,
		harness.ResolveApprovalRequest{Decision: request.Decision, Comment: request.Comment})
	handler.writeSubtaskSnapshot(ctx, result, err)
}

func (*Handler) writeSubtaskSnapshot(ctx *gin.Context, result harness.Snapshot, err error) {
	if err != nil {
		writeHarnessError(ctx, err)
		return
	}
	response, err := snapshotResponse(result)
	if err != nil {
		writeHarnessError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, response)
}
