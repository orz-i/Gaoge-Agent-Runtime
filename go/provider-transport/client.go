package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	valueFunctionB03625B4   = "function"
	valuePropertiesF7D82DD7 = "properties"
	valueType5AC5AA71       = "type"
	responseSummaryKey      = "summary"
)

const (
	valueRedacted7131EAAF         = "[redacted]"
	valueAssistantCAAB15A3        = "assistant"
	valueBudgetTokensA36C2638     = "budget_tokens"
	valueCacheTimeout9FA95B17     = "cache_timeout"
	valueCompleted96C680F2        = "completed"
	valueContent03EE2C26          = "content"
	valueEffortF632647D           = "effort"
	valueEnableThinking2A8ED023   = "enable_thinking"
	valueError20660AE7            = "error"
	valueFrequencyPenalty565FB915 =

	// EndpointResponses 表示 OpenAI Responses API 端点。
	"frequency_penalty"
	valueInProgress6CA4051A = "in_progress"
	valueJson6F55B6AF       = "json"

	// EndpointChatCompletions 表示 OpenAI Chat Completions API 端点。
	valueJsonObjectD320B892          = "json_object"
	valueMaxCompletionTokensF93E57D2 = "max_completion_tokens"
	valueMaxOutputTokens5044A27A     =

	// EndpointImageGenerations 表示 OpenAI Images API 生成端点。
	"max_output_tokens"
	valueObject53670695          = "object"
	valuePresencePenalty9F98DF6C = "presence_penalty"
	valuePromptCacheF470D33F     =

	// EndpointImageEdits 表示 OpenAI Images API 编辑端点。
	"prompt_cache"
	valuePromptCacheRetention08E74823 = "prompt_cache_retention"
	valueResponseFormat3EE8D395       =

	// 超时默认值。
	"response_format"
	valueSeedC8D041FC = "seed"
	valueStop1E96CF73 = "stop"

	// TCP 建连超时 10s
	valueTemperature75242E23 = "temperature"
	valueText1D495308        = "text"

	// 非流式/首字节超时 120s（含 LLM 推理）
	valueThinking0F938ED2        = "thinking"
	valueThinkingBudget78860FBE  = "thinking_budget"
	valueThinkingDisplayCE5281B8 =

	// 流式 chunk 间隔超时 60s
	"thinking_display"
	valueThinkingLevel3C762D05 = "thinking_level"
	valueTopK1E4D787F          =

	// Client 负责跨厂商共享的 HTTP client、adapter 路由和上游调试能力。
	"top_k"
	valueTopPC744CA21      = "top_p"
	valueTrue0F6181DB      = "true"
	valueUserD301E989      = "user"
	valueVerbosity33918016 = "verbosity"
	valueWebSearch8B501B71 = "web_search"
)

const (
	EndpointAudioGenerations    = "audio_generations"
	EndpointWorldGenerations    = "world_generations"
	Endpoint3DGenerations       = "3d_generations"
	EndpointAudioTranscriptions = "audio_transcriptions"
	EndpointEmbeddings          = "embeddings"
	EndpointRerank              = "rerank"
	EndpointResponses           = "responses"

	EndpointChatCompletions = "chat_completions"

	EndpointInteractions = "interactions"

	EndpointImageGenerations = "image_generations"

	EndpointImageEdits = "image_edits"

	EndpointMessages          = "messages"
	EndpointVideoGenerations  = "video_generations"
	EndpointSpeechGenerations = "speech_generations"
	EndpointMusicGenerations  = "music_generations"

	EndpointContentGenerationTasks = "content_generation_tasks"
)

const (
	defaultConnectTimeoutMS    = 10000
	defaultReadTimeoutMS       = 120000
	defaultStreamIdleTimeoutMS = 60000
	maxUpstreamBodyBytes       = 64 * 1024 * 1024
)

// HTTPClientFactory is supplied by the host and retains its SSRF, proxy and
// observability policies. It must return an immutable client with a transport.
type HTTPClientFactory func(connectTimeout time.Duration) *http.Client

type Client struct {
	httpClients       sync.Map
	adapters          map[string]transportAdapter
	httpClientFactory HTTPClientFactory
}

// RouteConfig 定义渠道路由调用参数。
type RouteConfig struct {
	// Set by the trusted Gateway only after a durable quoted admission claim.
	SingleProviderAttempt bool
	Protocol              string
	BaseURL               string
	APIKey                string
	HeadersJSON           string
	ConnectTimeoutMS      int // TCP 建连超时（默认 10s）
	ReadTimeoutMS         int // 非流式整体超时 / 流式首字节超时（默认 120s）
	StreamIdleTimeoutMS   int // 流式两个 chunk 之间最大间隔（默认 60s）
	Endpoint              string
	UpstreamModel         string
	ModelCapabilitiesJSON string
	AttributionReferer    string
	AttributionTitle      string
}

func normalizeConnectTimeoutMS(ms int) int {
	if ms <= 0 {
		return defaultConnectTimeoutMS
	}
	return ms
}

func resolveReadTimeout(ms int) time.Duration {
	if ms <= 0 {
		return time.Duration(defaultReadTimeoutMS) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

func resolveStreamIdleTimeout(ms int) time.Duration {
	if ms <= 0 {
		return time.Duration(defaultStreamIdleTimeoutMS) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

// ContentPart 类型常量。
const (
	ContentPartText  = valueText1D495308 // 纯文本
	ContentPartImage = "image"           // 图片（原始字节，序列化时 base64 编码）
	ContentPartAudio = "audio"
	ContentPartVideo = "video"
	ContentPartFile  = "file" // 文件提取文本，或受模型能力约束的原文件字节
)

// ContentPart 表示多模态消息中的一个内容片段。
type ContentPart struct {
	Kind         string        // text | image | file
	Text         string        // Kind=text 或 Kind=file 时的文本内容
	MimeType     string        // Kind=image 时的 MIME 类型（如 "image/jpeg"）
	Data         []byte        // Kind=image 时的原始字节（发送时 base64 编码）
	FileName     string        // Kind=file 时的文件显示名
	CacheControl *CacheControl // 支持块级缓存的 adapter 可读取该提示
}

// CacheControl 表示可被支持方言渲染为 prompt cache breakpoint 的提示。
type CacheControl struct {
	Type string
	TTL  string
}

// Message 定义发送给上游的消息结构。
// Parts 非空时覆盖 Content 用于多模态内容。
type Message struct {
	Role             string
	Content          string        // 纯文本消息内容（Parts 为空时使用）
	Parts            []ContentPart // 多模态内容片段（设置后优先于 Content）
	ReasoningContent string        // OpenAI-compatible thinking mode 的 reasoning_content 回灌字段
	ReplayStateJSON  string        // provider-owned opaque stateless replay envelope
	ToolCalls        []ToolCall    // assistant 请求执行的工具调用
	ToolResults      []ToolResult  // 工具执行结果，用于回灌下一轮模型调用
	CacheControl     *CacheControl // 支持块级缓存的 adapter 可读取该提示
}

// ToolChoiceMode is the provider-neutral function/tool calling mode.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNone     ToolChoiceMode = "none"
)

// ToolChoice constrains whether the model must emit native tool calls.
type ToolChoice struct {
	Mode             ToolChoiceMode
	AllowedToolNames []string
}

// GenerateInput 定义上游推理请求入参。
type ActorRef struct {
	TenantID string
	ActorID  string
}

type ThreadRef struct {
	Kind string
	ID   string
}

type GenerateInput struct {
	RequestID    string
	InvocationID string
	Actor        ActorRef
	Thread       ThreadRef
	Messages     []Message
	// Instructions 承载可映射到上游原生指令字段的系统/开发者指令。
	// 不支持原生指令字段的 adapter 应继续通过 messages 承载系统提示。
	Instructions string
	Tools        []ToolDefinition
	// DisableTools 表示本轮调用必须只生成文本，adapter 不再声明 MCP 或厂商原生工具。
	DisableTools bool
	// ToolChoice selects auto/required/none native tool calling when tools are enabled.
	ToolChoice ToolChoice
	// Options 承载本次调用的自由 JSON 参数。系统字段（model/messages/input/stream）
	// 由 adapter 固定构造；Options 只表达采样、推理、工具、缓存和厂商原生扩展。
	Options map[string]interface{}
	// PreviousResponseID 承载 provider-side stateful continuation 的上一轮标识。
	// OpenAI/Volcengine Responses 将其映射为 previous_response_id；Google Interactions
	// 将其映射为 previous_interaction_id。
	// 非空时：仅在 input 中发送本轮新消息，服务端从存储状态续接历史。
	// 空串时：退回全量发送模式，适用于所有 adapter。
	PreviousResponseID  string
	ResumeMediaTaskID   string
	CreateMediaTaskOnly bool
	// ImageEditMask 仅供图片编辑 adapter 使用，表示透明区域掩码。
	ImageEditMask *ContentPart
}

// ToolDefinition 是模型可调用工具的统一声明。
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

func modelParamString(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	value, ok := params[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func modelParamInt(params map[string]interface{}, key string) int {
	value, ok := modelParamIntValue(params, key)
	if !ok {
		return 0
	}
	return value
}

func modelParamIntValue(params map[string]interface{}, key string) (int, bool) {
	if params == nil {
		return 0, false
	}
	value, ok := params[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		return exactModelParamNumberInt(typed)
	default:
		return 0, false
	}
}

func modelParamFloat(params map[string]interface{}, key string) (float64, bool) {
	if params == nil {
		return 0, false
	}
	value, ok := params[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func modelParamBool(params map[string]interface{}, key string) bool {
	value, ok := modelParamBoolValue(params, key)
	return ok && value
}

func modelParamBoolValue(params map[string]interface{}, key string) (bool, bool) {
	if params == nil {
		return false, false
	}
	value, ok := params[key]
	if !ok {
		return false, false
	}
	typed, ok := value.(bool)
	return typed, ok
}

func modelParamStringList(params map[string]interface{}, key string) []string {
	if params == nil {
		return nil
	}
	value, ok := params[key]
	if !ok {
		return nil
	}
	return stringListFromModelParam(value)
}

func stringListFromModelParam(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil
		}
		return []string{text}
	case []string:
		return normalizeStringSlice(typed)
	case []interface{}:
		return normalizeInterfaceStringSlice(typed)
	default:
		return nil
	}
}

func normalizeStringSlice(raw []string) []string {
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		if text := strings.TrimSpace(item); text != "" {
			items = append(items, text)
		}
	}
	return items
}

func normalizeInterfaceStringSlice(raw []interface{}) []string {
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		if normalized := strings.TrimSpace(text); normalized != "" {
			items = append(items, normalized)
		}
	}
	return items
}

func modelParamMap(params map[string]interface{}, key string) map[string]interface{} {
	if params == nil {
		return nil
	}
	value, ok := params[key]
	if !ok {
		return nil
	}
	return asMap(value)
}

func applyProviderOptions(payload map[string]interface{}, options map[string]interface{}, protectedKeys ...string) {
	if len(options) == 0 {
		return
	}
	protected := make(map[string]struct{}, len(protectedKeys))
	for _, key := range protectedKeys {
		protected[key] = struct{}{}
	}
	for key, value := range options {
		if _, ok := protected[key]; ok {
			continue
		}
		if current, ok := payload[key].(map[string]interface{}); ok {
			if incoming, ok := value.(map[string]interface{}); ok {
				maps.Copy(current, incoming)
				continue
			}
		}
		if shouldSkipNormalizedProviderOption(key, value) {
			continue
		}
		payload[key] = value
	}
}

func providerToolsFromOptions(options map[string]interface{}) ([]map[string]interface{}, error) {
	if len(options) == 0 {
		return []map[string]interface{}{}, nil
	}
	raw, ok := options["tools"]
	if !ok || raw == nil {
		return []map[string]interface{}{}, nil
	}
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return append([]map[string]interface{}(nil), typed...), nil
	case []interface{}:
		items := make([]map[string]interface{}, 0, len(typed))
		for index, item := range typed {
			payload, ok := item.(map[string]interface{})
			if !ok {
				return nil, withMessage(errCategory5B6F90B115, fmt.Sprintf("model option tools[%d] must be an object", index))
			}
			items = append(items, payload)
		}
		return items, nil
	default:
		return nil, errCategory86487DB59A
	}
}

func toolDeclarationsForInput(input GenerateInput) ([]map[string]interface{}, []ToolDefinition, bool, error) {
	if input.DisableTools {
		return nil, nil, false, nil
	}
	providerTools, err := providerToolsFromOptions(input.Options)
	if err != nil {
		return nil, nil, false, err
	}
	return providerTools, input.Tools, true, nil
}

func providerStreamOptionsFromOptions(options map[string]interface{}) (map[string]interface{}, error) {
	if len(options) == 0 {
		return map[string]interface{}{}, nil
	}
	raw, ok := options["stream_options"]
	if !ok || raw == nil {
		return map[string]interface{}{}, nil
	}
	payload, ok := raw.(map[string]interface{})
	if !ok {
		return nil, errCategory3B6A59EE5B
	}
	return payload, nil
}

func decodeStreamObject(data string) (map[string]interface{}, bool) {
	parsed := make(map[string]interface{})
	if json.Unmarshal([]byte(data), &parsed) != nil {
		return nil, false
	}
	return parsed, true
}

func appendToolDeclarations(payload map[string]interface{}, tools ...[]map[string]interface{}) {
	total := 0
	for _, group := range tools {
		total += len(group)
	}
	if total == 0 {
		return
	}
	merged := make([]map[string]interface{}, 0, total)
	if existing, ok := payload["tools"].([]map[string]interface{}); ok {
		merged = append(merged, existing...)
	}
	for _, group := range tools {
		merged = append(merged, group...)
	}
	if len(merged) > 0 {
		payload["tools"] = merged
	}
}

func appendResponseInclude(payload map[string]interface{}, values ...string) {
	if payload == nil {
		return
	}
	current := make([]string, 0)
	switch existing := payload["include"].(type) {
	case []string:
		current = append(current, existing...)
	case []interface{}:
		for _, item := range existing {
			if text, ok := item.(string); ok {
				current = append(current, text)
			}
		}
	}
	payload["include"] = appendUniqueStrings(current, values...)
}

func responseIncludeValues(options map[string]interface{}, defaults ...string) []string {
	values := make([]string, 0, len(defaults))
	values = append(values, defaults...)
	values = append(values, modelParamStringList(options, "include")...)
	return appendUniqueStrings(nil, values...)
}

func mergeObjectParam(payload map[string]interface{}, key string, values map[string]interface{}) {
	if payload == nil || len(values) == 0 {
		return
	}
	current, _ := payload[key].(map[string]interface{})
	if current == nil {
		current = map[string]interface{}{}
		payload[key] = current
	}
	for field, value := range values {
		if strings.TrimSpace(field) != "" {
			current[field] = value
		}
	}
}

func normalizedJSONResponseFormat(options map[string]interface{}) (interface{}, bool) {
	if len(options) == 0 {
		return nil, false
	}
	raw, ok := options[valueResponseFormat3EE8D395]
	if !ok || raw == nil {
		return nil, false
	}
	if text := strings.TrimSpace(getString(raw)); text != "" {
		switch text {
		case valueJson6F55B6AF:
			return map[string]string{valueType5AC5AA71: valueJsonObjectD320B892}, true
		case valueJsonObjectD320B892, valueText1D495308:
			return map[string]string{valueType5AC5AA71: text}, true
		default:
			return nil, false
		}
	}
	format := asMap(raw)
	if len(format) == 0 {
		return nil, false
	}
	if strings.TrimSpace(getString(format[valueType5AC5AA71])) == "" {
		return nil, false
	}
	return format, true
}

func shouldSkipNormalizedProviderOption(key string, value interface{}) bool {
	if _, ok := value.(map[string]interface{}); ok {
		return false
	}
	switch key {
	case valueTemperature75242E23,
		valueTopPC744CA21,
		valueTopK1E4D787F,
		valueFrequencyPenalty565FB915,
		valuePresencePenalty9F98DF6C,
		valueSeedC8D041FC,
		valueStop1E96CF73,
		"reasoning_effort",
		valueVerbosity33918016,
		valueMaxOutputTokens5044A27A,
		valueMaxCompletionTokensF93E57D2,
		valueResponseFormat3EE8D395,
		valueWebSearch8B501B71,
		valuePromptCacheF470D33F,
		valuePromptCacheRetention08E74823,
		"enable_cache",
		valueCacheTimeout9FA95B17,
		valueEnableThinking2A8ED023,
		valueThinkingDisplayCE5281B8,
		valueEffortF632647D,
		valueBudgetTokensA36C2638,
		valueThinkingBudget78860FBE,
		valueThinkingLevel3C762D05:
		return true
	case valueThinking0F938ED2:
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}

// Usage 记录上游返回 token 使用量。
type Usage struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	ReasoningTokens    int64
	Speed              string
	ServiceTier        string
	RawUsageJSON       string
}

func nonCachedInputTokens(totalInputTokens int64, cacheReadTokens int64) int64 {
	if totalInputTokens <= 0 {
		return 0
	}
	if cacheReadTokens <= 0 {
		return totalInputTokens
	}
	remaining := totalInputTokens - cacheReadTokens
	if remaining < 0 {
		return 0
	}
	return remaining
}

func rawUsageJSONFromPath(payload map[string]interface{}, keys ...string) string {
	if len(payload) == 0 || len(keys) == 0 {
		return ""
	}
	var current interface{} = payload
	for _, key := range keys {
		currentMap, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current, ok = currentMap[key]
		if !ok {
			return ""
		}
	}
	if !canMarshalRawUsageJSON(current) {
		return ""
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return ""
	}
	return string(raw)
}

func canMarshalRawUsageJSON(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		return len(typed) > 0
	case []interface{}:
		return len(typed) > 0
	default:
		return false
	}
}

func MergeRawUsageJSON(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" || right == left {
		return left
	}
	items := make([]interface{}, 0, 2)
	items = appendRawUsageJSON(items, left)
	items = appendRawUsageJSON(items, right)
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		raw, err := json.Marshal(items[0])
		if err != nil {
			return ""
		}
		return string(raw)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return ""
	}
	return string(raw)
}

func appendRawUsageJSON(items []interface{}, raw string) []interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return items
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return items
	}
	switch value := decoded.(type) {
	case []interface{}:
		return append(items, value...)
	case map[string]interface{}:
		return append(items, value)
	default:
		return items
	}
}

// ToolCall 记录上游返回的工具调用请求。
type ToolCall struct {
	ProviderItemID   string
	ToolCallID       string
	ToolType         string
	ToolName         string
	ArgumentsJSON    string
	ThoughtSignature string
	Status           string
	OutputJSON       string
	ErrorJSON        string
}

// ToolResult 记录工具执行结果，由各 adapter 序列化为对应 SDK/API 所需格式。
type ToolResult struct {
	ToolCallID string
	ToolName   string
	OutputJSON string
	Status     string
	Error      string
}

// ReasoningOutput 定义结构化 reasoning 输出。
type ReasoningOutput struct {
	ItemID           string
	Status           string
	Summary          string
	Text             string
	Signature        string
	EncryptedContent string
}

// GenerateOutput 定义上游推理结果。
type GenerateOutput struct {
	Refused             bool
	ResponseID          string
	Text                string
	Reasoning           *ReasoningOutput
	ReplayStateJSON     string
	Usage               Usage
	ToolCalls           []ToolCall
	ServerToolCalls     []ToolCall
	ServerSideToolUsage map[string]int64
	Citations           []string
	GeneratedImages     []GeneratedImage
	GeneratedAssets     []GeneratedAsset
	RawJSON             string
	Debug               *UpstreamDebugSnapshot `json:"-"`
	StopReason          string                 `json:"-"`
	StopSequence        string                 `json:"-"`

	chatTextBuffer       string
	responsesPhases      map[int]string
	responsesItemPhases  map[string]string
	responsesPhased      bool
	responsesEmittedText string
	responsesTextParts   map[[2]int]string
	responsesItemIndexes map[string]int
	responsesIndexIDs    map[int]string
}

// GeneratedImage 表示图片生成/编辑接口返回的一张图片。
type GeneratedImage struct {
	URL           string
	B64JSON       string
	MIMEType      string
	RevisedPrompt string
}

const (
	GeneratedAssetKindImage = "image"
	GeneratedAssetKindVideo = "video"
	GeneratedAssetKindAudio = "audio"
	GeneratedAssetKindWorld = "world"
	GeneratedAssetKind3D    = "model_3d"
)

// GeneratedAsset is the provider-neutral generated media result. Video
// transports must use this contract instead of placing MP4 results in the
// legacy image-only field.
type GeneratedAsset struct {
	Kind          string
	URL           string
	B64JSON       string
	MIMEType      string
	RevisedPrompt string
	ExternalID    string
	MetadataJSON  string
}

// ReasoningDelta 定义流式 reasoning 增量。
type ReasoningDelta struct {
	EventType        string
	ItemID           string
	Status           string
	Kind             string
	Text             string
	Signature        string
	EncryptedContent string
}

// GenerateStreamEvent 定义上游流式增量片段。
type GenerateStreamEvent struct {
	Delta                 string
	Reasoning             *ReasoningDelta
	Usage                 Usage
	ServerToolCall        *ToolCall
	ResponseID            string
	GeneratedImage        *GeneratedImage
	GeneratedImageIndex   int
	GeneratedImagePartial bool
}

// ModelItem 定义上游模型目录项。
type ModelItem struct {
	ID                string
	OwnedBy           string
	KindsJSON         string
	SuggestedProtocol string
}

// UpstreamError 是上游 HTTP 调用错误。
type UpstreamError struct {
	StatusCode int
	Message    string
	Body       string
	Debug      *UpstreamDebugSnapshot
}

// upstreamErrorWithCause preserves the diagnostic projection without changing
// UpstreamError's public fields or discarding the original transport cause.
type upstreamErrorWithCause struct {
	*UpstreamError
	cause error
}

func (e *upstreamErrorWithCause) Unwrap() []error {
	return []error{e.UpstreamError, e.cause}
}

var errStreamDone = errors.New("llm stream done")

// UpstreamDebugSnapshot 记录上游请求与响应的调试快照。
// 对外返回前必须先经过 application 层脱敏，避免泄漏源站、密钥或上游响应头。
type UpstreamDebugSnapshot struct {
	Request  UpstreamDebugRequest  `json:"request"`
	Response UpstreamDebugResponse `json:"response"`
}

// UpstreamDebugRequest 表示上游请求侧的调试信息。
type UpstreamDebugRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// UpstreamDebugResponse 表示上游响应侧的调试信息。
type UpstreamDebugResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

func (e *UpstreamError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("upstream request failed: status=%d", e.StatusCode)
	}
	return fmt.Sprintf("upstream request failed: status=%d message=%s", e.StatusCode, e.Message)
}

// NewClient constructs the wire adapters with an explicit host network policy.
// A missing factory fails closed; it never falls back to http.DefaultClient.
func NewClient(factory HTTPClientFactory) *Client {
	client := &Client{httpClientFactory: factory}
	client.adapters = map[string]transportAdapter{
		AdapterMiMoSpeech:             &miMoSpeechAdapter{client: client},
		AdapterOpenAIResponses:        &openAIResponsesAdapter{client: client},
		AdapterOpenRouterChat:         &openRouterChatCompletionsAdapter{client: client},
		AdapterOpenRouterResponses:    &openRouterResponsesAdapter{client: client},
		AdapterOpenAIChatCompletions:  &openAIChatCompletionsAdapter{client: client},
		AdapterOpenAIImageGenerations: &openAIImageGenerationsAdapter{client: client},
		AdapterOpenAIImageEdits:       &openAIImageEditsAdapter{client: client},
		AdapterXAIResponses:           &xAIResponsesAdapter{client: client},
		AdapterXAIImage:               &xAIImageAdapter{client: client},
		AdapterXAIImageEdits:          &xAIImageEditsAdapter{client: client},
		AdapterAnthropicMessages:      &anthropicMessagesAdapter{client: client},
		AdapterGoogleInteractions:     &geminiInteractionsAdapter{client: client},
		AdapterGoogleImageGeneration:  &geminiImageGenerationAdapter{client: client},
		AdapterVolcengineResponses:    &volcengineResponsesAdapter{client: client},
		AdapterVolcengineImage:        &volcengineImageGenerationAdapter{client: client},
		AdapterVolcengineVideo:        &volcengineVideoGenerationAdapter{client: client},
	}
	for _, protocol := range []string{AdapterMiniMaxChat, AdapterMiniMaxAnthropic, AdapterMiniMaxImage, AdapterMiniMaxVideo, AdapterMiniMaxSpeech, AdapterMiniMaxMusic} {
		client.adapters[protocol] = &miniMaxAdapter{client: client, protocol: protocol}
	}
	for _, protocol := range []string{AdapterSenseAudioChat, AdapterSenseAudioImage, AdapterSenseAudioVideo, AdapterSenseAudioSpeech, AdapterSenseAudioMusic} {
		client.adapters[protocol] = &senseAudioAdapter{client: client, protocol: protocol}
	}
	for _, protocol := range bailianProtocols {
		client.adapters[protocol] = &bailianAdapter{client: client, protocol: protocol}
	}
	return client
}

func (c *Client) adapterFor(route RouteConfig) (transportAdapter, error) {
	adapterName := NormalizeAdapter(route.Protocol)
	if !IsImplementedAdapter(adapterName) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAdapter, adapterName)
	}
	adapter, ok := c.adapters[adapterName]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedAdapter, adapterName)
	}
	return adapter, nil
}

func (c *Client) httpClientForRoute(route RouteConfig) *http.Client {
	client := c.cachedHTTPClient(normalizeConnectTimeoutMS(route.ConnectTimeoutMS))
	if !route.SingleProviderAttempt {
		return client
	}
	// http.Client follows 307/308 by replaying the generation POST. Isolate
	// this policy per admitted call; never mutate the ordinary cached client.
	// Keep the first response so adapters report an upstream failure after
	// dispatch, not a refundable pre-provider rejection.
	bound := *client
	bound.Transport = quotedSingleAttemptTransport{next: client.Transport}
	bound.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &bound
}

func (c *Client) cachedHTTPClient(connectTimeoutMS int) *http.Client {
	if value, ok := c.httpClients.Load(connectTimeoutMS); ok {
		if client, castOK := value.(*http.Client); castOK {
			return client
		}
	}

	created := c.newHTTPClient(connectTimeoutMS)
	actual, _ := c.httpClients.LoadOrStore(connectTimeoutMS, created)
	if client, ok := actual.(*http.Client); ok {
		return client
	}
	return created
}

func (c *Client) newHTTPClient(connectTimeoutMS int) *http.Client {
	if c.httpClientFactory != nil {
		if client := c.httpClientFactory(time.Duration(connectTimeoutMS) * time.Millisecond); client != nil && client.Transport != nil {
			return client
		}
	}
	return &http.Client{Transport: deniedTransport{}}
}

// Generate 调用上游适配器并解析响应（非流式）。
func (c *Client) Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error) {
	if err := validateNativeMediaInput(route, input); err != nil {
		return nil, err
	}
	adapter, err := c.adapterFor(route)
	if err != nil {
		return nil, err
	}
	return adapter.Generate(ctx, route, input)
}

// GenerateStream 调用上游适配器并实时回传增量文本。
func (c *Client) GenerateStream(
	ctx context.Context,
	route RouteConfig,
	input GenerateInput,
	onEvent func(GenerateStreamEvent) error,
) (*GenerateOutput, error) {
	if err := validateNativeMediaInput(route, input); err != nil {
		return nil, err
	}
	adapter, err := c.adapterFor(route)
	if err != nil {
		return nil, err
	}
	return adapter.GenerateStream(ctx, route, input, onEvent)
}

// ListModels 调用上游 models 目录接口。
func (c *Client) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	adapter, err := c.adapterFor(route)
	if err != nil {
		return nil, err
	}
	items, err := adapter.ListModels(ctx, route)
	if err == nil {
		return items, nil
	}
	if !shouldFallbackToOpenAICompatibleModels(route) {
		return nil, err
	}

	fallbackRoute := route
	fallbackRoute.Protocol = AdapterOpenAIChatCompletions
	fallbackItems, fallbackErr := c.listModelsOpenAICompatible(ctx, fallbackRoute)
	if fallbackErr != nil {
		return nil, fmt.Errorf("%w; openai-compatible models fallback failed: %w", err, fallbackErr)
	}
	return fallbackItems, nil
}

func shouldFallbackToOpenAICompatibleModels(route RouteConfig) bool {
	if isOpenRouterBaseURL(route.BaseURL) {
		return false
	}
	switch NormalizeAdapter(route.Protocol) {
	case AdapterAnthropicMessages, AdapterGoogleInteractions, AdapterGoogleImageGeneration:
		return true
	default:
		return false
	}
}

// idleTimeoutReader 包装 io.Reader，在两次 Read 之间超过 idle timeout 时返回错误。
type idleTimeoutReader struct {
	reader  io.Reader
	timeout time.Duration
	timer   *time.Timer
	done    chan struct{}
}

func newIdleTimeoutReader(reader io.Reader, timeout time.Duration) *idleTimeoutReader {
	return &idleTimeoutReader{
		reader:  reader,
		timeout: timeout,
		timer:   time.NewTimer(timeout),
		done:    make(chan struct{}),
	}
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	// 每次成功读取后重置 idle timer
	type readResult struct {
		n   int
		err error
	}
	ch := make(chan readResult, 1)
	go func() {
		n, err := r.reader.Read(p)
		ch <- readResult{n, err}
	}()

	r.timer.Reset(r.timeout)
	select {
	case res := <-ch:
		return res.n, res.err
	case <-r.timer.C:
		return 0, streamIdleTimeoutError(r.timeout)
	}
}

func streamIdleTimeoutError(timeout time.Duration) error {
	return withMessage(errCategory47BCCFF335, fmt.Sprintf("stream idle timeout: no data received for %v", timeout))
}

func decodeToolSchema(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return map[string]interface{}{valueType5AC5AA71: valueObject53670695, valuePropertiesF7D82DD7: map[string]interface{}{}}
	}
	payload := make(map[string]interface{})
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload) == 0 {
		return map[string]interface{}{valueType5AC5AA71: valueObject53670695, valuePropertiesF7D82DD7: map[string]interface{}{}}
	}
	if strings.TrimSpace(getString(payload[valueType5AC5AA71])) == "" {
		payload[valueType5AC5AA71] = valueObject53670695
	}
	if _, ok := payload[valuePropertiesF7D82DD7]; !ok {
		payload[valuePropertiesF7D82DD7] = map[string]interface{}{}
	}
	return payload
}

func buildToolResultContent(item ToolResult) string {
	if strings.TrimSpace(item.Error) != "" {
		payload := map[string]interface{}{
			"ok":               false,
			"status":           strings.TrimSpace(item.Status),
			valueError20660AE7: strings.TrimSpace(item.Error),
		}
		raw, err := json.Marshal(payload)
		if err == nil {
			return string(raw)
		}
		return strings.TrimSpace(item.Error)
	}
	output := strings.TrimSpace(item.OutputJSON)
	if output == "" {
		return "{}"
	}
	return output
}

func normalizeMessages(messages []Message) []Message {
	normalized := make([]Message, 0, len(messages))
	for _, item := range messages {
		// 跳过真正空消息；provider replay state itself is semantic history and
		// must keep an otherwise-empty assistant item alive for stateless replay.
		if len(item.Parts) == 0 && strings.TrimSpace(item.Content) == "" && strings.TrimSpace(item.ReplayStateJSON) == "" && len(item.ToolCalls) == 0 && len(item.ToolResults) == 0 {
			continue
		}
		normalized = append(normalized, Message{
			Role:             normalizeRole(item.Role),
			Content:          item.Content,
			Parts:            item.Parts,
			ReasoningContent: item.ReasoningContent,
			ReplayStateJSON:  item.ReplayStateJSON,
			ToolCalls:        item.ToolCalls,
			ToolResults:      item.ToolResults,
			CacheControl:     item.CacheControl,
		})
	}
	if len(normalized) == 0 {
		normalized = append(normalized, Message{Role: valueUserD301E989, Content: ""})
	}
	return normalized
}

func setAdditionalHeaders(req *http.Request, headersJSON string) {
	value := strings.TrimSpace(headersJSON)
	if value == "" {
		return
	}
	parsed := make(map[string]interface{})
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return
	}
	for key, rawValue := range parsed {
		headerKey := strings.TrimSpace(key)
		if headerKey == "" {
			continue
		}
		req.Header.Set(headerKey, stringify(rawValue))
	}
}

func readUpstreamBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxUpstreamBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxUpstreamBodyBytes {
		return nil, withMessage(errCategoryDC8F665EF5, fmt.Sprintf("upstream response body exceeds %d bytes", maxUpstreamBodyBytes))
	}
	return body, nil
}

type upstreamBodyRecorder struct {
	reader io.Reader
	buffer bytes.Buffer
}

func newUpstreamBodyRecorder(reader io.Reader) *upstreamBodyRecorder {
	return &upstreamBodyRecorder{reader: reader}
}

func (r *upstreamBodyRecorder) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		_, _ = r.buffer.Write(p[:n])
	}
	return n, err
}

func (r *upstreamBodyRecorder) Bytes() []byte {
	return r.buffer.Bytes()
}

func streamErrorBody(recorder *upstreamBodyRecorder, err error) []byte {
	if recorder != nil && recorder.buffer.Len() > 0 {
		return recorder.Bytes()
	}
	return upstreamErrorBody(err)
}

func parseUpstreamError(statusCode int, body []byte, debug *UpstreamDebugSnapshot) error {
	message := fmt.Sprintf("upstream_status_%d", statusCode)
	parsed := make(map[string]interface{})
	if err := json.Unmarshal(body, &parsed); err == nil {
		if m := getStringFromPath(parsed, valueError20660AE7, "message"); m != "" {
			message = m
		} else if m := getString(parsed["message"]); m != "" {
			message = m
		}
	}
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    message,
		Body:       string(body),
		Debug:      debug,
	}
}

func upstreamDebugSnapshot(req *http.Request, requestBody []byte, resp *http.Response, responseBody []byte) *UpstreamDebugSnapshot {
	if req == nil {
		return nil
	}
	path := ""
	if req.URL != nil {
		path = req.URL.EscapedPath()
		if req.URL.RawQuery != "" {
			path += "?" + req.URL.RawQuery
		}
	}
	return &UpstreamDebugSnapshot{
		Request: UpstreamDebugRequest{
			Method:  req.Method,
			Path:    path,
			Headers: redactHeaders(req.Header),
			Body:    string(requestBody),
		},
		Response: UpstreamDebugResponse{
			StatusCode: responseStatusCode(resp),
			Headers:    responseHeaders(resp),
			Body:       string(responseBody),
		},
	}
}

func responseStatusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func responseHeaders(resp *http.Response) map[string]string {
	if resp == nil {
		return map[string]string{}
	}
	return redactHeaders(resp.Header)
}

func redactHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		headerKey := strings.TrimSpace(key)
		if headerKey == "" {
			continue
		}
		if isSecretHeader(headerKey) {
			result[headerKey] = valueRedacted7131EAAF
			continue
		}
		result[headerKey] = strings.Join(values, ", ")
	}
	return result
}

func isSecretHeader(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	return normalized == "authorization" ||
		normalized == "proxy-authorization" ||
		normalized == "cookie" ||
		normalized == "set-cookie" ||
		normalized == "x-api-key" ||
		normalized == "api-key" ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "key")
}

func attachUpstreamDebug(err error, debug *UpstreamDebugSnapshot) error {
	if err == nil || debug == nil {
		return err
	}
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) {
		if upstreamErr.Debug == nil {
			upstreamErr.Debug = debug
		}
		if upstreamErr.StatusCode == 0 {
			upstreamErr.StatusCode = responseStatusCodeFromDebug(debug)
		}
		if strings.TrimSpace(upstreamErr.Body) == "" {
			upstreamErr.Body = debug.Response.Body
		}
		return err
	}
	statusCode := responseStatusCodeFromDebug(debug)
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = fmt.Sprintf("upstream_status_%d", statusCode)
	}
	return &upstreamErrorWithCause{
		UpstreamError: &UpstreamError{
			StatusCode: statusCode,
			Message:    message,
			Body:       debug.Response.Body,
			Debug:      debug,
		},
		cause: err,
	}
}

func responseStatusCodeFromDebug(debug *UpstreamDebugSnapshot) int {
	if debug != nil && debug.Response.StatusCode >= 100 && debug.Response.StatusCode <= 599 {
		return debug.Response.StatusCode
	}
	return http.StatusBadGateway
}

func upstreamErrorBody(err error) []byte {
	var upstreamErr *UpstreamError
	if errors.As(err, &upstreamErr) && strings.TrimSpace(upstreamErr.Body) != "" {
		return []byte(upstreamErr.Body)
	}
	return nil
}

func parseStreamUpstreamError(parsed map[string]interface{}, rawBody string) error {
	errorPayload := asMap(parsed[valueError20660AE7])
	if len(errorPayload) == 0 {
		errorPayload = asMap(asMap(parsed["response"])[valueError20660AE7])
	}
	if len(errorPayload) == 0 {
		return nil
	}
	statusCode := streamErrorStatusCode(parsed, errorPayload)
	message := firstNonEmptyString(
		getString(errorPayload["message"]),
		getString(errorPayload["msg"]),
		getString(parsed["message"]),
		http.StatusText(statusCode),
	)
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    message,
		Body:       rawBody,
	}
}

func streamErrorStatusCode(parsed map[string]interface{}, errorPayload map[string]interface{}) int {
	for _, raw := range []interface{}{
		errorPayload["status"],
		errorPayload["status_code"],
		errorPayload["code"],
		parsed["status"],
		parsed["status_code"],
	} {
		statusCode := toHTTPStatusCode(raw)
		if statusCode >= http.StatusBadRequest {
			return statusCode
		}
	}
	return http.StatusBadGateway
}

func toHTTPStatusCode(raw interface{}) int {
	statusCode := toInt64(raw)
	if statusCode >= 100 && statusCode <= 599 {
		return int(statusCode)
	}
	value := strings.TrimSpace(getString(raw))
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 100 || parsed > 599 {
		return 0
	}
	return parsed
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func appendUniqueToolCall(items *[]ToolCall, item ToolCall) {
	if items == nil {
		return
	}
	for idx, existing := range *items {
		if shouldMergeToolCall(existing, item) {
			(*items)[idx] = mergeToolCall(existing, item)
			return
		}
	}
	*items = append(*items, item)
}

func shouldMergeToolCall(existing ToolCall, incoming ToolCall) bool {
	existingItemID := strings.TrimSpace(existing.ProviderItemID)
	incomingItemID := strings.TrimSpace(incoming.ProviderItemID)
	if existingItemID != "" && incomingItemID != "" {
		return existingItemID == incomingItemID
	}
	existingID := strings.TrimSpace(existing.ToolCallID)
	incomingID := strings.TrimSpace(incoming.ToolCallID)
	if existingID != "" && incomingID != "" {
		return existingID == incomingID
	}
	if existingID == "" && incomingID == "" {
		return sameToolCallKind(existing, incoming)
	}
	if existingID == "" && incomingID != "" {
		return sameToolCallKind(existing, incoming)
	}
	return false
}

func sameToolCallKind(left ToolCall, right ToolCall) bool {
	leftName := strings.TrimSpace(left.ToolName)
	rightName := strings.TrimSpace(right.ToolName)
	if leftName != "" && rightName != "" && leftName == rightName {
		return true
	}
	leftType := strings.TrimSpace(left.ToolType)
	rightType := strings.TrimSpace(right.ToolType)
	return leftType != "" && rightType != "" && leftType == rightType
}

func mergeToolCall(existing ToolCall, incoming ToolCall) ToolCall {
	merged := existing
	if value := strings.TrimSpace(incoming.ProviderItemID); value != "" {
		merged.ProviderItemID = value
	}
	if value := strings.TrimSpace(incoming.ToolCallID); value != "" {
		merged.ToolCallID = value
	}
	if value := strings.TrimSpace(incoming.ToolType); value != "" {
		merged.ToolType = value
	}
	if value := strings.TrimSpace(incoming.ToolName); value != "" {
		merged.ToolName = value
	}
	if value := strings.TrimSpace(incoming.ArgumentsJSON); value != "" {
		merged.ArgumentsJSON = value
	}
	if value := strings.TrimSpace(incoming.ThoughtSignature); value != "" {
		merged.ThoughtSignature = value
	}
	if value := strings.TrimSpace(incoming.OutputJSON); value != "" {
		merged.OutputJSON = value
	}
	if value := strings.TrimSpace(incoming.ErrorJSON); value != "" {
		merged.ErrorJSON = value
	}
	merged.Status = mergeToolCallStatus(existing.Status, incoming.Status)
	return merged
}

func mergeToolCallStatus(existing string, incoming string) string {
	current := strings.TrimSpace(existing)
	next := strings.TrimSpace(incoming)
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	if toolCallStatusRank(next) >= toolCallStatusRank(current) {
		return next
	}
	return current
}

func toolCallStatusRank(status string) int {
	switch strings.TrimSpace(status) {
	case "failed", valueError20660AE7:
		return 4
	case valueCompleted96C680F2, "success":
		return 3
	case valueInProgress6CA4051A, "queued", "searching", "streaming", "requested":
		return 2
	default:
		return 1
	}
}

func updateToolCallInput(items *[]ToolCall, itemID string, input string, done bool) (ToolCall, bool) {
	if items == nil || strings.TrimSpace(itemID) == "" {
		return ToolCall{}, false
	}
	for index, item := range *items {
		if strings.TrimSpace(item.ToolCallID) != itemID && strings.TrimSpace(item.ProviderItemID) != itemID {
			continue
		}
		item = applyToolCallInputUpdate(item, input, done)
		(*items)[index] = item
		return item, true
	}
	return ToolCall{}, false
}

func applyToolCallInputUpdate(item ToolCall, input string, done bool) ToolCall {
	if done {
		return completeToolCallInput(item, input)
	}
	return appendToolCallInput(item, input)
}

func completeToolCallInput(item ToolCall, input string) ToolCall {
	if value := strings.TrimSpace(input); value != "" {
		item.ArgumentsJSON = value
	}
	if status := strings.TrimSpace(item.Status); status == "" || status == valueInProgress6CA4051A {
		item.Status = valueCompleted96C680F2
	}
	return item
}

func appendToolCallInput(item ToolCall, input string) ToolCall {
	if strings.TrimSpace(input) == "" {
		return item
	}
	item.ArgumentsJSON += input
	if strings.TrimSpace(item.Status) == "" {
		item.Status = valueInProgress6CA4051A
	}
	return item
}

func appendUniqueStrings(items []string, values ...string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	for _, item := range values {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneInt64Map(value map[string]int64) map[string]int64 {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[string]int64, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func extractReasoningDeltaText(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := extractReasoningDeltaText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "")
	case map[string]interface{}:
		for _, key := range []string{valueText1D495308, "delta", valueThinking0F938ED2, responseSummaryKey, valueContent03EE2C26} {
			if text := extractReasoningDeltaText(value[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func extractContentText(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []interface{}:
		chunks := make([]string, 0, len(value))
		for _, item := range value {
			if text := extractContentText(item); text != "" {
				chunks = append(chunks, text)
			}
		}
		return strings.Join(chunks, "")
	case map[string]interface{}:
		return extractContentObjectText(value)
	}
	return ""
}

func extractContentObjectText(value map[string]interface{}) string {
	for _, key := range []string{valueText1D495308, "output_text", "input_text", "refusal"} {
		if text := getString(value[key]); text != "" {
			return text
		}
	}
	return extractContentText(value[valueContent03EE2C26])
}

func getStringFromPath(payload map[string]interface{}, keys ...string) string {
	if len(keys) == 0 {
		return ""
	}
	current := interface{}(payload)
	for _, key := range keys {
		node := asMap(current)
		if len(node) == 0 {
			return ""
		}
		current = node[key]
	}
	return strings.TrimSpace(getString(current))
}

func getInt64FromPath(payload map[string]interface{}, keys ...string) int64 {
	if len(keys) == 0 {
		return 0
	}
	current := interface{}(payload)
	for _, key := range keys {
		node := asMap(current)
		if len(node) == 0 {
			return 0
		}
		current = node[key]
	}
	return toInt64(current)
}

func firstMapItem(items []interface{}) map[string]interface{} {
	for _, item := range items {
		payload := asMap(item)
		if len(payload) > 0 {
			return payload
		}
	}
	return map[string]interface{}{}
}

func normalizeEndpoint(raw string) string {
	switch strings.TrimSpace(raw) {
	case EndpointChatCompletions:
		return EndpointChatCompletions
	case EndpointImageGenerations:
		return EndpointImageGenerations
	case EndpointImageEdits:
		return EndpointImageEdits
	default:
		return EndpointResponses
	}
}

func normalizeRole(raw string) string {
	switch strings.TrimSpace(raw) {
	case "developer", "system", valueUserD301E989, valueAssistantCAAB15A3, "tool", valueFunctionB03625B4:
		return strings.TrimSpace(raw)
	default:
		return valueUserD301E989
	}
}

func asMap(raw interface{}) map[string]interface{} {
	if payload, ok := raw.(map[string]interface{}); ok {
		return payload
	}
	return map[string]interface{}{}
}

func cloneMap(raw map[string]interface{}) map[string]interface{} {
	if len(raw) == 0 {
		return map[string]interface{}{}
	}
	payload := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		payload[key] = value
	}
	return payload
}

func mergeMapValueIfEmpty(payload map[string]interface{}, key string, value interface{}) {
	if payload == nil || value == nil {
		return
	}
	if existing, ok := payload[key]; ok && normalizeJSONString(existing) != "" {
		return
	}
	if normalizeJSONString(value) == "" {
		return
	}
	payload[key] = value
}

func asSlice(raw interface{}) []interface{} {
	if payload, ok := raw.([]interface{}); ok {
		return payload
	}
	return []interface{}{}
}

func getString(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	case uint64:
		return strconv.FormatUint(value, 10)
	case bool:
		if value {
			return valueTrue0F6181DB
		}
		return "false"
	default:
		return ""
	}
}

func toInt64(raw interface{}) int64 {
	if value, ok := signedIntegerToInt64(raw); ok {
		return value
	}
	if value, ok := unsignedIntegerToInt64(raw); ok {
		return value
	}
	if value, ok := floatToInt64(raw); ok {
		return value
	}
	if value, ok := stringLikeToInt64(raw); ok {
		return value
	}
	return 0
}

func signedIntegerToInt64(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case int32:
		return int64(value), true
	default:
		return 0, false
	}
}

func unsignedIntegerToInt64(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case uint:
		return uintToInt64(uint64(value)), true
	case uint64:
		return uintToInt64(value), true
	default:
		return 0, false
	}
}

func floatToInt64(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case float64:
		return int64(value), true
	case float32:
		return int64(value), true
	default:
		return 0, false
	}
}

func stringLikeToInt64(raw interface{}) (int64, bool) {
	switch value := raw.(type) {
	case json.Number:
		return jsonNumberToInt64(value), true
	case string:
		return stringToInt64(value), true
	default:
		return 0, false
	}
}

func uintToInt64(value uint64) int64 {
	if value > 1<<63-1 {
		return 0
	}
	return int64(value)
}

func jsonNumberToInt64(value json.Number) int64 {
	parsed, err := value.Int64()
	if err == nil {
		return parsed
	}
	f, ferr := value.Float64()
	if ferr != nil {
		return 0
	}
	return int64(f)
}

func stringToInt64(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func normalizeJSONString(raw interface{}) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		value := strings.TrimSpace(text)
		if value == "" {
			return ""
		}
		if json.Valid([]byte(value)) {
			return value
		}
		return value
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func stringify(raw interface{}) string {
	if raw == nil {
		return ""
	}
	if text, ok := raw.(string); ok {
		return text
	}
	if numeric, ok := raw.(json.Number); ok {
		return numeric.String()
	}
	return fmt.Sprintf("%v", raw)
}

func firstNonZero(values ...int64) int64 {
	for _, item := range values {
		if item > 0 {
			return item
		}
	}
	return 0
}

var (
	errCategory3B6A59EE5B = errors.New("model option stream_options must be an object")
	errCategory86487DB59A = errors.New("model option tools must be an array")
	errCategory5B6F90B115 = errors.New("error category 5B6F90B115")
	errCategory47BCCFF335 = errors.New("error category 47BCCFF335")
	errCategoryDC8F665EF5 = errors.New("error category DC8F665EF5")
)
