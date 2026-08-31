package http

import (
	"bytes"
	stdcontext "context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

func TestRunObjectAuthorizationHidesCrossTenantResources(t *testing.T) {
	t.Parallel()
	owner := kernel.ActorRef{TenantID: "tenant-a", ActorID: "owner"}
	intruder := kernel.ActorRef{TenantID: "tenant-b", ActorID: "intruder"}
	engine, runtime := newAuthorizationHTTPTest(t, intruder, nil)
	createAuthorizationRun(t, runtime, owner, "private-run")

	private := performAuthorizationRequest(t, engine, stdhttp.MethodGet, "/api/v1/runs/private-run", nil)
	missing := performAuthorizationRequest(t, engine, stdhttp.MethodGet, "/api/v1/runs/missing-run", nil)
	if private.Code != stdhttp.StatusNotFound || missing.Code != stdhttp.StatusNotFound || private.Body.String() != missing.Body.String() {
		t.Fatalf("forbidden=%d %s missing=%d %s", private.Code, private.Body.String(), missing.Code, missing.Body.String())
	}

	workbenchResponse := performAuthorizationRequest(
		t, engine, stdhttp.MethodGet, "/api/v1/runs/private-run/workbench", nil,
	)
	if workbenchResponse.Code != stdhttp.StatusNotFound {
		t.Fatalf("workbench status=%d body=%s", workbenchResponse.Code, workbenchResponse.Body.String())
	}

	cancelResponse := performAuthorizationRequest(
		t, engine, stdhttp.MethodPost, "/api/v1/runs/private-run/cancel",
		bytes.NewBufferString(`{"expectedRevision":1,"reason":"cross tenant"}`),
	)
	if cancelResponse.Code != stdhttp.StatusNotFound {
		t.Fatalf("cancel status=%d body=%s", cancelResponse.Code, cancelResponse.Body.String())
	}
	current, err := runtime.Load(t.Context(), "private-run")
	if err != nil || current.Run.Status != kernel.RunStatusRunning || current.Run.Revision != 1 {
		t.Fatalf("cross-tenant cancel mutated run: %#v err=%v", current.Run, err)
	}
}

func TestRunObjectAuthorizationAllowsHostPolicyOverride(t *testing.T) {
	t.Parallel()
	owner := kernel.ActorRef{TenantID: "tenant-a", ActorID: "owner"}
	admin := kernel.ActorRef{TenantID: "tenant-admin", ActorID: "admin"}
	authorizer := &recordingRunAuthorizer{}
	engine, runtime := newAuthorizationHTTPTest(t, admin, authorizer)
	createAuthorizationRun(t, runtime, owner, "admin-readable")

	response := performAuthorizationRequest(t, engine, stdhttp.MethodGet, "/api/v1/runs/admin-readable", nil)
	if response.Code != stdhttp.StatusOK || len(authorizer.operations) != 1 || authorizer.operations[0] != RunOperationRead {
		t.Fatalf("status=%d body=%s operations=%v", response.Code, response.Body.String(), authorizer.operations)
	}
}

type authorizationPrincipal struct{ actor kernel.ActorRef }

func (principal authorizationPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}

type recordingRunAuthorizer struct{ operations []RunOperation }

func (authorizer *recordingRunAuthorizer) AuthorizeRun(
	_ stdcontext.Context,
	_ kernel.ActorRef,
	_ kernel.Run,
	operation RunOperation,
) error {
	authorizer.operations = append(authorizer.operations, operation)
	return nil
}

func newAuthorizationHTTPTest(
	t *testing.T,
	principal kernel.ActorRef,
	authorizer RunAuthorizer,
) (*gin.Engine, *kernel.Runtime) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	query, err := workbench.NewQuery(runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	NewModule(NewHandler(Dependencies{
		Runtime: runtime, Workbench: query,
		Shared: NewShared(authorizationPrincipal{actor: principal}, nil, runtime, authorizer),
	})).RegisterRoutes(engine.Group("/api/v1"))
	return engine, runtime
}

func createAuthorizationRun(t *testing.T, runtime *kernel.Runtime, actor kernel.ActorRef, runID string) {
	t.Helper()
	_, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: runID, Kind: kernel.RunKind("authorization_test"), Actor: actor,
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "authorization", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func performAuthorizationRequest(
	t *testing.T,
	engine *gin.Engine,
	method string,
	path string,
	body *bytes.Buffer,
) *httptest.ResponseRecorder {
	t.Helper()
	var request *stdhttp.Request
	if body == nil {
		request = httptest.NewRequestWithContext(t.Context(), method, path, nil)
	} else {
		request = httptest.NewRequestWithContext(t.Context(), method, path, body)
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	return recorder
}
