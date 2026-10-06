package teamhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
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
	if len(routes) != 1 || routes[0].Method != http.MethodPost || routes[0].Path != "/v1/team-runs" {
		t.Fatalf("routes = %#v", routes)
	}
}

func TestStartRunRejectsUnavailableAndInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unavailable", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/team-runs", strings.NewReader("{}"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{}).StartRun(context)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/team-runs", strings.NewReader("{"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{Runner: &team.Runner{}}).StartRun(context)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", response.Code)
		}
	})
}
