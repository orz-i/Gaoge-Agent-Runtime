package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A named Provider's successful local-Tool response needs the exact physical
// completion identity just as a known-but-rejected response does. Otherwise
// its Tool executes without a durable identity and cannot be deduplicated.
type successIdentityModel struct {
	calls   int
	firstID string
}

func (*successIdentityModel) ProviderName() string { return "audit" }
func (*successIdentityModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func (m *successIdentityModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	result := model.Response{ResponseID: m.firstID, Usage: &model.Usage{InputTokens: 17, OutputTokens: 5}}
	if m.calls == 1 {
		result.ToolCalls = []tools.Call{{ID: "tool-first", ToolKey: "lookup", Name: "lookup", Arguments: json.RawMessage(`{}`)}}
	} else {
		result.Content, result.ResponseID = "done", "second-physical-id"
	}
	return result, nil
}

type streamSuccessIdentityModel struct {
	*successIdentityModel
	firstEventID    string
	secondEventID   string
	ignoreSinkError bool
}

func (m *streamSuccessIdentityModel) GenerateStream(ctx context.Context, request model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	result, err := m.Generate(ctx, request)
	if err != nil {
		return result, err
	}
	id := result.ResponseID
	if m.calls == 1 {
		id = m.firstEventID
	}
	if id != "" && emit != nil {
		if err := emit(model.StreamEvent{ResponseID: id, Usage: model.CloneUsage(result.Usage)}); err != nil {
			return model.Response{}, err
		}
	}
	if m.calls == 1 && m.secondEventID != "" && emit != nil {
		if err := emit(model.StreamEvent{ResponseID: m.secondEventID}); err != nil && !m.ignoreSinkError {
			return model.Response{}, err
		}
	}
	return result, nil
}

type mutationSuccessIdentityMiddleware struct{ replacement string }

func (mutationSuccessIdentityMiddleware) Name() string { return "audit-identity-mutation" }
func (m mutationSuccessIdentityMiddleware) Model(ctx context.Context, request model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	response, err := next(ctx, request, emit)
	if err == nil {
		response.ResponseID = m.replacement
	}
	return response, err
}

func TestNamedProviderSuccessfulToolRequiresExactPhysicalResponseIdentity(t *testing.T) {
	for _, tc := range []struct {
		name            string
		firstID         string
		firstEventID    string
		secondEventID   string
		ignoreSinkError bool
		mutateID        string
		mutate          bool
		stream          bool
		accepted        bool
	}{
		{name: "unary_missing_id", firstID: ""},
		{name: "unary_padded_id", firstID: " physical-id "},
		{name: "unary_valid_id", firstID: "physical-id", accepted: true},
		{name: "stream_missing_id", stream: true},
		{name: "stream_padded_event_id", stream: true, firstEventID: " physical-id "},
		{name: "stream_mismatched_event_id", stream: true, firstID: "physical-id", firstEventID: "different-id"},
		{name: "stream_conflicting_event_ids", stream: true, firstID: "physical-id", firstEventID: "physical-id", secondEventID: "different-id"},
		{name: "stream_ignores_callback_error", stream: true, firstID: "physical-id", firstEventID: "physical-id", secondEventID: "different-id", ignoreSinkError: true},
		{name: "stream_padded_final_id", stream: true, firstID: " physical-id ", firstEventID: "physical-id"},
		{name: "stream_valid_event_id", stream: true, firstID: "physical-id", firstEventID: "physical-id", accepted: true},
		{name: "stream_final_only_id", stream: true, firstID: "physical-id", accepted: true},
		{name: "stream_event_only_id", stream: true, firstEventID: "physical-id", accepted: true},
		{name: "middleware_erases_unary_id", firstID: "physical-id", mutate: true},
		{name: "middleware_pads_stream_id", stream: true, firstID: "physical-id", firstEventID: "physical-id", mutate: true, mutateID: " physical-id "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			underlying := &successIdentityModel{firstID: tc.firstID}
			var client model.Client = underlying
			if tc.stream {
				client = &streamSuccessIdentityModel{successIdentityModel: underlying, firstEventID: tc.firstEventID, secondEventID: tc.secondEventID, ignoreSinkError: tc.ignoreSinkError}
			}
			effects := 0
			registry := mustRegistry(t, []tools.Registration{{
				Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					effects++
					return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
				}),
			}})
			runtime := newModelInvocationRuntime(t, memory.NewStore())
			dependencies := agent.Dependencies{Runtime: runtime, Model: client, Catalog: registry, Executor: registry}
			if tc.mutate {
				dependencies.ModelMiddleware = []plugin.ModelMiddleware{mutationSuccessIdentityMiddleware{replacement: tc.mutateID}}
			}
			runner, err := agent.NewRunner(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			request := startRequest("identity-"+tc.name, "identity-request-"+tc.name, "call lookup", "lookup")
			request.Limits = agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2}
			snapshot, callErr := runner.StartRun(t.Context(), request)
			view := mustModelInvocationView(t, mustLoadModelInvocationRun(t, runtime, snapshot.Run.ID))
			if tc.accepted {
				if callErr != nil || snapshot.Run.Status != kernel.RunStatusCompleted || effects != 1 ||
					underlying.calls != 2 || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.ToolCalls != 1 {
					t.Fatalf("valid physical response rejected: status=%s err=%v effects=%d calls=%d budget=%+v", snapshot.Run.Status, callErr, effects, underlying.calls, view.Budget.Usage)
				}
				return
			}
			if !errors.Is(callErr, agent.ErrModelOutcomeUnknown) || snapshot.Run.Status != kernel.RunStatusRunning || effects != 0 ||
				underlying.calls != 1 || view.Budget.Usage.LLMCalls != 0 || view.Budget.Usage.ToolCalls != 0 ||
				len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationPending ||
				len(view.ModelInvocations[0].Response.ToolCalls) != 0 || view.ModelInvocations[0].DispatchAttempt != 1 || view.ModelInvocations[0].CompletedAt != nil {
				t.Fatalf("unverified physical response authorized Tool: status=%s err=%v effects=%d calls=%d budget=%+v invocations=%+v",
					snapshot.Run.Status, callErr, effects, underlying.calls, view.Budget.Usage, view.ModelInvocations)
			}
			before := mustLoadModelInvocationRun(t, runtime, snapshot.Run.ID)
			_, resumeErr := runner.Resume(t.Context(), before.Run.ID, before.Run.Revision)
			after := mustLoadModelInvocationRun(t, runtime, before.Run.ID)
			if !errors.Is(resumeErr, agent.ErrModelOutcomeUnknown) || underlying.calls != 1 || effects != 0 || !reflect.DeepEqual(before, after) {
				t.Fatalf("unproved physical identity replayed or changed: %v", resumeErr)
			}
		})
	}
}
