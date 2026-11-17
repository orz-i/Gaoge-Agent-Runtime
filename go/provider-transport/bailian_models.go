package transport

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

func (a *bailianAdapter) ListModels(ctx context.Context, route RouteConfig) ([]ModelItem, error) {
	route = normalizeBailianRoute(route, a.protocol)
	requestCtx, cancel := context.WithTimeout(ctx, resolveReadTimeout(route.ReadTimeoutMS))
	defer cancel()
	result := make([]ModelItem, 0)
	seen := map[string]bool{}
	received := int64(0)
	for page := 1; page <= 1000; page++ {
		path := bailianNativeBase + "/models?page_no=" + strconv.Itoa(page) + "&page_size=100"
		parsed, _, debug, err := a.client.doBailianRequest(requestCtx, route, http.MethodGet, path, nil, false)
		if err != nil {
			return nil, err
		}
		models, total, err := bailianCatalogPage(parsed, page, received)
		if err != nil {
			return nil, attachUpstreamDebug(err, debug)
		}
		for _, raw := range models {
			item, ok := bailianCatalogModel(asMap(raw))
			if ok && !seen[item.ID] {
				seen[item.ID] = true
				result = append(result, item)
			}
		}
		received += int64(len(models))
		if received >= total {
			return result, nil
		}
	}
	return nil, fmt.Errorf("%w: model catalog pagination limit exceeded", errBailianResponse)
}

func bailianCatalogModel(raw map[string]interface{}) (ModelItem, bool) {
	id := strings.TrimSpace(getString(raw[valueModel2D31E8EB]))
	protocol := bailianCatalogProtocol(id, modelParamStringList(raw, "capabilities"))
	if id == "" || protocol == "" {
		return ModelItem{}, false
	}
	return ModelItem{ID: id, OwnedBy: getString(raw["provider"]), KindsJSON: bailianCatalogKinds(id, protocol), SuggestedProtocol: protocol}, true
}

// Return only families with an implemented wire format. In particular, models
// needing WebSocket/RTC or another vendor's native API are not imported as chat.
func bailianCatalogProtocol(id string, capabilities []string) string {
	code := strings.ToLower(id)
	if strings.Contains(code, "realtime") || strings.Contains(code, "streaming") {
		return ""
	}
	if protocol := bailianCatalogMediaProtocol(code); protocol != "" {
		return protocol
	}
	if bailianNonChatModel(code) {
		return ""
	}
	for _, capability := range capabilities {
		if map[string]bool{"TG": true, "Reasoning": true, "VU": true}[capability] {
			return AdapterBailianChat
		}
	}
	for _, prefix := range []string{"qwen", "qwq", "deepseek-"} {
		if strings.HasPrefix(code, prefix) {
			return AdapterBailianChat
		}
	}
	return ""
}
func bailianNonChatModel(code string) bool {
	for _, name := range []string{"tts", "asr", "embedding", "rerank"} {
		if strings.Contains(code, name) {
			return true
		}
	}
	return false
}

func bailianCatalogMediaProtocol(code string) string {
	exact := map[string]string{bailianModelWorldAdventure: AdapterBailianWorld, bailianModelWorldDirecting: AdapterBailianWorld, bailianModelWorldActing: AdapterBailianWorld, "tripo/tripo-h3.1": AdapterBailian3D, "tripo/tripo-p1.0": AdapterBailian3D, bailianModelAudioNext: AdapterBailianAudio, bailianModelMusic: AdapterBailianMusic, "fun-music-preview": AdapterBailianMusic, bailianModelVLEmbedding3: AdapterBailianEmbedding, "qwen2.5-vl-embedding": AdapterBailianEmbedding, "qwen3.7-text-rerank": AdapterBailianRerank, bailianModelQwenRerank3: AdapterBailianRerank, "qwen3-vl-rerank": AdapterBailianRerank, "gte-rerank-v2": AdapterBailianRerank, "fun-asr": AdapterBailianTranscription, "fun-asr-mtl": AdapterBailianTranscription}
	if protocol := exact[code]; protocol != "" {
		return protocol
	}
	if protocol := bailianCatalogAudioProtocol(code); protocol != "" {
		return protocol
	}
	for _, prefix := range []string{"text-embedding-", bailianModelTextEmbedding37, "tongyi-embedding-vision-"} {
		if strings.HasPrefix(code, prefix) {
			return AdapterBailianEmbedding
		}
	}
	if strings.HasPrefix(code, "qwen-image") || strings.HasPrefix(code, "wan2.7-image") {
		return AdapterBailianImage
	}
	if strings.HasPrefix(code, "wan3.0-video") || bailianWan27VideoModel(code) {
		return AdapterBailianVideo
	}
	return ""
}
func bailianWan27VideoModel(code string) bool {
	return strings.HasPrefix(code, "wan2.7-") && (strings.Contains(code, "t2v") || strings.Contains(code, "i2v"))
}
func bailianCatalogAudioProtocol(code string) string {
	if strings.HasPrefix(code, "qwen-audio-3.") {
		if strings.Contains(code, "tts-") {
			return AdapterBailianSpeech
		}
		if strings.Contains(code, "asr-flash-filetrans") {
			return AdapterBailianTranscription
		}
	}
	if strings.HasPrefix(code, "qwen3-asr-flash") || strings.HasPrefix(code, "paraformer-") {
		return AdapterBailianTranscription
	}
	return ""
}

func bailianCatalogKinds(id, protocol string) string {
	if protocol == AdapterBailianImage {
		return bailianImageCatalogKinds(id)
	}
	kinds := map[string]string{AdapterBailianVideo: `["video_gen"]`, AdapterBailianWorld: `["world_gen"]`, AdapterBailian3D: `["model_3d_gen"]`, AdapterBailianSpeech: `["audio_gen"]`, AdapterBailianAudio: `["audio_gen"]`, AdapterBailianMusic: `["music_gen"]`, AdapterBailianTranscription: `["audio_transcription"]`, AdapterBailianEmbedding: `["embedding"]`, AdapterBailianRerank: `["rerank"]`}
	if kind := kinds[protocol]; kind != "" {
		return kind
	}
	return `["chat"]`
}
func bailianImageCatalogKinds(id string) string {
	code := strings.ToLower(id)
	if strings.HasPrefix(code, "qwen-image-edit") {
		return `["image_edit"]`
	}
	if strings.HasPrefix(code, "qwen-image-2.") || strings.HasPrefix(code, "qwen-image-3.") || strings.HasPrefix(code, "wan2.7-image") {
		return `["image_gen","image_edit"]`
	}
	return `["image_gen"]`
}

func bailianCatalogPage(parsed map[string]interface{}, page int, received int64) ([]interface{}, int64, error) {
	output := asMap(parsed["output"])
	models, valid := output["models"].([]interface{})
	if !valid || !bailianIntegerInRange(output, "total", 0, 1000000) {
		return nil, 0, fmt.Errorf("%w: missing model catalog or total", errBailianResponse)
	}
	if _, exists := output["page_no"]; exists && !bailianIntegerInRange(output, "page_no", float64(page), float64(page)) {
		return nil, 0, fmt.Errorf("%w: mismatched catalog page", errBailianResponse)
	}
	total := toInt64(output["total"])
	if len(models) == 0 && received < total {
		return nil, 0, fmt.Errorf("%w: incomplete model catalog", errBailianResponse)
	}

	return models, total, nil
}
