package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRunFirstRouteInventoryContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	NewModule(&Handler{}).RegisterRoutes(engine.Group("/api/v1"))

	want := []string{
		"DELETE /api/v1/run-queue/:queue_id",
		"GET /api/v1/agent-manifests", "GET /api/v1/agent-manifests/:manifest_id",
		"GET /api/v1/outputs", "GET /api/v1/outputs/:output_id", "GET /api/v1/outputs/:output_id/versions",
		"GET /api/v1/outputs/:output_id/versions/:version/download", "GET /api/v1/outputs/:output_id/versions/:version/preview",
		"GET /api/v1/run-queue", "GET /api/v1/runs", "GET /api/v1/runs/:run_id",
		"GET /api/v1/runs/:run_id/checkpoints", "GET /api/v1/runs/:run_id/events", "GET /api/v1/runs/:run_id/events/:event_id",
		"GET /api/v1/runs/:run_id/events/history", "GET /api/v1/runs/:run_id/interactions", "GET /api/v1/runs/:run_id/outputs",
		"GET /api/v1/runs/:run_id/handoff-joins", "GET /api/v1/runs/:run_id/handoff-joins/:join_id",
		"GET /api/v1/runs/:run_id/plan", "GET /api/v1/runs/:run_id/provenance", "GET /api/v1/runs/:run_id/task-tree", "GET /api/v1/runs/:run_id/workbench",
		"GET /api/v1/runs/:run_id/result", "GET /api/v1/workflow-definitions", "GET /api/v1/workflow-definitions/:workflow_id",
		"PATCH /api/v1/run-queue/:queue_id", "POST /api/v1/agent-teams", "POST /api/v1/evidence", "POST /api/v1/run-queue",
		"POST /api/v1/run-queue/:queue_id/interrupt-and-send", "POST /api/v1/run-queue/:queue_id/prioritize", "POST /api/v1/runs",
		"POST /api/v1/runs/:run_id/cancel", "POST /api/v1/runs/:run_id/handoff-joins", "POST /api/v1/runs/:run_id/handoffs", "POST /api/v1/runs/:run_id/interactions/:interaction_id/resolve",
		"POST /api/v1/runs/:run_id/resume", "POST /api/v1/runs/:run_id/retire",
		"POST /api/v1/workflows",
	}
	got := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("route count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("route[%d] = %q, want %q", index, got[index], want[index])
		}
	}

	for _, oldPath := range []string{"/api/v1/conversation-runs/run_1", "/api/v1/conversations/thread_1/runs", "/api/v1/conversations/thread_1/run-queue"} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, oldPath, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("old route %s status = %d, want 404", oldPath, response.Code)
		}
	}
}

func TestAdminContinuationRouteInventoryContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	NewModule(&Handler{}).RegisterAdminRoutes(engine.Group("/api/v1/admin"))
	want := []string{
		"GET /api/v1/admin/agentruntime/agent-manifests",
		"GET /api/v1/admin/agentruntime/continuations",
		"GET /api/v1/admin/agentruntime/workflow-definitions",
		"POST /api/v1/admin/agentruntime/agent-manifests",
		"POST /api/v1/admin/agentruntime/agent-manifests/:manifest_id/revisions",
		"POST /api/v1/admin/agentruntime/continuations/:job_id/requeue",
		"POST /api/v1/admin/agentruntime/workflow-definitions",
		"POST /api/v1/admin/agentruntime/workflow-definitions/:workflow_id/revisions",
		"POST /api/v1/admin/agentruntime/workflow-definitions/validate",
	}
	got := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("route count = %d, want %d: %v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("route[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
