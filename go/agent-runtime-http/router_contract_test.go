package http

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRouterExposesOnlyTargetRuntimeResources(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	module := NewModule(&Handler{})
	module.RegisterRoutes(engine.Group("/api/v1"))
	routes := engine.Routes()
	operations := make([]string, 0, len(routes))
	for _, route := range routes {
		operations = append(operations, route.Method+" "+route.Path)
	}
	sort.Strings(operations)
	expected := []string{
		"GET /api/v1/runs/:run_id",
		"GET /api/v1/runs/:run_id/workbench",
		"POST /api/v1/plan-runs",
		"POST /api/v1/plan-runs/:run_id/approval",
		"POST /api/v1/runs/:run_id/cancel",
		"POST /api/v1/team-runs",
		"POST /api/v1/text-runs",
		"POST /api/v1/workflow-runs",
		"POST /api/v1/workflow-runs/:run_id/wait",
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("routes = %#v, want %#v", operations, expected)
	}
}
