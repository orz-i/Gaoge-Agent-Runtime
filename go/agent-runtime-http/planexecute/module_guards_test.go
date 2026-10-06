package planexecutehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
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
	if len(routes) != 2 {
		t.Fatalf("routes = %#v", routes)
	}
	got := map[string]bool{}
	for _, route := range routes {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		http.MethodPost + " /v1/plan-runs",
		http.MethodPost + " /v1/plan-runs/:run_id/approval",
	} {
		if !got[want] {
			t.Fatalf("missing route %q in %#v", want, routes)
		}
	}
}

func TestStartRunRejectsUnavailableAndInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unavailable", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/plan-runs", strings.NewReader("{}"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{}).StartRun(context)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/plan-runs", strings.NewReader("{"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{Runner: &planexecute.Runner{}}).StartRun(context)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", response.Code)
		}
	})
}

func TestResolveApprovalRejectsUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPost, "/v1/plan-runs/run-1/approval", strings.NewReader("{}"))

	NewHandler(Dependencies{}).ResolveApproval(context)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.Code)
	}
}
