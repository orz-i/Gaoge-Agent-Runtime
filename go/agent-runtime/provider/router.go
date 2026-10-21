// Package provider routes provider-qualified model names to explicitly registered
// model clients. It does not discover providers, credentials or plugins globally.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var ErrInvalidRouting = errors.New("invalid model provider routing")

// Binding associates a stable provider name with a model.Client.
type Binding struct {
	Name   string
	Client model.Client
}

// Router is an immutable, concurrency-safe collection of model providers.
// Qualified names ("openai/gpt-4.1") select a provider. Unqualified names
// use the explicit default provider; the provider receives only the model ID.
type Router struct {
	defaultName string
	clients     map[string]model.Client
}

func NewRouter(defaultName string, bindings ...Binding) (*Router, error) {
	clients := make(map[string]model.Client, len(bindings))
	for _, binding := range bindings {
		name := strings.TrimSpace(binding.Name)
		if !validName(name) || binding.Client == nil {
			return nil, ErrInvalidRouting
		}
		if _, exists := clients[name]; exists {
			return nil, fmt.Errorf("%w: duplicate provider %s", ErrInvalidRouting, name)
		}
		clients[name] = binding.Client
	}
	if !validName(defaultName) || clients[defaultName] == nil {
		return nil, ErrInvalidRouting
	}
	return &Router{defaultName: defaultName, clients: clients}, nil
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' {
			continue
		}
		if i > 0 && (c >= '0' && c <= '9' || c == '-' || c == '_') {
			continue
		}
		return false
	}
	return true
}

func (r *Router) route(request model.Request) (model.Client, model.Request, string, error) {
	if r == nil {
		return nil, model.Request{}, "", ErrInvalidRouting
	}
	name := r.defaultName
	if prefix, tail, ok := strings.Cut(request.Model, "/"); ok {
		name = prefix
		request.Model = tail
	}
	if strings.TrimSpace(request.Model) != request.Model || strings.Contains(request.Model, "/") {
		return nil, model.Request{}, "", ErrInvalidRouting
	}
	client := r.clients[name]
	if client == nil {
		return nil, model.Request{}, "", fmt.Errorf("%w: unknown provider %q", ErrInvalidRouting, name)
	}
	return client, request, name, nil
}

func (r *Router) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	client, routed, _, err := r.route(request)
	if err != nil {
		return model.Response{}, err
	}
	return client.Generate(ctx, routed)
}

func (r *Router) GenerateStream(ctx context.Context, request model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	client, routed, _, err := r.route(request)
	if err != nil {
		return model.Response{}, err
	}
	if streaming, ok := client.(model.StreamingClient); ok {
		return streaming.GenerateStream(ctx, routed, emit)
	}
	return client.Generate(ctx, routed)
}

// ProviderNameForRequest records the chosen provider with durable Agent
// invocations, rather than an ambiguous "router" identity.
func (r *Router) ProviderNameForRequest(request model.Request) string {
	_, _, name, err := r.route(request)
	if err != nil {
		return ""
	}
	return name
}

var _ model.Client = (*Router)(nil)
var _ model.StreamingClient = (*Router)(nil)
var _ model.RequestProviderNamer = (*Router)(nil)
