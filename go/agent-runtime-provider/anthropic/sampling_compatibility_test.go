package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAnthropicSamplingCompatibilityBeforeUnaryAndStream(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if string(payload["temperature"]) != "0.5" || string(payload["model"]) != `"gateway-legacy"` {
			t.Errorf("legacy gateway sampling incorrectly altered: %s", payload)
		}
		var streaming bool
		_ = json.Unmarshal(payload["stream"], &streaming)
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, item := range []struct{ event, data string }{
				{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"sampling-stream","usage":{"input_tokens":1}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", item.event, item.data)
			}
			return
		}
		_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"sampling-unary","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client, err := anthropic.New(anthropic.Config{APIKey: "synthetic-key", Endpoint: server.URL, ModelProfiles: fixtureAnthropicProfiles()})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Model: "gateway-legacy",
		ModelOptions: json.RawMessage(`{"temperature":0.5}`),
		Messages:     []model.Message{{Role: model.RoleUser, Content: model.TextParts("hi")}},
	}
	if response, err := client.Generate(context.Background(), request); err != nil || response.ResponseID != "sampling-unary" {
		t.Fatalf("old gateway unary fallback rejected: %+v %v", response, err)
	}
	if response, err := client.GenerateStream(context.Background(), request, nil); err != nil || response.ResponseID != "sampling-stream" {
		t.Fatalf("old gateway stream fallback rejected: %+v %v", response, err)
	}
	before := physical.Load()
	for _, invalid := range []struct{ modelID, options string }{
		{"claude-sonnet-5-5", `{"reasoning":{"effort":"high"},"temperature":0.5}`},
		{"claude-fable-5", `{"reasoning":{"effort":"medium"},"top_p":0.5}`},
		{"claude-sonnet-4-6", `{"reasoning":{"effort":"medium"},"temperature":0.5}`},
		{"claude-sonnet-5-5", `{"temperature":0.5}`}, // model rejects sampling even when effort is omitted
		{"claude-opus-5-5", `{"top_p":0.5}`},
	} {
		request.Model, request.ModelOptions = invalid.modelID, json.RawMessage(invalid.options)
		if _, err := client.Generate(context.Background(), request); !errors.Is(err, anthropic.ErrUnsupported) {
			t.Errorf("bad unary %s %s: %v", invalid.modelID, invalid.options, err)
		}
		if _, err := client.GenerateStream(context.Background(), request, nil); !errors.Is(err, anthropic.ErrUnsupported) {
			t.Errorf("bad stream %s %s: %v", invalid.modelID, invalid.options, err)
		}
	}
	if physical.Load() != before {
		t.Fatalf("invalid combinations generated %d physical requests", physical.Load()-before)
	}
}
