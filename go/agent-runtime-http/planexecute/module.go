package planexecutehttp

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
)

type Dependencies struct {
	Runner *planexecute.Runner
	Shared *runtimehttp.Shared
}

type Handler struct {
	runner *planexecute.Runner
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
	routes.POST("/plan-runs", module.Handler.StartRun)
	routes.POST("/plan-runs/:run_id/approval", module.Handler.ResolveApproval)
}

type StartRunRequest struct {
	Thread         runtimehttp.ThreadRequest    `json:"thread" binding:"required"`
	Input          runtimehttp.TextInputRequest `json:"input" binding:"required"`
	ClientRunID    string                       `json:"clientRunID" binding:"omitempty,max=64"`
	Model          string                       `json:"model" binding:"omitempty,max=128"`
	ApprovalPolicy string                       `json:"approvalPolicy" binding:"omitempty,oneof=auto required"`
	MaxSteps       int                          `json:"maxSteps" binding:"omitempty,min=1,max=32"`
}

type ResolveApprovalRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Decision         string `json:"decision" binding:"required,oneof=approve reject"`
	Comment          string `json:"comment" binding:"omitempty,max=2000"`
}

func (handler *Handler) StartRun(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
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
	snapshot, err := handler.runner.StartRun(context.Request.Context(), planexecute.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: runtimehttp.NormalizeThread(request.Thread),
		RequestID: handler.shared.RequestID(context), Goal: strings.TrimSpace(request.Input.Content),
		Model: strings.TrimSpace(request.Model), ApprovalPolicy: planexecute.ApprovalPolicy(request.ApprovalPolicy),
		MaxSteps: request.MaxSteps,
	})
	if err != nil && !errors.Is(err, planexecute.ErrApprovalRequired) {
		writeError(context, err)
		return
	}
	context.JSON(stdhttp.StatusAccepted, runtimehttp.SnapshotResponse(snapshot))
}

func (handler *Handler) ResolveApproval(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "planexecute.unavailable", "plan-and-execute is unavailable")
		return
	}
	runID, err := runtimehttp.RunIDParam(context)
	if err != nil {
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "planexecute.invalid_request", err.Error())
		return
	}
	var request ResolveApprovalRequest
	if err = runtimehttp.BindStrictJSON(context, &request); err != nil {
		runtimehttp.InvalidBody(context, err)
		return
	}
	snapshot, err := handler.runner.ResolveApproval(context.Request.Context(), runID, request.ExpectedRevision,
		planexecute.ApprovalResponse{Decision: planexecute.ApprovalDecision(request.Decision), Comment: request.Comment})
	if err != nil {
		writeError(context, err)
		return
	}
	runtimehttp.WriteSuccess(context, runtimehttp.SnapshotResponse(snapshot))
}

func writeError(context *gin.Context, err error) {
	switch {
	case errors.Is(err, planexecute.ErrApprovalRequired):
		runtimehttp.WriteError(context, stdhttp.StatusConflict, "planexecute.approval_required", "plan approval is required")
	case errors.Is(err, planexecute.ErrInvalidApproval):
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "planexecute.invalid_approval", "invalid plan approval")
	case errors.Is(err, planexecute.ErrInvalidRequest), errors.Is(err, planexecute.ErrInvalidPlan):
		runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "planexecute.invalid_request", "invalid plan run request")
	default:
		runtimehttp.WriteKernelError(context, "planexecute", err)
	}
}
