package agenthttp

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

type Dependencies struct {
	Runner *agent.Runner
	Shared *runtimehttp.Shared
}

type Handler struct {
	runner *agent.Runner
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
	routes.POST("/agent-runs", module.Handler.StartRun)
}

type StartRunRequest struct {
	Thread      runtimehttp.ThreadRequest    `json:"thread" binding:"required"`
	Input       runtimehttp.TextInputRequest `json:"input" binding:"required"`
	ClientRunID string                       `json:"clientRunID" binding:"omitempty,max=64"`
	Model       string                       `json:"model" binding:"omitempty,max=128"`
	ToolKeys    []string                     `json:"toolKeys" binding:"omitempty,max=128,dive,max=255"`
}

func (handler *Handler) StartRun(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "agent.unavailable", "agent runtime is unavailable")
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
	snapshot, err := handler.runner.StartRun(context.Request.Context(), agent.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: runtimehttp.NormalizeThread(request.Thread),
		RequestID: handler.shared.RequestID(context), Goal: strings.TrimSpace(request.Input.Content),
		Model: strings.TrimSpace(request.Model), ToolKeys: append([]string(nil), request.ToolKeys...),
	})
	if err != nil {
		if errors.Is(err, agent.ErrInvalidRequest) {
			runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "agent.invalid_request", "invalid agent run request")
			return
		}
		runtimehttp.WriteKernelError(context, "agent", err)
		return
	}
	context.JSON(stdhttp.StatusAccepted, runtimehttp.SnapshotResponse(snapshot))
}
