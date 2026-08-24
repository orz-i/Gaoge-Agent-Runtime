package teamhttp

import (
	"errors"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

type Dependencies struct {
	Runner *team.Runner
	Shared *runtimehttp.Shared
}

type Handler struct {
	runner *team.Runner
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
	routes.POST("/team-runs", module.Handler.StartRun)
}

type MemberRequest struct {
	ID       string   `json:"id" binding:"required,max=128"`
	Goal     string   `json:"goal" binding:"required,max=200000"`
	ToolKeys []string `json:"toolKeys" binding:"omitempty,max=128,dive,max=255"`
}

type JoinRequest struct {
	Mode          handoff.JoinMode      `json:"mode" binding:"required,oneof=all any quorum"`
	Quorum        int                   `json:"quorum" binding:"omitempty,min=1,max=16"`
	FailurePolicy handoff.FailurePolicy `json:"failurePolicy" binding:"omitempty,oneof=collect fail_fast"`
}

type StartRunRequest struct {
	Thread      runtimehttp.ThreadRequest `json:"thread" binding:"required"`
	Goal        string                    `json:"goal" binding:"required,max=200000"`
	ClientRunID string                    `json:"clientRunID" binding:"omitempty,max=64"`
	Mode        team.ExecutionMode        `json:"mode" binding:"required,oneof=sequential parallel"`
	Members     []MemberRequest           `json:"members" binding:"required,min=1,max=16,dive"`
	Join        JoinRequest               `json:"join" binding:"required"`
}

func (handler *Handler) StartRun(context *gin.Context) {
	if handler == nil || handler.runner == nil {
		runtimehttp.WriteError(context, stdhttp.StatusServiceUnavailable, "team.unavailable", "team runtime is unavailable")
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
	snapshot, err := handler.runner.StartRun(context.Request.Context(), team.StartRequest{
		ID: strings.TrimSpace(request.ClientRunID), Actor: actor, Thread: runtimehttp.NormalizeThread(request.Thread),
		RequestID: handler.shared.RequestID(context), Goal: strings.TrimSpace(request.Goal),
		Mode: request.Mode, Members: members,
		Join: handoff.Join{Mode: request.Join.Mode, Quorum: request.Join.Quorum, FailurePolicy: failurePolicy},
	})
	if err != nil && !errors.Is(err, team.ErrMemberPending) {
		if errors.Is(err, team.ErrInvalidRequest) {
			runtimehttp.WriteError(context, stdhttp.StatusBadRequest, "team.invalid_request", "invalid runtime request")
			return
		}
		runtimehttp.WriteKernelError(context, "team", err)
		return
	}
	context.JSON(stdhttp.StatusAccepted, runtimehttp.SnapshotResponse(snapshot))
}
