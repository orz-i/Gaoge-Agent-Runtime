package openai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestExactModelEffortProjectedBeforePhysicalOpenAI(t *testing.T) {
	physical := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical++
		var payload struct {
			Model     string `json:"model"`
			Store     bool   `json:"store"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Temperature *float64 `json:"temperature"`
			Stream      bool     `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "gpt-6-sol" || payload.Store || payload.Reasoning.Effort != "high" || payload.Temperature != nil {
			t.Errorf("reasoning projection: %+v", payload)
		}
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"effort-stream\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
		} else {
			_, _ = fmt.Fprint(w, `{"object":"response","id":"effort-unary","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":3,"output_tokens":1}}`)
		}
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "secret", Endpoint: server.URL, DefaultModel: "gpt-6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err := client.Capabilities(context.Background(), "gpt-6-sol")
	if err != nil || !slices.Contains(caps.EffortLevels, "high") {
		t.Fatalf("caps %+v %v", caps, err)
	}
	base := model.Request{
		Model: "gpt-6-sol", ModelOptions: json.RawMessage(`{"reasoning":{"effort":"high"}}`),
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hi")}},
	}
	if result, err := client.Generate(context.Background(), base); err != nil || result.ResponseID != "effort-unary" {
		t.Fatalf("unary %+v %v", result, err)
	}
	if result, err := client.GenerateStream(context.Background(), base, nil); err != nil || result.ResponseID != "effort-stream" {
		t.Fatalf("stream %+v %v", result, err)
	}
	for _, test := range []struct{ name, modelID, options string }{
		{"unknown", "gpt-6-sol-private", `{"reasoning":{"effort":"high"}}`},
		{"unsupported-level", "gpt-6-astra", `{"reasoning":{"effort":"none"}}`},
		{"duplicate", "gpt-6-sol", `{"reasoning":{"effort":"high","effort":"low"}}`},
		{"injection", "gpt-6-sol", `{"reasoning":{"effort":"high"},"tools":[]}`},
		{"malformed", "gpt-6-sol", `{"reasoning":null}`},
		{"high-temperature", "gpt-6-sol", `{"reasoning":{"effort":"high"},"temperature":0.5}`},
		{"high-top-p", "gpt-6-sol", `{"reasoning":{"effort":"high"},"top_p":0.5}`},
		{"default-high-temperature", "gpt-6-sol", `{"temperature":0.5}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := base
			bad.Model = test.modelID
			bad.ModelOptions = json.RawMessage(test.options)
			if _, err := client.Generate(context.Background(), bad); err == nil {
				t.Fatal("accepted invalid effort")
			}
		})
	}
	if physical != 2 {
		t.Fatalf("invalid request crossed physical boundary: %d calls", physical)
	}
	unknown, err := client.Capabilities(context.Background(), "gpt-6-sol-private")
	if err != nil || len(unknown.EffortLevels) != 0 {
		t.Fatalf("invented capability: %+v %v", unknown, err)
	}
}

func TestOpenAIEffortOverrideIsBoundAndImmutable(t *testing.T) {
	overrides := map[string][]string{"proxy-private": {"medium", "high"}}
	client, err := openai.New(openai.Config{APIKey: "secret", DefaultModel: "proxy-private", EffortModelLevels: overrides})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := openai.New(openai.Config{APIKey: "secret", DefaultModel: "proxy-private"})
	if err != nil {
		t.Fatal(err)
	}
	if client.BindingFingerprintForRequest(model.Request{}) == baseline.BindingFingerprintForRequest(model.Request{}) {
		t.Fatal("certified effort policy missing from immutable provider identity")
	}
	overrides["proxy-private"][0] = "low"
	caps, err := client.Capabilities(context.Background(), "proxy-private")
	if err != nil || !slices.Equal(caps.EffortLevels, []string{"high", "medium"}) {
		t.Fatalf("caller mutated verified capability: %+v %v", caps, err)
	}
	if _, err := openai.New(openai.Config{APIKey: "secret", EffortModelLevels: map[string][]string{"private": {"high", "high"}}}); err == nil {
		t.Fatal("invalid duplicated levels accepted")
	}
	if _, err := openai.New(openai.Config{APIKey: "secret", EffortModelLevels: map[string][]string{"private": {"ultra"}}}); err == nil {
		t.Fatal("unknown level accepted")
	}
	if _, err := openai.New(openai.Config{APIKey: "secret", EffortModelLevels: map[string][]string{"private": {"high"}}}); err != nil {
		t.Fatalf("valid certified config rejected: %v", err)
	}
	if _, err := openai.New(openai.Config{APIKey: "secret", EffortModelLevels: map[string][]string{"gpt-6-astra": {"none"}}}); err == nil {
		t.Fatal("Host override re-enabled unsupported public Astra effort")
	}
}
