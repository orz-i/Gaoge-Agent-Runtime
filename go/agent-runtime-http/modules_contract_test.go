package http_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	agenthttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/agent"
	planhttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/planexecute"
	teamhttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/team"
	workflowhttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/workflow"
)

func TestExplicitFeatureModulesComposePublishedRoutes(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	runtimehttp.NewModule(
		runtimehttp.NewHandler(runtimehttp.Dependencies{}),
		agenthttp.NewModule(agenthttp.NewHandler(agenthttp.Dependencies{})),
		planhttp.NewModule(planhttp.NewHandler(planhttp.Dependencies{})),
		workflowhttp.NewModule(workflowhttp.NewHandler(workflowhttp.Dependencies{})),
		teamhttp.NewModule(teamhttp.NewHandler(teamhttp.Dependencies{})),
	).RegisterRoutes(engine.Group("/api/v1"))

	operations := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		operations = append(operations, route.Method+" "+route.Path)
	}
	sort.Strings(operations)
	expected := []string{
		"GET /api/v1/runs/:run_id",
		"GET /api/v1/runs/:run_id/feed",
		"GET /api/v1/runs/:run_id/workbench",
		"GET /api/v1/workflow-definitions",
		"GET /api/v1/workflow-definitions/:definition_id/revisions/:revision",
		"GET /api/v1/workflow-runs/:run_id/trace",
		"POST /api/v1/agent-runs",
		"POST /api/v1/plan-runs",
		"POST /api/v1/plan-runs/:run_id/approval",
		"POST /api/v1/runs/:run_id/cancel",
		"POST /api/v1/team-runs",
		"POST /api/v1/workflow-definitions",
		"POST /api/v1/workflow-definitions/:definition_id/activation",
		"POST /api/v1/workflow-definitions/compile",
		"POST /api/v1/workflow-runs",
		"POST /api/v1/workflow-runs/:run_id/cancel",
		"POST /api/v1/workflow-runs/:run_id/wait",
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("routes = %#v, want %#v", operations, expected)
	}
}
