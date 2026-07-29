package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const testProvenanceHTTPRunID = "run_1"

type provenanceHTTPStore struct {
	runtime.Store
	run model.Run
}

func (s *provenanceHTTPStore) GetRun(
	_ context.Context,
	actor model.ActorRef,
	runID string,
) (*model.Run, error) {
	if actor != s.run.Actor || runID != s.run.RunID {
		return nil, runtime.ErrNotFound
	}
	item := s.run
	return &item, nil
}

func (*provenanceHTTPStore) GetRunContextSnapshot(
	context.Context,
	model.ActorRef,
	string,
) (*model.ContextSnapshot, error) {
	return nil, runtime.ErrNotFound
}

func (*provenanceHTTPStore) GetRunResult(
	context.Context,
	model.ActorRef,
	string,
) (*model.RunResult, error) {
	return nil, runtime.ErrNotFound
}

type provenanceHTTPCache struct {
	runtime.GenerationStreamCacheRepository
}

type provenanceHTTPUnitOfWork struct {
	runtime.UnitOfWork
}

type provenanceHTTPPrincipal struct {
	actor model.ActorRef
}

func (p provenanceHTTPPrincipal) ResolvePrincipal(*gin.Context) (model.ActorRef, error) {
	return p.actor, nil
}

func TestRunExecutionProvenanceHTTPContractIsNeutral(t *testing.T) {
	actor := model.ActorRef{TenantID: "tenant_1", ActorID: "actor_1"}
	store := &provenanceHTTPStore{run: model.Run{
		RunID: testProvenanceHTTPRunID, RootRunID: "run_root", Actor: actor, Status: model.RunStatusCompleted,
		Environment:           model.ResourceRef{Kind: "environment", ID: "environment_1", Revision: "3"},
		AgentManifest:         model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent_1", Revision: "5"},
		RunConfigSnapshotJSON: `{"endpoint":"https://private.invalid","credential":"secret","prompt":"hidden","toolArguments":{"query":"hidden"}}`,
		PlatformModelName:     "5.6 Terra", Provider: "openai", UpstreamModelName: "gpt-5.6-terra",
	}}
	engine, err := runtime.New(
		runtime.StaticConfigProvider(runtime.Config{}),
		runtime.Dependencies{
			Store: store, Cache: &provenanceHTTPCache{},
			UnitOfWork: &provenanceHTTPUnitOfWork{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewHandler(engine, Dependencies{PrincipalResolver: provenanceHTTPPrincipal{actor: actor}})
	NewModule(handler).RegisterRoutes(router.Group("/api/v1"))

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/runs/run_1/provenance", nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	var payload runtime.RuntimeExecutionProvenanceV1
	if err = json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != 1 || payload.RunID != testProvenanceHTTPRunID ||
		payload.EnvironmentRef == nil || payload.EnvironmentRef.Revision != "3" {
		t.Fatalf("payload = %#v", payload)
	}
	body := response.Body.String()
	for _, forbidden := range []string{
		"private.invalid", "secret", "hidden", "toolArguments",
		`"endpoint"`, `"credential"`, `"prompt"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, body)
		}
	}
}

func TestRunExecutionProvenanceHTTPRejectsNonterminalRun(t *testing.T) {
	actor := model.ActorRef{TenantID: "tenant_1", ActorID: "actor_1"}
	store := &provenanceHTTPStore{run: model.Run{
		RunID: testProvenanceHTTPRunID, Actor: actor, Status: model.RunStatusRunning,
		RunConfigSnapshotJSON: `{}`,
	}}
	engine, err := runtime.New(
		runtime.StaticConfigProvider(runtime.Config{}),
		runtime.Dependencies{
			Store: store, Cache: &provenanceHTTPCache{},
			UnitOfWork: &provenanceHTTPUnitOfWork{},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewHandler(engine, Dependencies{PrincipalResolver: provenanceHTTPPrincipal{actor: actor}})
	router.GET("/runs/:run_id/provenance", handler.GetRunExecutionProvenance)

	response := httptest.NewRecorder()
	router.ServeHTTP(
		response,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/runs/run_1/provenance", nil),
	)
	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"run.provenance_not_frozen"`) {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
}
