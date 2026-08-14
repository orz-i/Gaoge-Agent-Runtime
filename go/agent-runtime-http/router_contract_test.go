package http

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCoreRouterExposesOnlyFeatureNeutralResources(t *testing.T) {
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
		"GET /api/v1/runs/:run_id/feed",
		"GET /api/v1/runs/:run_id/workbench",
		"POST /api/v1/runs/:run_id/cancel",
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("routes = %#v, want %#v", operations, expected)
	}
}
