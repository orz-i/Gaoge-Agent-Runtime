package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

func (handler *Handler) StartAgentRun(context *gin.Context) {
	if handler == nil || handler.agent == nil {
		writeError(context, http.StatusServiceUnavailable, "agent.unavailable", "agent runtime is unavailable")
		return
	}
	var request StartAgentRunRequest
	if err := bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	actor, err := handler.actorRef(context)
	if err != nil {
		writeError(context, http.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	snapshot, err := handler.agent.StartRun(context.Request.Context(), agent.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: normalizeThread(request.Thread),
		RequestID: handler.requestID(context), Goal: strings.TrimSpace(request.Input.Content),
		Model: strings.TrimSpace(request.Model), ToolKeys: append([]string(nil), request.ToolKeys...),
	})
	if err != nil {
		writeTargetRuntimeError(context, "agent", err)
		return
	}
	context.JSON(http.StatusAccepted, snapshotResponse(snapshot))
}

func (handler *Handler) StartWorkflowRun(context *gin.Context) {
	if handler == nil || handler.workflows == nil {
		writeError(context, http.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	var request StartWorkflowRunRequest
	if err := bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	actor, err := handler.actorRef(context)
	if err != nil {
		writeError(context, http.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	definition, err := workflow.CompileDefinition(request.Definition)
	if err != nil {
		writeTargetRuntimeError(context, "workflow", err)
		return
	}
	snapshot, err := handler.workflows.StartRun(context.Request.Context(), workflow.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: normalizeThread(request.Thread),
		RequestID: handler.requestID(context), Goal: strings.TrimSpace(request.Goal),
		Definition: definition, Input: append([]byte(nil), request.Input...),
	})
	if err != nil && !errors.Is(err, workflow.ErrWaitPending) {
		writeTargetRuntimeError(context, "workflow", err)
		return
	}
	context.JSON(http.StatusAccepted, snapshotResponse(snapshot))
}

func (handler *Handler) ResolveWorkflowWait(context *gin.Context) {
	if handler == nil || handler.workflows == nil {
		writeError(context, http.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "workflow.invalid_request", err.Error())
		return
	}
	var request ResolveWorkflowWaitRequest
	if err = bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	snapshot, err := handler.workflows.ResolveWait(
		context.Request.Context(), runID, request.ExpectedRevision, request.Response,
	)
	if err != nil && !errors.Is(err, workflow.ErrWaitPending) {
		writeTargetRuntimeError(context, "workflow", err)
		return
	}
	writeSuccess(context, snapshotResponse(snapshot))
}

func (handler *Handler) StartTeamRun(context *gin.Context) {
	if handler == nil || handler.teams == nil {
		writeError(context, http.StatusServiceUnavailable, "team.unavailable", "team runtime is unavailable")
		return
	}
	var request StartTeamRunRequest
	if err := bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	actor, err := handler.actorRef(context)
	if err != nil {
		writeError(context, http.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	members := make([]team.Member, 0, len(request.Members))
	for _, member := range request.Members {
		members = append(members, team.Member{
			ID: strings.TrimSpace(member.ID), Goal: strings.TrimSpace(member.Goal),
			ToolKeys: append([]string(nil), member.ToolKeys...),
		})
	}
	failurePolicy := request.Join.FailurePolicy
	if failurePolicy == "" {
		failurePolicy = handoff.FailureCollect
	}
	snapshot, err := handler.teams.StartRun(context.Request.Context(), team.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: normalizeThread(request.Thread),
		RequestID: handler.requestID(context), Goal: strings.TrimSpace(request.Goal),
		Mode: request.Mode, Members: members,
		Join: handoff.Join{Mode: request.Join.Mode, Quorum: request.Join.Quorum, FailurePolicy: failurePolicy},
	})
	if err != nil && !errors.Is(err, team.ErrMemberPending) {
		writeTargetRuntimeError(context, "team", err)
		return
	}
	context.JSON(http.StatusAccepted, snapshotResponse(snapshot))
}

func (handler *Handler) GetRun(context *gin.Context) {
	if handler == nil || handler.runtime == nil {
		writeError(context, http.StatusServiceUnavailable, "run.unavailable", "runtime is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "run.invalid_request", err.Error())
		return
	}
	snapshot, err := handler.runtime.Load(context.Request.Context(), runID)
	if err != nil {
		writeTargetRuntimeError(context, "run", err)
		return
	}
	writeSuccess(context, snapshotResponse(snapshot))
}

func (handler *Handler) CancelRun(context *gin.Context) {
	if handler == nil || handler.runtime == nil {
		writeError(context, http.StatusServiceUnavailable, "run.unavailable", "runtime is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "run.invalid_request", err.Error())
		return
	}
	var request CancelRunRequest
	if err = bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	snapshot, err := handler.runtime.Cancel(
		context.Request.Context(), runID, request.ExpectedRevision, request.Reason,
	)
	if err != nil {
		writeTargetRuntimeError(context, "run", err)
		return
	}
	writeSuccess(context, CancelRunResponse{Run: snapshot.Run})
}

func (handler *Handler) GetWorkbench(context *gin.Context) {
	if handler == nil || handler.workbench == nil {
		writeError(context, http.StatusServiceUnavailable, "workbench.unavailable", "workbench is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, http.StatusBadRequest, "workbench.invalid_request", err.Error())
		return
	}
	detail, err := handler.workbench.Get(context.Request.Context(), runID)
	if err != nil {
		writeTargetRuntimeError(context, "workbench", err)
		return
	}
	writeSuccess(context, workbenchResponse(detail))
}

func writeTargetRuntimeError(context *gin.Context, capability string, err error) {
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		writeError(context, http.StatusNotFound, capability+".not_found", "runtime resource not found")
	case errors.Is(err, kernel.ErrConflict):
		writeError(context, http.StatusConflict, capability+".conflict", "runtime revision conflict")
	case errors.Is(err, kernel.ErrTerminal):
		writeError(context, http.StatusConflict, capability+".terminal", "runtime is terminal")
	case errors.Is(err, kernel.ErrDeadline):
		writeError(context, http.StatusUnprocessableEntity, capability+".deadline", "runtime deadline exceeded")
	case errors.Is(err, kernel.ErrInvalidInput), errors.Is(err, workflow.ErrInvalidDefinition),
		errors.Is(err, workflow.ErrInvalidExecution), errors.Is(err, team.ErrInvalidRequest):
		writeError(context, http.StatusBadRequest, capability+".invalid_request", "invalid runtime request")
	default:
		writeError(context, http.StatusInternalServerError, capability+".failed", err.Error())
	}
}
