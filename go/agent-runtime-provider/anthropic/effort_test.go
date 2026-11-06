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
	client, err := anthropic.New(anthropic.Config{APIKey: "secret", Endpoint: server.URL, DefaultModel: "claude-sonnet-5-5", ModelProfiles: fixtureAnthropicProfiles()})
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
	sonnet46, capErr := client.Capabilities(context.Background(), "claude-sonnet-4-6")
	if capErr != nil || slices.Contains(sonnet46.EffortLevels, "xhigh") || !slices.Contains(sonnet46.EffortLevels, "max") {
		t.Fatalf("Sonnet 4.6 supports max but not xhigh: %+v %v", sonnet46, capErr)
	}
	for _, tc := range []struct{ name, modelID, opts string }{
		{"unknown-model", "claude-sonnet-5-5-proxy", `{"reasoning":{"effort":"high"}}`},
		{"unsupported-legacy", "claude-sonnet-4-5", `{"reasoning":{"effort":"low"}}`},
		{"unsupported-sonnet46-xhigh", "claude-sonnet-4-6", `{"reasoning":{"effort":"xhigh"}}`},
		{"none", "claude-sonnet-5-5", `{"reasoning":{"effort":"none"}}`},
		{"duplicate", "claude-sonnet-5-5", `{"reasoning":{"effort":"low","effort":"high"}}`},
		{"injection", "claude-sonnet-5-5", `{"reasoning":{"effort":"high"},"thinking":{"type":"disabled"}}`},
		{"high-temperature", "claude-sonnet-5-5", `{"reasoning":{"effort":"high"},"temperature":0.5}`},
		{"high-top-p", "claude-fable-5", `{"reasoning":{"effort":"medium"},"top_p":0.5}`},
		{"default-temperature", "claude-sonnet-5-5", `{"temperature":0.5}`},
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

func TestAnthropicImportedProfileFingerprintAndIsolation(t *testing.T) {
	source := model.StaticModelProfiles{"claude-private": {StructuredJSONSchema: true, EffortLevels: []string{"high", "low"}, SupportsSamplingWhenEffortOmitted: true}}
	a, err := anthropic.New(anthropic.Config{APIKey: "secret", ModelProfiles: source})
	if err != nil {
		t.Fatal(err)
	}
	b, err := anthropic.New(anthropic.Config{APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if a.BindingFingerprintForRequest(model.Request{Model: "claude-private"}) == b.BindingFingerprintForRequest(model.Request{Model: "claude-private"}) {
		t.Fatal("external profile missing from Provider HMAC")
	}
	profile := source["claude-private"]
	profile.EffortLevels[0] = "max"
	profile.StructuredJSONSchema = false
	source["claude-private"] = profile
	caps, err := a.Capabilities(t.Context(), "claude-private")
	if err != nil || !slices.Equal(caps.EffortLevels, []string{"high", "low"}) || !caps.StructuredJSONSchema {
		t.Fatalf("mutable source rewrote frozen provider policy: %+v %v", caps, err)
	}
	for _, bad := range []model.StaticModelProfiles{
		{"claude-private": {EffortLevels: []string{"none"}}},
		{"claude-private": {EffortLevels: []string{"low", "low"}}},
		{" claude-private": {EffortLevels: []string{"high"}}},
	} {
		if _, err := anthropic.New(anthropic.Config{APIKey: "secret", ModelProfiles: bad}); err == nil {
			t.Fatalf("invalid profile accepted: %+v", bad)
		}
	}
	if _, err := anthropic.New(anthropic.Config{APIKey: "secret", ModelProfiles: model.StaticModelProfiles{"claude-sonnet-5-5": {EffortLevels: []string{"low"}}}}); err != nil {
		t.Fatalf("Host model snapshot unexpectedly blocked by SDK model list: %v", err)
	}
}
