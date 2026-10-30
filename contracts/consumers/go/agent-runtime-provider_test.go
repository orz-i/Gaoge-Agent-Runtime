package consumer

import (
	"context"
	"testing"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type stubModelProvider struct{}

func (stubModelProvider) Generate(_ context.Context, request model.Request) (model.Response, error) {
	return model.Response{Content: request.Model}, nil
}

var (
	_ model.Client = (*openai.Client)(nil)
	_ model.Client = (*anthropic.Client)(nil)
	_ model.Client = (*provider.Router)(nil)
)

func TestStandaloneProviderModuleRoutesWithoutKernelVendorCoupling(t *testing.T) {
	router, err := provider.NewRouter("stub", provider.Binding{Name: "stub", Client: stubModelProvider{}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := router.Generate(context.Background(), model.Request{Model: "stub/any-model"})
	if err != nil || response.Content != "any-model" {
		t.Fatalf("provider route and normalization: response=%+v err=%v", response, err)
	}
}
