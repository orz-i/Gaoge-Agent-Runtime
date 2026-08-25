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
	Runner     *workflow.Runner
	Registry   *workflow.DefinitionRegistry
	Authorizer DefinitionAuthorizer
	Shared     *runtimehttp.Shared
}

type Handler struct {
	runner     *workflow.Runner
	registry   *workflow.DefinitionRegistry
	authorizer DefinitionAuthorizer
	shared     *runtimehttp.Shared
}

func NewHandler(dependencies Dependencies) *Handler {
	return &Handler{
		runner: dependencies.Runner, registry: dependencies.Registry,
		authorizer: dependencies.Authorizer, shared: dependencies.Shared,
	}
}

type Module struct{ Handler *Handler }

func NewModule(handler *Handler) *Module { return &Module{Handler: handler} }

func (module *Module) RegisterRoutes(routes *gin.RouterGroup) {
	if module == nil || module.Handler == nil || routes == nil {
		return
	}
	routes.POST("/workflow-runs", module.Handler.StartRun)
	routes.GET("/workflow-runs/:run_id/trace", module.Handler.GetTrace)
	routes.POST("/workflow-runs/:run_id/wait", module.Handler.ResolveWait)
	routes.POST("/workflow-runs/:run_id/cancel", module.Handler.CancelRun)
	routes.POST("/workflow-definitions/compile", module.Handler.CompileDefinition)
	routes.POST("/workflow-definitions", module.Handler.PublishDefinition)
	routes.GET("/workflow-definitions", module.Handler.ListDefinitions)
	routes.GET(
		"/workflow-definitions/:definition_id/revisions/:revision",
		module.Handler.GetDefinition,
	)
	routes.POST(
		"/workflow-definitions/:definition_id/activation",
		module.Handler.SetDefinitionActivation,
	)
}

type StartRunRequest struct {
	Thread              runtimehttp.ThreadRequest     `json:"thread" binding:"required"`
	Input               json.RawMessage               `json:"input" binding:"required"`
	ClientRunID         string                        `json:"clientRunID" binding:"omitempty,max=64"`
	Goal                string                        `json:"goal" binding:"required,max=200000"`
	Definition          *workflow.DefinitionDraft     `json:"definition,omitempty"`
	DefinitionReference *workflow.DefinitionReference `json:"definitionReference,omitempty"`
}

type ResolveWaitRequest struct {
	ExpectedRevision uint64          `json:"expectedRevision" binding:"required,min=1"`
	Response         json.RawMessage `json:"response" binding:"required"`
}

type CancelRunRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Reason           string `json:"reason" binding:"omitempty,max=2000"`
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
	actor, err := handler.actor(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", err.Error())
		return
	}
	definition, definitionScope, err := handler.resolveStartDefinition(
		context.Request.Context(), actor, request,
	)
	if err != nil {
		writeError(context, err)
		return
	}
	if err = handler.authorize(context.Request.Context(), DefinitionAuthorization{
		Actor: actor, Action: DefinitionActionStart, Scope: definitionScope,
		Definition: &definition,
	}, request.DefinitionReference != nil); err != nil {
		writeError(context, err)
		return
	}
	snapshot, err := handler.runner.StartRun(context.Request.Context(), workflow.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: runtimehttp.NormalizeThread(request.Thread),
		RequestID: handler.shared.RequestID(context), Goal: strings.TrimSpace(request.Goal),
		Definition: definition, Input: append([]byte(nil), request.Input...),
	})
	if err != nil && !isProgressError(err) {
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
	if err = handler.authorizeRunOwner(context, runID); err != nil {
		writeError(context, err)
		return
	}
	var request ResolveWaitRequest
	if err = runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	snapshot, err := handler.runner.ResolveWait(context.Request.Context(), runID, request.ExpectedRevision, request.Response)
	if err != nil && !isProgressError(err) {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, runtimehttp.SnapshotResponse(snapshot))
}

func (handler *Handler) CancelRun(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	runID, err := runtimehttp.RunIDParam(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.invalid_request", err.Error())
		return
	}
	if err = handler.authorizeRunOwner(context, runID); err != nil {
		writeError(context, err)
		return
	}
	var request CancelRunRequest
	if err = runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	snapshot, err := handler.runner.Cancel(
		context.Request.Context(), runID, request.ExpectedRevision, strings.TrimSpace(request.Reason),
	)
	if err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, runtimehttp.SnapshotResponse(snapshot))
}

func (handler *Handler) GetTrace(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "workflow.unavailable", "workflow runtime is unavailable")
		return
	}
	runID, err := runtimehttp.RunIDParam(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.invalid_request", err.Error())
		return
	}
	actor, err := handler.actor(context)
	if err != nil {
		writeError(context, err)
		return
	}
	trace, err := handler.runner.TraceForActor(context.Request.Context(), runID, actor)
	if err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, trace)
}

func (handler *Handler) authorizeRunOwner(context *gin.Context, runID string) error {
	actor, err := handler.actor(context)
	if err != nil {
		return err
	}
	snapshot, err := handler.runner.LoadRun(context.Request.Context(), runID)
	if err != nil {
		return err
	}
	if snapshot.Run.Actor != actor {
		return ErrDefinitionForbidden
	}
	return nil
}

func isProgressError(err error) bool {
	return errors.Is(err, workflow.ErrWaitPending) || errors.Is(err, workflow.ErrEffectPending) ||
		errors.Is(err, workflow.ErrSegmentYielded)
}

func writeError(context *gin.Context, err error) {
	if errors.Is(err, workflow.ErrInvalidDefinition) || errors.Is(err, workflow.ErrInvalidExecution) {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "workflow.invalid_request", "invalid runtime request")
		return
	}
	if errors.Is(err, workflow.ErrBudgetExceeded) {
		runtimehttp.WriteError(context, stdhttp.StatusUnprocessableEntity, "workflow.budget_exceeded", "workflow budget exceeded")
		return
	}
	if writeDefinitionError(context, err) {
		return
	}
	runtimehttp.WriteKernelError(context, "workflow", err)
}
