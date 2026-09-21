package consumer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type hostPrincipal struct{}

func (hostPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, nil
}

type authorizerContract interface {
	AuthorizeRun(context.Context, kernel.ActorRef, kernel.Run, runtimehttp.RunOperation) error
}

var (
	_ runtimehttp.PrincipalResolver                                                                                                                  = hostPrincipal{}
	_ runtimehttp.RunAuthorizer                                                                                                                      = (authorizerContract)(nil)
	_ authorizerContract                                                                                                                             = (runtimehttp.RunAuthorizer)(nil)
	_ func(runtimehttp.Dependencies) *runtimehttp.Handler                                                                                            = runtimehttp.NewHandler
	_ func(runtimehttp.PrincipalResolver, runtimehttp.RequestMetadataResolver, runtimehttp.RunLoader, runtimehttp.RunAuthorizer) *runtimehttp.Shared = runtimehttp.NewShared
	_ func(*runtimehttp.Handler, ...runtimehttp.RouteModule) *runtimehttp.Module                                                                     = runtimehttp.NewModule
)

func TestHostMountsHTTP(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	shared := runtimehttp.NewShared(hostPrincipal{}, nil, runtime, nil)
	handler := runtimehttp.NewHandler(runtimehttp.Dependencies{Runtime: runtime, Shared: shared})
	router := gin.New()
	runtimehttp.NewModule(handler).RegisterRoutes(router.Group("/api/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/runs/missing", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("mounted run route returned %d: %s", response.Code, response.Body)
	}
}
