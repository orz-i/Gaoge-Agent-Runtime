package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
)

const (
	planFieldSchemaVersion = "schemaVersion"
	planFieldActor         = "actor"
	planFieldTenantID      = "tenantID"
	planFieldRevision      = "revision"
	planFieldRuntimeKind   = "runtimeKind"
)

// StartPlanRunRequest starts one explicit Plan-and-Execute Runtime Kind.
type StartPlanRunRequest struct {
	Thread         RunThreadRequest    `json:"thread" binding:"required"`
	Input          TextRunRequestInput `json:"input" binding:"required"`
	ClientRunID    string              `json:"clientRunID" binding:"omitempty,max=64"`
	Model          string              `json:"model" binding:"omitempty,max=128"`
	ApprovalPolicy string              `json:"approvalPolicy" binding:"omitempty,oneof=auto required"`
	MaxSteps       int                 `json:"maxSteps" binding:"omitempty,min=1,max=32"`
}

type ResolvePlanApprovalRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Decision         string `json:"decision" binding:"required,oneof=approve reject"`
	Comment          string `json:"comment" binding:"omitempty,max=2000"`
}

func (h *Handler) StartPlanRun(c *gin.Context) {
	if h.plans == nil {
		writeError(c, http.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
		return
	}
	var request StartPlanRunRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeError(c, http.StatusBadRequest, "planexecute.invalid_request", "invalid plan run request")
		return
	}
	actor := h.actorRef(c)
	snapshot, err := h.plans.StartRun(c.Request.Context(), planexecute.StartRequest{
		ID:        strings.TrimSpace(request.ClientRunID),
		Actor:     kernel.ActorRef{TenantID: actor.TenantID, ActorID: actor.ActorID},
		Thread:    kernel.ThreadRef{Kind: request.Thread.Kind, ID: request.Thread.ID},
		RequestID: h.requestID(c), Goal: request.Input.Content, Model: request.Model,
		ApprovalPolicy: planexecute.ApprovalPolicy(request.ApprovalPolicy), MaxSteps: request.MaxSteps,
	})
	if err != nil {
		writePlanRunError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, planRunSnapshotResponse(snapshot))
}

func (h *Handler) ResolvePlanApproval(c *gin.Context) {
	if h.plans == nil {
		writeError(c, http.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
		return
	}
	runID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "planexecute.invalid_request", err.Error())
		return
	}
	var request ResolvePlanApprovalRequest
	if err = bindStrictJSON(c, &request); err != nil {
		writeError(c, http.StatusBadRequest, "planexecute.invalid_approval", "invalid plan approval request")
		return
	}
	snapshot, err := h.plans.ResolveApproval(c.Request.Context(), runID, request.ExpectedRevision, planexecute.ApprovalResponse{
		Decision: planexecute.ApprovalDecision(request.Decision), Comment: request.Comment,
	})
	if err != nil {
		writePlanRunError(c, err)
		return
	}
	writeSuccess(c, planRunSnapshotResponse(snapshot))
}

func planRunSnapshotResponse(snapshot kernel.Snapshot) map[string]interface{} {
	return map[string]interface{}{
		"run": map[string]interface{}{
			planFieldSchemaVersion: 1,
			valueRunID1DA2F0B6:     snapshot.Run.ID, planFieldRuntimeKind: snapshot.Run.Kind,
			planFieldActor: map[string]interface{}{
				planFieldTenantID: snapshot.Run.Actor.TenantID, "id": snapshot.Run.Actor.ActorID,
			},
			valueThread: snapshot.Run.Thread,
			"requestID": snapshot.Run.RequestID, valueGoal51342CCB: snapshot.Run.Goal,
			valueStatus00E8FE8E: snapshot.Run.Status, planFieldRevision: snapshot.Run.Revision,
			valueErrorCode8B63C5B4: snapshot.Run.ErrorCode, valueErrorMessage: snapshot.Run.ErrorDetail,
			"deadlineAt": snapshot.Run.DeadlineAt, "endedAt": snapshot.Run.EndedAt,
			valueCreatedAtE3B65D13: snapshot.Run.CreatedAt, valueUpdatedAt: snapshot.Run.UpdatedAt,
		},
		"state": snapshot.State, "checkpoint": snapshot.Checkpoint, "result": snapshot.Result,
	}
}

func writePlanRunError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		writeError(c, http.StatusNotFound, "planexecute.not_found", "plan run not found")
	case errors.Is(err, kernel.ErrConflict):
		writeError(c, http.StatusConflict, "planexecute.conflict", "plan run revision conflict")
	case errors.Is(err, planexecute.ErrApprovalRequired):
		writeError(c, http.StatusConflict, "planexecute.approval_required", "plan approval is required")
	case errors.Is(err, planexecute.ErrInvalidApproval):
		writeError(c, http.StatusBadRequest, "planexecute.invalid_approval", "invalid plan approval")
	case errors.Is(err, planexecute.ErrInvalidRequest), errors.Is(err, planexecute.ErrInvalidPlan):
		writeError(c, http.StatusBadRequest, "planexecute.invalid_request", "invalid plan run request")
	default:
		writeError(c, http.StatusInternalServerError, "planexecute.failed", "plan run failed")
	}
}
