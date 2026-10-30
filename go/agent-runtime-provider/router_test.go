package provider_test

import (
	"context"
	"errors"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type recorder struct{ last string }

func (f *recorder) Generate(_ context.Context, r model.Request) (model.Response, error) {
	f.last = r.Model
	return model.Response{Content: "ok"}, nil
}
func TestRouterRoutingAndImmutability(t *testing.T) {
	a, b := &recorder{}, &recorder{}
	r, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Identity: "host-binding-v1", Client: a}, provider.Binding{Name: "anthropic", Identity: "host-binding-v1", Client: b})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Model: "anthropic/claude-test"}
	_, err = r.Generate(context.Background(), request)
	if err != nil || b.last != "claude-test" || request.Model != "anthropic/claude-test" {
		t.Fatalf("routing=%v %+v", err, b)
	}
	if r.ProviderNameForRequest(request) != "anthropic" {
		t.Fatal("wrong provider name")
	}
	_, err = r.GenerateStream(context.Background(), model.Request{Model: "gpt-test"}, nil)
	if err != nil || a.last != "gpt-test" {
		t.Fatal("non-stream fallback")
	}
	if _, err = r.Generate(context.Background(), model.Request{Model: "unknown/model"}); !errors.Is(err, provider.ErrInvalidRouting) {
		t.Fatal("unknown provider accepted")
	}
}
func TestInvalidRegistry(t *testing.T) {
	f := &recorder{}
	for _, bindings := range [][]provider.Binding{
		nil,
		{{Name: "openai", Client: f}}, // Missing mandatory Host binding identity.
		{{Name: "openai", Identity: " ", Client: f}},
		{{Name: "OpenAI", Identity: "scope", Client: f}},
		{{Name: "openai", Identity: "scope", Client: nil}},
		{{Name: "openai", Identity: "scope", Client: f}, {Name: "openai", Identity: "scope", Client: f}},
	} {
		if _, err := provider.NewRouter("openai", bindings...); err == nil {
			t.Fatalf("invalid bindings accepted %+v", bindings)
		}
	}
}

func TestRouterAllowsProviderNamespacedModelIDsWithoutAmbiguity(t *testing.T) {
	backend := &recorder{}
	router, err := provider.NewRouter("gateway", provider.Binding{Name: "gateway", Identity: "host-binding-v1", Client: backend})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{Model: "gateway/anthropic/claude-opus-5-5"}
	response, err := router.Generate(context.Background(), request)
	if err != nil || backend.last != "anthropic/claude-opus-5-5" || response.Content != "ok" {
		t.Fatalf("provider model namespace not preserved: %+v, %v, %q", response, err, backend.last)
	}
	if router.ProviderNameForRequest(request) != "gateway" {
		t.Fatal("provider identity changed")
	}
	for _, name := range []string{"gateway/", "gateway//model", "gateway/model/", "gateway/model/ other", "gateway/ model", "unknown/model"} {
		if _, err := router.Generate(context.Background(), model.Request{Model: name}); !errors.Is(err, provider.ErrInvalidRouting) {
			t.Errorf("invalid route %q accepted: %v", name, err)
		}
	}
}
