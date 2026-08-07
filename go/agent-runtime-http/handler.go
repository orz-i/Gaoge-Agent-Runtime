package http

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/planexecute"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/team"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workbench"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/workflow"
)

// PrincipalResolver maps the authenticated host principal to an opaque Kernel actor.
type PrincipalResolver interface {
	ResolvePrincipal(*gin.Context) (kernel.ActorRef, error)
}

type RequestMetadata struct{ RequestID string }

type RequestMetadataResolver interface {
	ResolveRequestMetadata(*gin.Context) RequestMetadata
}

// Dependencies are the explicit Runtime capabilities mounted by this HTTP adapter.
type Dependencies struct {
	Runtime                 *kernel.Runtime
	Agent                   *agent.Runner
	Plans                   *planexecute.Runner
	Workflows               *workflow.Runner
	Teams                   *team.Runner
	Workbench               *workbench.Query
	PrincipalResolver       PrincipalResolver
	RequestMetadataResolver RequestMetadataResolver
}

type Handler struct {
	runtime   *kernel.Runtime
	agent     *agent.Runner
	plans     *planexecute.Runner
	workflows *workflow.Runner
	teams     *team.Runner
	workbench *workbench.Query
	principal PrincipalResolver
	metadata  RequestMetadataResolver
}

func NewHandler(dependencies Dependencies) *Handler {
	return &Handler{
		runtime: dependencies.Runtime, agent: dependencies.Agent, plans: dependencies.Plans,
		workflows: dependencies.Workflows, teams: dependencies.Teams, workbench: dependencies.Workbench,
		principal: dependencies.PrincipalResolver, metadata: dependencies.RequestMetadataResolver,
	}
}

func (handler *Handler) actorRef(context *gin.Context) (kernel.ActorRef, error) {
	if handler == nil || handler.principal == nil {
		return kernel.ActorRef{}, errPrincipalUnavailable
	}
	return handler.principal.ResolvePrincipal(context)
}

func (handler *Handler) requestID(context *gin.Context) string {
	if handler == nil || handler.metadata == nil {
		return ""
	}
	return strings.TrimSpace(handler.metadata.ResolveRequestMetadata(context).RequestID)
}

const requestIDContextKey = "agent-runtime.request-id"

func requestID(context *gin.Context) string {
	value, _ := context.Get(requestIDContextKey)
	result, _ := value.(string)
	return result
}

var (
	errRequiredPathParameter = errors.New("required path parameter is missing")
	errPrincipalUnavailable  = errors.New("runtime principal is unavailable")
)

func runIDParam(context *gin.Context) (string, error) {
	value := strings.TrimSpace(context.Param("run_id"))
	if value == "" {
		return "", errRequiredPathParameter
	}
	return value, nil
}
