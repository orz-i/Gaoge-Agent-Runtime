// Package bootstrap loads host configuration and manually assembles Agent
// Runtime components from explicit provider factories. It deliberately keeps
// configuration and DI outside the kernel state machine.
package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/yaml"
	env "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/v2"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

const (
	ConfigVersion    = "v1"
	DefaultEnvPrefix = "GAOGE_"
)

var (
	ErrInvalidConfig   = errors.New("invalid runtime bootstrap config")
	ErrUnknownProvider = errors.New("unknown runtime bootstrap provider")
	ErrDuplicate       = errors.New("duplicate runtime bootstrap provider")
	ErrSecretNotFound  = errors.New("runtime bootstrap secret not found")
)

// Config is the versioned host-side assembly document. It is not persisted in
// Run state and does not alter kernel durability semantics.
type Config struct {
	Version string       `koanf:"version"`
	Kernel  KernelConfig `koanf:"kernel"`
	Agent   AgentConfig  `koanf:"agent"`
}

type KernelConfig struct {
	Store    ComponentConfig   `koanf:"store"`
	Features []ComponentConfig `koanf:"features"`
}

type AgentConfig struct {
	Enabled   bool                  `koanf:"enabled"`
	Model     ComponentConfig       `koanf:"model"`
	Tools     []ComponentConfig     `koanf:"tools"`
	Telemetry []ComponentConfig     `koanf:"telemetry"`
	Limits    LimitsConfig          `koanf:"limits"`
	Execution ExecutionPolicyConfig `koanf:"execution"`
}

// ComponentConfig selects one factory registered by the host. SecretRefs are
// opaque names resolved at construction time; secret values never belong in
// this document.
type ComponentConfig struct {
	Provider   string            `koanf:"provider"`
	Options    map[string]any    `koanf:"options"`
	SecretRefs map[string]string `koanf:"secret_refs"`
}

type ExecutionPolicyConfig struct {
	Model CallPolicyConfig `koanf:"model"`
	Tool  CallPolicyConfig `koanf:"tool"`
}

type CallPolicyConfig struct {
	Timeout        string `koanf:"timeout"`
	MaxAttempts    int    `koanf:"max_attempts"`
	InitialBackoff string `koanf:"initial_backoff"`
	MaxBackoff     string `koanf:"max_backoff"`
	MaxConcurrency int64  `koanf:"max_concurrency"`
}

type LimitsConfig struct {
	MaxLLMCalls       int   `koanf:"max_llm_calls"`
	MaxToolCalls      int   `koanf:"max_tool_calls"`
	MaxInputTokens    int64 `koanf:"max_input_tokens"`
	MaxOutputTokens   int64 `koanf:"max_output_tokens"`
	MaxTotalTokens    int64 `koanf:"max_total_tokens"`
	MaxOutputBytes    int   `koanf:"max_output_bytes"`
	MaxStateBytes     int   `koanf:"max_state_bytes"`
	MaxChildRuns      int   `koanf:"max_child_runs"`
	MaxCostUnits      int64 `koanf:"max_cost_units"`
	MaxConcurrentRuns int   `koanf:"max_concurrent_runs"`
}

type LoadOptions struct {
	EnvPrefix   string
	DisableEnv  bool
	EnvironFunc func() []string
}

// Load reads YAML or JSON, then overlays environment variables. Nested env
// paths use "__" so snake_case config keys remain unambiguous, for example:
// GAOGE_AGENT__EXECUTION__MODEL__TIMEOUT=45s.
func Load(path string, options LoadOptions) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Config{}, ErrInvalidConfig
	}
	parser, err := parserForPath(path)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w: load %s: %w", ErrInvalidConfig, path, err)
	}
	k := koanf.New(".")
	if err = k.Load(bytesProvider(data), parser); err != nil {
		return Config{}, fmt.Errorf("%w: parse %s: %w", ErrInvalidConfig, path, err)
	}
	prefix := options.EnvPrefix
	if prefix == "" {
		prefix = DefaultEnvPrefix
	}
	if !options.DisableEnv {
		if err = k.Load(env.Provider(".", env.Opt{
			Prefix:      prefix,
			EnvironFunc: options.EnvironFunc,
			TransformFunc: func(key, value string) (string, any) {
				key = strings.TrimPrefix(key, prefix)
				key = strings.ToLower(strings.ReplaceAll(key, "__", "."))
				return key, parseEnvValue(value)
			},
		}), nil); err != nil {
			return Config{}, fmt.Errorf("%w: load environment: %w", ErrInvalidConfig, err)
		}
	}
	var config Config
	if err = k.Unmarshal("", &config); err != nil {
		return Config{}, fmt.Errorf("%w: decode: %w", ErrInvalidConfig, err)
	}
	if err = config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

type bytesProvider []byte

func (provider bytesProvider) ReadBytes() ([]byte, error) {
	return append([]byte(nil), provider...), nil
}

func (bytesProvider) Read() (map[string]any, error) {
	return nil, errors.New("bootstrap bytes provider requires a parser")
}

func parserForPath(path string) (koanf.Parser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".json":
		return json.Parser(), nil
	default:
		return nil, fmt.Errorf("%w: unsupported config extension %q", ErrInvalidConfig, filepath.Ext(path))
	}
}

func parseEnvValue(value string) any {
	if parsed, err := strconv.ParseBool(value); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
		return parsed
	}
	return value
}

func (config Config) Validate() error {
	if strings.TrimSpace(config.Version) != ConfigVersion {
		return fmt.Errorf("%w: version must be %q", ErrInvalidConfig, ConfigVersion)
	}
	if err := validateComponent("kernel.store", config.Kernel.Store, true); err != nil {
		return err
	}
	for index, component := range config.Kernel.Features {
		if err := validateComponent(fmt.Sprintf("kernel.features[%d]", index), component, true); err != nil {
			return err
		}
	}
	if !config.Agent.Enabled {
		return nil
	}
	if err := validateComponent("agent.model", config.Agent.Model, true); err != nil {
		return err
	}
	for index, component := range config.Agent.Tools {
		if err := validateComponent(fmt.Sprintf("agent.tools[%d]", index), component, true); err != nil {
			return err
		}
	}
	for index, component := range config.Agent.Telemetry {
		if err := validateComponent(fmt.Sprintf("agent.telemetry[%d]", index), component, true); err != nil {
			return err
		}
	}
	if _, err := config.Agent.Execution.Policy(); err != nil {
		return err
	}
	if !budget.ValidLimits(config.Agent.Limits.Value()) {
		return fmt.Errorf("%w: invalid agent limits", ErrInvalidConfig)
	}
	return nil
}

func validateComponent(path string, component ComponentConfig, required bool) error {
	if required && strings.TrimSpace(component.Provider) == "" {
		return fmt.Errorf("%w: %s.provider is required", ErrInvalidConfig, path)
	}
	for key, ref := range component.SecretRefs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(ref) == "" {
			return fmt.Errorf("%w: %s.secret_refs contains an empty key or reference", ErrInvalidConfig, path)
		}
	}
	return nil
}

func (config ExecutionPolicyConfig) Policy() (agent.ExecutionPolicy, error) {
	modelPolicy, err := config.Model.policy("agent.execution.model")
	if err != nil {
		return agent.ExecutionPolicy{}, err
	}
	toolPolicy, err := config.Tool.policy("agent.execution.tool")
	if err != nil {
		return agent.ExecutionPolicy{}, err
	}
	return agent.ExecutionPolicy{Model: modelPolicy, Tool: toolPolicy}.Resolve()
}

func (config CallPolicyConfig) policy(path string) (agent.CallPolicy, error) {
	if config.MaxAttempts < 0 || config.MaxConcurrency < 0 {
		return agent.CallPolicy{}, fmt.Errorf("%w: %s has a negative limit", ErrInvalidConfig, path)
	}
	timeout, err := parseDuration(path+".timeout", config.Timeout)
	if err != nil {
		return agent.CallPolicy{}, err
	}
	initialBackoff, err := parseDuration(path+".initial_backoff", config.InitialBackoff)
	if err != nil {
		return agent.CallPolicy{}, err
	}
	maxBackoff, err := parseDuration(path+".max_backoff", config.MaxBackoff)
	if err != nil {
		return agent.CallPolicy{}, err
	}
	if initialBackoff > 0 && maxBackoff > 0 && maxBackoff < initialBackoff {
		return agent.CallPolicy{}, fmt.Errorf("%w: %s.max_backoff is smaller than initial_backoff", ErrInvalidConfig, path)
	}
	return agent.CallPolicy{
		Timeout: timeout, MaxAttempts: config.MaxAttempts,
		InitialBackoff: initialBackoff, MaxBackoff: maxBackoff,
		MaxConcurrency: config.MaxConcurrency,
	}, nil
}

func parseDuration(path, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%w: %s must be a positive Go duration", ErrInvalidConfig, path)
	}
	return duration, nil
}

func (config LimitsConfig) Value() budget.Limits {
	return budget.Limits{
		MaxLLMCalls: config.MaxLLMCalls, MaxToolCalls: config.MaxToolCalls,
		MaxInputTokens: config.MaxInputTokens, MaxOutputTokens: config.MaxOutputTokens,
		MaxTotalTokens: config.MaxTotalTokens, MaxOutputBytes: config.MaxOutputBytes,
		MaxStateBytes: config.MaxStateBytes, MaxChildRuns: config.MaxChildRuns,
		MaxCostUnits: config.MaxCostUnits, MaxConcurrentRuns: config.MaxConcurrentRuns,
	}
}
