package transport

import (
	"context"
	"errors"
	"strings"
)

// 已支持的协议常量。每个协议固定对应一个 HTTP 端点，任务能力由模型类别和路由规则约束。
const (
	AdapterBailianChat          = "bailian_chat_completions"
	AdapterBailianImage         = "bailian_image_generation"
	AdapterBailianVideo         = "bailian_video_generation"
	AdapterBailianWorld         = "bailian_world_generation"
	AdapterBailian3D            = "bailian_3d_generation"
	AdapterBailianSpeech        = "bailian_speech_generation"
	AdapterBailianAudio         = "bailian_audio_generation"
	AdapterBailianTranscription = "bailian_audio_transcription"
	AdapterBailianMusic         = "bailian_music_generation"
	AdapterBailianEmbedding     = "bailian_embeddings"
	AdapterBailianRerank        = "bailian_rerank"

	AdapterMiniMaxChat      = "minimax_chat_completions"
	AdapterMiniMaxAnthropic = "minimax_anthropic_messages"
	AdapterMiniMaxImage     = "minimax_image_generation"
	AdapterMiniMaxVideo     = "minimax_video_generation"
	AdapterMiniMaxSpeech    = "minimax_speech_generation"
	AdapterMiniMaxMusic     = "minimax_music_generation"
	AdapterMiMoSpeech       = "mimo_speech_generation"
	AdapterSenseAudioChat   = "senseaudio_chat_completions"
	AdapterSenseAudioImage  = "senseaudio_image_generation"
	AdapterSenseAudioVideo  = "senseaudio_video_generation"
	AdapterSenseAudioSpeech = "senseaudio_speech_generation"
	AdapterSenseAudioMusic  = "senseaudio_music_generation"

	AdapterOpenAIResponses        = "openai_responses"            // POST /v1/responses
	AdapterOpenRouterChat         = "openrouter_chat_completions" // POST /v1/chat/completions（OpenRouter）
	AdapterOpenRouterResponses    = "openrouter_responses"        // POST /v1/responses（OpenRouter Responses Beta）
	AdapterOpenAIChatCompletions  = "openai_chat_completions"     // POST /v1/chat/completions
	AdapterOpenAIImageGenerations = "openai_image_generations"    // POST /v1/images/generations
	AdapterOpenAIImageEdits       = "openai_image_edits"          // POST /v1/images/edits
	AdapterAnthropicMessages      = "anthropic_messages"          // POST /v1/messages
	AdapterGoogleInteractions     = "google_interactions"         // POST /v1/interactions
	AdapterGoogleImageGeneration  = "google_image_generation"     // POST /v1beta/models/{model}:generateContent
	AdapterXAIResponses           = "xai_responses"               // POST /v1/responses（OpenAI 兼容）
	AdapterXAIImage               = "xai_image"                   // POST /v1/images/generations
	AdapterXAIImageEdits          = "xai_image_edits"             // POST /v1/images/edits
	AdapterVolcengineResponses    = "volcengine_responses"        // POST /api/v3/responses
	AdapterVolcengineImage        = "volcengine_image_generation" // POST /api/v3/images/generations（生成与编辑）
	AdapterVolcengineVideo        = "volcengine_video_generation" // POST/GET /api/v3/contents/generations/tasks
)

var (
	// ErrUnsupportedAdapter 表示协议没有可用适配器实现。
	ErrUnsupportedAdapter = errors.New("unsupported llm adapter")
	// ErrUnsupportedStream 表示协议存在但不支持真实流式输出。
	ErrUnsupportedStream = errors.New("unsupported llm stream")
	// ErrUnsupportedImageMask 表示输入含有该媒体协议无法表达的编辑掩码。
	ErrUnsupportedImageMask = errors.New("image edit mask is incompatible with this adapter")
)

type transportAdapter interface {
	Name() string
	Generate(ctx context.Context, route RouteConfig, input GenerateInput) (*GenerateOutput, error)
	GenerateStream(ctx context.Context, route RouteConfig, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error)
	ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error)
}

// NormalizeAdapter 规范化协议名；空值按历史默认使用 openai_responses，未知值保留给校验层处理。
func NormalizeAdapter(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return AdapterOpenAIResponses
	}
	return value
}

// IsImplementedAdapter 返回协议是否已有可用的传输层实现。
func IsImplementedAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIResponses, AdapterOpenRouterChat, AdapterOpenRouterResponses, AdapterOpenAIChatCompletions, AdapterOpenAIImageGenerations, AdapterOpenAIImageEdits, AdapterXAIResponses,
		AdapterAnthropicMessages, AdapterGoogleInteractions, AdapterGoogleImageGeneration, AdapterXAIImage, AdapterXAIImageEdits,
		AdapterVolcengineResponses, AdapterVolcengineImage, AdapterVolcengineVideo, AdapterMiniMaxChat, AdapterMiniMaxAnthropic, AdapterMiniMaxImage, AdapterMiniMaxVideo, AdapterMiniMaxSpeech, AdapterMiniMaxMusic, AdapterMiMoSpeech, AdapterBailianChat, AdapterBailianImage, AdapterBailianVideo, AdapterBailianWorld, AdapterBailian3D, AdapterBailianSpeech, AdapterBailianAudio, AdapterBailianTranscription, AdapterBailianMusic, AdapterBailianEmbedding, AdapterBailianRerank, AdapterSenseAudioChat, AdapterSenseAudioImage, AdapterSenseAudioVideo, AdapterSenseAudioSpeech, AdapterSenseAudioMusic:
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
		AdapterOpenAIImageGenerations,
		AdapterOpenAIImageEdits,
		AdapterAnthropicMessages,
		AdapterGoogleInteractions,
		AdapterGoogleImageGeneration,
		AdapterXAIResponses,
		AdapterVolcengineResponses, AdapterMiniMaxChat, AdapterMiniMaxAnthropic, AdapterBailianChat, AdapterSenseAudioChat:
		return true
	default:
		return false
	}
}

// SupportsImageGenerationStream 返回图片媒体协议和模型是否支持真实上游流式。
func SupportsImageGenerationStream(protocol string, model string) bool {
	switch NormalizeAdapter(protocol) {
	case AdapterOpenAIImageGenerations:
		return openAIImageGenerationModelSupportsStream(model)
	case AdapterGoogleImageGeneration:
		return true
	case AdapterOpenAIImageEdits:
		return openAIImageEditModelSupportsStream(model)
	default:
		return false
	}
}

// IsImageGenerationAdapter 返回协议是否属于独立图片生成链路。
func IsImageGenerationAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIImageGenerations, AdapterGoogleImageGeneration, AdapterXAIImage, AdapterVolcengineImage, AdapterMiniMaxImage, AdapterBailianImage, AdapterSenseAudioImage:
		return true
	default:
		return false
	}
}

// IsImageEditAdapter 返回协议是否属于独立图片编辑链路。
func IsImageEditAdapter(raw string) bool {
	switch NormalizeAdapter(raw) {
	case AdapterOpenAIImageEdits, AdapterGoogleImageGeneration, AdapterXAIImageEdits, AdapterVolcengineImage, AdapterMiniMaxImage, AdapterBailianImage, AdapterSenseAudioImage:
		return true
	default:
		return false
	}
}

// IsVideoGenerationAdapter 返回协议是否属于独立视频生成链路。
func IsVideoGenerationAdapter(raw string) bool {
	return NormalizeAdapter(raw) == AdapterVolcengineVideo || NormalizeAdapter(raw) == AdapterMiniMaxVideo || NormalizeAdapter(raw) == AdapterBailianVideo || NormalizeAdapter(raw) == AdapterSenseAudioVideo
}

var adapterDefaultEndpoints = map[string]string{
	AdapterOpenAIChatCompletions:  EndpointChatCompletions,
	AdapterOpenRouterChat:         EndpointChatCompletions,
	AdapterMiniMaxChat:            EndpointChatCompletions,
	AdapterBailianChat:            EndpointChatCompletions,
	AdapterMiMoSpeech:             EndpointChatCompletions,
	AdapterSenseAudioChat:         EndpointChatCompletions,
	AdapterGoogleInteractions:     EndpointInteractions,
	AdapterOpenAIImageGenerations: EndpointImageGenerations,
	AdapterGoogleImageGeneration:  EndpointImageGenerations,
	AdapterXAIImage:               EndpointImageGenerations,
	AdapterVolcengineImage:        EndpointImageGenerations,
	AdapterMiniMaxImage:           EndpointImageGenerations,
	AdapterBailianImage:           EndpointImageGenerations,
	AdapterSenseAudioImage:        EndpointImageGenerations,
	AdapterOpenAIImageEdits:       EndpointImageEdits,
	AdapterXAIImageEdits:          EndpointImageEdits,
	AdapterMiniMaxAnthropic:       EndpointMessages,
	AdapterMiniMaxVideo:           EndpointVideoGenerations,
	AdapterBailianVideo:           EndpointVideoGenerations,
	AdapterSenseAudioVideo:        EndpointVideoGenerations,
	AdapterMiniMaxSpeech:          EndpointSpeechGenerations,
	AdapterBailianSpeech:          EndpointSpeechGenerations,
	AdapterSenseAudioSpeech:       EndpointSpeechGenerations,
	AdapterMiniMaxMusic:           EndpointMusicGenerations,
	AdapterBailianMusic:           EndpointMusicGenerations,
	AdapterSenseAudioMusic:        EndpointMusicGenerations,
	AdapterBailianAudio:           EndpointAudioGenerations,
	AdapterBailianWorld:           EndpointWorldGenerations,
	AdapterBailian3D:              Endpoint3DGenerations,
	AdapterBailianTranscription:   EndpointAudioTranscriptions,
	AdapterBailianEmbedding:       EndpointEmbeddings,
	AdapterBailianRerank:          EndpointRerank,
	AdapterVolcengineVideo:        EndpointContentGenerationTasks,
}

// DefaultEndpointForAdapter 返回协议对应的固定端点标识。
func DefaultEndpointForAdapter(adapter string) string {
	if endpoint, ok := adapterDefaultEndpoints[NormalizeAdapter(adapter)]; ok {
		return endpoint
	}
	// openai_responses、openrouter_responses、xai_responses 及所有未知值均使用 Responses 端点。
	return EndpointResponses
}

// SupportsPreviousResponseID 返回协议是否明确支持 provider-side stateful continuation。
// Google Interactions intentionally stays stateless so Runtime snapshots remain
// self-contained; its provider-generated steps are replayed through opaque state.
func SupportsPreviousResponseID(adapter string) bool {
	switch NormalizeAdapter(adapter) {
	case AdapterOpenAIResponses, AdapterVolcengineResponses:
		return true
	default:
		return false
	}
}
