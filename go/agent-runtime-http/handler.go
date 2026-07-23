package http

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
	app "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type PrincipalResolver interface {
	ResolvePrincipal(*gin.Context) (domain.ActorRef, error)
}

type RequestMetadata struct{ RequestID string }

type RequestMetadataResolver interface {
	ResolveRequestMetadata(*gin.Context) RequestMetadata
}

type Dependencies struct {
	PrincipalResolver       PrincipalResolver
	RequestMetadataResolver RequestMetadataResolver
}

type Handler struct {
	service   *app.Engine
	principal PrincipalResolver
	metadata  RequestMetadataResolver
}

func NewHandler(service *app.Engine, dependencies Dependencies) *Handler {
	return &Handler{service: service, principal: dependencies.PrincipalResolver, metadata: dependencies.RequestMetadataResolver}
}

func (h *Handler) actorRef(c *gin.Context) domain.ActorRef {
	if h.principal == nil {
		return domain.ActorRef{}
	}
	actor, _ := h.principal.ResolvePrincipal(c)
	return actor
}

func (h *Handler) requestID(c *gin.Context) string {
	if h.metadata == nil {
		return ""
	}
	return strings.TrimSpace(h.metadata.ResolveRequestMetadata(c).RequestID)
}

const requestIDContextKey = "agent-runtime.request-id"

func requestID(c *gin.Context) string {
	value, _ := c.Get(requestIDContextKey)
	result, _ := value.(string)
	return result
}

func threadRef(kind, id string) domain.ThreadRef {
	return domain.ThreadRef{Kind: strings.TrimSpace(kind), ID: strings.TrimSpace(id)}
}

var errRequiredPathParameter = errors.New("required path parameter is missing")

func stringParam(c *gin.Context, name string) (string, error) {
	value := strings.TrimSpace(c.Param(name))
	if value == "" {
		return "", errRequiredPathParameter
	}
	return value, nil
}

func isEnvironmentModelError(err error) bool { _, _, ok := environmentModelError(err); return ok }

func environmentModelError(err error) (string, string, bool) {
	switch {
	case errors.Is(err, app.ErrEnvironmentModelUnconfigured):
		return "environment.model_unconfigured", "environment has no configured model", true
	case errors.Is(err, app.ErrEnvironmentDefaultUnavailable):
		return "environment.default_model_unavailable", "environment default model is unavailable", true
	case errors.Is(err, app.ErrEnvironmentModelNotAccessible):
		return "environment.model_not_accessible", "model is not accessible to the current user", true
	case errors.Is(err, app.ErrEnvironmentModelNotAuthorized):
		return "environment.model_not_authorized", "model is not authorized by the environment", true
	case errors.Is(err, app.ErrRunEnvironmentUnavailable):
		return "run.environment_unavailable", "environment is unavailable", true
	default:
		return "", "", false
	}
}
