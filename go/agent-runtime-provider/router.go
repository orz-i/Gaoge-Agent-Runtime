// Package provider routes provider-qualified model names to explicitly registered
// model clients. It does not discover providers, credentials or plugins globally.
package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var ErrInvalidRouting = errors.New("invalid model provider routing")

// Binding associates a stable provider name with an explicit, immutable
// Host-assigned binding identity. The identity must be rotated when a
// custom Client's effective endpoint, transport or policy changes.
type Binding struct {
	Name     string
	Identity string
	Client   model.Client
}

// Router is an immutable, concurrency-safe collection of model providers.
// Qualified names ("openai/gpt-4.1") select a provider. Unqualified names
// use the explicit default provider; the provider receives only the model ID.
type Router struct {
	defaultName string
	clients     map[string]Binding
}

func NewRouter(defaultName string, bindings ...Binding) (*Router, error) {
	clients := make(map[string]Binding, len(bindings))
	for _, binding := range bindings {
		name := strings.TrimSpace(binding.Name)
		if !validName(name) || binding.Client == nil ||
			binding.Identity == "" || binding.Identity != strings.TrimSpace(binding.Identity) ||
			len(binding.Identity) > 128 {
			return nil, ErrInvalidRouting
		}
		if _, exists := clients[name]; exists {
			return nil, fmt.Errorf("%w: duplicate provider %s", ErrInvalidRouting, name)
		}
		clients[name] = binding
	}
	if !validName(defaultName) || clients[defaultName].Client == nil {
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
	if strings.TrimSpace(request.Model) != request.Model || strings.TrimSpace(request.Model) == "" {
		return nil, model.Request{}, "", ErrInvalidRouting
	}
	// A provider may use namespaced model IDs, e.g. openrouter/anthropic/claude-*.
	// Only the first slash is the router delimiter; validate the remaining ID.
	for _, segment := range strings.Split(request.Model, "/") {
		if strings.TrimSpace(segment) == "" || strings.TrimSpace(segment) != segment {
			return nil, model.Request{}, "", ErrInvalidRouting
		}
	}
	client := r.clients[name].Client
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

// Capabilities resolves the exact routed model before an opt-in strict output
// is persisted. A provider without capability metadata must fail closed.
func (r *Router) Capabilities(ctx context.Context, modelID string) (model.Capabilities, error) {
	client, routed, _, err := r.route(model.Request{Model: modelID})
	if err != nil {
		return model.Capabilities{}, err
	}
	capable, ok := client.(model.CapabilityProvider)
	if !ok {
		return model.Capabilities{}, model.ErrCapabilityUnavailable
	}
	return capable.Capabilities(ctx, routed.Model)
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

// BindingFingerprintForRequest freezes both the selected route's Host-owned
// identity and any more precise fingerprint provided by its concrete client.
// A concrete Provider with an invalid binding proof must fail closed.
func (r *Router) BindingFingerprintForRequest(request model.Request) string {
	client, routed, name, err := r.route(request)
	if err != nil {
		return ""
	}
	identity := r.clients[name].Identity
	component := ""
	if bound, ok := client.(model.RequestBindingFingerprinter); ok {
		component = bound.BindingFingerprintForRequest(routed)
		if len(component) != sha256.Size*2 {
			return ""
		}
		if decoded, decodeErr := hex.DecodeString(component); decodeErr != nil || hex.EncodeToString(decoded) != component {
			return ""
		}
	}
	digest := sha256.Sum256([]byte(name + "\x00" + identity + "\x00" + component))
	return hex.EncodeToString(digest[:])
}

var _ model.RequestBindingFingerprinter = (*Router)(nil)
var _ model.CapabilityProvider = (*Router)(nil)
var _ model.Client = (*Router)(nil)
var _ model.StreamingClient = (*Router)(nil)
var _ model.RequestProviderNamer = (*Router)(nil)
