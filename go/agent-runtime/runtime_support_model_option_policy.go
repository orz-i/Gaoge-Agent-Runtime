// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"encoding/json"
	"strings"
)

const (
	valueAnthropicMessagesBE0A4CA2   = "anthropic_messages"
	valueOpenrouterResponses6266759F = "openrouter_responses"
)

const (
	valueAnthropic3FCC6FC6             = "anthropic"
	valueClaude16139728                = "claude"
	valueDefaultCDD5D372               = "default"
	valueGeminiCD5FCBB8                = "gemini"
	valueGeminiGenerateContent5B90A448 = "gemini_generate_content"
	valueGrokA78091EA                  = "grok"
	valueInput52A31A2C                 = "input"
	valueModel22D48A8A                 = "model"
	valueOpenai1473EEB2                = "openai"
	valueOpenaiResponses0489CB62       = "openai_responses"
	valuePrompt462422AF                = "prompt"
	valueStreamAB9103AA                = "stream"
	valueSystem81966A21                = "system"
	valueXaiResponses11B30F67          = "xai_responses"
	valueVolcengineResponses01990FC1   = "volcengine_responses"
	valueVolcengineVideo0FBB1FA0       = "volcengine_video_generation"
)

const (
	modelOptionPolicyAllowlist = "allowlist"
	modelOptionPolicyDenylist  = "denylist"
	modelOptionPolicyDisabled  = "disabled"
)

var hardDeniedModelOptionPaths = [][]string{
	{valueModel22D48A8A},
	{"messages"},
	{valueInput52A31A2C},
	{"instructions"},
	{valuePrompt462422AF},
	{valueSystem81966A21},
	{"systemInstruction"},
	{"headers"},
	{"api_key"},
	{"apiKey"},
	{"base_url"},
	{"baseURL"},
	{valueStreamAB9103AA},
	{"previous_response_id"},
}

type modelOptionPolicyConfig struct {
	Mode                  string
	AllowedPathsJSON      string
	DeniedPathsJSON       string
	ModelCapabilitiesJSON string
}

func filterModelOptions(options map[string]interface{}, protocol string, cfg modelOptionPolicyConfig) map[string]interface{} {
	mode := strings.TrimSpace(cfg.Mode)
	if mode == "" {
		mode = modelOptionPolicyAllowlist
	}
	if mode == modelOptionPolicyDisabled {
		return nil
	}

	protocolKey := modelOptionPolicyProtocolKey(protocol)
	defaultOptions := modelCapabilityDefaultOptions(cfg.ModelCapabilitiesJSON)
	policyOptions := mergeModelOptionDefaults(
		defaultOptions,
		options,
		modelCapabilityLockedOptionPaths(cfg.ModelCapabilitiesJSON),
	)
	if len(policyOptions) == 0 {
		return nil
	}
	nativeTools := providerToolOptionPayloads(policyOptions["tools"])
	delete(policyOptions, "tools")
	denied := append([][]string{}, hardDeniedModelOptionPaths...)

	var filtered map[string]interface{}
	switch mode {
	case modelOptionPolicyDenylist:
		denied = append(denied, modelOptionPathsForProtocol(cfg.DeniedPathsJSON, protocolKey)...)
		filtered = policyOptions
	default:
		filtered = make(map[string]interface{})
		for _, path := range modelOptionPathsForProtocol(cfg.AllowedPathsJSON, protocolKey) {
			copyModelOptionPath(filtered, policyOptions, path)
		}
	}

	for _, path := range denied {
		deleteModelOptionPath(filtered, path)
	}
	sanitizeModelOptionValues(filtered, protocolKey)
	filtered = withNativeModelTools(filtered, nativeTools)
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func withNativeModelTools(filtered map[string]interface{}, nativeTools []map[string]interface{}) map[string]interface{} {
	if len(nativeTools) == 0 {
		return filtered
	}
	if filtered == nil {
		filtered = make(map[string]interface{})
	}
	filtered["tools"] = nativeTools
	return filtered
}

// modelCapabilityDefaultOptions 提取管理员在模型能力 JSON 中声明的默认请求参数。
func modelCapabilityDefaultOptions(raw string) map[string]interface{} {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	var config struct {
		DefaultOptions map[string]interface{} `json:"defaultOptions"`
	}
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return nil
	}
	return cloneModelOptionMap(config.DefaultOptions)
}

func modelCapabilityLockedOptionPaths(raw string) [][]string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil
	}
	var config struct {
		LockedOptionPaths []string `json:"lockedOptionPaths"`
	}
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return nil
	}
	paths := make([][]string, 0, len(config.LockedOptionPaths))
	for _, value := range config.LockedOptionPaths {
		if path := splitModelOptionPath(value); len(path) > 0 {
			paths = append(paths, path)
		}
	}
	return paths
}

// mergeModelOptionDefaults 以能力默认值为基础合并本次显式参数，并对锁定路径恢复默认值。
func mergeModelOptionDefaults(defaults map[string]interface{}, options map[string]interface{}, lockedPaths [][]string) map[string]interface{} {
	merged := cloneModelOptionMap(defaults)
	if merged == nil {
		merged = make(map[string]interface{}, len(options))
	}
	mergeModelOptionMap(merged, options)
	for _, path := range lockedPaths {
		if value, ok := readModelOptionPath(defaults, path); ok {
			writeModelOptionPath(merged, path, cloneModelOptionValue(value))
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeModelOptionMap(dst map[string]interface{}, src map[string]interface{}) {
	for key, value := range src {
		srcMap, srcIsMap := value.(map[string]interface{})
		dstMap, dstIsMap := dst[key].(map[string]interface{})
		if srcIsMap && dstIsMap && dstMap != nil {
			mergeModelOptionMap(dstMap, srcMap)
			continue
		}
		dst[key] = cloneModelOptionValue(value)
	}
}

// providerToolOptionPayloads 从自由 JSON 中提取 tools 数组对象。
func providerToolOptionPayloads(raw interface{}) []map[string]interface{} {
	var source []map[string]interface{}
	switch typed := raw.(type) {
	case []map[string]interface{}:
		source = typed
	case []interface{}:
		source = make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if payload, ok := item.(map[string]interface{}); ok {
				source = append(source, payload)
			}
		}
	}
	items := make([]map[string]interface{}, 0, len(source))
	for _, item := range source {
		items = append(items, cloneModelOptionMap(item))
	}
	return items
}

func sanitizeModelOptionValues(options map[string]interface{}, protocolKey string) {
	if len(options) == 0 {
		return
	}
	switch protocolKey {
	case "openai_chat_completions", valueOpenaiResponses0489CB62, valueOpenrouterResponses6266759F:
		sanitizeOpenAIServiceTier(options)
	}
}

func sanitizeOpenAIServiceTier(options map[string]interface{}) {
	serviceTier, ok := options["service_tier"]
	if !ok {
		return
	}
	value, ok := serviceTier.(string)
	if !ok {
		delete(options, "service_tier")
		return
	}
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case valueDefaultCDD5D372, "flex", "priority":
		options["service_tier"] = normalized
	default:
		delete(options, "service_tier")
	}
}

var modelOptionProtocolKeyByAdapter = map[string]string{
	valueOpenai1473EEB2:          valueOpenaiResponses0489CB62,
	"openrouter":                 valueOpenrouterResponses6266759F,
	valueAnthropic3FCC6FC6:       valueAnthropicMessagesBE0A4CA2,
	valueClaude16139728:          valueAnthropicMessagesBE0A4CA2,
	"xai":                        valueXaiResponses11B30F67,
	valueGrokA78091EA:            valueXaiResponses11B30F67,
	"google":                     valueGeminiGenerateContent5B90A448,
	valueGeminiCD5FCBB8:          valueGeminiGenerateContent5B90A448,
	AdapterGoogleGenerateContent: valueGeminiGenerateContent5B90A448,
	AdapterOpenAIChatCompletions: "openai_chat_completions",
	AdapterOpenRouterChat:        "openrouter_chat_completions",
	AdapterOpenRouterResponses:   valueOpenrouterResponses6266759F,
	AdapterAnthropicMessages:     valueAnthropicMessagesBE0A4CA2,
	AdapterXAIResponses:          valueXaiResponses11B30F67,
	"volcengine":                 valueVolcengineResponses01990FC1,
	"doubao":                     valueVolcengineResponses01990FC1,
	AdapterVolcengineResponses:   valueVolcengineResponses01990FC1,
	valueVolcengineVideo0FBB1FA0: valueVolcengineVideo0FBB1FA0,
}

func modelOptionPolicyProtocolKey(protocol string) string {
	if key := modelOptionProtocolKeyByAdapter[NormalizeAdapter(protocol)]; key != "" {
		return key
	}
	return valueOpenaiResponses0489CB62
}

func modelOptionPathsForProtocol(raw string, protocol string) [][]string {
	var config map[string][]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &config); err != nil {
		return nil
	}
	paths := make([][]string, 0, len(config[valueDefaultCDD5D372])+len(config[protocol]))
	for _, value := range append(config[valueDefaultCDD5D372], config[protocol]...) {
		if path := splitModelOptionPath(value); len(path) > 0 {
			paths = append(paths, path)
		}
	}
	return paths
}

func splitModelOptionPath(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, " \t\r\n") {
		return nil
	}
	parts := strings.Split(value, ".")
	for _, part := range parts {
		if part == "" {
			return nil
		}
	}
	return parts
}

func copyModelOptionPath(dst map[string]interface{}, src map[string]interface{}, path []string) {
	value, ok := readModelOptionPath(src, path)
	if !ok {
		return
	}
	writeModelOptionPath(dst, path, cloneModelOptionValue(value))
}

func readModelOptionPath(src map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}
	current := src
	for index, segment := range path {
		value, ok := current[segment]
		if !ok {
			return nil, false
		}
		if index == len(path)-1 {
			return value, true
		}
		next, ok := value.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current = next
	}
	return nil, false
}

func writeModelOptionPath(dst map[string]interface{}, path []string, value interface{}) {
	current := dst
	for index, segment := range path {
		if index == len(path)-1 {
			current[segment] = value
			return
		}
		next, ok := current[segment].(map[string]interface{})
		if !ok {
			next = make(map[string]interface{})
			current[segment] = next
		}
		current = next
	}
}

func deleteModelOptionPath(dst map[string]interface{}, path []string) {
	if len(path) == 0 || len(dst) == 0 {
		return
	}
	current := dst
	for index, segment := range path {
		if index == len(path)-1 {
			delete(current, segment)
			return
		}
		next, ok := current[segment].(map[string]interface{})
		if !ok {
			return
		}
		current = next
	}
}

func cloneModelOptionMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = cloneModelOptionValue(value)
	}
	return dst
}

func cloneModelOptionValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneModelOptionMap(typed)
	case []interface{}:
		items := make([]interface{}, len(typed))
		for index, item := range typed {
			items[index] = cloneModelOptionValue(item)
		}
		return items
	default:
		return typed
	}
}
