package http

import (
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	agentruntime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
)

var errProviderSpecificWorkspace = errors.New("provider-specific workspace error")

func TestWriteTextRunErrorMapsWorkspaceClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		kind           agentruntime.WorkspaceErrorKind
		wantStatusCode int
	}{
		{name: "invalid input", kind: agentruntime.WorkspaceErrorInvalidInput, wantStatusCode: nethttp.StatusBadRequest},
		{name: "conflict", kind: agentruntime.WorkspaceErrorConflict, wantStatusCode: nethttp.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			writeTextRunError(context, agentruntime.NewWorkspaceError(
				agentruntime.WorkspaceErrorClassification{Kind: test.kind},
				errProviderSpecificWorkspace,
			))
			if recorder.Code != test.wantStatusCode {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.wantStatusCode, recorder.Body.String())
			}
		})
	}
}
