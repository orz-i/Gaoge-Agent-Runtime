package agentruntime

import (
	"context"
	"strings"
	"unicode/utf8"
)

const (
	defaultEvaluationTextBytes = 2 << 20
	defaultEvaluationJSONBytes = 8 << 20
	evaluationContentTypeText  = "text/plain"
	evaluationPhasePlanner     = "planner"
	evaluationPhasePlanRepair  = "planner_repair"
)

// BoundaryIntegrityEvaluator enforces transport-safe boundary payloads and
// prevents tool-protocol text from becoming a public model response. It is
// host-neutral and intentionally does not inspect product-specific semantics.
type BoundaryIntegrityEvaluator struct {
	MaxTextBytes int
	MaxJSONBytes int
}

func NewBoundaryIntegrityEvaluator() BoundaryIntegrityEvaluator {
	return BoundaryIntegrityEvaluator{MaxTextBytes: defaultEvaluationTextBytes, MaxJSONBytes: defaultEvaluationJSONBytes}
}

func (e BoundaryIntegrityEvaluator) Evaluate(_ context.Context, request EvaluationRequest) (EvaluationResult, error) {
	maxText, maxJSON := e.boundaryLimits()
	for _, code := range []string{
		boundaryEncodingFailure(request),
		boundarySizeFailure(request, maxText, maxJSON),
		boundaryProtocolFailure(request),
	} {
		if code != "" {
			return deniedEvaluation(code), nil
		}
	}
	return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "boundary_integrity_ok"}, nil
}

func (e BoundaryIntegrityEvaluator) boundaryLimits() (int, int) {
	maxText, maxJSON := e.MaxTextBytes, e.MaxJSONBytes
	if maxText <= 0 {
		maxText = defaultEvaluationTextBytes
	}
	if maxJSON <= 0 {
		maxJSON = defaultEvaluationJSONBytes
	}
	return maxText, maxJSON
}

func boundaryEncodingFailure(request EvaluationRequest) string {
	if !utf8.ValidString(request.Content) || !utf8.ValidString(request.PayloadJSON) {
		return "invalid_utf8"
	}
	if strings.ContainsRune(request.Content, '\x00') || strings.ContainsRune(request.PayloadJSON, '\x00') {
		return "nul_byte_rejected"
	}
	return ""
}

func boundarySizeFailure(request EvaluationRequest, maxText, maxJSON int) string {
	if len(request.Content) > maxText {
		return "text_boundary_too_large"
	}
	if len(request.PayloadJSON) > maxJSON {
		return "json_boundary_too_large"
	}
	return ""
}

func boundaryProtocolFailure(request EvaluationRequest) string {
	if request.Stage == EvaluationStageModelOutput && shouldEvaluatePublicModelText(request) && classifyModelText(request.Content) == ModelTextToolProtocol {
		return "tool_protocol_not_public"
	}
	return ""
}

func shouldEvaluatePublicModelText(request EvaluationRequest) bool {
	phase := strings.TrimSpace(request.Metadata[valuePhaseA62799FA])
	if phase == evaluationPhasePlanner || phase == evaluationPhasePlanRepair {
		return false
	}
	// Local tool turns may include provider commentary alongside actual tool
	// calls. The commentary is already suppressed and never becomes public.
	return strings.TrimSpace(request.PayloadJSON) == ""
}

func deniedEvaluation(code string) EvaluationResult {
	return EvaluationResult{Decision: EvaluationDecisionDeny, Code: strings.TrimSpace(code)}
}
