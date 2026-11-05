package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestOpenAIReasoningSamplingCombinationsBeforeUnaryAndStream(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		var effort struct {
			Effort string `json:"effort"`
		}
		_ = json.Unmarshal(payload["reasoning"], &effort)
		var name string
		_ = json.Unmarshal(payload["model"], &name)
		switch name {
		case "gpt-6-sol":
			if effort.Effort != "none" || string(payload["temperature"]) != "0.5" {
				t.Errorf("none-effort sampling lost: %s", payload)
			}
		case "gpt-4.1":
			if effort.Effort != "" || string(payload["top_p"]) != "0.5" {
				t.Errorf("legacy nonreasoning sampling altered: %s", payload)
			}
		default:
			t.Errorf("unexpected model crossed request boundary: %s", name)
		}
		var streaming bool
		_ = json.Unmarshal(payload["stream"], &streaming)
		if streaming {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"sample-stream\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"response","id":"sample-unary","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "synthetic-key", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hi")}}}
	for _, valid := range []struct{ modelID, options string }{
		{"gpt-6-sol", `{"reasoning":{"effort":"none"},"temperature":0.5}`},
		{"gpt-4.1", `{"top_p":0.5}`},
	} {
		request.Model, request.ModelOptions = valid.modelID, json.RawMessage(valid.options)
		if response, err := client.Generate(context.Background(), request); err != nil || response.ResponseID != "sample-unary" {
			t.Fatalf("valid unary rejected: %+v %v", response, err)
		}
		if response, err := client.GenerateStream(context.Background(), request, nil); err != nil || response.ResponseID != "sample-stream" {
			t.Fatalf("valid stream rejected: %+v %v", response, err)
		}
	}
	before := physical.Load()
	for _, invalid := range []struct{ modelID, options string }{
		{"gpt-6-sol", `{"reasoning":{"effort":"high"},"temperature":0.5}`},
		{"gpt-6-sol", `{"reasoning":{"effort":"high"},"top_p":0.5}`},
		{"gpt-6-sol", `{"temperature":0.5}`}, // omitted effort defaults non-none
		{"gpt-6-astra", `{"reasoning":{"effort":"none"}}`},
		{"gpt-6.1-sol", `{"top_p":0.5}`},
	} {
		request.Model, request.ModelOptions = invalid.modelID, json.RawMessage(invalid.options)
		if _, err := client.Generate(context.Background(), request); !errors.Is(err, openai.ErrUnsupported) {
			t.Errorf("bad unary %s %s: %v", invalid.modelID, invalid.options, err)
		}
		if _, err := client.GenerateStream(context.Background(), request, nil); !errors.Is(err, openai.ErrUnsupported) {
			t.Errorf("bad stream %s %s: %v", invalid.modelID, invalid.options, err)
		}
	}
	if physical.Load() != before {
		t.Fatalf("invalid combinations generated %d paid physical requests", physical.Load()-before)
	}
}
