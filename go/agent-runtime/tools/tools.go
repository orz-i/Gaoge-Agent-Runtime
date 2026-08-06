package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const (
	CapabilityCatalog  kernel.Capability = "tools.catalog"
	CapabilityExecutor kernel.Capability = "tools.executor"
)

var (
	ErrInvalidDefinition = errors.New("invalid tool definition")
	ErrDuplicateTool     = errors.New("duplicate tool definition")
	ErrToolNotFound      = errors.New("tool not found")
	ErrInvalidCall       = errors.New("invalid tool call")
)

// ApprovalMode declares whether a Tool call requires explicit interaction.
type ApprovalMode string

const (
	ApprovalNever  ApprovalMode = "never"
	ApprovalAlways ApprovalMode = "always"
)

// Definition is the model-visible, provider-neutral Tool contract.
type Definition struct {
	Key          string          `json:"key"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	ApprovalMode ApprovalMode    `json:"approvalMode"`
}

// Call is one stable Tool intent produced by a Text model.
type Call struct {
	ID        string          `json:"id"`
	ToolKey   string          `json:"toolKey"`
	Arguments json.RawMessage `json:"arguments"`
}

// ExecutionRequest carries a stable call identity for idempotent execution.
type ExecutionRequest struct {
	RunID string
	Call  Call
}

// Receipt records the executor identity and replay disposition.
type Receipt struct {
	ExecutionID string `json:"executionID"`
	Disposition string `json:"disposition"`
}

// ExecutionResult is the JSON Tool output and its durable receipt.
type ExecutionResult struct {
	Content json.RawMessage `json:"content"`
	Receipt Receipt         `json:"receipt"`
}

// Catalog resolves immutable Tool definitions.
type Catalog interface {
	Resolve(string) (Definition, bool)
	List([]string) ([]Definition, error)
}

// Executor executes one stable Tool intent.
type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

// Handler executes one registered Tool.
type Handler interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

// HandlerFunc adapts a function into a Handler.
type HandlerFunc func(context.Context, ExecutionRequest) (ExecutionResult, error)

// Execute calls the adapted handler function.
func (handler HandlerFunc) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	return handler(ctx, request)
}

// Registration binds one immutable definition to its executor.
type Registration struct {
	Definition Definition
	Handler    Handler
}

// Registry is an immutable, explicitly composed Tool catalog and executor.
type Registry struct {
	definitions map[string]Definition
	handlers    map[string]Handler
}

// NewRegistry validates and freezes Tool registrations.
func NewRegistry(registrations []Registration) (*Registry, error) {
	registry := &Registry{
		definitions: make(map[string]Definition, len(registrations)),
		handlers:    make(map[string]Handler, len(registrations)),
	}
	for _, registration := range registrations {
		definition, err := normalizeDefinition(registration.Definition)
		if err != nil || registration.Handler == nil {
			return nil, ErrInvalidDefinition
		}
		if _, duplicate := registry.definitions[definition.Key]; duplicate {
			return nil, ErrDuplicateTool
		}
		registry.definitions[definition.Key] = definition
		registry.handlers[definition.Key] = registration.Handler
	}
	return registry, nil
}

// Descriptor provides the Tool catalog and executor capabilities.
func (registry *Registry) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "tools", Provides: []kernel.Capability{CapabilityCatalog, CapabilityExecutor},
	}
}

// Resolve returns one isolated Tool definition.
func (registry *Registry) Resolve(key string) (Definition, bool) {
	if registry == nil {
		return Definition{}, false
	}
	definition, ok := registry.definitions[strings.TrimSpace(key)]
	return cloneDefinition(definition), ok
}

// List resolves the exact selected Tool set in caller order.
func (registry *Registry) List(keys []string) ([]Definition, error) {
	result := make([]Definition, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, ErrToolNotFound
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		definition, ok := registry.Resolve(key)
		if !ok {
			return nil, ErrToolNotFound
		}
		seen[key] = struct{}{}
		result = append(result, definition)
	}
	return result, nil
}

// Execute dispatches one validated Tool call by stable key.
func (registry *Registry) Execute(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	if registry == nil || !validCall(request.Call) || strings.TrimSpace(request.RunID) == "" {
		return ExecutionResult{}, ErrInvalidCall
	}
	handler, ok := registry.handlers[request.Call.ToolKey]
	if !ok {
		return ExecutionResult{}, ErrToolNotFound
	}
	result, err := handler.Execute(ctx, request)
	if err != nil {
		return ExecutionResult{}, err
	}
	if !validExecutionResult(result) {
		return ExecutionResult{}, ErrInvalidCall
	}
	result.Content = cloneJSON(result.Content)
	return result, nil
}

func normalizeDefinition(definition Definition) (Definition, error) {
	definition.Key = strings.TrimSpace(definition.Key)
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Description = strings.TrimSpace(definition.Description)
	definition.InputSchema = cloneJSON(definition.InputSchema)
	if definition.Key == "" || definition.Name == "" || !json.Valid(definition.InputSchema) ||
		(definition.ApprovalMode != ApprovalNever && definition.ApprovalMode != ApprovalAlways) {
		return Definition{}, ErrInvalidDefinition
	}
	return definition, nil
}

func validCall(call Call) bool {
	return strings.TrimSpace(call.ID) != "" && strings.TrimSpace(call.ToolKey) != "" && json.Valid(call.Arguments)
}

func validExecutionResult(result ExecutionResult) bool {
	return json.Valid(result.Content) && strings.TrimSpace(result.Receipt.ExecutionID) != "" &&
		strings.TrimSpace(result.Receipt.Disposition) != ""
}

func cloneDefinition(definition Definition) Definition {
	definition.InputSchema = cloneJSON(definition.InputSchema)
	return definition
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
