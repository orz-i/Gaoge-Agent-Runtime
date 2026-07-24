package agentruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// EvaluationStage identifies a semantic Runtime boundary. Values are stable
// public contract strings so hosts can register reusable evaluators without
// importing host-specific execution details.
type EvaluationStage string

const (
	EvaluationStageRunInput    EvaluationStage = "run.input"
	EvaluationStageModelOutput EvaluationStage = "model.output"
	EvaluationStageToolInput   EvaluationStage = "tool.input"
	EvaluationStageToolOutput  EvaluationStage = "tool.output"
)

type EvaluationMode string

const (
	// EvaluationModeObserve records findings but never blocks execution.
	EvaluationModeObserve EvaluationMode = "observe"
	// EvaluationModeEnforce fails closed when an evaluator denies or errors.
	EvaluationModeEnforce EvaluationMode = "enforce"
)

type EvaluationDecision string

const (
	EvaluationDecisionAllow EvaluationDecision = "allow"
	EvaluationDecisionDeny  EvaluationDecision = "deny"
)

var (
	ErrEvaluationBlocked         = errors.New("runtime evaluation blocked")
	errEvaluationDecisionInvalid = errors.New("invalid evaluation decision")
	errEvaluationFindingDenied   = errors.New("evaluation finding denied")
)

// EvaluationRequest carries only one boundary payload. Content and PayloadJSON
// are mutually optional; evaluators must not retain either value after the call.
type EvaluationRequest struct {
	Stage       EvaluationStage
	Actor       domain.ActorRef
	Thread      domain.ThreadRef
	RunID       string
	StepID      string
	ToolCallID  string
	ToolKey     string
	ToolName    string
	ContentType string
	Content     string
	PayloadJSON string
	Metadata    map[string]string
}

// EvaluationResult is intentionally bounded and transport-neutral. Metadata
// must contain classifications only; raw model/tool content belongs in neither
// durable events nor traces.
type EvaluationResult struct {
	Decision EvaluationDecision
	Code     string
	Message  string
	Score    *float64
	Labels   []string
	Metadata map[string]string
}

type Evaluator interface {
	Evaluate(context.Context, EvaluationRequest) (EvaluationResult, error)
}

type EvaluationRegistration struct {
	Name      string
	Stages    []EvaluationStage
	Mode      EvaluationMode
	Evaluator Evaluator
}

type EvaluationFinding struct {
	Evaluator string             `json:"evaluator"`
	Mode      EvaluationMode     `json:"mode"`
	Decision  EvaluationDecision `json:"decision"`
	Code      string             `json:"code,omitempty"`
	Message   string             `json:"message,omitempty"`
	Score     *float64           `json:"score,omitempty"`
	Labels    []string           `json:"labels,omitempty"`
	Metadata  map[string]string  `json:"metadata,omitempty"`
	Error     string             `json:"error,omitempty"`
	LatencyMS int64              `json:"latencyMS"`
}

type EvaluationReport struct {
	Stage     EvaluationStage     `json:"stage"`
	Decision  EvaluationDecision  `json:"decision"`
	Findings  []EvaluationFinding `json:"findings,omitempty"`
	LatencyMS int64               `json:"latencyMS"`
}

func (report EvaluationReport) Blocked() bool {
	return report.Decision == EvaluationDecisionDeny
}

type EvaluationRegistry interface {
	Evaluate(context.Context, EvaluationRequest) (EvaluationReport, error)
	Count(EvaluationStage) int
	Enforces(EvaluationStage) bool
}

type registeredEvaluator struct {
	name      string
	mode      EvaluationMode
	evaluator Evaluator
}

type immutableEvaluationRegistry struct {
	byStage map[EvaluationStage][]registeredEvaluator
}

// NewEvaluationRegistry freezes registration order by evaluator name. This
// makes evaluation reports deterministic across hosts and process restarts.
func NewEvaluationRegistry(registrations []EvaluationRegistration) (EvaluationRegistry, error) {
	byStage := make(map[EvaluationStage][]registeredEvaluator)
	seen := make(map[string]struct{})
	for _, registration := range registrations {
		item, stages, err := normalizeEvaluationRegistration(registration, seen)
		if err != nil {
			return nil, err
		}
		for _, stage := range stages {
			byStage[stage] = append(byStage[stage], item)
		}
	}
	for stage := range byStage {
		sort.SliceStable(byStage[stage], func(i, j int) bool { return byStage[stage][i].name < byStage[stage][j].name })
	}
	return immutableEvaluationRegistry{byStage: byStage}, nil
}

func normalizeEvaluationRegistration(registration EvaluationRegistration, seen map[string]struct{}) (registeredEvaluator, []EvaluationStage, error) {
	name := strings.TrimSpace(registration.Name)
	if name == "" || registration.Evaluator == nil || len(registration.Stages) == 0 {
		return registeredEvaluator{}, nil, fmt.Errorf("%w: invalid evaluation registration", ErrInvalidInput)
	}
	if _, duplicate := seen[name]; duplicate {
		return registeredEvaluator{}, nil, fmt.Errorf("%w: duplicate evaluator %s", ErrInvalidInput, name)
	}
	mode := normalizedEvaluationMode(registration.Mode)
	if mode == "" {
		return registeredEvaluator{}, nil, fmt.Errorf("%w: evaluator %s mode", ErrInvalidInput, name)
	}
	stages := uniqueEvaluationStages(registration.Stages)
	if !validEvaluationStages(stages) {
		return registeredEvaluator{}, nil, fmt.Errorf("%w: evaluator %s stage", ErrInvalidInput, name)
	}
	seen[name] = struct{}{}
	return registeredEvaluator{name: name, mode: mode, evaluator: registration.Evaluator}, stages, nil
}

func normalizedEvaluationMode(mode EvaluationMode) EvaluationMode {
	if mode == "" {
		return EvaluationModeEnforce
	}
	if mode == EvaluationModeObserve || mode == EvaluationModeEnforce {
		return mode
	}
	return ""
}

func validEvaluationStages(stages []EvaluationStage) bool {
	for _, stage := range stages {
		if !validEvaluationStage(stage) {
			return false
		}
	}
	return true
}

func uniqueEvaluationStages(values []EvaluationStage) []EvaluationStage {
	seen := make(map[EvaluationStage]struct{}, len(values))
	result := make([]EvaluationStage, 0, len(values))
	for _, stage := range values {
		if _, ok := seen[stage]; ok {
			continue
		}
		seen[stage] = struct{}{}
		result = append(result, stage)
	}
	return result
}

func validEvaluationStage(stage EvaluationStage) bool {
	switch stage {
	case EvaluationStageRunInput, EvaluationStageModelOutput, EvaluationStageToolInput, EvaluationStageToolOutput:
		return true
	default:
		return false
	}
}

func (registry immutableEvaluationRegistry) Count(stage EvaluationStage) int {
	return len(registry.byStage[stage])
}

func (registry immutableEvaluationRegistry) Enforces(stage EvaluationStage) bool {
	for _, item := range registry.byStage[stage] {
		if item.mode == EvaluationModeEnforce {
			return true
		}
	}
	return false
}

func (registry immutableEvaluationRegistry) Evaluate(ctx context.Context, request EvaluationRequest) (EvaluationReport, error) {
	startedAt := time.Now()
	report := EvaluationReport{Stage: request.Stage, Decision: EvaluationDecisionAllow}
	for _, item := range registry.byStage[request.Stage] {
		findingStartedAt := time.Now()
		result, err := item.evaluator.Evaluate(ctx, cloneEvaluationRequest(request))
		finding := normalizedEvaluationFinding(item, result, err, time.Since(findingStartedAt))
		report.Findings = append(report.Findings, finding)
		if item.mode == EvaluationModeEnforce && (err != nil || finding.Decision == EvaluationDecisionDeny) {
			report.Decision = EvaluationDecisionDeny
			report.LatencyMS = time.Since(startedAt).Milliseconds()
			return report, errors.Join(ErrEvaluationBlocked, evaluationFindingError(finding))
		}
	}
	report.LatencyMS = time.Since(startedAt).Milliseconds()
	return report, nil
}

func cloneEvaluationRequest(request EvaluationRequest) EvaluationRequest {
	request.Metadata = cloneEvaluationMetadata(request.Metadata)
	return request
}

func cloneEvaluationMetadata(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return result
}

func normalizedEvaluationFinding(item registeredEvaluator, result EvaluationResult, err error, latency time.Duration) EvaluationFinding {
	decision := result.Decision
	if decision == "" {
		decision = EvaluationDecisionAllow
	}
	if decision != EvaluationDecisionAllow && decision != EvaluationDecisionDeny {
		err = errors.Join(err, fmt.Errorf("%w: %q", errEvaluationDecisionInvalid, decision))
		decision = EvaluationDecisionDeny
	}
	finding := EvaluationFinding{
		Evaluator: item.name,
		Mode:      item.mode,
		Decision:  decision,
		Code:      strings.TrimSpace(result.Code),
		Message:   truncateEvaluationText(result.Message, 240),
		Score:     result.Score,
		Labels:    uniqueEvaluationLabels(result.Labels),
		Metadata:  cloneEvaluationMetadata(result.Metadata),
		LatencyMS: latency.Milliseconds(),
	}
	if err != nil {
		finding.Error = truncateEvaluationText(err.Error(), 240)
		finding.Decision = EvaluationDecisionDeny
		if finding.Code == "" {
			finding.Code = "evaluator_error"
		}
	}
	return finding
}

func uniqueEvaluationLabels(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func truncateEvaluationText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func evaluationFindingError(finding EvaluationFinding) error {
	code := firstNonEmptyString(finding.Code, "evaluation_denied")
	return fmt.Errorf("%w: evaluator=%s code=%s", errEvaluationFindingDenied, finding.Evaluator, code)
}
