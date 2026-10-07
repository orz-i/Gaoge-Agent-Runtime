package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// SecretResolver resolves opaque secret references during startup.
type SecretResolver interface {
	ResolveSecret(context.Context, string) (string, error)
}

// EnvSecretResolver treats each reference as an environment variable name.
type EnvSecretResolver struct{}

func (EnvSecretResolver) ResolveSecret(_ context.Context, ref string) (string, error) {
	value, ok := os.LookupEnv(strings.TrimSpace(ref))
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, ref)
	}
	return value, nil
}

type FactoryInput struct {
	Options    map[string]any
	SecretRefs map[string]string
	Runtime    *kernel.Runtime

	resolvedSecrets map[string]string
}

// Secret returns a secret that was resolved during Build. All declared
// references are resolved before their factory is invoked, so missing secrets
// fail startup even when a factory forgets to request them explicitly.
func (input FactoryInput) Secret(_ context.Context, key string) (string, error) {
	key = strings.TrimSpace(key)
	value, ok := input.resolvedSecrets[key]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSecretNotFound, key)
	}
	return value, nil
}

type StoreFactory func(context.Context, FactoryInput) (kernel.Store, error)
type FeatureFactory func(context.Context, FactoryInput) (kernel.Feature, error)
type ModelFactory func(context.Context, FactoryInput) (model.Client, error)
type ToolFactory func(context.Context, FactoryInput) (tools.Registration, error)
type RecorderFactory func(context.Context, FactoryInput) (observability.Recorder, error)

// Registry is the manual DI registry. The host registers concrete adapter
// factories in code; configuration can select only those explicit providers.
type Registry struct {
	stores    map[string]StoreFactory
	features  map[string]FeatureFactory
	models    map[string]ModelFactory
	tools     map[string]ToolFactory
	recorders map[string]RecorderFactory
}

func NewRegistry() *Registry {
	return &Registry{
		stores: make(map[string]StoreFactory), features: make(map[string]FeatureFactory),
		models: make(map[string]ModelFactory), tools: make(map[string]ToolFactory),
		recorders: make(map[string]RecorderFactory),
	}
}

// NewDefaultRegistry exposes only dependency-free core providers. External
// Postgres, Redis, model SDK and telemetry adapters remain host registrations.
func NewDefaultRegistry() *Registry {
	registry := NewRegistry()
	_ = registry.RegisterStore("memory", func(context.Context, FactoryInput) (kernel.Store, error) {
		return memory.NewStore(), nil
	})
	return registry
}

func (registry *Registry) RegisterStore(name string, factory StoreFactory) error {
	if factory == nil {
		return ErrInvalidConfig
	}
	return registerFactory(registry, name, factory, registry.stores)
}

func (registry *Registry) RegisterFeature(name string, factory FeatureFactory) error {
	if factory == nil {
		return ErrInvalidConfig
	}
	return registerFactory(registry, name, factory, registry.features)
}

func (registry *Registry) RegisterModel(name string, factory ModelFactory) error {
	if factory == nil {
		return ErrInvalidConfig
	}
	return registerFactory(registry, name, factory, registry.models)
}

func (registry *Registry) RegisterTool(name string, factory ToolFactory) error {
	if factory == nil {
		return ErrInvalidConfig
	}
	return registerFactory(registry, name, factory, registry.tools)
}

func (registry *Registry) RegisterRecorder(name string, factory RecorderFactory) error {
	if factory == nil {
		return ErrInvalidConfig
	}
	return registerFactory(registry, name, factory, registry.recorders)
}

func registerFactory[T any](registry *Registry, name string, factory T, target map[string]T) error {
	if registry == nil {
		return ErrInvalidConfig
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrInvalidConfig
	}
	if _, exists := target[name]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	target[name] = factory
	return nil
}

func prepareFactoryInput(
	ctx context.Context,
	config ComponentConfig,
	secrets SecretResolver,
	runtime *kernel.Runtime,
) (FactoryInput, error) {
	input := FactoryInput{
		Options:         cloneMap(config.Options),
		SecretRefs:      cloneStringMap(config.SecretRefs),
		Runtime:         runtime,
		resolvedSecrets: make(map[string]string, len(config.SecretRefs)),
	}
	for key, ref := range input.SecretRefs {
		if secrets == nil {
			return FactoryInput{}, fmt.Errorf("%w: %s", ErrSecretNotFound, key)
		}
		value, err := secrets.ResolveSecret(ctx, ref)
		if err != nil {
			return FactoryInput{}, fmt.Errorf("%w: %s: %w", ErrSecretNotFound, key, err)
		}
		input.resolvedSecrets[key] = value
	}
	return input, nil
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneValue(value)
	}
	return result
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	default:
		return typed
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
