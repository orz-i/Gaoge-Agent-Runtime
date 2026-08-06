package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
)

func (handler *Handler) StartPlanRun(context *gin.Context) {
	if handler == nil || handler.plans == nil {
		writeError(context, http.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
		return
	}
	var request StartPlanRunRequest
	if err := bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	actor, err := handler.actorRef(context)
	if err != nil {
		writeError(context, http.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	snapshot, err := handler.plans.StartRun(context.Request.Context(), planexecute.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: normalizeThread(request.Thread),
		RequestID: handler.requestID(context), Goal: strings.TrimSpace(request.Input.Content),
		Model:          strings.TrimSpace(request.Model),
		ApprovalPolicy: planexecute.ApprovalPolicy(request.ApprovalPolicy), MaxSteps: request.MaxSteps,
	})
	if err != nil && !errors.Is(err, planexecute.ErrApprovalRequired) {
		writePlanRunError(context, err)
		return
	}
	context.JSON(http.StatusAccepted, snapshotResponse(snapshot))
}

func (handler *Handler) ResolvePlanApproval(context *gin.Context) {
	if handler == nil || handler.plans == nil {
		writeError(context, http.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "planexecute.invalid_request", err.Error())
		return
	}
	var request ResolvePlanApprovalRequest
	if err = bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	snapshot, err := handler.plans.ResolveApproval(
		context.Request.Context(), runID, request.ExpectedRevision,
		planexecute.ApprovalResponse{
			Decision: planexecute.ApprovalDecision(request.Decision), Comment: request.Comment,
		},
	)
	if err != nil {
		writePlanRunError(context, err)
		return
	}
	writeSuccess(context, snapshotResponse(snapshot))
}

func writePlanRunError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, planexecute.ErrApprovalRequired):
		writeError(context, http.StatusConflict, "planexecute.approval_required", "plan approval is required")
	case errors.Is(err, planexecute.ErrInvalidApproval):
		writeError(context, http.StatusBadRequest, "planexecute.invalid_approval", "invalid plan approval")
	case errors.Is(err, planexecute.ErrInvalidRequest), errors.Is(err, planexecute.ErrInvalidPlan):
		writeError(context, http.StatusBadRequest, "planexecute.invalid_request", "invalid plan run request")
	case errors.Is(err, kernel.ErrNotFound):
		writeError(context, http.StatusNotFound, "planexecute.not_found", "plan run not found")
	case errors.Is(err, kernel.ErrConflict):
		writeError(context, http.StatusConflict, "planexecute.conflict", "plan run revision conflict")
	default:
		writeError(context, http.StatusInternalServerError, "planexecute.failed", err.Error())
	}
}
