package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestLoadKoanfYAMLWithEnvironmentOverride(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	data := []byte("version: v1\nkernel:\n  store:\n    provider: memory\nagent:\n  enabled: true\n  model:\n    provider: fake\n  execution:\n    model:\n      timeout: 45s\n      max_attempts: 2\n      max_concurrency: 8\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path, LoadOptions{
		EnvironFunc: func() []string {
			return []string{
				"GAOGE_AGENT__EXECUTION__MODEL__MAX_CONCURRENCY=3",
				"GAOGE_AGENT__EXECUTION__MODEL__MAX_ATTEMPTS=4",
				"GAOGE_AGENT__EXECUTION__MODEL__TIMEOUT=12s",
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.Agent.Execution.Model.MaxConcurrency != 3 ||
		config.Agent.Execution.Model.MaxAttempts != 4 ||
		config.Agent.Execution.Model.Timeout != "12s" {
		t.Fatalf("execution config = %#v", config.Agent.Execution.Model)
	}
}

func TestBuildConfiguredHost(t *testing.T) {
	t.Parallel()
	registry := NewDefaultRegistry()
	if err := registry.RegisterFeature("fixture", func(_ context.Context, input FactoryInput) (kernel.Feature, error) {
		if input.Runtime == nil {
			return nil, errors.New("runtime missing from feature factory")
		}
		return bootstrapFeature{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterModel("fake", func(context.Context, FactoryInput) (model.Client, error) {
		return bootstrapModel{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Version: ConfigVersion,
		Kernel: KernelConfig{
			Store:    ComponentConfig{Provider: "memory"},
			Features: []ComponentConfig{{Provider: "fixture"}},
		},
		Agent: AgentConfig{
			Enabled: true,
			Model:   ComponentConfig{Provider: "fake"},
			Execution: ExecutionPolicyConfig{
				Model: CallPolicyConfig{Timeout: "1s", MaxAttempts: 2, MaxConcurrency: 2},
				Tool:  CallPolicyConfig{Timeout: "1s", MaxAttempts: 1, MaxConcurrency: 2},
			},
		},
	}
	host, err := Build(t.Context(), config, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host.Runtime == nil || host.Agent == nil || host.Application == nil {
		t.Fatalf("incomplete host = %#v", host)
	}
	snapshot, err := host.Agent.StartRun(t.Context(), agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "test", ID: "thread"},
		Goal:   "configured",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != "completed" {
		t.Fatalf("status = %q", snapshot.Run.Status)
	}
}

func TestBuildRejectsUnknownProvider(t *testing.T) {
	t.Parallel()
	config := Config{
		Version: ConfigVersion,
		Kernel:  KernelConfig{Store: ComponentConfig{Provider: "missing"}},
	}
	_, err := Build(t.Context(), config, NewDefaultRegistry(), nil)
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("expected unknown provider, got %v", err)
	}
}

func TestBuildFailsBeforeFactoryWhenSecretIsMissing(t *testing.T) {
	t.Parallel()
	registry := NewDefaultRegistry()
	called := false
	if err := registry.RegisterModel("secret-model", func(context.Context, FactoryInput) (model.Client, error) {
		called = true
		return bootstrapModel{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	config := Config{
		Version: ConfigVersion,
		Kernel:  KernelConfig{Store: ComponentConfig{Provider: "memory"}},
		Agent: AgentConfig{
			Enabled: true,
			Model: ComponentConfig{
				Provider:   "secret-model",
				SecretRefs: map[string]string{"api_key": "missing"},
			},
		},
	}
	_, err := Build(t.Context(), config, registry, bootstrapSecrets{})
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected missing secret, got %v", err)
	}
	if called {
		t.Fatal("model factory ran before secret validation")
	}
}

func TestHostStartsAndClosesConfiguredWorkers(t *testing.T) {
	t.Parallel()
	registry := NewDefaultRegistry()
	worker := &bootstrapWorker{}
	if err := registry.RegisterFeature("worker", func(_ context.Context, input FactoryInput) (kernel.Feature, error) {
		if input.Runtime == nil {
			return nil, errors.New("runtime missing")
		}
		return worker, nil
	}); err != nil {
		t.Fatal(err)
	}
	host, err := Build(t.Context(), Config{
		Version: ConfigVersion,
		Kernel: KernelConfig{
			Store:    ComponentConfig{Provider: "memory"},
			Features: []ComponentConfig{{Provider: "worker"}},
		},
	}, registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if worker.starts != 1 || worker.closes != 1 {
		t.Fatalf("worker lifecycle starts=%d closes=%d", worker.starts, worker.closes)
	}
}

func TestFactoryInputResolvesOnlySecretReferences(t *testing.T) {
	t.Parallel()
	input, err := prepareFactoryInput(
		t.Context(),
		ComponentConfig{SecretRefs: map[string]string{"api_key": "model/api-key"}},
		bootstrapSecrets{"model/api-key": "secret-value"},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := input.Secret(t.Context(), "api_key")
	if err != nil {
		t.Fatal(err)
	}
	if value != "secret-value" {
		t.Fatalf("secret = %q", value)
	}
	if _, err = input.Secret(t.Context(), "missing"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("missing secret error = %v", err)
	}
}

type bootstrapFeature struct{}

func (bootstrapFeature) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "fixture", Provides: []kernel.Capability{"fixture"}}
}

type bootstrapWorker struct {
	starts int
	closes int
}

func (*bootstrapWorker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "worker", Provides: []kernel.Capability{"worker"}}
}

func (worker *bootstrapWorker) Start(context.Context) error {
	worker.starts++
	return nil
}

func (worker *bootstrapWorker) Close(context.Context) error {
	worker.closes++
	return nil
}

type bootstrapModel struct{}

func (bootstrapModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "ok"}, nil
}

type bootstrapSecrets map[string]string

func (secrets bootstrapSecrets) ResolveSecret(_ context.Context, ref string) (string, error) {
	value, ok := secrets[ref]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}
