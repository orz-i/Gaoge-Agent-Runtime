package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSenseAudioUnaryCreationAndQueryOnlyResume(t *testing.T) {
	for _, scenario := range []struct {
		protocol string
		taskID   string
		pollBody string
	}{
		{AdapterSenseAudioVideo, "video-checkpoint-1", `{"task_id":"video-checkpoint-1","status":"completed","video_url":"https://assets.example/recover.mp4"}`},
		{AdapterSenseAudioMusic, "music-checkpoint-1", `{"task_id":"music-checkpoint-1","status":"SUCCESS","response":{"data":[{"audio_url":"https://assets.example/recover.mp3"}]}}`},
	} {
		t.Run(scenario.protocol, func(t *testing.T) {
			var posts, gets atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					_, _ = io.WriteString(w, `{"task_id":"`+scenario.taskID+`"}`)
					return
				}
				gets.Add(1)
				_, _ = io.WriteString(w, scenario.pollBody)
			}))
			defer server.Close()
			adapter := &senseAudioAdapter{client: newTestProviderClient(), protocol: scenario.protocol}
			input := senseAudioFixtureInput()
			input.CreateMediaTaskOnly = true
			created, err := adapter.Generate(t.Context(), senseAudioFixtureRoute(server, scenario.protocol), input)
			require.NoError(t, err)
			require.Equal(t, scenario.taskID, created.ResponseID)
			require.Empty(t, created.GeneratedAssets, "unary create cannot already carry completed media")
			require.EqualValues(t, 1, posts.Load())
			require.Zero(t, gets.Load(), "querying before the host's durable CAS is forbidden")
			// The host persists created.ResponseID before it starts the second RPC.
			input.CreateMediaTaskOnly = false
			input.ResumeMediaTaskID = created.ResponseID
			resumed, err := adapter.GenerateStream(t.Context(), senseAudioFixtureRoute(server, scenario.protocol), input, nil)
			require.NoError(t, err)
			require.Equal(t, created.ResponseID, resumed.ResponseID)
			require.EqualValues(t, 1, posts.Load(), "resume must never generate a second paid task")
			require.EqualValues(t, 1, gets.Load())
		})
	}
}

func TestSenseAudioFailedCheckpointCannotQueryOrCreateThroughStreaming(t *testing.T) {
	var posts, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts.Add(1)
			_, _ = io.WriteString(w, `{"task_id":"video-unknown-1"}`)
			return
		}
		gets.Add(1)
	}))
	defer server.Close()
	adapter := &senseAudioAdapter{client: newTestProviderClient(), protocol: AdapterSenseAudioVideo}
	input := senseAudioFixtureInput()
	input.CreateMediaTaskOnly = true
	created, err := adapter.Generate(context.Background(), senseAudioFixtureRoute(server, AdapterSenseAudioVideo), input)
	require.NoError(t, err)
	require.Equal(t, "video-unknown-1", created.ResponseID)
	require.Zero(t, gets.Load())
	// Simulate a crash or failed Conversation CAS. No query may start until the
	// durable caller explicitly supplies the checkpointed task ID.
	input.CreateMediaTaskOnly = false
	_, err = adapter.GenerateStream(context.Background(), senseAudioFixtureRoute(server, AdapterSenseAudioVideo), input, nil)
	require.Error(t, err)
	require.EqualValues(t, 1, posts.Load())
	require.Zero(t, gets.Load())
}

func TestSenseAudioDefaultEndpointsAfterMasterAdapterMapMerge(t *testing.T) {
	for _, scenario := range []struct {
		protocol string
		endpoint string
	}{
		{AdapterSenseAudioChat, EndpointChatCompletions},
		{AdapterSenseAudioImage, EndpointImageGenerations},
		{AdapterSenseAudioVideo, EndpointVideoGenerations},
		{AdapterSenseAudioSpeech, EndpointSpeechGenerations},
		{AdapterSenseAudioMusic, EndpointMusicGenerations},
	} {
		t.Run(scenario.protocol, func(t *testing.T) {
			require.Equal(t, scenario.endpoint, DefaultEndpointForAdapter(scenario.protocol))
			require.True(t, IsImplementedAdapter(scenario.protocol))
		})
	}
}
