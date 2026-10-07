package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/compose"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Host is the explicitly assembled runtime graph produced from Config.
type Host struct {
	Runtime     *kernel.Runtime
	Agent       *agent.Runner
	Application *compose.Application
}

// Start starts explicitly composed WorkerFeatures in dependency order.
func (host *Host) Start(ctx context.Context) error {
	if host == nil || host.Application == nil {
		return ErrInvalidConfig
	}
	return host.Application.Start(ctx)
}

// Close stops explicitly composed WorkerFeatures in reverse order.
func (host *Host) Close(ctx context.Context) error {
	if host == nil || host.Application == nil {
		return ErrInvalidConfig
	}
	return host.Application.Close(ctx)
}

// Build resolves the versioned config through host-registered factories. It
// fails before serving traffic when any provider, secret, or policy is invalid.
func Build(ctx context.Context, config Config, registry *Registry, secrets SecretResolver) (*Host, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, ErrInvalidConfig
	}
	storeFactory, ok := registry.stores[strings.TrimSpace(config.Kernel.Store.Provider)]
	if !ok {
		return nil, unknownProvider("store", config.Kernel.Store.Provider)
	}
	storeInput, err := prepareFactoryInput(ctx, config.Kernel.Store, secrets, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: store %q: %w", ErrInvalidConfig, config.Kernel.Store.Provider, err)
	}
	store, err := storeFactory(ctx, storeInput)
	if err != nil {
		return nil, fmt.Errorf("%w: store %q: %w", ErrInvalidConfig, config.Kernel.Store.Provider, err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		return nil, err
	}
	features := []kernel.Feature{runtime}
	for _, component := range config.Kernel.Features {
		factory, exists := registry.features[strings.TrimSpace(component.Provider)]
		if !exists {
			return nil, unknownProvider("feature", component.Provider)
		}
		input, inputErr := prepareFactoryInput(ctx, component, secrets, runtime)
		if inputErr != nil {
			return nil, fmt.Errorf("%w: feature %q: %w", ErrInvalidConfig, component.Provider, inputErr)
		}
		feature, factoryErr := factory(ctx, input)
		if factoryErr != nil {
			return nil, fmt.Errorf("%w: feature %q: %w", ErrInvalidConfig, component.Provider, factoryErr)
		}
		if feature == nil {
			return nil, fmt.Errorf("%w: feature %q returned nil", ErrInvalidConfig, component.Provider)
		}
		features = append(features, feature)
	}
	host := &Host{Runtime: runtime}
	if !config.Agent.Enabled {
		host.Application, err = compose.New(features...)
		return host, err
	}

	modelFactory, ok := registry.models[strings.TrimSpace(config.Agent.Model.Provider)]
	if !ok {
		return nil, unknownProvider("model", config.Agent.Model.Provider)
	}
	modelInput, err := prepareFactoryInput(ctx, config.Agent.Model, secrets, runtime)
	if err != nil {
		return nil, fmt.Errorf("%w: model %q: %w", ErrInvalidConfig, config.Agent.Model.Provider, err)
	}
	modelClient, err := modelFactory(ctx, modelInput)
	if err != nil {
		return nil, fmt.Errorf("%w: model %q: %w", ErrInvalidConfig, config.Agent.Model.Provider, err)
	}

	var toolRegistry *tools.Registry
	if len(config.Agent.Tools) > 0 {
		registrations := make([]tools.Registration, 0, len(config.Agent.Tools))
		for _, component := range config.Agent.Tools {
			factory, exists := registry.tools[strings.TrimSpace(component.Provider)]
			if !exists {
				return nil, unknownProvider("tool", component.Provider)
			}
			input, inputErr := prepareFactoryInput(ctx, component, secrets, runtime)
			if inputErr != nil {
				return nil, fmt.Errorf("%w: tool %q: %w", ErrInvalidConfig, component.Provider, inputErr)
			}
			registration, factoryErr := factory(ctx, input)
			if factoryErr != nil {
				return nil, fmt.Errorf("%w: tool %q: %w", ErrInvalidConfig, component.Provider, factoryErr)
			}
			registrations = append(registrations, registration)
		}
		toolRegistry, err = tools.NewRegistry(registrations)
		if err != nil {
			return nil, err
		}
		features = append(features, toolRegistry)
	}

	var recorders []observability.Recorder
	if len(config.Agent.Telemetry) > 0 {
		recorders = make([]observability.Recorder, 0, len(config.Agent.Telemetry))
		for _, component := range config.Agent.Telemetry {
			factory, exists := registry.recorders[strings.TrimSpace(component.Provider)]
			if !exists {
				return nil, unknownProvider("telemetry", component.Provider)
			}
			input, inputErr := prepareFactoryInput(ctx, component, secrets, runtime)
			if inputErr != nil {
				return nil, fmt.Errorf("%w: telemetry %q: %w", ErrInvalidConfig, component.Provider, inputErr)
			}
			recorder, factoryErr := factory(ctx, input)
			if factoryErr != nil {
				return nil, fmt.Errorf("%w: telemetry %q: %w", ErrInvalidConfig, component.Provider, factoryErr)
			}
			recorders = append(recorders, recorder)
		}
	}

	execution, err := config.Agent.Execution.Policy()
	if err != nil {
		return nil, err
	}
	dependencies := agent.Dependencies{
		Runtime: runtime, Model: modelClient, Telemetry: recorders,
		Limits: config.Agent.Limits.Value(), Execution: execution,
	}
	if toolRegistry != nil {
		dependencies.Catalog = toolRegistry
		dependencies.Executor = toolRegistry
	}
	runner, err := agent.NewRunner(dependencies)
	if err != nil {
		return nil, err
	}
	host.Agent = runner
	features = append(features, runner)
	host.Application, err = compose.New(features...)
	if err != nil {
		return nil, err
	}
	return host, nil
}

func unknownProvider(kind, name string) error {
	return fmt.Errorf("%w: %s %q", ErrUnknownProvider, kind, strings.TrimSpace(name))
}
