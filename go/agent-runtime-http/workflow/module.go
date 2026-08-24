package workflowhttp

import (
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type Dependencies struct {
	Runner *workflow.Runner
	Shared *runtimehttp.Shared
}

type Handler struct {
	runner *workflow.Runner
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
	routes.POST("/workflow-runs", module.Handler.StartRun)
	routes.POST("/workflow-runs/:run_id/wait", module.Handler.ResolveWait)
}

type StartRunRequest struct {
	Thread      runtimehttp.ThreadRequest `json:"thread" binding:"required"`
	Input       json.RawMessage           `json:"input" binding:"required"`
	ClientRunID string                    `json:"clientRunID" binding:"omitempty,max=64"`
	Goal        string                    `json:"goal" binding:"required,max=200000"`
	Definition  workflow.DefinitionDraft  `json:"definition" binding:"required"`
}

type ResolveWaitRequest struct {
	ExpectedRevision uint64          `json:"expectedRevision" binding:"required,min=1"`
	Response         json.RawMessage `json:"response" binding:"required"`
}

func (handler *Handler) StartRun(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	var request StartRunRequest
	if err := runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	actor, err := handler.shared.ActorRef(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	definition, err := workflow.CompileDefinition(request.Definition)
	if err != nil {
		writeError(context, err)
		return
	}
	snapshot, err := handler.runner.StartRun(context.Request.Context(), workflow.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: runtimehttp.NormalizeThread(request.Thread),
		RequestID: handler.shared.RequestID(context), Goal: strings.TrimSpace(request.Goal),
		Definition: definition, Input: append([]byte(nil), request.Input...),
	})
	if err != nil && !errors.Is(err, workflow.ErrWaitPending) {
		writeError(context, err)
		return
	}
	context.JSON(stdhttp.StatusAccepted, runtimehttp.SnapshotResponse(snapshot))
}

func (handler *Handler) ResolveWait(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	runID, err := runtimehttp.RunIDParam(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.invalid_request", err.Error())
		return
	}
	var request ResolveWaitRequest
	if err = runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	snapshot, err := handler.runner.ResolveWait(context.Request.Context(), runID, request.ExpectedRevision, request.Response)
	if err != nil && !errors.Is(err, workflow.ErrWaitPending) {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, runtimehttp.SnapshotResponse(snapshot))
}

func writeError(context *gin.Context, err error) {
	if errors.Is(err, workflow.ErrInvalidDefinition) || errors.Is(err, workflow.ErrInvalidExecution) {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.invalid_request", "invalid runtime request")
		return
	}
	runtimehttp.WriteKernelError(context, "workflow", err)
}
