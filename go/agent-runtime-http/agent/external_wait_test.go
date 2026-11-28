package agenthttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type waitProjectionModel struct{}

func (waitProjectionModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{ID: "wait-call", ToolKey: "test.http_wait", Arguments: json.RawMessage(`{}`)}}}, nil
}

func TestPendingToolSnapshotPublicProjectionHasNoHostCorrelation(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "test.http_wait", Name: "http_wait", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			deadline := time.Now().Add(time.Minute)
			return tools.ExecutionResult{Content: json.RawMessage(`null`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
				Pending: &tools.PendingExecution{Kind: tools.PendingExternalResult, OperationID: "private-host-operation", InputDigest: strings.Repeat("b", 64), DeadlineAt: &deadline}}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: waitProjectionModel{}, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), agent.StartRequest{ID: "http-wait", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "wait", ToolKeys: []string{"test.http_wait"}})
	if err != nil {
		t.Fatal(err)
	}
	response := runtimehttp.SnapshotResponse(snapshot)
	if response.PendingTool == nil || response.PendingTool.Kind != tools.PendingExternalResult || response.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("response=%#v", response)
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-host-operation", strings.Repeat("b", 64), "externalSignals", "toolWait", "toolReturns"} {
		if strings.Contains(string(data), private) {
			t.Fatalf("correlation %q leaked: %s", private, data)
		}
	}
	for _, authorized := range []bool{true, false} {
		actor := snapshot.Run.Actor
		if !authorized {
			actor.ActorID = "other-actor"
		}
		shared := runtimehttp.NewShared(waitProjectionPrincipal{actor: actor}, nil, runtime, nil)
		handler := runtimehttp.NewHandler(runtimehttp.Dependencies{
			Runtime: runtime, Shared: shared,
		})
		router := gin.New()
		runtimehttp.NewModule(handler).RegisterRoutes(router.Group("/v1"))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/runs/http-wait", nil))
		if !authorized {
			if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "pendingTool") {
				t.Fatalf("denied status=%d body=%s", recorder.Code, recorder.Body)
			}
			continue
		}
		var actual runtimehttp.RunSnapshotResponse
		if err = json.Unmarshal(recorder.Body.Bytes(), &actual); err != nil || recorder.Code != http.StatusOK || actual.PendingTool == nil {
			t.Fatalf("authorized response=%s err=%v", recorder.Body, err)
		}
		for _, private := range []string{"private-host-operation", strings.Repeat("b", 64), "externalSignals", "toolWait", "toolReturns"} {
			if strings.Contains(recorder.Body.String(), private) {
				t.Fatalf("HTTP correlation %q leaked: %s", private, recorder.Body)
			}
		}
	}
}

type waitProjectionPrincipal struct{ actor kernel.ActorRef }

func (principal waitProjectionPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}
