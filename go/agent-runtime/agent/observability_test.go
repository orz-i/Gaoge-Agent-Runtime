package agent_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type telemetryAgentModel struct{ calls int }

func (model *telemetryAgentModel) Generate(
	_ context.Context,
	_ runtimemodel.Request,
) (runtimemodel.Response, error) {
	model.calls++
	if model.calls == 1 {
		return runtimemodel.Response{
			ToolCalls: []tools.Call{{ID: callOne, ToolKey: manifestToolKey, Arguments: json.RawMessage(`{}`)}},
			Usage:     &runtimemodel.Usage{InputTokens: 5, OutputTokens: 2}, ResponseID: "response-1",
		}, nil
	}
	return runtimemodel.Response{
		Content: "COMPLETION_SECRET", Usage: &runtimemodel.Usage{InputTokens: 4, OutputTokens: 1}, ResponseID: "response-2",
	}, nil
}

func TestAgentTelemetryExposesStructuralRunModelAndToolFacts(t *testing.T) {
	runtime, approvals := newTestRuntimeAndApprovals(t)
	registry := mustRegistry(t, []tools.Registration{{
		Definition: tools.Definition{
			Key: manifestToolKey, Name: manifestToolName,
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		Handler: tools.HandlerFunc(func(context.Context, tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return tools.ExecutionResult{
				Content: json.RawMessage(`{"secret":"TOOL_RESULT_SECRET"}`),
				Receipt: tools.Receipt{ExecutionID: "telemetry-tool", Disposition: committedDisposition},
			}, nil
		}),
	}})
	events := make([]observability.Event, 0)
	model := &telemetryAgentModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry,
		Approvals: interactionadapter.New(approvals),
		Telemetry: []observability.Recorder{observability.RecorderFunc{
			RecorderName: "capture",
			RecordFunc: func(_ context.Context, event observability.Event) {
				events = append(events, event)
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest(
		"run_telemetry", "request_telemetry", "PROMPT_SECRET", manifestToolKey,
	))
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("snapshot=%#v err=%v", snapshot.Run, err)
	}
	assertTelemetryPair(t, events, observability.ScopeRun, "", 1)
	assertTelemetryPair(t, events, observability.ScopeModelInvocation, "generate", 2)
	assertTelemetryPair(t, events, observability.ScopeToolInvocation, manifestToolKey, 1)
	var modelUsageInput int64
	var modelUsageOutput int64
	for _, event := range events {
		if event.Scope == observability.ScopeModelInvocation && event.Phase == observability.PhaseCompleted {
			if event.OperationID == "" || event.ResponseID == "" {
				t.Fatalf("completed model telemetry lacks correlation identity: %#v", event)
			}
			modelUsageInput += event.Usage.InputTokens
			modelUsageOutput += event.Usage.OutputTokens
		}
	}
	if modelUsageInput != 9 || modelUsageOutput != 3 {
		t.Fatalf("model telemetry usage input=%d output=%d events=%#v", modelUsageInput, modelUsageOutput, events)
	}
	terminal := events[len(events)-1]
	if terminal.Scope != observability.ScopeRun || terminal.Phase != observability.PhaseCompleted ||
		terminal.Usage.LLMCalls != 2 || terminal.Usage.ToolCalls != 1 || terminal.Usage.TotalTokens != 12 {
		t.Fatalf("terminal telemetry = %#v", terminal)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"PROMPT_SECRET", "COMPLETION_SECRET", "TOOL_RESULT_SECRET"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("telemetry leaked %q: %s", secret, encoded)
		}
	}
}

func assertTelemetryPair(
	t *testing.T,
	events []observability.Event,
	scope observability.Scope,
	operation string,
	pairs int,
) {
	t.Helper()
	started := 0
	terminal := 0
	for _, event := range events {
		if event.Scope != scope || (operation != "" && event.Operation != operation) {
			continue
		}
		switch event.Phase {
		case observability.PhaseStarted:
			started++
		case observability.PhaseCompleted, observability.PhaseFailed, observability.PhaseCancelled:
			terminal++
		}
	}
	if started != pairs || terminal != pairs {
		t.Fatalf("scope=%s operation=%q started=%d terminal=%d events=%#v", scope, operation, started, terminal, events)
	}
}
