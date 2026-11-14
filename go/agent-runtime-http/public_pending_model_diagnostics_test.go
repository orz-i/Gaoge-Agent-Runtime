package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

type pendingDiagnosticModel struct{ calls int }

func (m *pendingDiagnosticModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	return model.Response{}, errors.New(privateDiagnosticFixture)
}

// This exercises a real dispatched pending invocation, not a hand-authored
// terminal fixture. The model is an in-process stub, not an HTTP provider.
func TestPendingModelDiagnosticPrivateAcrossOwnerHTTPReads(t *testing.T) {
	actor := kernel.ActorRef{TenantID: "pending-tenant", ActorID: "pending-owner"}
	engine, runtime := newAuthorizationHTTPTest(t, actor, nil)
	registry, err := tools.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &pendingDiagnosticModel{}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.StartRun(t.Context(), agent.StartRequest{ID: "pending-private", Actor: actor, Thread: kernel.ThreadRef{Kind: "test", ID: "pending-thread"}, Goal: "author-visible goal", Model: "test-model"})
	if !errors.Is(err, agent.ErrModelOutcomeUnknown) {
		t.Fatalf("expected dispatched unknown: %v", err)
	}
	before, err := runtime.Load(t.Context(), "pending-private")
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(before)
	if err != nil || before.Run.Status != kernel.RunStatusRunning || len(view.ModelInvocations) != 1 {
		t.Fatalf("not a real pending invocation: %#v %v", before.Run, err)
	}
	invocation := view.ModelInvocations[0]
	if invocation.Status != agent.ModelInvocationPending || invocation.DispatchAttempt != 1 || !strings.Contains(invocation.OutcomeErrorDetail, privateDiagnosticFixture) {
		t.Fatalf("missing private durable pending evidence: %#v", invocation)
	}
	paths := []string{"/api/v1/runs/pending-private", "/api/v1/runs/pending-private/events", "/api/v1/runs/pending-private/workbench"}
	for _, path := range paths {
		response := performAuthorizationRequest(t, engine, stdhttp.MethodGet, path, nil)
		if response.Code != stdhttp.StatusOK || strings.Contains(response.Body.String(), privateDiagnosticFixture) || strings.Contains(response.Body.String(), "outcomeErrorDetail") {
			t.Fatalf("owner projection %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := performAuthorizationRequest(t, engine, stdhttp.MethodGet, paths[0], nil)
	if !strings.Contains(response.Body.String(), "author-visible goal") || !strings.Contains(response.Body.String(), invocation.ID) || !strings.Contains(response.Body.String(), invocation.RequestHash) {
		t.Fatalf("ordinary visible source facts lost: %s", response.Body.String())
	}
	query, err := workbench.NewQuery(runtime, runtime, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := gin.New()
	NewModule(NewHandler(Dependencies{Runtime: runtime, Workbench: query, Shared: NewShared(authorizationPrincipal{actor: kernel.ActorRef{TenantID: "foreign", ActorID: actor.ActorID}}, nil, runtime, nil)})).RegisterRoutes(foreign.Group("/api/v1"))
	for _, path := range paths {
		response := performAuthorizationRequest(t, foreign, stdhttp.MethodGet, path, nil)
		if response.Code != stdhttp.StatusNotFound || strings.Contains(response.Body.String(), privateDiagnosticFixture) || strings.Contains(response.Body.String(), invocation.ID) {
			t.Fatalf("cross-tenant disclosure %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	after, err := runtime.Load(t.Context(), before.Run.ID)
	if err != nil || !reflect.DeepEqual(before, after) || client.calls != 1 {
		t.Fatalf("HTTP projection changed durable source or replayed: %v calls=%d", err, client.calls)
	}
}

func TestPublicPendingOutcomeDiagnosticKeysAreBoundedWithoutErasingBusinessDetails(t *testing.T) {
	input := json.RawMessage(`{"modelInvocations":[{"status":"pending","outcomeErrorCode":"model.upstream_auth_failed","outcomeErrorDetail":"private"},{"outcome_error_code":"untrusted.private","outcome-error-detail":"private"},{"outcomeErrorCode":42}],"result":{"detail":"ordinary detail","error":{"code":"OUT_OF_STOCK","message":"ordinary message"},"outcomeErrorCode":"BUSINESS_CODE","outcomeErrorDetail":"private"}}`)
	original := string(input)
	got := PublicDiagnosticPayload(input)
	want := json.RawMessage(`{"modelInvocations":[{"status":"pending","outcomeErrorCode":"model.upstream_auth_failed"},{"outcome_error_code":"execution.failed"},{}],"result":{"detail":"ordinary detail","error":{"code":"OUT_OF_STOCK","message":"ordinary message"},"outcomeErrorCode":"BUSINESS_CODE"}}`)
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) || string(input) != original {
		t.Fatalf("bounded diagnostic projection changed business payload or input: %s", got)
	}
}
