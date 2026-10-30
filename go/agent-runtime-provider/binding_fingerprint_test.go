package provider_test

import (
	"net/http"
	"strings"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestRoutedProviderBindingFingerprintFencesSameNameEndpointAndCredentialDrift(t *testing.T) {
	t.Parallel()
	const firstEndpoint = "https://gateway.example.test/v1/responses"
	build := func(endpoint, key, scope string) *provider.Router {
		t.Helper()
		client, err := openai.New(openai.Config{Endpoint: endpoint, APIKey: key})
		if err != nil {
			t.Fatal(err)
		}
		router, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: scope, Client: client})
		if err != nil {
			t.Fatal(err)
		}
		return router
	}
	request := model.Request{Model: "openai/gpt-fixture"}
	original := build(firstEndpoint, "secret-key-1", "host-binding-v1")
	fingerprint := original.BindingFingerprintForRequest(request)
	if len(fingerprint) != 64 || strings.Contains(fingerprint, "gateway") || strings.Contains(fingerprint, "secret") ||
		original.ProviderNameForRequest(request) != "openai" ||
		build(firstEndpoint, "secret-key-1", "host-binding-v1").BindingFingerprintForRequest(request) != fingerprint {
		t.Fatalf("unstable or unsafe Provider binding identity %q", fingerprint)
	}
	for _, test := range []struct{ name, endpoint, key, scope string }{
		{"endpoint", "https://different.example.test/v1/responses", "secret-key-1", "host-binding-v1"},
		{"credentials", firstEndpoint, "rotated-secret-key", "host-binding-v1"},
		{"host-policy", firstEndpoint, "secret-key-1", "host-binding-v2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := build(test.endpoint, test.key, test.scope)
			if changed.ProviderNameForRequest(request) != "openai" ||
				changed.BindingFingerprintForRequest(request) == fingerprint {
				t.Fatal("same named Provider was retargeted without binding identity change")
			}
		})
	}
	if got := original.BindingFingerprintForRequest(model.Request{Model: "other/model"}); got != "" {
		t.Fatalf("unknown routed Provider gained a binding proof: %s", got)
	}
}

func TestProviderBindingFingerprintIncludesProtocolAndEffectivePolicy(t *testing.T) {
	t.Parallel()
	openaiClient, err := openai.New(openai.Config{APIKey: "test-credential", Endpoint: "https://gateway.example/v1/messages",
		StructuredOutputModels: []string{"model-z", "model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	same, err := openai.New(openai.Config{APIKey: "test-credential", Endpoint: "https://gateway.example/v1/messages",
		StructuredOutputModels: []string{"model-a", "model-z"}})
	if err != nil || openaiClient.BindingFingerprintForRequest(model.Request{}) != same.BindingFingerprintForRequest(model.Request{}) {
		t.Fatalf("semantically identical model allowlists changed binding identity: %v", err)
	}
	anthropicClient, err := anthropic.New(anthropic.Config{APIKey: "test-credential", Endpoint: "https://gateway.example/v1/messages"})
	if err != nil || anthropicClient.BindingFingerprintForRequest(model.Request{}) == openaiClient.BindingFingerprintForRequest(model.Request{}) {
		t.Fatalf("different provider wire protocol collided with same endpoint/credential: %v", err)
	}
	if _, err = openai.New(openai.Config{APIKey: "test-credential", HTTPClient: &http.Client{}}); err == nil {
		t.Fatal("custom transport without stable Host binding scope was admitted")
	}
	if _, err = anthropic.New(anthropic.Config{APIKey: "test-credential", HTTPClient: &http.Client{}}); err == nil {
		t.Fatal("custom Anthropic transport without stable Host binding scope was admitted")
	}
	if _, err = openai.New(openai.Config{APIKey: "test-credential", HTTPClient: &http.Client{}, BindingScope: "host-custom-transport-v1"}); err != nil {
		t.Fatal(err)
	}
}
