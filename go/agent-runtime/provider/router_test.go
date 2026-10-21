package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider"
)

type recorder struct{ last string }

func (f *recorder) Generate(_ context.Context, r model.Request) (model.Response, error) {
	f.last = r.Model
	return model.Response{Content: "ok"}, nil
}
func TestRouterRoutingAndImmutability(t *testing.T) {
	a, b := &recorder{}, &recorder{}
	r, err := provider.NewRouter("openai", provider.Binding{Name: "openai", Client: a}, provider.Binding{Name: "anthropic", Client: b})
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
		{{Name: "OpenAI", Client: f}},
		{{Name: "openai", Client: nil}},
		{{Name: "openai", Client: f}, {Name: "openai", Client: f}},
	} {
		if _, err := provider.NewRouter("openai", bindings...); err == nil {
			t.Fatalf("invalid bindings accepted %+v", bindings)
		}
	}
}
