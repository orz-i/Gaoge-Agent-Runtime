package agentruntime

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	valueClaude52767871 = "claude"
	valueGemini9B948C46 = "gemini"
	valueGrok69C810FF   =

	// ModelCaps 保存模型的上下文窗口与输出 Token 上限。
	"grok"
)

type ModelCaps struct {
	ContextWindow   int
	MaxOutputTokens int
}

// autocompactBufferTokens 是预留给系统开销与安全缓冲的 Token 数量。
// 参考 claude-code autoCompact.ts 中的 AUTOCOMPACT_BUFFER_TOKENS = 13_000。
const autocompactBufferTokens = 13_000

// GetModelCaps 返回已知模型的上下文窗口与输出 Token 上限。
// 对未知模型返回保守默认值（128k / 8k）。
func GetModelCaps(modelName string) ModelCaps {
	code := strings.ToLower(strings.TrimSpace(modelName))
	for _, rule := range modelCapRules {
		if rule.matches(code) {
			return rule.caps
		}
	}
	return ModelCaps{ContextWindow: 128_000, MaxOutputTokens: 8_192}
}

type modelCapRule struct {
	needles []string
	caps    ModelCaps
}

var modelCapRules = []modelCapRule{
	{needles: []string{"claude-opus-4", "claude-sonnet-4"}, caps: ModelCaps{ContextWindow: 1_000_000, MaxOutputTokens: 32_000}},
	{needles: []string{"claude-3-7", "claude-3.7"}, caps: ModelCaps{ContextWindow: 200_000, MaxOutputTokens: 16_000}},
	{needles: []string{"claude-3-5", "claude-3.5"}, caps: ModelCaps{ContextWindow: 200_000, MaxOutputTokens: 8_192}},
	{needles: []string{"claude-3", valueClaude52767871}, caps: ModelCaps{ContextWindow: 200_000, MaxOutputTokens: 8_192}},
	{needles: []string{"gpt-4.1"}, caps: ModelCaps{ContextWindow: 1_047_576, MaxOutputTokens: 32_768}},
	{needles: []string{"gpt-4.5"}, caps: ModelCaps{ContextWindow: 128_000, MaxOutputTokens: 16_384}},
	{needles: []string{"gpt-4o"}, caps: ModelCaps{ContextWindow: 128_000, MaxOutputTokens: 16_384}},
	{needles: []string{"gpt-4"}, caps: ModelCaps{ContextWindow: 128_000, MaxOutputTokens: 4_096}},
	{needles: []string{"o4", "o3", "o1"}, caps: ModelCaps{ContextWindow: 200_000, MaxOutputTokens: 100_000}},
	{needles: []string{"gpt-3.5"}, caps: ModelCaps{ContextWindow: 16_385, MaxOutputTokens: 4_096}},
	{needles: []string{"gemini-2.5", "gemini-2.0"}, caps: ModelCaps{ContextWindow: 1_000_000, MaxOutputTokens: 8_192}},
	{needles: []string{"gemini-1.5"}, caps: ModelCaps{ContextWindow: 1_000_000, MaxOutputTokens: 8_192}},
	{needles: []string{valueGemini9B948C46}, caps: ModelCaps{ContextWindow: 128_000, MaxOutputTokens: 8_192}},
	{needles: []string{"grok-3"}, caps: ModelCaps{ContextWindow: 131_072, MaxOutputTokens: 16_384}},
	{needles: []string{valueGrok69C810FF}, caps: ModelCaps{ContextWindow: 131_072, MaxOutputTokens: 8_192}},
}

func (r modelCapRule) matches(code string) bool {
	for _, needle := range r.needles {
		if strings.Contains(code, needle) {
			return true
		}
	}
	return false
}

// GetModelCapsFromCapabilities 使用平台模型能力配置覆盖模型名推断值。
//
// 支持的能力字段：
// - contextWindow / context_window / contextWindowTokens / context_window_tokens
// - maxOutputTokens / max_output_tokens
func GetModelCapsFromCapabilities(modelName string, capabilitiesJSON string) ModelCaps {
	caps := GetModelCaps(modelName)
	raw := strings.TrimSpace(capabilitiesJSON)
	if raw == "" {
		return caps
	}

	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return caps
	}
	if value, ok := firstPositiveInt(payload, "contextWindow", "context_window", "contextWindowTokens", "context_window_tokens"); ok {
		caps.ContextWindow = value
	}
	if value, ok := firstPositiveInt(payload, "maxOutputTokens", "max_output_tokens"); ok {
		caps.MaxOutputTokens = value
	}
	return caps
}

func firstPositiveInt(payload map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if value, ok := positiveInt(payload[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func positiveInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v > 0 {
			return int(v), true
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n > 0 {
			return int(n), true
		}
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}

// EffectiveContextBudget 返回上下文组装可用的最大 Token 数。
// = context_window - min(max_output, 20_000) - autocompact_buffer
func EffectiveContextBudget(modelName string) int {
	return effectiveContextBudget(GetModelCaps(modelName))
}

// EffectiveContextBudgetFromCapabilities 返回使用平台模型能力配置后的上下文预算。
func EffectiveContextBudgetFromCapabilities(modelName string, capabilitiesJSON string) int {
	return effectiveContextBudget(GetModelCapsFromCapabilities(modelName, capabilitiesJSON))
}

func effectiveContextBudget(caps ModelCaps) int {
	reserve := caps.MaxOutputTokens
	if reserve > 20_000 {
		reserve = 20_000
	}
	budget := caps.ContextWindow - reserve - autocompactBufferTokens
	if budget < 4_000 {
		budget = 4_000
	}
	return budget
}

// CompactionThreshold 返回触发上下文压缩的 Token 阈值（等于有效上下文预算）。
func CompactionThreshold(modelName string) int64 {
	return int64(EffectiveContextBudget(modelName))
}

// CompactionThresholdFromCapabilities 返回使用平台模型能力配置后的压缩阈值。
func CompactionThresholdFromCapabilities(modelName string, capabilitiesJSON string) int64 {
	return int64(EffectiveContextBudgetFromCapabilities(modelName, capabilitiesJSON))
}

func measureToolDefinitionsBytes(tools []ToolDefinition) (int, error) {
	// Internal stable measure of frozen ToolDefinition list (not provider wire size).
	raw, err := json.Marshal(tools)
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

const (
	ErrorCodeUpstreamPayloadTooLarge  = "upstream_payload_too_large"
	errorCodeWorkspaceArtifactMissing = "workspace_artifact_missing"
)
