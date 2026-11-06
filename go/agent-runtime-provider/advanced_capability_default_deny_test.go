package provider_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// New-model features are Host privileges: unconfigured is NOT equivalent to
// "known compatible". Regression for the two missing-profile P2 variants.
// Both transports must refuse before any physical HTTP, independent of the
// chosen vendor model name; no public model catalog is reintroduced.
func TestUncertifiedAdvancedModelFeaturesNeverCrossProviderHTTP(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			var physical atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				physical.Add(1)
				http.Error(w, "unverified feature reached Provider", http.StatusBadRequest)
			}))
			defer server.Close()
			var client model.Client
			var stream model.StreamingClient
			if protocol == "openai" {
				actual, err := openai.New(openai.Config{APIKey: "synthetic-key", Endpoint: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				client, stream = actual, actual
			} else {
				actual, err := anthropic.New(anthropic.Config{APIKey: "synthetic-key", Endpoint: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				client, stream = actual, actual
			}
			base := model.Request{Model: "unknown-unregistered-provider-native-model",
				Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}},
			}
			for _, kind := range []string{"sample-with-omitted-effort", "mandatory-tool"} {
				t.Run(kind, func(t *testing.T) {
					request := model.CloneRequest(base)
					switch kind {
					case "sample-with-omitted-effort":
						request.ModelOptions = json.RawMessage(`{"temperature":0.5}`)
					case "mandatory-tool":
						request.RequireToolCall = true
						request.Tools = []tools.Definition{{
							Key: "lookup:v1", Name: "lookup",
							InputSchema: json.RawMessage(`{"type":"object"}`),
						}}
					}
					if _, err := client.Generate(t.Context(), request); err == nil {
						t.Fatal("missing Host capability certified an advanced Unary request")
					}
					if _, err := stream.GenerateStream(t.Context(), request, nil); err == nil {
						t.Fatal("missing Host capability certified an advanced SSE request")
					}
				})
			}
			if physical.Load() != 0 {
				t.Fatalf("unconfigured model advanced features produced %d physical Provider HTTP calls", physical.Load())
			}
		})
	}
}

// A positive Host certificate is distinguishable from the absent model and
// must rotate a frozen pending grant when revoked. An unrelated model entry
// has no impact on the original selected request's HMAC.
func TestPositiveAdvancedModelProfileIsBoundToSelectedModel(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			source := model.StaticModelProfiles{
				"future-model": {
					SupportsSamplingWhenEffortOmitted: true,
					SupportsForcedToolChoice:          true,
				},
			}
			build := func() model.RequestBindingFingerprinter {
				t.Helper()
				if protocol == "openai" {
					c, err := openai.New(openai.Config{APIKey: "synthetic-key", ModelProfiles: source})
					if err != nil {
						t.Fatal(err)
					}
					return c
				}
				c, err := anthropic.New(anthropic.Config{APIKey: "synthetic-key", ModelProfiles: source})
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			request := model.Request{Model: "future-model"}
			original := build().BindingFingerprintForRequest(request)
			source["unrelated-model"] = model.Capabilities{StructuredJSONSchema: true}
			if original != build().BindingFingerprintForRequest(request) {
				t.Fatal("unrelated model catalog addition revoked the selected model")
			}
			source["future-model"] = model.Capabilities{SupportsSamplingWhenEffortOmitted: true}
			if original == build().BindingFingerprintForRequest(request) {
				t.Fatal("revoking mandatory Tool certification did not change binding identity")
			}
			source["future-model"] = model.Capabilities{SupportsForcedToolChoice: true}
			if original == build().BindingFingerprintForRequest(request) {
				t.Fatal("revoking omitted-effort sampling certification did not change binding identity")
			}
		})
	}
}
