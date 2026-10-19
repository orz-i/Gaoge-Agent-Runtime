package harnesshttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestManualNewsCapabilityRefusalReturnsSafeActionableHTTPError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	err := tools.NewRecoverableCallErrorAllowCompletion("delegation.current_evidence_unavailable", "PRIVATE_INTERNAL_MODEL_PROMPT", harness.ErrInvalidRequest)
	writeHarnessError(context, err)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), `"code":"delegation.current_evidence_unavailable"`) ||
		!strings.Contains(recorder.Body.String(), "authorized web research tool") || strings.Contains(recorder.Body.String(), "PRIVATE_INTERNAL_MODEL_PROMPT") {
		t.Fatalf("manual delegation error projection=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
