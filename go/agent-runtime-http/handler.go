package http

import (
	stdcontext "context"
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

func (handler *Handler) authorizedRun(
	context *gin.Context,
	runID string,
	operation RunOperation,
) (kernel.Snapshot, error) {
	if handler == nil || handler.shared == nil {
		return kernel.Snapshot{}, errRunAuthorizationUnavailable
	}
	return handler.shared.AuthorizedRun(context, runID, operation)
}

// AuthorizedRun loads the authoritative Run and applies host object policy.
// Explicit authorization denials are intentionally collapsed to not-found so
// callers cannot distinguish another principal's object from an absent one.
func (shared *Shared) AuthorizedRun(
	context *gin.Context,
	runID string,
	operation RunOperation,
) (kernel.Snapshot, error) {
	if shared == nil || shared.runs == nil || shared.authorizer == nil {
		return kernel.Snapshot{}, errRunAuthorizationUnavailable
	}
	principal, err := shared.ActorRef(context)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	snapshot, err := shared.runs.Load(context.Request.Context(), strings.TrimSpace(runID))
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if err = shared.authorizer.AuthorizeRun(
		context.Request.Context(), principal, snapshot.Run, operation,
	); err != nil {
		if errors.Is(err, ErrRunForbidden) {
			return kernel.Snapshot{}, kernel.ErrNotFound
		}
		return kernel.Snapshot{}, err
	}
	return snapshot, nil
}

// RunOperation identifies one object-level HTTP action against an existing Run.
type RunOperation string

const (
	RunOperationRead            RunOperation = "run.read"
	RunOperationEventsRead      RunOperation = "run.events.read"
	RunOperationCancel          RunOperation = "run.cancel"
	RunOperationFeedRead        RunOperation = "run.feed.read"
	RunOperationWorkbenchRead   RunOperation = "run.workbench.read"
	RunOperationApprovalResolve RunOperation = "run.approval.resolve"
	RunOperationWaitResolve     RunOperation = "run.wait.resolve"
	RunOperationTraceRead       RunOperation = "run.trace.read"
)

// RunLoader is the minimum authoritative Run source needed by the HTTP edge.
type RunLoader interface {
	Load(stdcontext.Context, string) (kernel.Snapshot, error)
}

// RunAuthorizer is the host-owned object authorization seam. Implementations
// may consult request-scoped RBAC/tenant claims from ctx; Kernel remains policy-free.
type RunAuthorizer interface {
	AuthorizeRun(stdcontext.Context, kernel.ActorRef, kernel.Run, RunOperation) error
}

// OwnerRunAuthorizer is the fail-closed default policy for HTTP composition.
type OwnerRunAuthorizer struct{}

func (OwnerRunAuthorizer) AuthorizeRun(
	_ stdcontext.Context,
	principal kernel.ActorRef,
	run kernel.Run,
	_ RunOperation,
) error {
	if principal != run.Actor {
		return ErrRunForbidden
	}
	return nil
}

// RunIDParam resolves the stable Run path parameter for feature modules.
func RunIDParam(context *gin.Context) (string, error) { return runIDParam(context) }

type RequestMetadata struct{ RequestID string }

type RequestMetadataResolver interface {
	ResolveRequestMetadata(*gin.Context) RequestMetadata
}

// Shared exposes only host request metadata needed by independently mounted feature modules.
type Shared struct {
	principal  PrincipalResolver
	metadata   RequestMetadataResolver
	runs       RunLoader
	authorizer RunAuthorizer
}

// NewShared constructs the host HTTP context shared by core and feature modules.
func NewShared(
	principal PrincipalResolver,
	metadata RequestMetadataResolver,
	runs RunLoader,
	authorizer RunAuthorizer,
) *Shared {
	if authorizer == nil {
		authorizer = OwnerRunAuthorizer{}
	}
	return &Shared{principal: principal, metadata: metadata, runs: runs, authorizer: authorizer}
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
	Runtime       *kernel.Runtime
	Workbench     *workbench.Query
	Feed          *runfeed.Feed
	Cancellations *CancellationRouter
	Shared        *Shared
}

type Handler struct {
	runtime       *kernel.Runtime
	workbench     *workbench.Query
	feed          *runfeed.Feed
	cancellations *CancellationRouter
	shared        *Shared
}

func NewHandler(dependencies Dependencies) *Handler {
	return &Handler{
		runtime: dependencies.Runtime, workbench: dependencies.Workbench,
		feed: dependencies.Feed, cancellations: dependencies.Cancellations,
		shared: dependencies.Shared,
	}
}

const requestIDContextKey = "agent-runtime.request-id"

func requestID(context *gin.Context) string {
	value, _ := context.Get(requestIDContextKey)
	result, _ := value.(string)
	return result
}

var (
	errRequiredPathParameter       = errors.New("required path parameter is missing")
	errPrincipalUnavailable        = errors.New("runtime principal is unavailable")
	errRunAuthorizationUnavailable = errors.New("runtime run authorization is unavailable")
	ErrRunForbidden                = errors.New("runtime run access forbidden")
)

func runIDParam(context *gin.Context) (string, error) {
	value := strings.TrimSpace(context.Param("run_id"))
	if value == "" {
		return "", errRequiredPathParameter
	}
	return value, nil
}
