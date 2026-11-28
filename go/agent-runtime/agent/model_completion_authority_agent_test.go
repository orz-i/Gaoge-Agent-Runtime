package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type completionAuthorityModel struct {
	calls       int
	reused      bool
	resultError error
}

func (*completionAuthorityModel) ProviderName() string { return "audit" }
func (*completionAuthorityModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func (m *completionAuthorityModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	id := fmt.Sprintf("physical-%d", m.calls)
	if m.reused {
		id = "physical-1"
	}
	response := model.Response{ResponseID: id, Usage: &model.Usage{InputTokens: 17, OutputTokens: 5}}
	if m.calls == 1 {
		response.ToolCalls = []tools.Call{{ID: "first-tool", ToolKey: "lookup", Name: "lookup", Arguments: json.RawMessage(`{}`)}}
	} else {
		response.Content = "done"
	}
	return response, m.resultError
}

type streamedCompletionAuthorityModel struct {
	*completionAuthorityModel
	eventID                 string
	missingID, missingUsage bool
}

func (m *streamedCompletionAuthorityModel) GenerateStream(ctx context.Context, request model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	response, err := m.Generate(ctx, request)
	if emit != nil {
		id := response.ResponseID
		if m.eventID != "" {
			id = m.eventID
		}
		if emitErr := emit(model.StreamEvent{ResponseID: id, Usage: model.CloneUsage(response.Usage)}); emitErr != nil {
			return model.Response{}, emitErr
		}
	}
	if m.missingID {
		response.ResponseID = ""
	}
	if m.missingUsage {
		response.Usage = nil
	}
	return response, err
}

func TestNamedProviderCompletionAuthorityRequiresPhysicalReceipt(t *testing.T) {
	rejected := model.NewResponseRejectedError(errors.New("semantic rejection"))
	for _, tc := range []struct {
		name, eventID                          string
		sourceError                            error
		stream, reused, valid, rejectedReceipt bool
		missingID, missingUsage                bool
	}{
		{name: "valid_unary", valid: true},
		{name: "valid_stream", stream: true, valid: true},
		{name: "provider_reuses_physical_id", reused: true},
		{name: "provider_transport_failure", sourceError: errors.New("transport failed")},
		{name: "valid_rejected_unary", sourceError: rejected, rejectedReceipt: true},
		{name: "rejected_stream_id_conflict", stream: true, eventID: "other-physical", sourceError: rejected},
		{name: "rejected_stream_padded_event_id", stream: true, eventID: " physical-1 ", sourceError: rejected},
		{name: "rejected_stream_missing_final_id", stream: true, missingID: true, sourceError: rejected},
		{name: "rejected_stream_missing_final_usage", stream: true, missingUsage: true, sourceError: rejected},
		{name: "valid_rejected_stream", stream: true, sourceError: rejected, rejectedReceipt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &completionAuthorityModel{reused: tc.reused, resultError: tc.sourceError}
			var client model.Client = source
			if tc.stream {
				client = &streamedCompletionAuthorityModel{completionAuthorityModel: source, eventID: tc.eventID, missingID: tc.missingID, missingUsage: tc.missingUsage}
			}
			effects := 0
			registry := mustRegistry(t, []tools.Registration{{
				Definition: tools.Definition{Key: "lookup", Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					effects++
					return tools.ExecutionResult{Content: json.RawMessage(`{}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
				}),
			}})
			rt := newModelInvocationRuntime(t, memory.NewStore())
			deps := agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry}
			runner, err := agent.NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			req := startRequest("completion-"+tc.name, "request-"+tc.name, "lookup", "lookup")
			req.Limits = agent.Limits{MaxLLMCalls: 3, MaxToolCalls: 2}
			if tc.rejectedReceipt {
				req.Limits.MaxLLMCalls = 1
			}
			result, callErr := runner.StartRun(t.Context(), req)
			view := mustModelInvocationView(t, mustLoadModelInvocationRun(t, rt, result.Run.ID))
			if tc.valid {
				if callErr != nil || result.Run.Status != kernel.RunStatusCompleted || effects != 1 || source.calls != 2 || view.Budget.Usage.LLMCalls != 2 {
					t.Fatalf("valid completion rejected: err=%v status=%s effects=%d calls=%d budget=%+v", callErr, result.Run.Status, effects, source.calls, view.Budget.Usage)
				}
				return
			}
			if tc.rejectedReceipt {
				if callErr == nil || effects != 0 || source.calls != 1 || view.Budget.Usage.LLMCalls != 1 || view.ModelInvocations[0].ProviderResponseID != "physical-1" || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed {
					t.Fatalf("valid rejected receipt lost: err=%v effects=%d calls=%d view=%+v", callErr, effects, source.calls, view)
				}
				return
			}
			if tc.reused {
				if !errors.Is(callErr, agent.ErrModelOutcomeUnknown) || effects != 1 || source.calls != 2 || view.Budget.Usage.LLMCalls != 1 ||
					len(view.ModelInvocations) != 2 || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed || view.ModelInvocations[1].Status != agent.ModelInvocationPending {
					t.Fatalf("reused physical identity accepted: err=%v effects=%d calls=%d view=%+v", callErr, effects, source.calls, view)
				}
				return
			}
			if !errors.Is(callErr, agent.ErrModelOutcomeUnknown) || effects != 0 || source.calls != 1 || view.Budget.Usage.LLMCalls != 0 || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationPending {
				t.Fatalf("unverified completion accepted: err=%v status=%s effects=%d calls=%d budget=%+v invocations=%+v", callErr, result.Run.Status, effects, source.calls, view.Budget.Usage, view.ModelInvocations)
			}
		})
	}
}
