package transport

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const miMoTestInvalidFormat = "flac"

func miMoWAVFixture() []byte {
	data := make([]byte, 46)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], 38)
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], 24000)
	binary.LittleEndian.PutUint32(data[28:], 48000)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], valueDataE4BFD03E)
	binary.LittleEndian.PutUint32(data[40:], 2)
	return data
}

func TestMiMoSpeechDispatchConvertsPromptAndReturnsAudioAsset(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(miMoWAVFixture())
	apiKey := t.Name()
	requests := make(chan map[string]interface{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "Bearer "+apiKey, r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var payload map[string]interface{}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		requests <- payload
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "mimo-response", "choices": []interface{}{map[string]interface{}{
				"finish_reason": valueStop1E96CF73, valueMessageF06D0A65: map[string]interface{}{
					valueRole01C0A112: valueAssistantCAAB15A3, valueContent03EE2C26: "", ContentPartAudio: map[string]interface{}{"id": "mimo-audio", valueDataE4BFD03E: encoded},
				},
			}}, "usage": map[string]interface{}{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
		})
	}))
	defer server.Close()
	options := map[string]interface{}{miMoVoiceOption: "茉莉", miniMaxFormatKey: miniMaxFormatWAV, "rate": 1.5, "model": "wrong", "messages": "wrong", valueStream1E52954F: true}
	output, err := newTestProviderClient().Generate(t.Context(), RouteConfig{
		Protocol: AdapterMiMoSpeech, BaseURL: server.URL + "/v1", APIKey: apiKey, UpstreamModel: miMoSpeechModel,
	}, GenerateInput{Messages: []Message{{Role: valueUserD301E989, Content: "你好，世界。"}}, Options: options})
	require.NoError(t, err)
	require.Len(t, output.GeneratedAssets, 1)
	asset := output.GeneratedAssets[0]
	assert.Equal(t, GeneratedAssetKindAudio, asset.Kind)
	assert.Equal(t, nativeMediaTestMIMEWAV, asset.MIMEType)
	assert.Equal(t, encoded, asset.B64JSON)
	assert.Equal(t, "mimo-audio", asset.ExternalID)
	assert.Equal(t, "mimo-response", output.ResponseID)
	assert.NotNil(t, output.Debug)
	payload := <-requests
	assert.Len(t, payload, 4)
	assert.Equal(t, miMoSpeechModel, payload["model"])
	assert.Equal(t, false, payload[valueStream1E52954F])
	assert.Equal(t, map[string]interface{}{miMoVoiceOption: "茉莉", miniMaxFormatKey: miniMaxFormatWAV}, asMap(payload[ContentPartAudio]))
	assert.Equal(t, []interface{}{map[string]interface{}{valueRole01C0A112: valueAssistantCAAB15A3, valueContent03EE2C26: "你好，世界。"}}, payload["messages"])
	assert.Equal(t, "茉莉", options[miMoVoiceOption])
	assert.Equal(t, true, options[valueStream1E52954F])
	assert.EqualValues(t, 12, output.Usage.InputTokens)
	assert.EqualValues(t, 7, output.Usage.OutputTokens)
}

func TestMiMoVoiceDesignAndCloneRequestSemantics(t *testing.T) {
	input := GenerateInput{
		Messages: []Message{{Role: valueUserD301E989, Content: "温暖沉稳的女声"}, {Role: valueAssistantCAAB15A3, Content: "欢迎收听。"}},
		Options:  map[string]interface{}{ContentPartAudio: map[string]interface{}{miniMaxFormatKey: miniMaxFormatMP3, miMoOptimizePreview: true}},
	}
	payload, err := buildMiMoSpeechRequest(miMoVoiceDesignModel, input)
	require.NoError(t, err)
	assert.NotContains(t, asMap(payload[ContentPartAudio]), miMoVoiceOption)
	messages, ok := payload["messages"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, messages, 2)
	assert.Equal(t, "温暖沉稳的女声", messages[0][valueContent03EE2C26])
	assert.Equal(t, "欢迎收听。", messages[1][valueContent03EE2C26])
	_, err = buildMiMoSpeechRequest(miMoVoiceDesignModel, GenerateInput{
		Instructions: "温暖沉稳的女声", Options: map[string]interface{}{ContentPartAudio: map[string]interface{}{miMoOptimizePreview: true}},
	})
	require.NoError(t, err)
	sample := base64.StdEncoding.EncodeToString(miMoWAVFixture())
	payload, err = buildMiMoSpeechRequest(miMoVoiceCloneModel, GenerateInput{
		Messages: []Message{{Role: valueUserD301E989, Content: "请朗读这句话。"}}, Options: map[string]interface{}{ContentPartAudio: map[string]interface{}{miMoVoiceOption: sample}},
	})
	require.NoError(t, err)
	assert.Equal(t, sample, asMap(payload[ContentPartAudio])[miMoVoiceOption])
	assert.Equal(t, miniMaxFormatWAV, asMap(payload[ContentPartAudio])[miniMaxFormatKey])
}

func TestMiMoSpeechRejectsInvalidInputBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		name, model string
		input       GenerateInput
	}{
		{"unknown model", "mimo-v2.5-pro", GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}}},
		{"empty prompt", miMoSpeechModel, GenerateInput{}},
		{"invalid voice", miMoSpeechModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{miMoVoiceOption: "alloy"}}},
		{"invalid audio object", miMoSpeechModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{ContentPartAudio: false}}},
		{"invalid format", miMoSpeechModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{miniMaxFormatKey: miMoTestInvalidFormat}}},
		{"design lacks style", miMoVoiceDesignModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}}},
		{"design rejects preset voice", miMoVoiceDesignModel, GenerateInput{Instructions: "warm voice", Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{miMoVoiceOption: "Mia"}}},
		{"clone lacks sample", miMoVoiceCloneModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}}},
		{"clone rejects nonaudio", miMoVoiceCloneModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{miMoVoiceOption: base64.StdEncoding.EncodeToString([]byte("not audio"))}}},
		{"regular model rejects optimization", miMoSpeechModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{ContentPartAudio: map[string]interface{}{miMoOptimizePreview: true}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newTestProviderClient().Generate(t.Context(), RouteConfig{Protocol: AdapterMiMoSpeech, BaseURL: "http://127.0.0.1:1", UpstreamModel: test.model}, test.input)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "connectex")
		})
	}
}

func TestMiMoSpeechAudioFormatsAndMalformedResponses(t *testing.T) {
	for _, test := range []struct {
		format, mime string
		data         []byte
	}{
		{miniMaxFormatWAV, nativeMediaTestMIMEWAV, miMoWAVFixture()},
		{miniMaxFormatMP3, miMoMIMEMP3, []byte("ID3\x04\x00\x00\x00\x00\x00\x00")},
		{miMoFormatPCM16, miMoMIMEPCM, []byte{0, 0}},
	} {
		body, err := json.Marshal(map[string]interface{}{"choices": []interface{}{map[string]interface{}{valueMessageF06D0A65: map[string]interface{}{
			ContentPartAudio: map[string]interface{}{valueDataE4BFD03E: base64.StdEncoding.EncodeToString(test.data)}, "final_text_preview": "优化后的播报文本",
		}}}})
		require.NoError(t, err)
		output, err := parseMiMoSpeechOutput(body, test.format)
		require.NoError(t, err)
		assert.Equal(t, test.mime, output.GeneratedAssets[0].MIMEType)
		assert.Equal(t, "优化后的播报文本", output.GeneratedAssets[0].RevisedPrompt)
	}
	for _, body := range []string{`broken`, `{}`, `{"choices":[]}`, `{"choices":[{"message":{"audio":{"data":"invalid-base64"}}}]}`, `{"choices":[{"message":{"audio":{"data":"aGVsbG8="}}}]}`} {
		_, err := parseMiMoSpeechOutput([]byte(body), miniMaxFormatWAV)
		require.ErrorIs(t, err, errMiMoSpeechResponse)
	}
	payload, err := buildMiMoSpeechRequest(miMoSpeechModel, GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}, Options: map[string]interface{}{miniMaxFormatKey: bailianFormatPCM}})
	require.NoError(t, err)
	assert.Equal(t, miMoFormatPCM16, asMap(payload[ContentPartAudio])[miniMaxFormatKey])
}

func TestMiMoSpeechHTTPErrorAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()
	route := RouteConfig{Protocol: AdapterMiMoSpeech, BaseURL: server.URL, UpstreamModel: miMoSpeechModel}
	input := GenerateInput{Messages: []Message{{Content: valueHello29F339F3}}}
	_, err := newTestProviderClient().Generate(t.Context(), route, input)
	require.Error(t, err)
	var upstream *UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
	assert.Contains(t, upstream.Message, "invalid api key")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = newTestProviderClient().Generate(ctx, route, input)
	require.ErrorIs(t, err, context.Canceled)
	_, err = newTestProviderClient().GenerateStream(t.Context(), route, input, nil)
	require.ErrorIs(t, err, ErrUnsupportedStream)
}

func TestMiMoSpeechListsModelsThroughOfficialOpenAIEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, http.MethodGet, r.Method)
		_, _ = w.Write([]byte(`{"data":[{"id":"mimo-v2.5-tts","owned_by":"xiaomi"},{"id":"mimo-v2.6-pro","owned_by":"xiaomi"}]}`))
	}))
	defer server.Close()
	models, err := newTestProviderClient().ListModels(t.Context(), RouteConfig{Protocol: AdapterMiMoSpeech, BaseURL: server.URL + "/v1"})
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, miMoSpeechModel, models[0].ID)
}
