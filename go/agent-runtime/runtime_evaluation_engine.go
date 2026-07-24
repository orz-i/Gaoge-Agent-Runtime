package agentruntime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	eventGuardrailEvaluated = "guardrail.evaluated"
)

func (s *Engine) evaluateRuntimeBoundary(ctx context.Context, request EvaluationRequest) (EvaluationReport, error) {
	startedAt := time.Now()
	report := EvaluationReport{Stage: request.Stage, Decision: EvaluationDecisionAllow}
	if s == nil || s.evaluations == nil || s.evaluations.Count(request.Stage) == 0 {
		return report, nil
	}
	ctx, span := s.startSpan(ctx, "agentruntime.guardrail.evaluate",
		String("evaluation.stage", string(request.Stage)),
		String("run.id", strings.TrimSpace(request.RunID)),
		String("step.id", strings.TrimSpace(request.StepID)),
		String("tool.call_id", strings.TrimSpace(request.ToolCallID)),
		String("tool.key", strings.TrimSpace(request.ToolKey)),
		Int("evaluation.count", s.evaluations.Count(request.Stage)),
	)
	defer span.End()
	report, err := s.evaluations.Evaluate(ctx, request)
	span.SetAttributes(
		String("evaluation.decision", string(report.Decision)),
		Int("evaluation.findings", len(report.Findings)),
		Int64("evaluation.latency_ms", time.Since(startedAt).Milliseconds()),
	)
	if err != nil {
		blockedEvaluator, blockedCode := blockedEvaluationIdentity(report)
		span.SetAttributes(
			String("evaluation.blocked_evaluator", blockedEvaluator),
			String("evaluation.blocked_code", blockedCode),
		)
		if s.logger != nil {
			s.logger.Warn("runtime_evaluation_blocked",
				String("evaluation_stage", string(request.Stage)),
				String("run_id", strings.TrimSpace(request.RunID)),
				String("step_id", strings.TrimSpace(request.StepID)),
				String("tool_call_id", strings.TrimSpace(request.ToolCallID)),
				String("evaluator", blockedEvaluator),
				String("evaluation_code", blockedCode),
			)
		}
	}
	return report, err
}

func (s *Engine) evaluateAndPersistRuntimeBoundary(ctx context.Context, run domain.Run, request EvaluationRequest) error {
	report, evaluationErr := s.evaluateRuntimeBoundary(ctx, request)
	if persistErr := s.appendRuntimeEvaluationReport(context.WithoutCancel(ctx), run, request.StepID, report); persistErr != nil {
		return persistErr
	}
	return evaluationErr
}

func (s *Engine) appendRuntimeEvaluationReport(ctx context.Context, run domain.Run, stepID string, report EvaluationReport) error {
	if len(report.Findings) == 0 {
		return nil
	}
	event := runtimeEvaluationEvent(run, stepID, report)
	return s.appendRunEvents(ctx, run.RunID, []domain.Event{event})
}

func runtimeEvaluationEvent(run domain.Run, stepID string, report EvaluationReport) domain.Event {
	payload := map[string]interface{}{
		"stage":       report.Stage,
		"decision":    report.Decision,
		"latencyMS":   report.LatencyMS,
		"evaluations": durableEvaluationFindings(report.Findings),
	}
	event := newRunEvent(run, eventGuardrailEvaluated, stepID, "Runtime guardrail evaluated", payload, nil)
	if report.Blocked() {
		event.Status = "blocked"
	} else {
		event.Status = "completed"
	}
	return event
}

type durableEvaluationFinding struct {
	Evaluator string             `json:"evaluator"`
	Mode      EvaluationMode     `json:"mode"`
	Decision  EvaluationDecision `json:"decision"`
	Code      string             `json:"code,omitempty"`
	Score     *float64           `json:"score,omitempty"`
	LatencyMS int64              `json:"latencyMS"`
}

func durableEvaluationFindings(findings []EvaluationFinding) []durableEvaluationFinding {
	result := make([]durableEvaluationFinding, 0, len(findings))
	for _, finding := range findings {
		result = append(result, durableEvaluationFinding{
			Evaluator: strings.TrimSpace(finding.Evaluator),
			Mode:      finding.Mode,
			Decision:  finding.Decision,
			Code:      strings.TrimSpace(finding.Code),
			Score:     finding.Score,
			LatencyMS: finding.LatencyMS,
		})
	}
	return result
}

func blockedEvaluationIdentity(report EvaluationReport) (string, string) {
	for _, finding := range report.Findings {
		if finding.Mode == EvaluationModeEnforce && finding.Decision == EvaluationDecisionDeny {
			return strings.TrimSpace(finding.Evaluator), firstNonEmptyString(strings.TrimSpace(finding.Code), "evaluation_denied")
		}
	}
	return "", "evaluation_denied"
}

func modelOutputEvaluationRequest(run domain.Run, stepID, phase string, output *GenerateOutput) EvaluationRequest {
	request := EvaluationRequest{
		Stage:       EvaluationStageModelOutput,
		Actor:       run.Actor,
		Thread:      run.Thread,
		RunID:       run.RunID,
		StepID:      stepID,
		ContentType: evaluationContentTypeText,
		Metadata:    map[string]string{valuePhaseA62799FA: strings.TrimSpace(phase)},
	}
	if output == nil {
		return request
	}
	request.Content = output.Text
	if len(output.ToolCalls) > 0 || len(output.ServerToolCalls) > 0 {
		payload, err := json.Marshal(map[string]interface{}{
			"toolCalls":       output.ToolCalls,
			"serverToolCalls": output.ServerToolCalls,
		})
		if err == nil {
			request.PayloadJSON = string(payload)
		}
	}
	return request
}

func toolInputEvaluationRequest(run domain.Run, stepID string, tool ResolvedTool, call ToolCall) EvaluationRequest {
	return EvaluationRequest{
		Stage:       EvaluationStageToolInput,
		Actor:       run.Actor,
		Thread:      run.Thread,
		RunID:       run.RunID,
		StepID:      stepID,
		ToolCallID:  call.ToolCallID,
		ToolKey:     tool.ToolKey,
		ToolName:    tool.ModelName,
		ContentType: "application/json",
		PayloadJSON: call.ArgumentsJSON,
		Metadata: map[string]string{
			"providerKind":    tool.ProviderKind,
			"sideEffectLevel": tool.SideEffectLevel,
		},
	}
}

func toolOutputEvaluationRequest(run domain.Run, stepID string, tool ResolvedTool, call ToolCall, output string) EvaluationRequest {
	return EvaluationRequest{
		Stage:       EvaluationStageToolOutput,
		Actor:       run.Actor,
		Thread:      run.Thread,
		RunID:       run.RunID,
		StepID:      stepID,
		ToolCallID:  call.ToolCallID,
		ToolKey:     tool.ToolKey,
		ToolName:    tool.ModelName,
		ContentType: "application/json",
		PayloadJSON: output,
		Metadata: map[string]string{
			"providerKind": tool.ProviderKind,
		},
	}
}
