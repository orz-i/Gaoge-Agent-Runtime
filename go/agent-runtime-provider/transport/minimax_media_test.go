package transport

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiniMaxImageReferenceAndBase64Result(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aM9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(png)
	response, err := json.Marshal(map[string]interface{}{"id": "image-2", valueData7C9F25C7: map[string]interface{}{"image_base64": []string{encoded}}})
	require.NoError(t, err)
	server, requests := miniMaxFixtureServer(t, miniMaxImagePath, string(response))
	output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterMiniMaxImage, BaseURL: server.URL, APIKey: miniMaxFixtureAPIKey, UpstreamModel: "image-01-live"}, GenerateInput{
		Messages: []Message{{Role: valueUser72795C11, Parts: []ContentPart{{Kind: ContentPartText, Text: "portrait"}, {Kind: ContentPartImage, Data: png}}}},
		Options:  map[string]interface{}{valueResponseFormat1C776FA1: miniMaxImageBase64, valueAspectRatioA5C1E4FE: miniMaxPortraitRatio, "n": 2, valueSeedC8D041FC: 42},
	})
	require.NoError(t, err)
	require.Len(t, output.GeneratedImages, 1)
	assert.Equal(t, encoded, output.GeneratedImages[0].B64JSON)
	assert.Equal(t, valueImagePng415383B9, output.GeneratedImages[0].MIMEType)
	payload := <-requests
	references := asSlice(payload["subject_reference"])
	require.Len(t, references, 1)
	assert.Equal(t, "character", asMap(references[0])[valueTypeF2B37301])
	assert.Equal(t, "data:image/png;base64,"+encoded, asMap(references[0])["image_file"])
	assert.Equal(t, "portrait", payload[valuePrompt9071DA16])
}

func TestMiniMaxMediaRejectsInvalidInputBeforeDispatch(t *testing.T) {
	for _, n := range []interface{}{0, 10, 1.5, "2"} {
		_, err := buildMiniMaxImageRequest(miniMaxModelImage01, GenerateInput{Messages: []Message{{Content: GeneratedAssetKindImage}}, Options: map[string]interface{}{"n": n}})
		require.Error(t, err)
	}
	_, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterMiniMaxImage}, GenerateInput{ImageEditMask: &ContentPart{Kind: ContentPartImage, Data: []byte("mask")}})
	require.ErrorIs(t, err, ErrUnsupportedImageMask)
	for _, test := range []struct {
		model, protocol, prompt string
		options                 map[string]interface{}
	}{
		{miniMaxModelSpeech28HD, AdapterMiniMaxSpeech, strings.Repeat("音", 10000), nil},
		{miniMaxModelSpeech28HD, AdapterMiniMaxSpeech, miniMaxFixtureSpeechPrompt, map[string]interface{}{miniMaxVoiceSettingOption: "invalid"}},
		{miniMaxModelSpeech28HD, AdapterMiniMaxSpeech, miniMaxFixtureSpeechPrompt, map[string]interface{}{miniMaxAudioSettingOption: false}},
		{miniMaxModelMusic30, AdapterMiniMaxMusic, "music", map[string]interface{}{"lyrics_optimizer": false}},
		{miniMaxModelMusic30, AdapterMiniMaxMusic, "", map[string]interface{}{"is_instrumental": true}},
		{miniMaxModelMusic30, AdapterMiniMaxMusic, "music", map[string]interface{}{miniMaxAudioSettingOption: map[string]interface{}{miniMaxFormatKey: "flac"}}},
		{miniMaxModelMusicCover, AdapterMiniMaxMusic, miniMaxFixtureCoverPrompt, nil},
		{miniMaxModelMusicCover, AdapterMiniMaxMusic, miniMaxFixtureCoverPrompt, map[string]interface{}{miniMaxAudioURLInput: "https://cdn.example/song.mp3", "audio_base64": "AAAA"}},
		{miniMaxModelMusicCover, AdapterMiniMaxMusic, miniMaxFixtureCoverPrompt, map[string]interface{}{miniMaxCoverFeatureInput: "feature-1"}},
	} {
		_, err := buildMiniMaxAudioRequest(test.model, test.protocol, GenerateInput{Messages: []Message{{Content: test.prompt}}, Options: test.options})
		require.Error(t, err, "%s %v", test.model, test.options)
	}
}

func TestMiniMaxMusicLyricsInstrumentalAndCover(t *testing.T) {
	for _, test := range []struct {
		model, prompt string
		options       map[string]interface{}
	}{
		{miniMaxModelMusic30, "", map[string]interface{}{"lyrics": "[Verse]\n夜色温柔"}},
		{"music-2.6", "quiet piano", map[string]interface{}{"is_instrumental": true}},
		{miniMaxModelMusicCover, miniMaxFixtureCoverPrompt, map[string]interface{}{miniMaxAudioURLInput: "https://cdn.example/song.mp3"}},
		{miniMaxModelMusicCover, miniMaxFixtureCoverPrompt, map[string]interface{}{miniMaxCoverFeatureInput: "feature-1", "lyrics": "[Verse]\n夜色温柔伴我前行"}},
	} {
		payload, err := buildMiniMaxAudioRequest(test.model, AdapterMiniMaxMusic, GenerateInput{Messages: []Message{{Content: test.prompt}}, Options: test.options})
		require.NoError(t, err)
		assert.Nil(t, payload["lyrics_optimizer"])
	}
	payload, err := buildMiniMaxAudioRequest(miniMaxModelSpeech28HD, AdapterMiniMaxSpeech, GenerateInput{Messages: []Message{{Content: miniMaxFixtureSpeechPrompt}}, Options: map[string]interface{}{"timbre_weights": []interface{}{map[string]interface{}{"voice_id": "voice-1", "weight": 100}}}})
	require.NoError(t, err)
	assert.Empty(t, asMap(payload[miniMaxVoiceSettingOption])["voice_id"])
}

func TestMiniMaxRejectsMalformedOrIncompleteMediaOutput(t *testing.T) {
	for _, response := range []string{
		`{"data":{"audio":"494433","status":1}}`,
		`{"data":{"audio":"494433","status":"complete"}}`,
		`{"data":{"audio":"invalid-hex","status":2}}`,
		`{"data":{"status":2}}`,
		`{"data":{"audio":"494433","status":2},"extra_info":{"audio_format":"unknown"}}`,
	} {
		var parsed map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(response), &parsed))
		_, err := parseMiniMaxAudioOutput(parsed, map[string]interface{}{valueOutputFormatA54B283B: miniMaxOutputHex, miniMaxAudioSettingOption: map[string]interface{}{miniMaxFormatKey: miniMaxFormatMP3}})
		require.Error(t, err)
	}
	for _, response := range []string{`{"data":{}}`, `{"data":{"image_base64":["invalid-base64"]}}`, `{"data":{"image_base64":["bm90IGFuIGltYWdl"]}}`} {
		var parsed map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(response), &parsed))
		_, err := parseMiniMaxImageOutput(parsed)
		require.Error(t, err)
	}
}
