package modelcap

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

type Source string

const (
	SourceConfigured Source = "configured"
	SourceInferred   Source = "inferred"
	SourceDefault    Source = "default"
)

type ConfigurationStatus string

const (
	ConfigurationAbsent  ConfigurationStatus = "absent"
	ConfigurationValid   ConfigurationStatus = "valid"
	ConfigurationInvalid ConfigurationStatus = "invalid"
)

type StructuredOutputMode string

const (
	StructuredOutputStrictJSONSchema StructuredOutputMode = "strict_json_schema"
	StructuredOutputJSONObject       StructuredOutputMode = "json_object"
	StructuredOutputJSONText         StructuredOutputMode = "json_text"
	StructuredOutputUnsupported      StructuredOutputMode = "unsupported"
)

type StructuredOutputResolution struct {
	Mode                StructuredOutputMode `json:"mode"`
	ConfigurationStatus ConfigurationStatus  `json:"configurationStatus"`
}

func ResolveStructuredOutput(raw string) StructuredOutputResolution {
	payload, status := parseCapabilities(raw)
	if status == ConfigurationAbsent {
		return StructuredOutputResolution{Mode: StructuredOutputJSONText, ConfigurationStatus: status}
	}
	if status != ConfigurationValid {
		return StructuredOutputResolution{Mode: StructuredOutputUnsupported, ConfigurationStatus: status}
	}
	value, found := structuredOutputModeValue(payload)
	if !found {
		return StructuredOutputResolution{Mode: StructuredOutputJSONText, ConfigurationStatus: ConfigurationAbsent}
	}
	mode := StructuredOutputMode(strings.ToLower(strings.TrimSpace(value)))
	switch mode {
	case StructuredOutputStrictJSONSchema, StructuredOutputJSONObject, StructuredOutputJSONText, StructuredOutputUnsupported:
		return StructuredOutputResolution{Mode: mode, ConfigurationStatus: ConfigurationValid}
	default:
		return StructuredOutputResolution{Mode: StructuredOutputUnsupported, ConfigurationStatus: ConfigurationInvalid}
	}
}

func structuredOutputModeValue(payload map[string]interface{}) (string, bool) {
	value, exists := payload["structuredOutput"]
	if exists {
		if mode, ok := value.(string); ok {
			return mode, true
		}
		object, ok := value.(map[string]interface{})
		if !ok {
			return "", true
		}
		mode, _ := object["mode"].(string)
		return mode, true
	}
	if value, ok := payload["structuredOutputMode"].(string); ok {
		return value, true
	}
	return "", false
}

type Limits struct {
	ContextWindow                 int `json:"contextWindow"`
	MaxOutputTokens               int `json:"maxOutputTokens"`
	EffectiveContextWindowPercent int `json:"effectiveContextWindowPercent,omitempty"`
}

type Resolution struct {
	Limits
	ContextWindowSource   Source              `json:"contextWindowSource"`
	MaxOutputTokensSource Source              `json:"maxOutputTokensSource"`
	MatchedRule           string              `json:"matchedRule,omitempty"`
	ConfigurationStatus   ConfigurationStatus `json:"configurationStatus"`
}

type Rule struct {
	ID      string
	Needles []string
	Limits  Limits
}

type Registry struct {
	defaults Limits
	rules    []Rule
}

var errInvalidRegistry = errors.New("invalid model capability registry")

func NewRegistry(defaults Limits, rules []Rule) (Registry, error) {
	if !validLimits(defaults) {
		return Registry{}, errInvalidRegistry
	}
	normalized := make([]Rule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		item, err := normalizeRule(rule, seen)
		if err != nil {
			return Registry{}, err
		}
		normalized = append(normalized, item)
	}
	return Registry{defaults: defaults, rules: normalized}, nil
}

func normalizeRule(rule Rule, seen map[string]struct{}) (Rule, error) {
	rule.ID = strings.TrimSpace(rule.ID)
	if rule.ID == "" || !validLimits(rule.Limits) {
		return Rule{}, errInvalidRegistry
	}
	if _, duplicate := seen[rule.ID]; duplicate {
		return Rule{}, errInvalidRegistry
	}
	rule.Needles = normalizedNeedles(rule.Needles)
	if len(rule.Needles) == 0 {
		return Rule{}, errInvalidRegistry
	}
	seen[rule.ID] = struct{}{}
	return rule, nil
}

func normalizedNeedles(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
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

func validLimits(limits Limits) bool {
	return limits.ContextWindow > 0 && limits.MaxOutputTokens > 0 &&
		limits.EffectiveContextWindowPercent >= 0 && limits.EffectiveContextWindowPercent <= 100
}

func (registry Registry) Resolve(modelName, capabilitiesJSON string) Resolution {
	resolution := registry.resolveName(modelName)
	payload, status := parseCapabilities(capabilitiesJSON)
	resolution.ConfigurationStatus = status
	if status != ConfigurationValid {
		return resolution
	}
	if value, ok := firstPositiveInt(payload, "contextWindow", "context_window", "contextWindowTokens", "context_window_tokens"); ok {
		resolution.ContextWindow = value
		resolution.ContextWindowSource = SourceConfigured
	}
	if value, ok := firstPositiveInt(payload, "maxContextWindow", "max_context_window"); ok {
		if resolution.ContextWindowSource != SourceConfigured || value < resolution.ContextWindow {
			resolution.ContextWindow = value
		}
		resolution.ContextWindowSource = SourceConfigured
	}
	if value, ok := firstPositiveInt(payload, "effectiveContextWindowPercent", "effective_context_window_percent"); ok && value <= 100 {
		resolution.EffectiveContextWindowPercent = value
	}
	if value, ok := firstPositiveInt(payload, "maxOutputTokens", "max_output_tokens"); ok {
		resolution.MaxOutputTokens = value
		resolution.MaxOutputTokensSource = SourceConfigured
	}
	return resolution
}

func (registry Registry) resolveName(modelName string) Resolution {
	code := strings.ToLower(strings.TrimSpace(modelName))
	for _, rule := range registry.rules {
		if ruleMatches(rule, code) {
			return Resolution{
				Limits:                rule.Limits,
				ContextWindowSource:   SourceInferred,
				MaxOutputTokensSource: SourceInferred,
				MatchedRule:           rule.ID,
				ConfigurationStatus:   ConfigurationAbsent,
			}
		}
	}
	return Resolution{
		Limits:                registry.defaults,
		ContextWindowSource:   SourceDefault,
		MaxOutputTokensSource: SourceDefault,
		ConfigurationStatus:   ConfigurationAbsent,
	}
}

func ruleMatches(rule Rule, code string) bool {
	for _, needle := range rule.Needles {
		if strings.Contains(code, needle) {
			return true
		}
	}
	return false
}

func parseCapabilities(raw string) (map[string]interface{}, ConfigurationStatus) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ConfigurationAbsent
	}
	payload := make(map[string]interface{})
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, ConfigurationInvalid
	}
	return payload, ConfigurationValid
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
	switch typed := value.(type) {
	case float64:
		return positiveFloat(typed)
	case json.Number:
		return positiveNumber(typed)
	case string:
		return positiveString(typed)
	default:
		return 0, false
	}
}

func positiveFloat(value float64) (int, bool) {
	if value <= 0 {
		return 0, false
	}
	return int(value), true
}

func positiveNumber(value json.Number) (int, bool) {
	number, err := value.Int64()
	if err != nil || number <= 0 {
		return 0, false
	}
	return int(number), true
}

func positiveString(value string) (int, bool) {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

const (
	autocompactBufferTokens = 13_000
	maxOutputReserveTokens  = 20_000
	minimumContextBudget    = 4_000
)

func EffectiveContextBudget(limits Limits) int {
	reserve := limits.MaxOutputTokens
	if reserve > maxOutputReserveTokens {
		reserve = maxOutputReserveTokens
	}
	contextWindow := limits.ContextWindow
	if limits.EffectiveContextWindowPercent > 0 {
		contextWindow = int(int64(contextWindow) * int64(limits.EffectiveContextWindowPercent) / 100)
	}
	budget := contextWindow - reserve - autocompactBufferTokens
	if budget < minimumContextBudget {
		return minimumContextBudget
	}
	return budget
}

func (resolution Resolution) EffectiveContextBudget() int {
	return EffectiveContextBudget(resolution.Limits)
}

var Default = mustDefaultRegistry()

func mustDefaultRegistry() Registry {
	registry, err := NewRegistry(Limits{ContextWindow: 128_000, MaxOutputTokens: 8_192}, []Rule{
		{ID: "anthropic.claude-4", Needles: []string{"claude-opus-4", "claude-sonnet-4"}, Limits: Limits{ContextWindow: 1_000_000, MaxOutputTokens: 32_000}},
		{ID: "anthropic.claude-3.7", Needles: []string{"claude-3-7", "claude-3.7"}, Limits: Limits{ContextWindow: 200_000, MaxOutputTokens: 16_000}},
		{ID: "anthropic.claude-3.5", Needles: []string{"claude-3-5", "claude-3.5"}, Limits: Limits{ContextWindow: 200_000, MaxOutputTokens: 8_192}},
		{ID: "anthropic.claude", Needles: []string{"claude-3", "claude"}, Limits: Limits{ContextWindow: 200_000, MaxOutputTokens: 8_192}},
		{ID: "openai.gpt-4.1", Needles: []string{"gpt-4.1"}, Limits: Limits{ContextWindow: 1_047_576, MaxOutputTokens: 32_768}},
		{ID: "openai.gpt-4.5", Needles: []string{"gpt-4.5"}, Limits: Limits{ContextWindow: 128_000, MaxOutputTokens: 16_384}},
		{ID: "openai.gpt-4o", Needles: []string{"gpt-4o"}, Limits: Limits{ContextWindow: 128_000, MaxOutputTokens: 16_384}},
		{ID: "openai.gpt-4", Needles: []string{"gpt-4"}, Limits: Limits{ContextWindow: 128_000, MaxOutputTokens: 4_096}},
		{ID: "openai.reasoning", Needles: []string{"o4", "o3", "o1"}, Limits: Limits{ContextWindow: 200_000, MaxOutputTokens: 100_000}},
		{ID: "openai.gpt-3.5", Needles: []string{"gpt-3.5"}, Limits: Limits{ContextWindow: 16_385, MaxOutputTokens: 4_096}},
		{ID: "google.gemini-2", Needles: []string{"gemini-2.5", "gemini-2.0"}, Limits: Limits{ContextWindow: 1_000_000, MaxOutputTokens: 8_192}},
		{ID: "google.gemini-1.5", Needles: []string{"gemini-1.5"}, Limits: Limits{ContextWindow: 1_000_000, MaxOutputTokens: 8_192}},
		{ID: "google.gemini", Needles: []string{"gemini"}, Limits: Limits{ContextWindow: 128_000, MaxOutputTokens: 8_192}},
		{ID: "xai.grok-3", Needles: []string{"grok-3"}, Limits: Limits{ContextWindow: 131_072, MaxOutputTokens: 16_384}},
		{ID: "xai.grok", Needles: []string{"grok"}, Limits: Limits{ContextWindow: 131_072, MaxOutputTokens: 8_192}},
	})
	if err != nil {
		panic(err)
	}
	return registry
}
