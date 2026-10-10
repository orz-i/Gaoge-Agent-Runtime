package harnesshttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type allowanceHTTPPrincipal struct{ actor kernel.ActorRef }

func (p allowanceHTTPPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return p.actor, nil
}

type allowanceHTTPModel struct{}

func (allowanceHTTPModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

type allowanceHTTPCorrection struct{ calls int }

func (policy *allowanceHTTPCorrection) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	policy.calls++
	if policy.calls == 1 {
		return agent.CompletionCorrection{Code: "repair", Message: "Improve final response"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func newAllowanceHTTPFixture(t *testing.T) (*harness.Runner, *kernel.Runtime, harness.Snapshot, kernel.Snapshot, kernel.ActorRef) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	policy := &allowanceHTTPCorrection{}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: allowanceHTTPModel{},
		CompletionPolicies: []agent.CompletionPolicy{policy},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 6},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: harness.NewMemoryStore(),
		Clock: allowanceHTTPClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "alice-tenant", ActorID: "alice"}
	paused, err := host.Start(t.Context(), harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "private-thread"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "private-turn"},
		Actor:      actor, Thread: kernel.ThreadRef{Kind: "conversation", ID: "private-thread"},
		Goal: "complete private task", CallAllowance: &agent.CallAllowance{LLMCalls: 1},
		Config: harness.ConfigSnapshot{Model: "model"},
	})
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("pause %#v err=%v", paused.Turn, err)
	}
	invocation, ok := harness.TopLevelInvocation(paused)
	if !ok {
		t.Fatal("missing invocation")
	}
	run, err := runtime.Load(t.Context(), invocation.ExecutionRefID)
	if err != nil {
		t.Fatal(err)
	}
	return host, runtime, paused, run, actor
}

type allowanceHTTPClock struct{}

func (allowanceHTTPClock) Now() time.Time { return time.Now().UTC() }

func newAllowanceHTTPEngine(host *harness.Runner, actor kernel.ActorRef) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	handler := NewHandler(Dependencies{Runner: host, Shared: runtimehttp.NewShared(allowanceHTTPPrincipal{actor: actor}, nil, nil, nil)})
	NewModule(handler).RegisterRoutes(engine.Group("/v1"))
	return engine
}
func allowanceHTTPRequest(t *testing.T, engine *gin.Engine, turnID string, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/v1/harness/turns/" + turnID + "/allowance"
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(stdhttp.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

func TestBudgetGrantHTTPAuthorizesOwningTurnAndEnforcesCAS(t *testing.T) {
	host, runtime, paused, run, actor := newAllowanceHTTPFixture(t)
	wrong := newAllowanceHTTPEngine(host, kernel.ActorRef{TenantID: "another", ActorID: "intruder"})
	body := fmt.Sprintf(`{"expectedTurnRevision":%d,"modelCalls":1}`, paused.Turn.Revision)
	response := allowanceHTTPRequest(t, wrong, paused.Turn.ID, body)
	missing := allowanceHTTPRequest(t, wrong, "unknown-turn", body)
	if response.Code != stdhttp.StatusNotFound || missing.Code != stdhttp.StatusNotFound ||
		response.Body.String() != missing.Body.String() {
		t.Fatalf("cross-tenant leak forbidden=%d %s missing=%d %s", response.Code, response.Body.String(), missing.Code, missing.Body.String())
	}
	current, err := runtime.Load(t.Context(), run.Run.ID)
	if err != nil || current.Run.Status != kernel.RunStatusPausedBudget || current.Run.Revision != run.Run.Revision {
		t.Fatalf("forbidden grant mutated Run=%#v err=%v", current.Run, err)
	}
	owner := newAllowanceHTTPEngine(host, actor)
	for _, bad := range []string{
		`{"expectedTurnRevision":1,"modelCalls":0}`,
		fmt.Sprintf(`{"expectedTurnRevision":%d,"modelCalls":-1}`, paused.Turn.Revision),
		fmt.Sprintf(`{"expectedTurnRevision":%d,"modelCalls":1,"absoluteMaxCalls":999}`, paused.Turn.Revision),
		fmt.Sprintf(`{"expectedTurnRevision":%d,"modelCalls":0,"toolCalls":1}`, paused.Turn.Revision),
	} {
		rejected := allowanceHTTPRequest(t, owner, paused.Turn.ID, bad)
		if rejected.Code != stdhttp.StatusBadRequest {
			t.Fatalf("bad grant %s status=%d body=%s", bad, rejected.Code, rejected.Body.String())
		}
	}
	stale := allowanceHTTPRequest(t, owner, paused.Turn.ID, fmt.Sprintf(`{"expectedTurnRevision":%d,"modelCalls":1}`, paused.Turn.Revision+1))
	if stale.Code != stdhttp.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}
	granted := allowanceHTTPRequest(t, owner, paused.Turn.ID, body)
	if granted.Code != stdhttp.StatusOK || !strings.Contains(granted.Body.String(), `"status":"running"`) {
		t.Fatalf("grant status=%d body=%s", granted.Code, granted.Body.String())
	}
	repeat := allowanceHTTPRequest(t, owner, paused.Turn.ID, body)
	if repeat.Code != stdhttp.StatusConflict {
		t.Fatalf("duplicate status=%d body=%s", repeat.Code, repeat.Body.String())
	}
	updated, err := runtime.Load(t.Context(), run.Run.ID)
	if err != nil || updated.Run.Status != kernel.RunStatusRunning || updated.Run.Revision != run.Run.Revision+1 {
		t.Fatalf("post-grant Run=%#v err=%v", updated.Run, err)
	}
	var state struct {
		CallAllowance agent.CallAllowance `json:"callAllowance"`
	}
	if err = json.Unmarshal(updated.State, &state); err != nil || state.CallAllowance.LLMCalls != 2 {
		t.Fatalf("cumulative grant=%#v err=%v", state, err)
	}
}
