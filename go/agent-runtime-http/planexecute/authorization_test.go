package planexecutehttp

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/planexecute"
)

func TestResolveApprovalHidesCrossTenantRun(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	owner := kernel.ActorRef{TenantID: "tenant-a", ActorID: "owner"}
	intruder := kernel.ActorRef{TenantID: "tenant-b", ActorID: "intruder"}
	_, err = runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "private-plan", Kind: planexecute.RunKind, Actor: owner,
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "plan", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := planexecute.NewRunner(planexecute.Dependencies{
		Runtime: runtime, Planner: authorizationPlanner{}, Agent: authorizationAgent{},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	shared := runtimehttp.NewShared(authorizationPrincipal{actor: intruder}, nil, runtime, nil)
	NewModule(NewHandler(Dependencies{Runner: runner, Shared: shared})).RegisterRoutes(engine.Group("/api/v1"))

	request := httptest.NewRequestWithContext(
		t.Context(), stdhttp.MethodPost, "/api/v1/plan-runs/private-plan/approval",
		bytes.NewBufferString(`{"expectedRevision":1,"decision":"approve"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != stdhttp.StatusNotFound {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	current, err := runtime.Load(t.Context(), "private-plan")
	if err != nil || current.Run.Revision != 1 || current.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("cross-tenant approval mutated run: %#v err=%v", current.Run, err)
	}
}

type authorizationPrincipal struct{ actor kernel.ActorRef }

func (principal authorizationPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}

type authorizationPlanner struct{}

func (authorizationPlanner) GeneratePlan(context.Context, planexecute.PlannerRequest) (planexecute.PlannerResponse, error) {
	return planexecute.PlannerResponse{}, nil
}

type authorizationAgent struct{}

func (authorizationAgent) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, nil
}

func (authorizationAgent) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrNotFound
}
