package workflowhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	workflowhttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/workflow"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

func TestDefinitionRegistryHTTPPublishesStartsAndDisablesImmutableRevision(t *testing.T) {
	t.Parallel()
	fixture := newHTTPFixture(t, true, completedHTTPExecutor{})
	draft := definitionDraftPayload()

	compiled := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-definitions/compile", map[string]any{
		"scope": map[string]any{"kind": "actor"}, "baseRevision": 0, "draft": draft,
	})
	if compiled.Code != stdhttp.StatusOK || !bytes.Contains(compiled.Body.Bytes(), []byte(`"baseRevision":0`)) {
		t.Fatalf("compile status=%d body=%s", compiled.Code, compiled.Body.String())
	}

	publishBody := map[string]any{
		"scope": map[string]any{"kind": "actor"}, "expectedRevision": 0,
		"idempotencyKey": "publish-1", "draft": draft,
	}
	published := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-definitions", publishBody)
	if published.Code != stdhttp.StatusCreated || !bytes.Contains(published.Body.Bytes(), []byte(`"revision":1`)) {
		t.Fatalf("publish status=%d body=%s", published.Code, published.Body.String())
	}
	replayed := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-definitions", publishBody)
	if replayed.Code != stdhttp.StatusCreated || !bytes.Contains(replayed.Body.Bytes(), []byte(`"reused":true`)) {
		t.Fatalf("replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}

	started := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"thread": map[string]any{"kind": "conversation", "id": "thread-1"},
		"input":  map[string]any{"storyID": "story-1"}, "goal": "run published workflow",
		"definitionReference": map[string]any{"id": "story.change-set.v1"},
	})
	if started.Code != stdhttp.StatusAccepted || !bytes.Contains(started.Body.Bytes(), []byte(`"status":"completed"`)) {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}

	listed := fixture.request(t, stdhttp.MethodGet, "/api/v1/workflow-definitions", nil)
	if listed.Code != stdhttp.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"definitionID":"story.change-set.v1"`)) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	exact := fixture.request(
		t, stdhttp.MethodGet,
		"/api/v1/workflow-definitions/story.change-set.v1/revisions/1?scope=actor", nil,
	)
	if exact.Code != stdhttp.StatusOK || !bytes.Contains(exact.Body.Bytes(), []byte(`"hash"`)) {
		t.Fatalf("get status=%d body=%s", exact.Code, exact.Body.String())
	}

	disabled := fixture.request(
		t, stdhttp.MethodPost,
		"/api/v1/workflow-definitions/story.change-set.v1/activation",
		map[string]any{
			"scope": map[string]any{"kind": "actor"}, "availability": "disabled",
			"expectedVersion": 1,
		},
	)
	if disabled.Code != stdhttp.StatusOK || !bytes.Contains(disabled.Body.Bytes(), []byte(`"availability":"disabled"`)) {
		t.Fatalf("disable status=%d body=%s", disabled.Code, disabled.Body.String())
	}
	blocked := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"thread": map[string]any{"kind": "conversation", "id": "thread-1"},
		"input":  map[string]any{}, "goal": "must not start",
		"definitionReference": map[string]any{"id": "story.change-set.v1"},
	})
	if blocked.Code != stdhttp.StatusConflict || !bytes.Contains(blocked.Body.Bytes(), []byte(`workflow.definition_disabled`)) {
		t.Fatalf("blocked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
}

func TestDefinitionMutationRequiresHostAuthorizer(t *testing.T) {
	t.Parallel()
	fixture := newHTTPFixture(t, false, completedHTTPExecutor{})
	response := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-definitions", map[string]any{
		"scope": map[string]any{"kind": "actor"}, "expectedRevision": 0,
		"idempotencyKey": "denied", "draft": definitionDraftPayload(),
	})
	if response.Code != stdhttp.StatusForbidden || !bytes.Contains(response.Body.Bytes(), []byte(`workflow.definition_forbidden`)) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestStartRunReturnsAcceptedForPersistedPendingEffect(t *testing.T) {
	t.Parallel()
	fixture := newHTTPFixture(t, true, pendingHTTPExecutor{})
	draft := map[string]any{
		"id": "pending", "revision": 1, "name": "Pending",
		"nodes": []any{
			map[string]any{
				"id": "effect", "type": "effect",
				"effect": map[string]any{"kind": "provider.pending", "input": map[string]any{}},
			},
			map[string]any{"id": "done", "type": "return", "return": map[string]any{"value": true}},
		},
	}
	response := fixture.request(t, stdhttp.MethodPost, "/api/v1/workflow-runs", map[string]any{
		"thread": map[string]any{"kind": "conversation", "id": "thread-1"},
		"input":  map[string]any{}, "goal": "persist before pending", "definition": draft,
	})
	if response.Code != stdhttp.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"pending"`)) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func definitionDraftPayload() map[string]any {
	return map[string]any{
		"id": "story.change-set.v1", "revision": 99, "name": "Story Change Set",
		"nodes": []any{
			map[string]any{
				"id": "done", "type": "return",
				"return": map[string]any{"value": map[string]any{"status": "ready"}},
			},
		},
		"policy": map[string]any{
			"costClass": "none", "maxCostUnits": 0, "sideEffectClass": "none",
		},
	}
}

type httpFixture struct {
	engine *gin.Engine
}

func newHTTPFixture(t *testing.T, authorize bool, executor workflow.EffectExecutor) httpFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: httpClock{}, IDs: &httpIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := workflow.NewRunner(workflow.Dependencies{Runtime: runtime, Effects: executor})
	if err != nil {
		t.Fatal(err)
	}
	registry, err := workflow.NewDefinitionRegistry(workflow.NewMemoryDefinitionStore(), httpClock{})
	if err != nil {
		t.Fatal(err)
	}
	var authorizer workflowhttp.DefinitionAuthorizer
	if authorize {
		authorizer = allowDefinitionAuthorizer{}
	}
	shared := runtimehttp.NewShared(httpPrincipal{}, nil)
	engine := gin.New()
	workflowhttp.NewModule(workflowhttp.NewHandler(workflowhttp.Dependencies{
		Runner: runner, Registry: registry, Authorizer: authorizer, Shared: shared,
	})).RegisterRoutes(engine.Group("/api/v1"))
	return httpFixture{engine: engine}
}

func (fixture httpFixture) request(t *testing.T, method string, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.engine.ServeHTTP(recorder, request)
	return recorder
}

type httpPrincipal struct{}

func (httpPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return kernel.ActorRef{TenantID: "tenant-1", ActorID: "actor-1"}, nil
}

type allowDefinitionAuthorizer struct{}

func (allowDefinitionAuthorizer) AuthorizeDefinition(
	context.Context,
	workflowhttp.DefinitionAuthorization,
) error {
	return nil
}

type completedHTTPExecutor struct{}

func (completedHTTPExecutor) Execute(
	context.Context,
	workflow.EffectRequest,
) (workflow.EffectResult, error) {
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted, ReceiptID: "receipt", Output: json.RawMessage(`{}`),
	}, nil
}

type pendingHTTPExecutor struct{}

func (pendingHTTPExecutor) Execute(
	context.Context,
	workflow.EffectRequest,
) (workflow.EffectResult, error) {
	return workflow.EffectResult{Disposition: workflow.DispositionPending}, nil
}

type httpClock struct{}

func (httpClock) Now() time.Time { return time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) }

type httpIDs struct {
	mu   sync.Mutex
	next int
}

func (ids *httpIDs) NewID(prefix string) (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}
