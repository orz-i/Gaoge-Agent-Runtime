package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type progressModelFunc func(context.Context, model.Request) (model.Response, error)

func (fn progressModelFunc) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	return fn(ctx, request)
}

func progressRegistry(t *testing.T, handler tools.Handler) *tools.Registry {
	t.Helper()
	return mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler:    handler,
	}})
}

func TestToolProgressDistinctQueriesAndFrozenRestart(t *testing.T) {
	for _, optIn := range []bool{false, true} {
		t.Run(fmt.Sprint(optIn), func(t *testing.T) {
			runtime, _ := newTestRuntimeAndApprovals(t)
			modelCalls, toolCalls := 0, 0
			registry := progressRegistry(t, tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				toolCalls++
				if toolCalls <= 3 {
					return tools.RecoverableNoEffectResult(request.Call.ID,
						tools.NewRecoverableCallError("lookup.no_match", "Try a different source identifier", tools.ErrInvalidCall))
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{"found":true}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
			}))
			client := progressModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
				modelCalls++
				if modelCalls == 4 && !optIn {
					if slices.ContainsFunc(request.Tools, func(def tools.Definition) bool { return def.Key == "lookup" }) {
						t.Fatal("legacy suppression changed")
					}
					return model.Response{Content: "Unable to find evidence"}, nil
				}
				if modelCalls <= 4 {
					if !slices.ContainsFunc(request.Tools, func(def tools.Definition) bool { return def.Key == "lookup" }) {
						t.Fatal("distinct authorized query was suppressed")
					}
					return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("read-%d", modelCalls), ToolKey: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"query":"source-%d"}`, modelCalls))}}}, nil
				}
				return model.Response{Content: "Verified evidence found"}, nil
			})
			dependencies := agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry}
			policy := &agent.ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}
			if optIn {
				dependencies.ToolProgressPolicy = policy
			}
			runner, err := agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			policy.ExplorationToolKeys[0] = "mutated-after-composition"
			prepared, err := runner.PrepareRun(t.Context(), startRequest("progress-run", "progress-request", "Find source evidence", "lookup"))
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(prepared)
			if err != nil || optIn && (view.ToolProgressPolicy == nil || !slices.Equal(view.ToolProgressPolicy.ExplorationToolKeys, []string{"lookup"})) {
				t.Fatalf("policy not frozen: %+v, %v", view.ToolProgressPolicy, err)
			}
			// A new process binding with no policy must use the Run's frozen policy,
			// not silently switch solving behavior at recovery time.
			dependencies.ToolProgressPolicy = nil
			reopened, err := agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			result, err := reopened.Resume(t.Context(), prepared.Run.ID, prepared.Run.Revision)
			wantCalls := 3
			if optIn {
				wantCalls = 4
			}
			if err != nil || result.Run.Status != kernel.RunStatusCompleted || toolCalls != wantCalls {
				t.Fatalf("status=%s error=%v tools=%d want=%d", result.Run.Status, err, toolCalls, wantCalls)
			}
		})
	}
}

func TestToolProgressExactRepeatsDoNotDispatchAndDifferentArgumentsRemainAvailable(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	modelCalls, toolCalls := 0, 0
	registry := progressRegistry(t, tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		return tools.ExecutionResult{Content: json.RawMessage(`{"found":false}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
	}))
	client := progressModelFunc(func(_ context.Context, request model.Request) (model.Response, error) {
		modelCalls++
		if modelCalls == 4 {
			last := model.TextValue(request.Messages[len(request.Messages)-1].Content)
			if !strings.Contains(last, "tool.exploration_repeat") || toolCalls != 2 {
				t.Fatalf("identical read dispatched instead of correction: calls=%d last=%s", toolCalls, last)
			}
		}
		if modelCalls == 5 {
			return model.Response{Content: "No evidence found"}, nil
		}
		arguments := json.RawMessage(`{"query":"same","page":1}`)
		if modelCalls == 3 {
			arguments = json.RawMessage(`{ "page": 1, "query": "same" }`)
		} else if modelCalls == 4 {
			arguments = json.RawMessage(`{"query":"different","page":2}`)
		}
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("call-%d", modelCalls), ToolKey: "lookup", Arguments: arguments}}}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		ToolProgressPolicy: &agent.ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("repeat-run", "repeat-request", "Find evidence", "lookup"))
	if err != nil || result.Run.Status != kernel.RunStatusCompleted || toolCalls != 3 || modelCalls != 5 {
		t.Fatalf("status=%s error=%v model=%d tools=%d", result.Run.Status, err, modelCalls, toolCalls)
	}
}

func TestToolProgressUnknownOutcomeNeverRetries(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	toolCalls := 0
	registry := progressRegistry(t, tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		return tools.ExecutionResult{}, errors.New("response lost after dispatch")
	}))
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		return model.Response{ToolCalls: []tools.Call{{ID: "uncertain-read", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		ToolProgressPolicy: &agent.ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("unknown-run", "unknown-request", "Read evidence", "lookup"))
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("unknown outcome lost: %v", err)
	}
	_, err = runner.Resume(t.Context(), result.Run.ID, result.Run.Revision)
	if !errors.Is(err, agent.ErrToolOutcomeUnknown) || toolCalls != 1 {
		t.Fatalf("unknown call was replayed: %v, calls=%d", err, toolCalls)
	}
}

func TestToolProgressDoesNotRaiseHardCallLimit(t *testing.T) {
	runtime, _ := newTestRuntimeAndApprovals(t)
	modelCalls, toolCalls := 0, 0
	registry := progressRegistry(t, tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
		toolCalls++
		return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError("lookup.no_match", "Try another source", tools.ErrInvalidCall))
	}))
	client := progressModelFunc(func(context.Context, model.Request) (model.Response, error) {
		modelCalls++
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("read-%d", modelCalls), ToolKey: "lookup", Arguments: json.RawMessage(fmt.Sprintf(`{"page":%d}`, modelCalls))}}}, nil
	})
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry,
		Limits: agent.Limits{MaxLLMCalls: 8, MaxToolCalls: 2}, ToolProgressPolicy: &agent.ToolProgressPolicy{ExplorationToolKeys: []string{"lookup"}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.StartRun(t.Context(), startRequest("limit-run", "limit-request", "Read evidence", "lookup"))
	if !errors.Is(err, agent.ErrCallLimit) || result.Run.Status != kernel.RunStatusFailed || toolCalls != 2 {
		t.Fatalf("hard limit changed: status=%s err=%v tools=%d", result.Run.Status, err, toolCalls)
	}
}
