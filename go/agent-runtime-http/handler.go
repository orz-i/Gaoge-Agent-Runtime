package http

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

// PrincipalResolver maps the authenticated host principal to an opaque Kernel actor.
type PrincipalResolver interface {
	ResolvePrincipal(*gin.Context) (kernel.ActorRef, error)
}

// RunIDParam resolves the stable Run path parameter for feature modules.
func RunIDParam(context *gin.Context) (string, error) { return runIDParam(context) }

type RequestMetadata struct{ RequestID string }

type RequestMetadataResolver interface {
	ResolveRequestMetadata(*gin.Context) RequestMetadata
}

// Shared exposes only host request metadata needed by independently mounted feature modules.
type Shared struct {
	principal PrincipalResolver
	metadata  RequestMetadataResolver
}

// NewShared constructs the host HTTP context shared by core and feature modules.
func NewShared(principal PrincipalResolver, metadata RequestMetadataResolver) *Shared {
	return &Shared{principal: principal, metadata: metadata}
}

// ActorRef resolves the authenticated principal to a Kernel actor.
func (shared *Shared) ActorRef(context *gin.Context) (kernel.ActorRef, error) {
	if shared == nil || shared.principal == nil {
		return kernel.ActorRef{}, errPrincipalUnavailable
	}
	return shared.principal.ResolvePrincipal(context)
}

// RequestID returns optional request metadata supplied by the host.
func (shared *Shared) RequestID(context *gin.Context) string {
	if shared == nil || shared.metadata == nil {
		return ""
	}
	return strings.TrimSpace(shared.metadata.ResolveRequestMetadata(context).RequestID)
}

// Dependencies are the feature-neutral Runtime capabilities mounted by the core HTTP adapter.
type Dependencies struct {
	Runtime   *kernel.Runtime
	Workbench *workbench.Query
	Feed      *runfeed.Feed
	Shared    *Shared
}

type Handler struct {
	runtime   *kernel.Runtime
	workbench *workbench.Query
	feed      *runfeed.Feed
	shared    *Shared
}

func NewHandler(dependencies Dependencies) *Handler {
	return &Handler{
		runtime: dependencies.Runtime, workbench: dependencies.Workbench,
		feed: dependencies.Feed, shared: dependencies.Shared,
	}
}

func (handler *Handler) actorRef(context *gin.Context) (kernel.ActorRef, error) {
	if handler == nil || handler.shared == nil {
		return kernel.ActorRef{}, errPrincipalUnavailable
	}
	return handler.shared.ActorRef(context)
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
