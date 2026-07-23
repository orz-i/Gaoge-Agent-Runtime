package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
)

const (
	valueErrorCode8B63C5B4 = "errorCode"
	valueInput4EDF8CD8     = "input"
	valueModelF176C55D     = "model"
	valueType84963231      = "type"
)

var reservedRunOptionKeys = map[string]struct{}{
	"contents":          {},
	"instructions":      {},
	valueInput4EDF8CD8:  {},
	"messages":          {},
	valueModelF176C55D:  {},
	"prompt":            {},
	"stream":            {},
	"system":            {},
	"systemInstruction": {},
	"tools":             {},
}

func sanitizeRunOptions(options map[string]interface{}) map[string]interface{} {
	if len(options) == 0 {
		return nil
	}
	sanitized := make(map[string]interface{}, len(options))
	for key, value := range options {
		if _, reserved := reservedRunOptionKeys[key]; !reserved {
			sanitized[key] = value
		}
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func shouldReleaseUsageReservationAfterBillingError(err error) bool {
	return runtime.ShouldReleaseRunUsageReservationAfterBillingError(err)
}

func (h *Handler) releaseUsageReservation(reservation *runtime.UsageBalanceReservation, description string) error {
	if reservation == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.service.ReleaseRunUsageReservation(ctx, reservation, description)
}

func usageBillingStreamErrorPayload(err error) map[string]interface{} {
	status := http.StatusInternalServerError
	message := "record billing failed"
	if errors.Is(err, runtime.ErrUsageBalanceInsufficient) {
		status = http.StatusPaymentRequired
		message = "usage balance is insufficient"
	} else if errors.Is(err, runtime.ErrModelPricingRequired) {
		status = http.StatusPaymentRequired
		message = "model pricing is required"
	}
	code := inferErrorCode(status)
	return map[string]interface{}{
		valueType84963231:      "error",
		"message":              publicMessage(status, message),
		valueErrorCode8B63C5B4: code,
	}
}

// CancelRun cancels one text or media run owned by the current user.
func (h *Handler) CancelRun(c *gin.Context) {
	runID, err := stringParam(c, "run_id")
	if err != nil {
		writeError(c, http.StatusBadRequest, "", "invalid run id")
		return
	}
	canceled, cancelErr := h.service.CancelRun(c.Request.Context(), h.actorRef(c), runID)
	if errors.Is(cancelErr, runtime.ErrRunCancelUnavailable) {
		writeError(c, http.StatusServiceUnavailable, "run.cancel_unavailable", "run cancellation is temporarily unavailable; retry shortly")
		return
	}
	if cancelErr != nil {
		writeError(c, http.StatusInternalServerError, "", "failed to cancel run")
		return
	}
	writeSuccess(c, CancelRunResponse{Canceled: canceled})
}
