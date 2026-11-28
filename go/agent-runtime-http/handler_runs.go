package http

import (
	"errors"
	stdhttp "net/http"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func (handler *Handler) GetRun(context *gin.Context) {
	if handler == nil || handler.runtime == nil {
		writeError(context, stdhttp.StatusServiceUnavailable, "run.unavailable", "runtime is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, stdhttp.StatusBadRequest, "run.invalid_request", err.Error())
		return
	}
	snapshot, err := handler.authorizedRun(context, runID, RunOperationRead)
	if err != nil {
		WriteRunAccessError(context, "run", err)
		return
	}
	handler.releaseTerminalRunFeed(context, snapshot)
	writeSuccess(context, SnapshotResponse(snapshot))
}

// WriteRunAccessError preserves authentication failures while collapsing an
// object-level denial to the same response as an absent Run.
func WriteRunAccessError(context *gin.Context, capability string, err error) {
	switch {
	case errors.Is(err, errPrincipalUnavailable):
		writeError(context, stdhttp.StatusUnauthorized, "auth.unauthorized", "runtime principal is unavailable")
	case errors.Is(err, errRunAuthorizationUnavailable):
		writeError(context, stdhttp.StatusServiceUnavailable, capability+".unavailable", "run authorization is unavailable")
	default:
		WriteKernelError(context, capability, err)
	}
}

func (handler *Handler) CancelRun(context *gin.Context) {
	if handler == nil || handler.runtime == nil {
		writeError(context, stdhttp.StatusServiceUnavailable, "run.unavailable", "runtime is unavailable")
		return
	}
	runID, err := runIDParam(context)
	if err != nil {
		writeError(context, stdhttp.StatusBadRequest, "run.invalid_request", err.Error())
		return
	}
	current, err := handler.authorizedRun(context, runID, RunOperationCancel)
	if err != nil {
		WriteRunAccessError(context, "run", err)
		return
	}
	var request CancelRunRequest
	if err = bindStrictJSON(context, &request); err != nil {
		invalidBody(context, err)
		return
	}
	if current.Run.Revision != request.ExpectedRevision {
		WriteKernelError(context, "run", kernel.ErrConflict)
		return
	}
	canceller := handler.canceller
	if canceller == nil {
		if current.Run.Kind == handoff.RemoteAgentRunKind {
			writeError(context, stdhttp.StatusServiceUnavailable, "run.unavailable", "remote run owner is unavailable")
			return
		}
		canceller = handler.runtime
	}
	snapshot, err := canceller.Cancel(context.Request.Context(), runID, request.ExpectedRevision, request.Reason)
	if err != nil {
		WriteKernelError(context, "run", err)
		return
	}
	writeSuccess(context, SnapshotResponse(snapshot))
}

func (handler *Handler) releaseTerminalRunFeed(context *gin.Context, snapshot kernel.Snapshot) {
	if handler == nil || handler.feed == nil || !terminalRunStatus(snapshot.Run.Status) {
		return
	}
	_ = handler.feed.ReleaseTerminal(context.Request.Context(), snapshot.Run.ID)
}

// WriteKernelError maps only feature-neutral Kernel failures. Feature modules
// own their private validation and errors before delegating here.
func WriteKernelError(context *gin.Context, capability string, err error) {
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		writeError(context, stdhttp.StatusNotFound, capability+".not_found", "runtime resource not found")
	case errors.Is(err, kernel.ErrConflict):
		writeError(context, stdhttp.StatusConflict, capability+".conflict", "runtime revision conflict")
	case errors.Is(err, kernel.ErrTerminal):
		writeError(context, stdhttp.StatusConflict, capability+".terminal", "runtime is terminal")
	case errors.Is(err, kernel.ErrDeadline):
		writeError(context, stdhttp.StatusUnprocessableEntity, capability+".deadline", "runtime deadline exceeded")
	case errors.Is(err, kernel.ErrInvalidInput):
		writeError(context, stdhttp.StatusBadRequest, capability+".invalid_request", "invalid runtime request")
	default:
		writeError(context, stdhttp.StatusInternalServerError, capability+".failed", err.Error())
	}
}
