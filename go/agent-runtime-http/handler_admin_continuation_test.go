package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	app "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestContinuationJobResponseRedactsSegmentKeyAndIncludesReceipt(t *testing.T) {
	now := time.Now().UTC()
	response := continuationJobResponseFromDomain(app.ContinuationJobInspection{
		Job: domain.ContinuationJob{
			JobID: "continuation-1", SegmentKey: "secret-segment-key", RunID: "run-1", CheckpointID: "checkpoint-1",
			Actor: domain.ActorRef{TenantID: "tenant-1", ActorID: "actor-1"}, Source: "worker", Status: domain.ContinuationJobDeadLetter,
			ReservationAmountNanousd: 42, ReservationRefNo: "reservation-1", AttemptCount: 5, MaxAttempts: 5,
			AvailableAt: now, LastError: strings.Repeat("x", 600), CreatedAt: now, UpdatedAt: now,
		},
		RunStatus: domain.RunStatusRunning, Recoverable: true, RecoveryBlockReason: "ready",
	})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(raw)
	if strings.Contains(encoded, "secret-segment-key") || !strings.Contains(encoded, "sha256:") {
		t.Fatalf("segment key redaction failed: %s", encoded)
	}
	if response.ReservationAmountNanousd != 42 || response.ReservationRefNo != "reservation-1" || !response.Recoverable {
		t.Fatalf("receipt response=%#v", response)
	}
	if len([]rune(response.LastErrorSummary)) != 512 {
		t.Fatalf("last error summary length=%d", len([]rune(response.LastErrorSummary)))
	}
}

func TestContinuationJobFilterRejectsInvalidStatusAndPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, target := range []string{
		"/continuations?status=unknown",
		"/continuations?limit=-1",
		"/continuations?offset=bad",
	} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequestWithContext(t.Context(), stdhttp.MethodGet, target, nil)
		if _, err := continuationJobFilter(context); err == nil {
			t.Fatalf("filter %s unexpectedly succeeded", target)
		}
	}
}

func TestContinuationAdminTerminalErrorIsStableConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	writeContinuationAdminError(context, app.ErrContinuationRunTerminal)
	if recorder.Code != stdhttp.StatusConflict || !strings.Contains(recorder.Body.String(), `"code":"continuation.run_terminal"`) {
		t.Fatalf("response=%d %s", recorder.Code, recorder.Body.String())
	}
}
