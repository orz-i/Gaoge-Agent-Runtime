package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestMythosPreviewEffortMaxWithoutXhighBeforeUnaryAndSSE(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var payload struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "claude-mythos-preview" || payload.OutputConfig.Effort != "max" {
			t.Errorf("expected exact Mythos Preview effort projection, got %+v", payload)
		}
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, item := range []struct{ event, data string }{
				{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"mythos-stream","usage":{"input_tokens":2}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"ok"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", item.event, item.data)
			}
			return
		}
		_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"mythos-unary","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	defer server.Close()
	client, err := anthropic.New(anthropic.Config{Endpoint: server.URL, APIKey: "synthetic-key"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := client.Capabilities(t.Context(), "claude-mythos-preview")
	if err != nil || !slices.Equal(caps.EffortLevels, []string{"low", "medium", "high", "max"}) {
		t.Fatalf("unsupported or invented public effort level: %+v err=%v", caps, err)
	}
	request := model.Request{
		Model:        "claude-mythos-preview",
		ModelOptions: json.RawMessage(`{"reasoning":{"effort":"max"}}`),
		Messages:     []model.Message{{Role: model.RoleUser, Content: model.TextParts("hi")}},
	}
	result, err := client.Generate(t.Context(), request)
	if err != nil || result.ResponseID != "mythos-unary" {
		t.Fatalf("Mythos Preview unary effort failed: %+v %v", result, err)
	}
	result, err = client.GenerateStream(context.Background(), request, nil)
	if err != nil || result.ResponseID != "mythos-stream" {
		t.Fatalf("Mythos Preview SSE effort failed: %+v %v", result, err)
	}
	before := physical.Load()
	for _, bad := range []string{
		`{"reasoning":{"effort":"xhigh"}}`,
		`{"reasoning":{"effort":"max"},"temperature":0.5}`,
		`{"temperature":0.5}`,
	} {
		request.ModelOptions = json.RawMessage(bad)
		if _, err := client.Generate(t.Context(), request); !errors.Is(err, anthropic.ErrUnsupported) {
			t.Errorf("invalid request accepted in unary: %s err=%v", bad, err)
		}
		if _, err := client.GenerateStream(t.Context(), request, nil); !errors.Is(err, anthropic.ErrUnsupported) {
			t.Errorf("invalid request accepted in SSE: %s err=%v", bad, err)
		}
	}
	if physical.Load() != before {
		t.Fatalf("unsupported Mythos Preview settings issued %d physical requests", physical.Load()-before)
	}
}
