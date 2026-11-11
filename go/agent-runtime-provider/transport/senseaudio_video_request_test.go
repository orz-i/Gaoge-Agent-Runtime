package transport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	senseAudioTestFlag   = "test"
	senseAudioTestLyrics = "lyrics"
)

func TestSenseAudioImageRequiresDocumentedSizeWithoutReference(t *testing.T) {
	input := senseAudioFixtureInput()
	for _, fixture := range []struct{ model, expected string }{
		{"senseaudio-image-2.0-260319", "1024x1024"},
		{"doubao-seedream-5-0-260128", "2048x2048"},
		{"sensenova-u1-fast", "2048x2048"},
	} {
		payload, err := senseAudioImageRequest(fixture.model, input)
		require.NoError(t, err)
		assert.Equal(t, fixture.expected, payload["size"])
	}
	_, err := senseAudioImageRequest("future-image-model", input)
	require.Error(t, err)
	input.Options["size"] = "1536x1024"
	payload, err := senseAudioImageRequest("future-image-model", input)
	require.NoError(t, err)
	assert.Equal(t, "1536x1024", payload["size"])
	delete(input.Options, "size")
	input.Options[senseAudioVideoReferenceRole] = "https://asset.example/reference.png"
	payload, err = senseAudioImageRequest("future-image-model", input)
	require.NoError(t, err)
	assert.Equal(t, "https://asset.example/reference.png", payload[senseAudioVideoReferenceRole])
}

func TestSenseAudioTTSRejectsBase64RatherThanGuessEncoding(t *testing.T) {
	_, err := senseAudioSpeechOutput(map[string]interface{}{"data": map[string]interface{}{"audio": "SUQzAQ=="}}, map[string]interface{}{"audio_setting": map[string]interface{}{miniMaxFormatKey: "mp3"}})
	require.Error(t, err)
}

func TestSenseAudioVideoDefaultsAndReferenceRoles(t *testing.T) {
	input := GenerateInput{
		Messages: []Message{{Role: valueUser72795C11, Parts: []ContentPart{{Kind: ContentPartText, Text: "Animate a scene"}, {Kind: ContentPartImage, MimeType: valueImagePng415383B9, Data: []byte{1, 2, 3}}, {Kind: ContentPartImage, MimeType: valueImagePng415383B9, Data: []byte{4, 5, 6}}, {Kind: ContentPartImage, MimeType: valueImagePng415383B9, Data: []byte{7, 8, 9}}}}},
		Options:  map[string]interface{}{},
	}
	payload, err := senseAudioVideoRequest("doubao-seedance-2-0-260128", input)
	require.NoError(t, err)
	assert.EqualValues(t, 10, payload["duration"])
	assert.Equal(t, "720p", payload["resolution"])
	assert.Equal(t, "16:9", payload["ratio"])
	parts, ok := payload["content"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, parts, 4)
	for _, part := range parts[1:] {
		assert.Equal(t, senseAudioVideoReferenceRole, part["role"])
	}
	input.Options["image_roles"] = []string{miniMaxRoleFirstFrame, miniMaxRoleLastFrame, senseAudioVideoReferenceRole}
	_, err = senseAudioVideoRequest("doubao-seedance-2-0-260128", input)
	require.Error(t, err, "mixed frame and reference modes must fail")
	for _, roles := range [][]string{
		{miniMaxRoleLastFrame, senseAudioVideoReferenceRole, senseAudioVideoReferenceRole},
		{miniMaxRoleFirstFrame, miniMaxRoleFirstFrame, miniMaxRoleFirstFrame},
		{miniMaxRoleLastFrame, miniMaxRoleLastFrame, miniMaxRoleLastFrame},
	} {
		input.Options["image_roles"] = roles
		_, err = senseAudioVideoRequest("doubao-seedance-2-0-260128", input)
		require.Error(t, err, "invalid/duplicate frame roles must fail")
	}
	input.Options["image_roles"] = []string{senseAudioVideoReferenceRole, senseAudioVideoReferenceRole, senseAudioVideoReferenceRole}
	input.Options["duration"] = 5
	input.Options["resolution"] = "480p"
	input.Options["aspect_ratio"] = "9:16"
	payload, err = senseAudioVideoRequest("doubao-seedance-2-0-260128", input)
	require.NoError(t, err)
	assert.Equal(t, 5, payload["duration"])
	assert.Equal(t, "480p", payload["resolution"])
	assert.Equal(t, "9:16", payload["ratio"])
}

func TestSenseAudioMusicV2Fields(t *testing.T) {
	input := senseAudioFixtureInput()
	input.Options["style"] = "jazz"
	input.Options["mode"] = "lyrics_to_instrumental"
	input.Options["model_settings"] = map[string]interface{}{senseAudioTestFlag: true}
	input.Options["music_settings"] = map[string]interface{}{senseAudioTestFlag: true}
	input.Options["audio_settings"] = map[string]interface{}{"format": "wav"}
	payload, err := senseAudioMusicRequest("music-model", input)
	require.NoError(t, err)
	assert.Equal(t, "lyrics_to_instrumental", payload["mode"])
	assert.Equal(t, "jazz", payload["style"])
	assert.Equal(t, "wav", asMap(payload["audio_settings"])["format"])
	assert.NotNil(t, payload["model_settings"])
	assert.NotNil(t, payload["music_settings"])
	input.Options[senseAudioTestLyrics] = senseAudioTestLyrics
	input.Options["prebuilt_lyrics"] = map[string]interface{}{"id": "from-preview"}
	_, err = senseAudioMusicRequest("music-model", input)
	require.Error(t, err)
}
