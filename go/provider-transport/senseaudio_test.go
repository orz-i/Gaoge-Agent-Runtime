package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const senseAudioFixtureKey = "senseaudio-test-key"

func senseAudioFixtureRoute(server *httptest.Server, protocol string) RouteConfig {
	return RouteConfig{
		Protocol: protocol, BaseURL: server.URL + "/v1",
		APIKey: senseAudioFixtureKey, UpstreamModel: "fixture-model", ReadTimeoutMS: 1500,
	}
}

func senseAudioFixtureInput() GenerateInput {
	return GenerateInput{Messages: []Message{{Role: valueUser72795C11, Content: "Hello"}}, Options: map[string]interface{}{"poll_interval_ms": 1}}
}

func TestSenseAudioCatalogUsesAccountModelModes(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer "+senseAudioFixtureKey, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"text-1","mode":"llm","owned_by":"senseaudio","protocols":["http"]},{"id":"voice-1","mode":"tts","protocols":["http","websocket"]},{"id":"image-1","mode":"image","protocols":["http"]},{"id":"movie-1","mode":"video","protocols":["http"]},{"id":"song-1","mode":"music","protocols":["http"]},{"id":"stt-1","mode":"stt","protocols":["http"]},{"id":"effects-1","mode":"sound_effect","protocols":["http"]},{"id":"ws-1","mode":"llm","protocols":["websocket"]},{"id":"text-1","mode":"llm","protocols":["http"]},{"id":"bad-1","mode":"unknown","protocols":["http"]}]}`)
	}))
	defer server.Close()
	client := newTestProviderClient()
	route := senseAudioFixtureRoute(server, AdapterSenseAudioChat)
	models, err := client.ListModels(t.Context(), route)
	require.NoError(t, err)
	require.Len(t, models, 5)
	assert.Equal(t, int32(1), calls.Load())
	expected := []struct{ id, protocol, kinds string }{
		{"text-1", AdapterSenseAudioChat, `["chat"]`},
		{"voice-1", AdapterSenseAudioSpeech, `["audio_gen"]`},
		{"image-1", AdapterSenseAudioImage, `["image_gen","image_edit"]`},
		{"movie-1", AdapterSenseAudioVideo, `["video_gen"]`},
		{"song-1", AdapterSenseAudioMusic, `["music_gen"]`},
	}
	for i, want := range expected {
		assert.Equal(t, want.id, models[i].ID)
		assert.Equal(t, want.protocol, models[i].SuggestedProtocol)
		assert.JSONEq(t, want.kinds, models[i].KindsJSON)
		assert.Equal(t, "senseaudio", models[i].OwnedBy)
	}
}

func TestSenseAudioCatalogRejectsMalformedResponse(t *testing.T) {
	for _, response := range []string{`{"data":[]}`, `{"object":"list"}`, `{"object":"list","data":null}`} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, response)
			}))
			defer server.Close()
			_, err := newTestProviderClient().ListModels(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioChat))
			var upstream *UpstreamError
			require.ErrorAs(t, err, &upstream)
			assert.Contains(t, upstream.Message, errSenseAudioResponse.Error())
		})
	}
}

func TestSenseAudioChatAndStreamToolUsage(t *testing.T) {
	tools := []ToolDefinition{{Name: testInteractionToolName, Description: testInteractionToolName, InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)}}
	t.Run("nonstream", func(t *testing.T) {
		var request map[string]interface{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/chat/completions", r.URL.Path)
			assert.Equal(t, "Bearer "+senseAudioFixtureKey, r.Header.Get("Authorization"))
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			_, _ = io.WriteString(w, `{"id":"reply-1","choices":[{"message":{"content":"Answer","tool_calls":[{"id":"tool-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":6}}`)
		}))
		defer server.Close()
		input := senseAudioFixtureInput()
		input.Tools = tools
		input.Options["max_completion_tokens"] = 1024
		input.Options["max_output_tokens"] = 2048
		output, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioChat), input)
		require.NoError(t, err)
		assert.Equal(t, "Answer", output.Text)
		require.Len(t, output.ToolCalls, 1)
		assert.Equal(t, testInteractionToolName, output.ToolCalls[0].ToolName)
		assert.EqualValues(t, 9, output.Usage.InputTokens)
		assert.EqualValues(t, 6, output.Usage.OutputTokens)
		assert.Equal(t, "fixture-model", request["model"])
		assert.EqualValues(t, 1024, request["max_tokens"])
		assert.NotContains(t, request, "max_completion_tokens")
		assert.NotContains(t, request, "max_output_tokens")
		assert.NotEmpty(t, request["tools"])
	})
	t.Run("stream", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/chat/completions", r.URL.Path)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"id":"stream-1","choices":[{"delta":{"content":"Hello "}}]}

`)
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}

`)
			_, _ = io.WriteString(w, `data: [DONE]

`)
		}))
		defer server.Close()
		var events []string
		output, err := newTestProviderClient().GenerateStream(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioChat), senseAudioFixtureInput(), func(event GenerateStreamEvent) error {
			events = append(events, event.Delta)
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, "Hello world", output.Text)
		assert.EqualValues(t, 2, output.Usage.OutputTokens)
		assert.Equal(t, "Hello world", strings.Join(events, ""))
	})
}

func TestSenseAudioImageAndSpeechNativeWire(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/image/sync", r.URL.Path)
			assert.Equal(t, http.MethodPost, r.Method)
			var req map[string]interface{}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, "fixture-model", req["model"])
			assert.Equal(t, "1024x1024", req["size"])
			assert.Equal(t, "Hello", req["prompt"])
			_, _ = io.WriteString(w, `{"url":"https://assets.example/image.png"}`)
		}))
		defer server.Close()
		input := senseAudioFixtureInput()
		input.Options["size"] = "1024x1024"
		output, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioImage), input)
		require.NoError(t, err)
		require.Len(t, output.GeneratedImages, 1)
		assert.Equal(t, "https://assets.example/image.png", output.GeneratedImages[0].URL)
	})
	t.Run("speech", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/v1/t2a_v2", r.URL.Path)
			var req map[string]interface{}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, false, req["stream"])
			assert.Equal(t, "miao", asMap(req["voice_setting"])["voice_id"])
			assert.InDelta(t, 1.2, asMap(req["voice_setting"])["speed"], 0.000001)
			_, _ = io.WriteString(w, `{"trace_id":"speech-1","data":{"audio":"49443301"},"extra_info":{"audio_length":1500,"audio_format":"mp3","usage_characters":5},"base_resp":{"status_code":0}}`)
		}))
		defer server.Close()
		input := senseAudioFixtureInput()
		input.Options["voice"] = "miao"
		input.Options["speed"] = 1.2
		output, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioSpeech), input)
		require.NoError(t, err)
		require.Len(t, output.GeneratedAssets, 1)
		assert.Equal(t, "SUQzAQ==", output.GeneratedAssets[0].B64JSON)
		assert.Equal(t, "audio/mpeg", output.GeneratedAssets[0].MIMEType)
		assert.Contains(t, output.Usage.RawUsageJSON, "usage_characters")
		assert.NotContains(t, output.Debug.Response.Body, "49443301")
		assert.NotContains(t, output.RawJSON, "49443301")
	})
}

func TestSenseAudioVideoCreatesOnceAndPollsByQueryID(t *testing.T) {
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer "+senseAudioFixtureKey, r.Header.Get("Authorization"))
		if r.Method == http.MethodPost {
			posts.Add(1)
			assert.Equal(t, "/v1/video/create", r.URL.Path)
			var payload map[string]interface{}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			assert.Equal(t, "fixture-model", payload["model"])
			parts, ok := payload["content"].([]interface{})
			if assert.True(t, ok) && assert.NotEmpty(t, parts) {
				assert.Equal(t, "Hello", asMap(parts[0])["text"])
			}
			_, _ = io.WriteString(w, `{"task_id":"video-1"}`)
			return
		}
		gets.Add(1)
		assert.Equal(t, "/v1/video/status", r.URL.Path)
		assert.Equal(t, "video-1", r.URL.Query().Get("id"))
		if gets.Load() == 1 {
			_, _ = io.WriteString(w, `{"task_id":"video-1","status":"processing"}`)
		} else {
			_, _ = io.WriteString(w, `{"task_id":"video-1","status":"completed","duration":8,"video_url":"https://assets.example/video.mp4"}`)
		}
	}))
	defer server.Close()
	output, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioVideo), senseAudioFixtureInput())
	require.NoError(t, err)
	assert.Equal(t, int32(1), posts.Load())
	assert.Equal(t, int32(2), gets.Load())
	require.Len(t, output.GeneratedAssets, 1)
	assert.Equal(t, "video-1", output.GeneratedAssets[0].ExternalID)
	assert.Equal(t, "https://assets.example/video.mp4", output.GeneratedAssets[0].URL)
}

func TestSenseAudioMusicCreatesOnceAndPollsResults(t *testing.T) {
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			assert.Equal(t, "/v2/music/song/create", r.URL.Path)
			var payload map[string]interface{}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			assert.Equal(t, "sing", payload["mode"])
			assert.Equal(t, "Hello", payload["prompt"])
			_, _ = io.WriteString(w, `{"task_id":"music-1"}`)
		} else {
			gets.Add(1)
			assert.Equal(t, "/v1/music/song/pending/music-1", r.URL.Path)
			if gets.Load() == 1 {
				_, _ = io.WriteString(w, `{"task_id":"music-1","status":"PENDING"}`)
			} else {
				_, _ = io.WriteString(w, `{"task_id":"music-1","status":"SUCCESS","response":{"data":[{"id":"track-1","audio_url":"https://assets.example/music.mp3","duration":180}]}}`)
			}
		}
	}))
	defer server.Close()
	output, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioMusic), senseAudioFixtureInput())
	require.NoError(t, err)
	assert.Equal(t, int32(1), posts.Load())
	assert.Equal(t, int32(2), gets.Load())
	require.Len(t, output.GeneratedAssets, 1)
	assert.Equal(t, "audio/mpeg", output.GeneratedAssets[0].MIMEType)
	assert.Equal(t, "https://assets.example/music.mp3", output.GeneratedAssets[0].URL)
	assert.Equal(t, "music-1", output.ResponseID)
}

func TestSenseAudioAsyncFailuresAreNotReplayed(t *testing.T) {
	t.Run("query failure", func(t *testing.T) {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
				_, _ = io.WriteString(w, `{"task_id":"video-unknown"}`)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"upstream down"}`)
			}
		}))
		defer server.Close()
		_, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioVideo), senseAudioFixtureInput())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "video-unknown")
		var upstream *UpstreamError
		require.ErrorAs(t, err, &upstream)
		assert.Equal(t, http.StatusBadGateway, upstream.StatusCode)
		assert.Equal(t, int32(1), posts.Load())
	})
	t.Run("cancellation", func(t *testing.T) {
		var posts, gets atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				posts.Add(1)
				_, _ = io.WriteString(w, `{"task_id":"music-cancel"}`)
			} else {
				gets.Add(1)
			}
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		_, err := newTestProviderClient().Generate(ctx, senseAudioFixtureRoute(server, AdapterSenseAudioMusic), senseAudioFixtureInput())
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "music-cancel")
		assert.Equal(t, int32(1), posts.Load())
		assert.Equal(t, int32(0), gets.Load())
	})
	t.Run("paid redirect", func(t *testing.T) {
		var posts atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			http.Redirect(w, r, "/v1/video/create", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		_, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioVideo), senseAudioFixtureInput())
		require.Error(t, err)
		assert.Equal(t, int32(1), posts.Load())
	})
}

func TestSenseAudioMissingTaskIDIsUnknownNotRejected(t *testing.T) {
	for _, protocol := range []string{AdapterSenseAudioVideo, AdapterSenseAudioMusic} {
		t.Run(protocol, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			_, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, protocol), senseAudioFixtureInput())
			var upstream *UpstreamError
			require.ErrorAs(t, err, &upstream)
			assert.Equal(t, http.StatusBadGateway, upstream.StatusCode)
			assert.Contains(t, upstream.Message, "missing task_id")
			assert.EqualValues(t, 1, posts.Load(), "ambiguous paid creation must never be replayed")
		})
	}
}

func TestSenseAudioTerminalFailureVersusUnknownTask(t *testing.T) {
	for _, protocol := range []string{AdapterSenseAudioVideo, AdapterSenseAudioMusic} {
		t.Run(protocol, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					_, _ = io.WriteString(w, `{"task_id":"terminal-1"}`)
					return
				}
				_, _ = io.WriteString(w, `{"task_id":"terminal-1","status":"FAILED","fail_reason":"rejected"}`)
			}))
			defer server.Close()
			_, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, protocol), senseAudioFixtureInput())
			var upstream *UpstreamError
			require.ErrorAs(t, err, &upstream)
			assert.Equal(t, http.StatusUnprocessableEntity, upstream.StatusCode)
			assert.Contains(t, upstream.Message, "terminal-1")
			assert.EqualValues(t, 1, posts.Load())
		})
	}
}

func TestSenseAudioBusinessErrorAndInputGuards(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"audio":"49443301"},"base_resp":{"status_code":2049,"status_msg":"invalid key"}}`)
	}))
	defer server.Close()
	_, err := newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioSpeech), senseAudioFixtureInput())
	var upstream *UpstreamError
	require.ErrorAs(t, err, &upstream)
	assert.Contains(t, upstream.Message, "2049")
	assert.Equal(t, http.StatusUnauthorized, upstream.StatusCode)
	require.NotNil(t, upstream.Debug)
	assert.NotContains(t, upstream.Body, "49443301")
	assert.NotContains(t, upstream.Debug.Response.Body, "49443301")
	for _, protocol := range []string{AdapterSenseAudioImage, AdapterSenseAudioVideo, AdapterSenseAudioSpeech, AdapterSenseAudioMusic} {
		_, err := newTestProviderClient().GenerateStream(t.Context(), senseAudioFixtureRoute(server, protocol), senseAudioFixtureInput(), nil)
		require.ErrorIs(t, err, ErrUnsupportedStream)
	}
	input := senseAudioFixtureInput()
	input.ImageEditMask = &ContentPart{Kind: ContentPartImage, Data: []byte("invalid mask")}
	_, err = newTestProviderClient().Generate(t.Context(), senseAudioFixtureRoute(server, AdapterSenseAudioImage), input)
	require.ErrorIs(t, err, ErrUnsupportedImageMask)
	input = senseAudioFixtureInput()
	input.Messages = []Message{{Role: valueUser72795C11, Parts: []ContentPart{{Kind: ContentPartText, Text: "hi"}, {Kind: ContentPartImage, Data: []byte("first")}, {Kind: ContentPartImage, Data: []byte("second")}}}}
	_, err = senseAudioImageRequest("image-model", input)
	require.Error(t, err)
}
