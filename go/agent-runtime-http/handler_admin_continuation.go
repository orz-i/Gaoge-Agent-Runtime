package http

import (
	"crypto/sha256"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	app "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var (
	errContinuationStatusUnsupported = errors.New("unsupported continuation status")
	errContinuationQueryNonnegative  = errors.New("continuation query value must be non-negative")
)

type continuationJobResponse struct {
	JobID                    string     `json:"jobID"`
	SegmentFingerprint       string     `json:"segmentFingerprint"`
	RunID                    string     `json:"runID"`
	CheckpointID             string     `json:"checkpointID"`
	TenantID                 string     `json:"tenantID"`
	ActorID                  string     `json:"actorID"`
	Source                   string     `json:"source"`
	Status                   string     `json:"status"`
	RunStatus                string     `json:"runStatus,omitempty"`
	Recoverable              bool       `json:"recoverable"`
	RecoveryBlockReason      string     `json:"recoveryBlockReason,omitempty"`
	ReservationAmountNanousd int64      `json:"reservationAmountNanousd"`
	ReservationRefNo         string     `json:"reservationRefNo,omitempty"`
	AttemptCount             int        `json:"attemptCount"`
	MaxAttempts              int        `json:"maxAttempts"`
	AvailableAt              time.Time  `json:"availableAt"`
	LeaseOwner               string     `json:"leaseOwner,omitempty"`
	LeaseExpiresAt           *time.Time `json:"leaseExpiresAt,omitempty"`
	HeartbeatAt              *time.Time `json:"heartbeatAt,omitempty"`
	LastErrorSummary         string     `json:"lastErrorSummary,omitempty"`
	CreatedAt                time.Time  `json:"createdAt"`
	UpdatedAt                time.Time  `json:"updatedAt"`
}

type requeueContinuationRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) ListContinuationJobs(c *gin.Context) {
	filter, err := continuationJobFilter(c)
	if err != nil {
		writeError(c, stdhttp.StatusBadRequest, "continuation.filter_invalid", err.Error())
		return
	}
	page, err := h.service.ListContinuationJobs(c.Request.Context(), filter)
	if err != nil {
		writeContinuationAdminError(c, err)
		return
	}
	results := make([]continuationJobResponse, 0, len(page.Items))
	for _, item := range page.Items {
		results = append(results, continuationJobResponseFromDomain(item))
	}
	writePage(c, page.Total, results)
}

func (h *Handler) RequeueDeadLetterContinuationJob(c *gin.Context) {
	jobID, err := stringParam(c, "job_id")
	if err != nil {
		writeError(c, stdhttp.StatusBadRequest, "continuation.job_id_required", err.Error())
		return
	}
	var request requeueContinuationRequest
	if err = c.ShouldBindJSON(&request); err != nil {
		invalidBody(c, err)
		return
	}
	job, err := h.service.RequeueDeadLetterContinuationJob(c.Request.Context(), app.RequeueDeadLetterContinuationInput{
		Actor: h.actorRef(c), JobID: jobID, Reason: request.Reason, RequestID: requestID(c),
	})
	if err != nil {
		writeContinuationAdminError(c, err)
		return
	}
	writeSuccess(c, continuationJobResponseFromDomain(*job))
}

func continuationJobFilter(c *gin.Context) (domain.ContinuationJobFilter, error) {
	limit, err := nonnegativeQueryInt(c, "limit", 50)
	if err != nil {
		return domain.ContinuationJobFilter{}, err
	}
	offset, err := nonnegativeQueryInt(c, "offset", 0)
	if err != nil {
		return domain.ContinuationJobFilter{}, err
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && !validContinuationStatus(status) {
		return domain.ContinuationJobFilter{}, fmt.Errorf("%w: %q", errContinuationStatusUnsupported, status)
	}
	return domain.ContinuationJobFilter{
		TenantID: strings.TrimSpace(c.Query("tenantID")),
		ActorID:  strings.TrimSpace(c.Query("actorID")),
		Status:   status,
		RunID:    strings.TrimSpace(c.Query("runID")),
		JobID:    strings.TrimSpace(c.Query("jobID")),
		Source:   strings.TrimSpace(c.Query("source")),
		Limit:    limit,
		Offset:   offset,
	}, nil
}

func nonnegativeQueryInt(c *gin.Context, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%w: %s", errContinuationQueryNonnegative, key)
	}
	return value, nil
}

func validContinuationStatus(status string) bool {
	switch status {
	case domain.ContinuationJobQueued, domain.ContinuationJobRunning, domain.ContinuationJobRetryWait, domain.ContinuationJobCompleted, domain.ContinuationJobDeadLetter:
		return true
	default:
		return false
	}
}

func continuationJobResponseFromDomain(inspection app.ContinuationJobInspection) continuationJobResponse {
	item := inspection.Job
	digest := sha256.Sum256([]byte(item.SegmentKey))
	return continuationJobResponse{
		JobID:                    item.JobID,
		SegmentFingerprint:       fmt.Sprintf("sha256:%x", digest[:12]),
		RunID:                    item.RunID,
		CheckpointID:             item.CheckpointID,
		TenantID:                 item.Actor.TenantID,
		ActorID:                  item.Actor.ActorID,
		Source:                   item.Source,
		Status:                   item.Status,
		RunStatus:                inspection.RunStatus,
		Recoverable:              inspection.Recoverable,
		RecoveryBlockReason:      inspection.RecoveryBlockReason,
		ReservationAmountNanousd: item.ReservationAmountNanousd,
		ReservationRefNo:         item.ReservationRefNo,
		AttemptCount:             item.AttemptCount,
		MaxAttempts:              item.MaxAttempts,
		AvailableAt:              item.AvailableAt,
		LeaseOwner:               item.LeaseOwner,
		LeaseExpiresAt:           item.LeaseExpiresAt,
		HeartbeatAt:              item.HeartbeatAt,
		LastErrorSummary:         truncatePublicContinuationError(item.LastError),
		CreatedAt:                item.CreatedAt,
		UpdatedAt:                item.UpdatedAt,
	}
}

func truncatePublicContinuationError(value string) string {
	runes := []rune(strings.TrimSpace(value))
	const maxRunes = 512
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes-1]) + "…"
}

func writeContinuationAdminError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, app.ErrInvalidInput):
		writeError(c, stdhttp.StatusBadRequest, "continuation.invalid", "invalid continuation request")
	case errors.Is(err, app.ErrNotFound):
		writeError(c, stdhttp.StatusNotFound, "continuation.not_found", "continuation job not found")
	case errors.Is(err, app.ErrContinuationJobConflict):
		writeError(c, stdhttp.StatusConflict, "continuation.conflict", "continuation job is not recoverable")
	case errors.Is(err, app.ErrContinuationRunTerminal):
		writeError(c, stdhttp.StatusConflict, "continuation.run_terminal", "terminal runs cannot be requeued")
	default:
		writeError(c, stdhttp.StatusInternalServerError, "continuation.internal", "continuation operation failed")
	}
}
