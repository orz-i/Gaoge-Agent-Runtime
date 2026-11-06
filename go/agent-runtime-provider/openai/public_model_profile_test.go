package openai_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// This fixture is imported by Host test code; the SDK contains no catalog
// entries for public or private model IDs.
func TestExternallyImportedOpenAIModelProfilesAndSnapshotIsolation(t *testing.T) {
	external := fixtureOpenAIProfiles()
	c, err := openai.New(openai.Config{APIKey: "synthetic-key", ModelProfiles: external})
	if err != nil {
		t.Fatal(err)
	}
	without, err := openai.New(openai.Config{APIKey: "synthetic-key"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gpt-6.1-terra", "gpt-6-sol", "gpt-5.6-sol", "gpt-4.1", "gpt-6-astra"} {
		want := external[id]
		want.EffortLevels = slices.Clone(want.EffortLevels)
		slices.Sort(want.EffortLevels)
		got, err := c.Capabilities(t.Context(), id)
		if err != nil || got.StructuredJSONSchema != want.StructuredJSONSchema || !slices.Equal(got.EffortLevels, want.EffortLevels) || got.DisallowSamplingWhenEffortOmitted != want.DisallowSamplingWhenEffortOmitted {
			t.Fatalf("Host profile for %s changed: %+v wanted %+v err=%v", id, got, want, err)
		}
		unconfigured, err := without.Capabilities(t.Context(), id)
		if err != nil || unconfigured.StructuredJSONSchema || len(unconfigured.EffortLevels) != 0 || unconfigured.DisallowSamplingWhenEffortOmitted {
			t.Fatalf("SDK invented built-in model authority for %s: %+v %v", id, unconfigured, err)
		}
	}
	original, _ := c.Capabilities(t.Context(), "gpt-6.1-terra")
	// Host can replace its external source while a previous adapter keeps
	// the validated snapshot. No model list or global cache in the SDK.
	p := external["gpt-6.1-terra"]
	p.EffortLevels[0] = "minimal"
	p.StructuredJSONSchema = false
	external["gpt-6.1-terra"] = p
	after, _ := c.Capabilities(t.Context(), "gpt-6.1-terra")
	if !slices.Equal(after.EffortLevels, original.EffortLevels) || !after.StructuredJSONSchema {
		t.Fatalf("mutable source changed a frozen Provider: %+v", after)
	}
	after.EffortLevels[0] = "minimal"
	onceMore, _ := c.Capabilities(t.Context(), "gpt-6.1-terra")
	if !slices.Equal(onceMore.EffortLevels, original.EffortLevels) {
		t.Fatalf("mutable returned capabilities changed Provider: %+v", onceMore)
	}
	// New model names can be added entirely by the Host; changing metadata
	// must rotate original Provider binding. No SDK redeploy is involved.
	hostFuture := model.StaticModelProfiles{"future-externally-discovered": {StructuredJSONSchema: true, EffortLevels: []string{"high"}}}
	future, err := openai.New(openai.Config{APIKey: "synthetic-key", ModelProfiles: hostFuture})
	if err != nil {
		t.Fatal(err)
	}
	if newCaps, _ := future.Capabilities(t.Context(), "future-externally-discovered"); !newCaps.StructuredJSONSchema || !slices.Equal(newCaps.EffortLevels, []string{"high"}) {
		t.Fatalf("external model not importable: %+v", newCaps)
	}
	futureRequest := model.Request{Model: "future-externally-discovered"}
	if future.BindingFingerprintForRequest(futureRequest) == c.BindingFingerprintForRequest(futureRequest) ||
		future.BindingFingerprintForRequest(futureRequest) == without.BindingFingerprintForRequest(futureRequest) {
		t.Fatal("selected Host model capability was not bound to the original Provider")
	}
	// Updating a different model in the external catalog may not prevent
	// an existing pending ModelInvocation for this selected model resuming.
	unchangedRequest := model.Request{Model: "gpt-6-sol"}
	sameOwner := c.BindingFingerprintForRequest(unchangedRequest)
	expanded := fixtureOpenAIProfiles()
	expanded["future-externally-discovered"] = model.Capabilities{StructuredJSONSchema: true}
	newClient, err := openai.New(openai.Config{APIKey: "synthetic-key", ModelProfiles: expanded})
	if err != nil || sameOwner != newClient.BindingFingerprintForRequest(unchangedRequest) {
		t.Fatalf("unrelated model discovery revoked original pending execution: %v", err)
	}
}

// The previously unlisted public Terra model must gate its default non-none
// reasoning before *either* physical HTTP transport. Explicit none can still
// preserve temperature/top_p; strict output is now usable without Host opt-in.
func TestPublicGPT61TerraUnaryAndSSECompatibilityMatrix(t *testing.T) {
	var physical atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		physical.Add(1)
		var p struct {
			Model     string `json:"model"`
			Store     bool   `json:"store"`
			Stream    bool   `json:"stream"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
			Temperature *float64 `json:"temperature"`
			TopP        *float64 `json:"top_p"`
			Text        struct {
				Format struct {
					Type   string `json:"type"`
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		if p.Model != "gpt-6.1-terra" || p.Store {
			t.Errorf("wrong outbound OpenAI request owner: %+v", p)
		}
		switch p.Reasoning.Effort {
		case "none":
			if p.Temperature == nil || *p.Temperature != 0.5 || p.TopP != nil {
				t.Errorf("none-effort valid sampling lost: %+v", p)
			}
		case "high":
			if p.Temperature != nil || p.TopP != nil ||
				p.Text.Format.Type != "json_schema" || !p.Text.Format.Strict || p.Text.Format.Name != "audit_answer" {
				t.Errorf("high effort and native strict output altered: %+v", p)
			}
		default:
			t.Errorf("unexpected/request-default effort sent: %+v", p)
		}
		if p.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"terra-stream\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"{\\\"ok\\\":true}\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, `{"object":"response","id":"terra-unary","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"{\"ok\":true}"}]}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer server.Close()
	client, err := openai.New(openai.Config{APIKey: "synthetic-key", Endpoint: server.URL, ModelProfiles: fixtureOpenAIProfiles()})
	if err != nil {
		t.Fatal(err)
	}
	base := model.Request{Model: "gpt-6.1-terra",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("output schema")}},
	}
	valid := []struct {
		name, options string
		strict        bool
	}{
		{"none-sampling", `{"reasoning":{"effort":"none"},"temperature":0.5}`, false},
		{"high-structured", `{"reasoning":{"effort":"high"}}`, true},
	}
	for _, v := range valid {
		t.Run("valid/"+v.name, func(t *testing.T) {
			req := base
			req.ModelOptions = json.RawMessage(v.options)
			if v.strict {
				req.OutputContract = &model.OutputContract{Name: "audit_answer",
					Schema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)}
			}
			for _, stream := range []bool{false, true} {
				var response model.Response
				var err error
				if stream {
					response, err = client.GenerateStream(t.Context(), req, nil)
				} else {
					response, err = client.Generate(t.Context(), req)
				}
				expected := "terra-unary"
				if stream {
					expected = "terra-stream"
				}
				if err != nil || response.ResponseID != expected || response.Usage == nil {
					t.Fatalf("valid Terra effort/structured request rejected: stream=%t response=%+v err=%v", stream, response, err)
				}
			}
		})
	}
	before := physical.Load()
	for _, bad := range []struct{ name, options string }{
		{"default-medium-temperature", `{"temperature":0.5}`},
		{"default-medium-top-p", `{"top_p":0.5}`},
		{"high-temperature", `{"reasoning":{"effort":"high"},"temperature":0.5}`},
		{"high-top-p", `{"reasoning":{"effort":"high"},"top_p":0.5}`},
		{"unsupported-minimal", `{"reasoning":{"effort":"minimal"}}`},
		{"duplicate-effort", `{"reasoning":{"effort":"high","effort":"none"}}`},
	} {
		t.Run("pre-dispatch-reject/"+bad.name, func(t *testing.T) {
			req := base
			req.ModelOptions = json.RawMessage(bad.options)
			for _, stream := range []bool{false, true} {
				var err error
				if stream {
					_, err = client.GenerateStream(t.Context(), req, nil)
				} else {
					_, err = client.Generate(t.Context(), req)
				}
				if err == nil {
					t.Fatalf("invalid request accepted: stream=%t", stream)
				}
			}
		})
	}
	if physical.Load() != before {
		t.Fatalf("unsupported Terra configurations generated %d physical requests", physical.Load()-before)
	}
}

// Property oracle: registering a public exact ID must not silently certify
// private descendants, gateway aliases or undocumented dated snapshots.
func TestPublicOpenAIProfileNeverGrantsPrefixAliases(t *testing.T) {
	client, err := openai.New(openai.Config{APIKey: "synthetic-key"})
	if err != nil {
		t.Fatal(err)
	}
	prefixes := []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-4.1", "gpt-5"}
	rng := rand.New(rand.NewSource(20261106))
	for index := 0; index < 512; index++ {
		id := fmt.Sprintf("%s-unverified-%08x", prefixes[rng.Intn(len(prefixes))], rng.Uint32())
		caps, err := client.Capabilities(t.Context(), id)
		if err != nil || caps.StructuredJSONSchema || len(caps.EffortLevels) != 0 {
			t.Fatalf("seed=20261106 case=%d unknown descendant %s inherited public authority: %+v %v", index, id, caps, err)
		}
	}
}
