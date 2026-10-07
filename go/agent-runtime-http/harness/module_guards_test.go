package harnesshttp

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
)

func TestRegisterRoutesAndNilComposition(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	group := engine.Group("/v1")

	var nilModule *Module
	nilModule.RegisterRoutes(group)
	NewModule(nil).RegisterRoutes(group)
	NewModule(NewHandler(Dependencies{})).RegisterRoutes(nil)

	handler := NewHandler(Dependencies{})
	if handler == nil {
		t.Fatal("NewHandler returned nil")
	}
	NewModule(handler).RegisterRoutes(group)

	routes := engine.Routes()
	if len(routes) != 9 {
		t.Fatalf("route count = %d, routes = %#v", len(routes), routes)
	}
}

func TestHandlerGuardsUnavailableAndMissingTurnID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("commands unavailable", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodGet, "/v1/harness/commands", nil)

		NewHandler(Dependencies{}).ListCommands(context)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("turn unavailable", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodGet, "/v1/harness/turns/run-1", nil)
		context.Params = gin.Params{{Key: "turn_id", Value: "run-1"}}

		NewHandler(Dependencies{}).GetTurn(context)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("turn id required", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodGet, "/v1/harness/turns/", nil)

		NewHandler(Dependencies{Runner: &harness.Runner{}, Shared: &runtimehttp.Shared{}}).GetTurn(context)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", response.Code)
		}
	})
}
