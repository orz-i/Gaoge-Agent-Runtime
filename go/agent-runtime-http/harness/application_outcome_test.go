package harnesshttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestApplicationUnknownOutcomeReturnsSafeConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	writeHarnessError(ctx, errors.Join(harness.ErrApplicationOutcomeUnknown, errors.New("PRIVATE HOST EFFECT")))
	if recorder.Code != http.StatusConflict ||
		!strings.Contains(recorder.Body.String(), "harness.application_outcome_unknown") ||
		strings.Contains(recorder.Body.String(), "PRIVATE HOST EFFECT") {
		t.Fatalf("error projection=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
