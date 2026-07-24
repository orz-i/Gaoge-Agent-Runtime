package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type evaluatorFunc func(context.Context, EvaluationRequest) (EvaluationResult, error)

func (fn evaluatorFunc) Evaluate(ctx context.Context, request EvaluationRequest) (EvaluationResult, error) {
	return fn(ctx, request)
}

func TestEvaluationRegistryIsDeterministicAndObserveDoesNotBlock(t *testing.T) {
	registry, err := NewEvaluationRegistry([]EvaluationRegistration{
		{Name: "z_observer", Stages: []EvaluationStage{EvaluationStageRunInput}, Mode: EvaluationModeObserve, Evaluator: evaluatorFunc(func(context.Context, EvaluationRequest) (EvaluationResult, error) {
			return EvaluationResult{Decision: EvaluationDecisionDeny, Code: "observed_risk"}, nil
		})},
		{Name: "a_enforcer", Stages: []EvaluationStage{EvaluationStageRunInput}, Mode: EvaluationModeEnforce, Evaluator: evaluatorFunc(func(context.Context, EvaluationRequest) (EvaluationResult, error) {
			return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "allowed"}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := registry.Evaluate(t.Context(), EvaluationRequest{Stage: EvaluationStageRunInput, Content: "safe"})
	if err != nil || report.Decision != EvaluationDecisionAllow {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if len(report.Findings) != 2 || report.Findings[0].Evaluator != "a_enforcer" || report.Findings[1].Evaluator != "z_observer" {
		t.Fatalf("findings are not deterministic: %#v", report.Findings)
	}
	if !registry.Enforces(EvaluationStageRunInput) || registry.Count(EvaluationStageRunInput) != 2 {
		t.Fatalf("registry stage metadata is wrong")
	}
}

func TestEvaluationRegistryFailsClosedWithoutLeakingEvaluatorMessage(t *testing.T) {
	registry, err := NewEvaluationRegistry([]EvaluationRegistration{{
		Name: "secret_policy", Stages: []EvaluationStage{EvaluationStageModelOutput}, Mode: EvaluationModeEnforce,
		Evaluator: evaluatorFunc(func(context.Context, EvaluationRequest) (EvaluationResult, error) {
			return EvaluationResult{Decision: EvaluationDecisionDeny, Code: "policy_denied", Message: "secret raw output"}, nil
		}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := registry.Evaluate(t.Context(), EvaluationRequest{Stage: EvaluationStageModelOutput, Content: "secret raw output"})
	if !errors.Is(err, ErrEvaluationBlocked) || !report.Blocked() {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if strings.Contains(err.Error(), "secret raw output") {
		t.Fatalf("evaluation error leaked evaluator message: %v", err)
	}
}

func TestRuntimeEvaluationEventPersistsOnlySafeClassifications(t *testing.T) {
	run := model.Run{RunID: "run_eval", Actor: model.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread"}}
	event := runtimeEvaluationEvent(run, "step_eval", EvaluationReport{
		Stage: EvaluationStageToolOutput, Decision: EvaluationDecisionDeny, Findings: []EvaluationFinding{{
			Evaluator: "output_policy", Mode: EvaluationModeEnforce, Decision: EvaluationDecisionDeny, Code: "unsafe_output",
			Message: "secret model output", Error: "secret evaluator error", Labels: []string{"secret label"}, Metadata: map[string]string{"raw": "secret metadata"},
		}},
	})
	for _, forbidden := range []string{"secret model output", "secret evaluator error", "secret label", "secret metadata"} {
		if strings.Contains(event.PayloadJSON, forbidden) {
			t.Fatalf("durable evaluation event leaked %q: %s", forbidden, event.PayloadJSON)
		}
	}
	if !strings.Contains(event.PayloadJSON, `"evaluator":"output_policy"`) || !strings.Contains(event.PayloadJSON, `"code":"unsafe_output"`) {
		t.Fatalf("durable classification missing: %s", event.PayloadJSON)
	}
}

func TestBoundaryIntegrityEvaluatorRejectsUnsafeBoundaries(t *testing.T) {
	evaluator := NewBoundaryIntegrityEvaluator()
	tests := []struct {
		name    string
		request EvaluationRequest
		code    string
	}{
		{name: "nul input", request: EvaluationRequest{Stage: EvaluationStageRunInput, Content: "bad\x00input"}, code: "nul_byte_rejected"},
		{name: "public tool protocol", request: EvaluationRequest{Stage: EvaluationStageModelOutput, Content: `<tool_call>{"name":"search"}</tool_call>`, Metadata: map[string]string{valuePhaseA62799FA: "direct"}}, code: "tool_protocol_not_public"},
		{name: "oversized tool payload", request: EvaluationRequest{Stage: EvaluationStageToolInput, PayloadJSON: strings.Repeat("x", 17)}, code: "json_boundary_too_large"},
	}
	evaluator.MaxJSONBytes = 16
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(t.Context(), test.request)
			if err != nil || result.Decision != EvaluationDecisionDeny || result.Code != test.code {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
	planner, err := evaluator.Evaluate(t.Context(), EvaluationRequest{Stage: EvaluationStageModelOutput, Content: `{"steps":[]}`, Metadata: map[string]string{valuePhaseA62799FA: evaluationPhasePlanner}})
	if err != nil || planner.Decision != EvaluationDecisionAllow {
		t.Fatalf("planner JSON should pass: %#v %v", planner, err)
	}
}

func TestRunDeltaCollectorHoldsPublicDeltasUntilEvaluationCompletes(t *testing.T) {
	published := make([]string, 0, 1)
	collector := &runDeltaCollector{holdForEvaluation: true, publishDelta: func(delta string) error {
		published = append(published, delta)
		return nil
	}}
	if err := collector.accept(GenerateStreamEvent{Delta: "safe final answer"}); err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Fatalf("held stream published before evaluation: %#v", published)
	}
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 || published[0] != "safe final answer" {
		t.Fatalf("held stream did not publish after evaluation: %#v", published)
	}
}

func TestModelOutputEvaluationRequestContainsOnlyWireOutput(t *testing.T) {
	run := model.Run{RunID: "run_wire", Actor: model.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread"}}
	request := modelOutputEvaluationRequest(run, "step_wire", valueStepB959B536, &GenerateOutput{Text: "comment", ToolCalls: []ToolCall{{ToolCallID: "call_1", ToolName: testFirewallToolName, ArgumentsJSON: `{}`}}})
	if request.Content != "comment" || request.Metadata[valuePhaseA62799FA] != valueStepB959B536 || !json.Valid([]byte(request.PayloadJSON)) {
		t.Fatalf("request=%#v", request)
	}
}
