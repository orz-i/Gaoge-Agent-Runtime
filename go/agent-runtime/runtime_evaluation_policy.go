package agentruntime

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

const (
	evaluationCodeSecretMaterial              = "secret_material_detected"
	evaluationCodeInstructionInjection        = "instruction_injection_detected"
	evaluationCodeDestructiveApprovalRequired = "destructive_tool_requires_approval"
	evaluationCodeUnknownApprovalRequired     = "unknown_side_effect_requires_approval"
	evaluationCodeProviderReceiptRequired     = "provider_receipt_required"
)

type classifiedEvaluationPattern struct {
	code    string
	pattern *regexp.Regexp
}

var secretMaterialPatterns = []classifiedEvaluationPattern{
	{code: "private_key", pattern: regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`)},
	{code: "authorization_bearer", pattern: regexp.MustCompile(`(?i)\bauthorization\s*[:=]\s*bearer\s+[a-z0-9._~+/=-]{16,}`)},
	{code: "provider_token", pattern: regexp.MustCompile(`\b(?:AKIA[0-9A-Z]{16}|ASIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9-]{16,}|sk-(?:proj-)?[A-Za-z0-9_-]{20,})\b`)},
}

var instructionInjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)\b(?:ignore|disregard|override|forget)\b.{0,80}\b(?:previous|prior|system|developer|original)\b.{0,40}\b(?:instruction|message|prompt|rule)s?\b`),
	regexp.MustCompile(`(?is)\b(?:reveal|print|return|expose|leak|send)\b.{0,80}\b(?:system prompt|developer message|api key|secret|credential|access token)s?\b`),
	regexp.MustCompile(`(?i)\b(?:you are now|act as a new|new system instructions|developer override)\b`),
}

// ProductionEvaluationRegistrations returns the host-neutral online safety
// policy pack used by production Runtime hosts. Registration modes are
// deliberately asymmetric: suspicious ingress is observed, while unsafe
// egress and tool execution are enforced.
func ProductionEvaluationRegistrations() []EvaluationRegistration {
	secretEvaluator := NewSecretMaterialEvaluator()
	injectionEvaluator := NewInstructionInjectionEvaluator()
	return []EvaluationRegistration{
		{
			Name: "boundary_integrity",
			Stages: []EvaluationStage{
				EvaluationStageRunInput,
				EvaluationStageModelOutput,
				EvaluationStageToolInput,
				EvaluationStageToolOutput,
			},
			Mode: EvaluationModeEnforce, Evaluator: NewBoundaryIntegrityEvaluator(),
		},
		{
			Name: "credential_exposure",
			Stages: []EvaluationStage{
				EvaluationStageModelOutput,
				EvaluationStageToolOutput,
			},
			Mode: EvaluationModeEnforce, Evaluator: secretEvaluator,
		},
		{
			Name: "credential_ingress_observer",
			Stages: []EvaluationStage{
				EvaluationStageRunInput,
				EvaluationStageToolInput,
			},
			Mode: EvaluationModeObserve, Evaluator: secretEvaluator,
		},
		{
			Name: "instruction_injection_observer",
			Stages: []EvaluationStage{
				EvaluationStageRunInput,
				EvaluationStageModelOutput,
			},
			Mode: EvaluationModeObserve, Evaluator: injectionEvaluator,
		},
		{
			Name: "tool_semantic_safety",
			Stages: []EvaluationStage{
				EvaluationStageToolInput,
			},
			Mode: EvaluationModeEnforce, Evaluator: NewToolSemanticSafetyEvaluator(),
		},
		{
			Name: "untrusted_tool_injection",
			Stages: []EvaluationStage{
				EvaluationStageToolOutput,
			},
			Mode: EvaluationModeEnforce, Evaluator: injectionEvaluator,
		},
	}
}

type SecretMaterialEvaluator struct{}

func NewSecretMaterialEvaluator() SecretMaterialEvaluator {
	return SecretMaterialEvaluator{}
}

func (SecretMaterialEvaluator) Evaluate(_ context.Context, request EvaluationRequest) (EvaluationResult, error) {
	if classification := classifySecretMaterial(request); classification != "" {
		return EvaluationResult{
			Decision: EvaluationDecisionDeny,
			Code:     evaluationCodeSecretMaterial,
			Labels:   []string{classification},
		}, nil
	}
	return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "secret_material_clear"}, nil
}

func classifySecretMaterial(request EvaluationRequest) string {
	corpus := evaluationCorpus(request)
	for _, item := range secretMaterialPatterns {
		if item.pattern.MatchString(corpus) {
			return item.code
		}
	}
	return credentialClassificationFromJSON(request.PayloadJSON)
}

type InstructionInjectionEvaluator struct{}

func NewInstructionInjectionEvaluator() InstructionInjectionEvaluator {
	return InstructionInjectionEvaluator{}
}

func (InstructionInjectionEvaluator) Evaluate(_ context.Context, request EvaluationRequest) (EvaluationResult, error) {
	corpus := evaluationCorpus(request)
	for _, pattern := range instructionInjectionPatterns {
		if pattern.MatchString(corpus) {
			return EvaluationResult{Decision: EvaluationDecisionDeny, Code: evaluationCodeInstructionInjection}, nil
		}
	}
	return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "instruction_injection_clear"}, nil
}

type ToolSemanticSafetyEvaluator struct{}

func NewToolSemanticSafetyEvaluator() ToolSemanticSafetyEvaluator {
	return ToolSemanticSafetyEvaluator{}
}

func (ToolSemanticSafetyEvaluator) Evaluate(_ context.Context, request EvaluationRequest) (EvaluationResult, error) {
	if request.Stage != EvaluationStageToolInput {
		return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "tool_semantic_not_applicable"}, nil
	}
	sideEffect := strings.ToLower(strings.TrimSpace(request.Metadata[evaluationMetadataSideEffectLevel]))
	approvalMode := strings.ToLower(strings.TrimSpace(request.Metadata[evaluationMetadataApprovalMode]))
	idempotencyMode := normalizeToolIdempotencyMode(request.Metadata[evaluationMetadataIdempotencyMode])
	if sideEffect == ToolSideEffectDestructive && approvalMode != valueAlwaysE613B9F9 {
		return deniedEvaluation(evaluationCodeDestructiveApprovalRequired), nil
	}
	if sideEffect == valueUnknown26BF6906 && approvalMode == valueNeverF5C79F24 {
		return deniedEvaluation(evaluationCodeUnknownApprovalRequired), nil
	}
	if toolRequiresProviderReceipt(sideEffect) && idempotencyMode != ToolIdempotencyProviderReceipt {
		return deniedEvaluation(evaluationCodeProviderReceiptRequired), nil
	}
	return EvaluationResult{Decision: EvaluationDecisionAllow, Code: "tool_semantic_safe"}, nil
}

func evaluationCorpus(request EvaluationRequest) string {
	parts := []string{request.Content, request.PayloadJSON}
	if strings.TrimSpace(request.PayloadJSON) != "" {
		var decoded interface{}
		if json.Unmarshal([]byte(request.PayloadJSON), &decoded) == nil {
			appendEvaluationStrings(&parts, decoded)
		}
	}
	return strings.Join(parts, "\n")
}

func appendEvaluationStrings(parts *[]string, value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, item := range typed {
			appendEvaluationStrings(parts, item)
		}
	case []interface{}:
		for _, item := range typed {
			appendEvaluationStrings(parts, item)
		}
	case string:
		*parts = append(*parts, typed)
	}
}

func credentialClassificationFromJSON(payload string) string {
	if strings.TrimSpace(payload) == "" {
		return ""
	}
	var decoded interface{}
	if json.Unmarshal([]byte(payload), &decoded) != nil {
		return ""
	}
	return findCredentialValue(decoded)
}

func findCredentialValue(value interface{}) string {
	switch typed := value.(type) {
	case map[string]interface{}:
		return findCredentialInMap(typed)
	case []interface{}:
		return findCredentialInList(typed)
	}
	return ""
}

func findCredentialInMap(items map[string]interface{}) string {
	for key, item := range items {
		if isCredentialKey(key) && containsMaterialCredential(item) {
			return normalizedCredentialKey(key)
		}
		if classification := findCredentialValue(item); classification != "" {
			return classification
		}
	}
	return ""
}

func findCredentialInList(items []interface{}) string {
	for _, item := range items {
		if classification := findCredentialValue(item); classification != "" {
			return classification
		}
	}
	return ""
}

func isCredentialKey(key string) bool {
	switch normalizedCredentialKey(key) {
	case "apikey", "accesstoken", "refreshtoken", "authorization", "password", "passwd", "privatekey", "clientsecret", "credential", "credentials":
		return true
	default:
		return false
	}
}

func normalizedCredentialKey(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func containsMaterialCredential(value interface{}) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	text = strings.TrimSpace(text)
	if len(text) < 8 {
		return false
	}
	lower := strings.ToLower(text)
	for _, marker := range []string{"redacted", "placeholder", "example", "changeme", "not-set", "not_set"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return !strings.HasPrefix(text, "${") && !strings.HasPrefix(text, "{{") && !strings.HasPrefix(text, "<") && strings.Trim(text, "*") != ""
}
