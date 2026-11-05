package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestAnthropicEffortAndStrictOutputShareOneConfig(t *testing.T) {
	physical := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical++
		var payload struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
				Format struct {
					Type string `json:"type"`
				} `json:"format"`
			} `json:"output_config"`
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "claude-sonnet-5-5" || payload.OutputConfig.Effort != "medium" ||
			payload.OutputConfig.Format.Type != "json_schema" {
			t.Errorf("output_config effort lost strict format: %+v", payload)
		}
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, event := range []struct{ name, data string }{
				{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"effort-stream","usage":{"input_tokens":3}}}`},
				{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
				{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"ok\":true}"}}`},
				{"content_block_stop", `{"type":"content_block_stop","index":0}`},
				{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`},
				{"message_stop", `{"type":"message_stop"}`},
			} {
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data)
			}
		} else {
			_, _ = fmt.Fprint(w, `{"type":"message","role":"assistant","id":"effort-unary","stop_reason":"end_turn","content":[{"type":"text","text":"{\"ok\":true}"}],"usage":{"input_tokens":3,"output_tokens":4}}`)
		}
	}))
	defer server.Close()
	client, err := anthropic.New(anthropic.Config{APIKey: "secret", Endpoint: server.URL, DefaultModel: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := client.Capabilities(context.Background(), "claude-sonnet-5-5")
	if err != nil || !slices.Contains(caps.EffortLevels, "medium") {
		t.Fatalf("caps: %+v %v", caps, err)
	}
	req := model.Request{
		Model: "claude-sonnet-5-5", ModelOptions: json.RawMessage(`{"reasoning":{"effort":"medium"}}`),
		Messages:       []model.Message{{Role: model.RoleUser, Content: model.TextParts("return ok")}},
		OutputContract: &model.OutputContract{Name: "effort_probe", Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)},
	}
	if response, err := client.Generate(context.Background(), req); err != nil || response.ResponseID != "effort-unary" {
		t.Fatalf("unary %+v %v", response, err)
	}
	if response, err := client.GenerateStream(context.Background(), req, nil); err != nil || response.ResponseID != "effort-stream" {
		t.Fatalf("stream %+v %v", response, err)
	}
	for _, tc := range []struct{ name, modelID, opts string }{
		{"unknown-model", "claude-sonnet-5-5-proxy", `{"reasoning":{"effort":"high"}}`},
		{"unsupported-legacy", "claude-sonnet-4-5", `{"reasoning":{"effort":"low"}}`},
		{"none", "claude-sonnet-5-5", `{"reasoning":{"effort":"none"}}`},
		{"duplicate", "claude-sonnet-5-5", `{"reasoning":{"effort":"low","effort":"high"}}`},
		{"injection", "claude-sonnet-5-5", `{"reasoning":{"effort":"high"},"thinking":{"type":"disabled"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := req
			bad.Model = tc.modelID
			bad.ModelOptions = json.RawMessage(tc.opts)
			if _, err := client.Generate(context.Background(), bad); err == nil {
				t.Fatal("unverified effort or extra wire options accepted")
			}
		})
	}
	if physical != 2 {
		t.Fatalf("unexpected physical requests: %d", physical)
	}
}

func TestAnthropicEffortPolicyFingerprintAndIsolation(t *testing.T) {
	levels := map[string][]string{"claude-private": {"high", "low"}}
	a, err := anthropic.New(anthropic.Config{APIKey: "secret", EffortModelLevels: levels})
	if err != nil {
		t.Fatal(err)
	}
	b, err := anthropic.New(anthropic.Config{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if a.BindingFingerprintForRequest(model.Request{}) == b.BindingFingerprintForRequest(model.Request{}) {
		t.Fatal("effort Host opt-in omitted from bound provider identity")
	}
	levels["claude-private"][0] = "max"
	caps, err := a.Capabilities(context.Background(), "claude-private")
	if err != nil || !slices.Equal(caps.EffortLevels, []string{"high", "low"}) {
		t.Fatalf("mutable host config changed existing binding: %+v %v", caps, err)
	}
	for _, input := range []map[string][]string{
		{"claude-private": {"none"}}, {"claude-private": {"low", "low"}}, {" claude-private": {"high"}},
	} {
		if _, err := anthropic.New(anthropic.Config{APIKey: "secret", EffortModelLevels: input}); err == nil {
			t.Fatalf("invalid policy accepted: %+v", input)
		}
	}
}
