package agenthttp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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
	if len(routes) != 1 || routes[0].Method != http.MethodPost || routes[0].Path != "/v1/agent-runs" {
		t.Fatalf("routes = %#v", routes)
	}
}

type outputCeilingModel struct{ requests []model.Request }

func (client *outputCeilingModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, request)
	return model.Response{Content: "done"}, nil
}

func TestStartRunModelOutputCeilingAdmissionAndDispatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name   string
		field  string
		status int
		limit  int64
	}{
		{name: "omitted", status: http.StatusAccepted},
		{name: "zero", field: `,"maxModelOutputTokens":0`, status: http.StatusAccepted},
		{name: "bounded", field: `,"maxModelOutputTokens":1024`, status: http.StatusAccepted, limit: 1024},
		{name: "negative", field: `,"maxModelOutputTokens":-1`, status: http.StatusBadRequest},
		{name: "fractional", field: `,"maxModelOutputTokens":1.5`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			client := &outputCeilingModel{}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client})
			if err != nil {
				t.Fatal(err)
			}
			shared := runtimehttp.NewShared(replayHTTPPrincipal{actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}}, nil, runtime, nil)
			router := gin.New()
			NewModule(NewHandler(Dependencies{Runner: runner, Shared: shared})).RegisterRoutes(router.Group("/v1"))
			body := fmt.Sprintf(`{"thread":{"kind":"conversation","id":"test"},"input":{"content":"Answer"}%s}`, test.field)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/agent-runs", strings.NewReader(body)))
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusBadRequest {
				if len(client.requests) != 0 {
					t.Fatal("invalid ceiling reached the model")
				}
			} else if len(client.requests) != 1 || client.requests[0].MaxOutputTokens != test.limit {
				t.Fatalf("model ceiling not preserved: %+v", client.requests)
			}
		})
	}
}

func TestStartRunRejectsUnavailableAndInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unavailable", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/agent-runs", strings.NewReader("{}"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{}).StartRun(context)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d", response.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodPost, "/v1/agent-runs", strings.NewReader("{"))
		context.Request.Header.Set("Content-Type", "application/json")

		NewHandler(Dependencies{Runner: &agent.Runner{}}).StartRun(context)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", response.Code)
		}
	})
}
