package agentruntime

import (
	"errors"
	"strings"
)

// 已支持的协议常量。每个协议固定对应一个 HTTP 端点，任务能力由模型类别和路由规则约束。
const (
	AdapterOpenAIResponses       = "openai_responses"            // POST /v1/responses
	AdapterOpenRouterChat        = "openrouter_chat_completions" // POST /v1/chat/completions（OpenRouter）
	AdapterOpenRouterResponses   = "openrouter_responses"        // POST /v1/responses（OpenRouter Responses Beta）
	AdapterOpenAIChatCompletions = "openai_chat_completions"     // POST /v1/chat/completions
	AdapterAnthropicMessages     = "anthropic_messages"          // POST /v1/messages
	AdapterGoogleGenerateContent = "google_generate_content"     // POST /v1beta/models/{model}:generateContent
	AdapterXAIResponses          = "xai_responses"               // POST /v1/responses（OpenAI 兼容）
)

var (
	// ErrUnsupportedAdapter 表示协议没有可用适配器实现。
	ErrUnsupportedAdapter = errors.New("unsupported llm adapter")
	// ErrUnsupportedStream 表示协议存在但不支持真实流式输出。
	ErrUnsupportedStream = errors.New("unsupported llm stream")
)

// NormalizeAdapter 规范化协议名；空值按历史默认使用 openai_responses，未知值保留给校验层处理。
func NormalizeAdapter(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return AdapterOpenAIResponses
	}
	return value
}

// IsKnownAdapter 返回协议是否为已知值（含未实现的）。
func IsKnownAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIResponses,
		AdapterOpenRouterChat,
		AdapterOpenRouterResponses,
		AdapterOpenAIChatCompletions,
		AdapterAnthropicMessages,
		AdapterGoogleGenerateContent,
		AdapterXAIResponses:
		return true
	default:
		return false
	}
}

// IsImplementedAdapter 返回协议是否已有可用的传输层实现。
func IsImplementedAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIResponses, AdapterOpenRouterChat, AdapterOpenRouterResponses, AdapterOpenAIChatCompletions, AdapterXAIResponses,
		AdapterAnthropicMessages, AdapterGoogleGenerateContent:
		return true
	default:
		return false
	}
}

// SupportsStreamingAdapter 返回协议是否有真实的上游流式传输。
func SupportsStreamingAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIResponses,
		AdapterOpenRouterChat,
		AdapterOpenRouterResponses,
		AdapterOpenAIChatCompletions,
		AdapterAnthropicMessages,
		AdapterGoogleGenerateContent,
		AdapterXAIResponses:
		return true
	default:
		return false
	}
}

// DefaultEndpointForAdapter 返回协议对应的固定端点标识。
func DefaultEndpointForAdapter(adapter string) string {
	switch NormalizeAdapter(adapter) {
	case AdapterOpenAIChatCompletions, AdapterOpenRouterChat:
		return EndpointChatCompletions
	default:
		// openai_responses、openrouter_responses、xai_responses 及所有未知值均使用 Responses 端点。
		return EndpointResponses
	}
}

// SupportsPreviousResponseID 返回协议是否明确支持 previous_response_id 有状态续接。
// 兼容/逆向实现即使复用 Responses 形状，也不一定支持该字段；默认保持关闭。
func SupportsPreviousResponseID(adapter string) bool {
	return NormalizeAdapter(adapter) == AdapterOpenAIResponses
}
