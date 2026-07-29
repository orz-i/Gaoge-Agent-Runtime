package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
)

func (h *Handler) GetRunExecutionProvenance(c *gin.Context) {
	runID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "run.provenance_invalid_request", err.Error())
		return
	}
	provenance, err := h.service.GetRuntimeExecutionProvenance(
		c.Request.Context(),
		h.actorRef(c),
		runID,
	)
	if err != nil {
		writeRunExecutionProvenanceError(c, err)
		return
	}
	writeSuccess(c, provenance)
}

func writeRunExecutionProvenanceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, runtime.ErrInvalidInput):
		writeError(c, http.StatusBadRequest, "run.provenance_invalid_request", "invalid provenance request")
	case errors.Is(err, runtime.ErrNotFound):
		writeError(c, http.StatusNotFound, "run.not_found", "run not found")
	case errors.Is(err, runtime.ErrRuntimeExecutionProvenanceNotFrozen):
		writeError(c, http.StatusConflict, "run.provenance_not_frozen", "run provenance is available after terminal completion")
	default:
		writeError(c, http.StatusInternalServerError, "run.provenance_unavailable", "run provenance unavailable")
	}
}
