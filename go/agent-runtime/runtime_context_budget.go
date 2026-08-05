package agentruntime

import (
	"context"
	"encoding/json"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

const (
	contextTrimDropFailedToolAttempt  = "drop_superseded_failed_tool_attempt"
	contextTrimDropOldestCompleteTurn = "drop_oldest_complete_turn"
)

// ContextBudgetAssessment is the server-owned accounting result for one complete model request.
type ContextBudgetAssessment struct {
	HardInputTokens       int64
	SoftInputTokens       int64
	RawTokenEstimate      int64
	AdjustedTokenEstimate int64
	TokenCountSource      ContextTokenCountSource
	SerializedBytes       int64
	TrimActions           []PromptTrimAction
}

// ContextManagementTrace is the persisted, content-free management decision for a snapshot revision.
type ContextManagementTrace struct {
	Mode                   string                  `json:"mode"`
	SnapshotRevision       int                     `json:"snapshotRevision"`
	HardInputTokens        int64                   `json:"hardInputTokens"`
	SoftInputTokens        int64                   `json:"softInputTokens"`
	RawTokenEstimate       int64                   `json:"rawTokenEstimate"`
	AdjustedTokenEstimate  int64                   `json:"adjustedTokenEstimate"`
	TokenCountSource       ContextTokenCountSource `json:"tokenCountSource"`
	LoadedMessageCount     int                     `json:"loadedMessageCount"`
	RetainedMessageCount   int                     `json:"retainedMessageCount"`
	SummarizedMessageCount int                     `json:"summarizedMessageCount"`
	TrimmedMessageCount    int                     `json:"trimmedMessageCount"`
	SummaryArtifactID      string                  `json:"summaryArtifactID,omitempty"`
	SummaryStrategy        string                  `json:"summaryStrategy,omitempty"`
	CoveredThrough         model.ProjectionRef     `json:"coveredThrough,omitempty"`
	CoveredPathHash        string                  `json:"coveredPathHash,omitempty"`
	SummaryTokenEstimate   int64                   `json:"summaryTokenEstimate"`
	CompressionRatio       float64                 `json:"compressionRatio"`
	DurationMS             int64                   `json:"durationMS"`
	Fallback               bool                    `json:"fallback"`
}

func contextHardInputBudget(config ContextConfig, route *LLMRoute, modelName string) int64 {
	capabilityName, capabilitiesJSON := strings.TrimSpace(modelName), ""
	if route != nil {
		capabilityName = firstNonEmptyString(strings.TrimSpace(route.PlatformModelName), capabilityName)
		capabilitiesJSON = route.ModelCapabilitiesJSON
	}
	modelBudget := int64(modelcap.Default.Resolve(capabilityName, capabilitiesJSON).EffectiveContextBudget())
	configured := int64(config.MaxInputTokens)
	if configured > 0 && (modelBudget <= 0 || configured < modelBudget) {
		return configured
	}
	return modelBudget
}

func contextSoftInputBudget(hard int64, percent int) int64 {
	if hard <= 0 {
		return 0
	}
	if percent <= 0 || percent >= 100 {
		percent = 80
	}
	soft := hard * int64(percent) / 100
	if soft < 1 {
		return 1
	}
	return soft
}

func estimateGenerateInputTokens(input GenerateInput) int64 {
	raw, err := json.Marshal(input)
	if err != nil {
		return estimatePromptTokens(input.Messages) + estimateTokens(input.Instructions) + estimateToolDefinitionTokens(input.Tools)
	}
	return estimateTokens(string(raw))
}

func estimateGenerateInputBytes(input GenerateInput) int64 {
	raw, err := json.Marshal(input)
	if err != nil {
		return 0
	}
	return int64(len(raw))
}

func (s *Engine) countContextTokens(ctx context.Context, route *LLMRoute, input GenerateInput, safetyPercent int) ContextTokenCountResult {
	if s != nil && s.contextTokenCounter != nil {
		counterInput := ContextTokenCountInput{Request: input}
		if route != nil {
			counterInput.PlatformModelName, counterInput.UpstreamModel = route.PlatformModelName, route.UpstreamModel
			counterInput.Protocol, counterInput.ModelCapabilitiesJSON = route.Protocol, route.ModelCapabilitiesJSON
		}
		if result, err := s.contextTokenCounter.CountContextTokens(ctx, counterInput); err == nil && result.Tokens >= 0 {
			result.Source = ContextTokenCountExact
			return result
		}
	}
	raw := estimateGenerateInputTokens(input)
	if safetyPercent < 0 {
		safetyPercent = 0
	}
	adjusted := raw
	if raw > 0 && safetyPercent > 0 {
		adjusted = (raw*int64(100+safetyPercent) + 99) / 100
	}
	return ContextTokenCountResult{Tokens: raw, AdjustedTokens: adjusted, Source: ContextTokenCountEstimated}
}

func (s *Engine) assessContextBudget(ctx context.Context, route *LLMRoute, effective effectiveTextRunConfig, input GenerateInput) ContextBudgetAssessment {
	config := normalizeContextConfig(effective.Context)
	count := s.countContextTokens(ctx, route, input, config.EstimateSafetyPercent)
	adjusted := count.AdjustedTokens
	if count.Source == ContextTokenCountExact || adjusted <= 0 {
		adjusted = count.Tokens
	}
	hard := contextHardInputBudget(config, route, effective.PlatformModelName)
	return ContextBudgetAssessment{
		HardInputTokens:       hard,
		SoftInputTokens:       contextSoftInputBudget(hard, config.SoftLimitPercent),
		RawTokenEstimate:      count.Tokens,
		AdjustedTokenEstimate: adjusted,
		TokenCountSource:      count.Source,
		SerializedBytes:       estimateGenerateInputBytes(input),
	}
}

func contextTransportByteBudget(hardTokens int64) int64 {
	if hardTokens <= 0 {
		return 0
	}
	budget := hardTokens * promptTransportBytesNumerator / promptTransportBytesDenominator
	if budget < 1 {
		return 1
	}
	return budget
}

func contextInputWithinHardBudget(assessment ContextBudgetAssessment) bool {
	if assessment.HardInputTokens > 0 && assessment.AdjustedTokenEstimate > assessment.HardInputTokens {
		return false
	}
	byteBudget := contextTransportByteBudget(assessment.HardInputTokens)
	return byteBudget <= 0 || assessment.SerializedBytes <= byteBudget
}

// enforceGenerateInputBudget is the mandatory final gate before every upstream generation call.
func (s *Engine) enforceGenerateInputBudget(ctx context.Context, run model.Run, effective effectiveTextRunConfig, route *LLMRoute, input GenerateInput) (GenerateInput, ContextBudgetAssessment, error) {
	assessment := s.assessContextBudget(ctx, route, effective, input)
	if contextInputWithinHardBudget(assessment) {
		return input, assessment, nil
	}

	result := input
	result.Messages = cloneLLMMessages(input.Messages)
	result, assessment, withinBudget := s.trimSupersededToolFailures(ctx, result, assessment, route, effective)
	if withinBudget {
		return result, assessment, nil
	}

	var artifacts []model.ContextArtifact
	result.Messages, artifacts, assessment.TrimActions = compactOversizedToolResults(run, result.Messages, assessment.HardInputTokens, assessment.TrimActions)
	if err := s.persistCompactedToolArtifacts(ctx, run, artifacts); err != nil {
		return GenerateInput{}, assessment, err
	}
	assessment = mergeBudgetActions(s.assessContextBudget(ctx, route, effective, result), assessment.TrimActions)
	return s.trimOldestTurnsToBudget(ctx, result, assessment, route, effective)
}

func (s *Engine) trimSupersededToolFailures(ctx context.Context, input GenerateInput, assessment ContextBudgetAssessment, route *LLMRoute, effective effectiveTextRunConfig) (GenerateInput, ContextBudgetAssessment, bool) {
	result := input
	for {
		trimmed, ok := trimSupersededFailedToolAttempt(result.Messages)
		if !ok {
			return result, assessment, false
		}
		result.Messages = trimmed
		assessment.TrimActions = append(assessment.TrimActions, PromptTrimAction{Action: contextTrimDropFailedToolAttempt, MessageCount: 2})
		assessment = mergeBudgetActions(s.assessContextBudget(ctx, route, effective, result), assessment.TrimActions)
		if contextInputWithinHardBudget(assessment) {
			return result, assessment, true
		}
	}
}

func (s *Engine) persistCompactedToolArtifacts(ctx context.Context, run model.Run, artifacts []model.ContextArtifact) error {
	if len(artifacts) == 0 {
		return nil
	}
	snapshot, err := s.repo.GetRunContextSnapshot(ctx, run.Actor, run.RunID)
	if err != nil {
		return err
	}
	artifacts = sealContextArtifacts(run.RunID, snapshot.SnapshotID, artifacts)
	s.applyContextArtifactRetention(artifacts)
	if err = s.repo.CreateContextArtifacts(context.WithoutCancel(ctx), artifacts); err != nil {
		return err
	}
	return nil
}

func (s *Engine) trimOldestTurnsToBudget(ctx context.Context, input GenerateInput, assessment ContextBudgetAssessment, route *LLMRoute, effective effectiveTextRunConfig) (GenerateInput, ContextBudgetAssessment, error) {
	result := input
	for !contextInputWithinHardBudget(assessment) {
		trimmed, ok := trimOldestPromptTurn(result.Messages)
		if !ok {
			return GenerateInput{}, assessment, ErrContextBudgetExceeded
		}
		removed := len(result.Messages) - len(trimmed)
		result.Messages = trimmed
		assessment.TrimActions = append(assessment.TrimActions, PromptTrimAction{Action: contextTrimDropOldestCompleteTurn, MessageCount: removed})
		assessment = mergeBudgetActions(s.assessContextBudget(ctx, route, effective, result), assessment.TrimActions)
	}
	return result, assessment, nil
}

func mergeBudgetActions(current ContextBudgetAssessment, actions []PromptTrimAction) ContextBudgetAssessment {
	current.TrimActions = append([]PromptTrimAction(nil), actions...)
	return current
}
