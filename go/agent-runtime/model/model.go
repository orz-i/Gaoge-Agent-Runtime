// Package model owns provider-neutral text model contracts shared by Agent Runtime features and plugins.
package model

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Role identifies one provider-neutral model message role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one provider-neutral transcript item consumed by a model.
type Message struct {
	Role       Role         `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []tools.Call `json:"toolCalls,omitempty"`
	ToolCallID string       `json:"toolCallID,omitempty"`
}

// Request is one provider-neutral model call.
type Request struct {
	RunID        string             `json:"runID"`
	InvocationID string             `json:"invocationID,omitempty"`
	Model        string             `json:"model,omitempty"`
	ModelOptions json.RawMessage    `json:"modelOptions,omitempty"`
	Messages     []Message          `json:"messages"`
	Tools        []tools.Definition `json:"tools,omitempty"`
	HostedTools  []HostedTool       `json:"hostedTools,omitempty"`
	// RequireToolCall asks the host model adapter to enforce a provider Tool call
	// instead of accepting another text-only response.
	RequireToolCall bool `json:"requireToolCall,omitempty"`
}

// HostedTool is one provider-hosted Tool activation resolved by the host.
// Target is opaque host metadata; Agent Runtime does not interpret provider protocols or payloads.
type HostedTool struct {
	Key    string          `json:"key"`
	Target json.RawMessage `json:"target,omitempty"`
}

// HostedToolCall records one provider-executed Tool fact. It never enters the local Tool executor.
type HostedToolCall struct {
	ID      string          `json:"id,omitempty"`
	ToolKey string          `json:"toolKey"`
	Status  string          `json:"status,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  json.RawMessage `json:"output,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// ArtifactRef is a durable host-owned artifact reference. Binary payloads must not enter Runtime state or result.
type ArtifactRef struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	MediaType string          `json:"mediaType,omitempty"`
	Name      string          `json:"name,omitempty"`
	SizeBytes int64           `json:"sizeBytes,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// Response returns final content, local Tool calls, provider-hosted Tool facts, and durable artifact refs.
type Response struct {
	Content         string           `json:"content,omitempty"`
	ToolCalls       []tools.Call     `json:"toolCalls,omitempty"`
	HostedToolCalls []HostedToolCall `json:"hostedToolCalls,omitempty"`
	Artifacts       []ArtifactRef    `json:"artifacts,omitempty"`
	Citations       []string         `json:"citations,omitempty"`
	ResponseID      string           `json:"responseID,omitempty"`
	Usage           *Usage           `json:"usage,omitempty"`
}

// ReasoningDelta is one provider-neutral reasoning progress observation.
type ReasoningDelta struct {
	EventType string `json:"eventType,omitempty"`
	ItemID    string `json:"itemID,omitempty"`
	Status    string `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Text      string `json:"text,omitempty"`
}

// Usage is one cumulative provider-neutral token usage observation.
type Usage struct {
	InputTokens        int64  `json:"inputTokens,omitempty"`
	OutputTokens       int64  `json:"outputTokens,omitempty"`
	CacheReadTokens    int64  `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens   int64  `json:"cacheWriteTokens,omitempty"`
	CacheWrite5mTokens int64  `json:"cacheWrite5mTokens,omitempty"`
	CacheWrite1hTokens int64  `json:"cacheWrite1hTokens,omitempty"`
	ReasoningTokens    int64  `json:"reasoningTokens,omitempty"`
	Speed              string `json:"speed,omitempty"`
	ServiceTier        string `json:"serviceTier,omitempty"`
	BillingRateClass   string `json:"billingRateClass,omitempty"`
}

// StreamEvent is one provider-neutral live model event.
type StreamEvent struct {
	Delta          string          `json:"delta,omitempty"`
	Reasoning      *ReasoningDelta `json:"reasoning,omitempty"`
	Usage          *Usage          `json:"usage,omitempty"`
	HostedToolCall *HostedToolCall `json:"hostedToolCall,omitempty"`
	ResponseID     string          `json:"responseID,omitempty"`
}

// StreamSink receives one provider-neutral model stream event.
type StreamSink func(StreamEvent) error

// Client is the provider-neutral unary model capability.
type Client interface {
	Generate(context.Context, Request) (Response, error)
}

// StreamingClient optionally exposes real provider stream events while preserving the final Response contract.
type StreamingClient interface {
	GenerateStream(context.Context, Request, func(StreamEvent) error) (Response, error)
}

// ProviderNamer optionally exposes a stable provider identifier for durable
// invocation receipts. Runtime correctness does not require providers to
// implement it.
type ProviderNamer interface {
	ProviderName() string
}

// RetryableError lets provider adapters distinguish transient invocation
// failures without forcing Agent to fail the durable Run. The same logical
// InvocationID is retried.
type RetryableError interface {
	error
	Retryable() bool
}

type retryableError struct{ cause error }

func (err retryableError) Error() string { return err.cause.Error() }
func (err retryableError) Unwrap() error { return err.cause }
func (retryableError) Retryable() bool   { return true }

// NewRetryableError marks a provider error as safe to retry using the same
// durable logical invocation identity.
func NewRetryableError(err error) error {
	if err == nil {
		return nil
	}
	return retryableError{cause: err}
}

// IsRetryableError reports whether a provider/middleware failure explicitly
// opted into durable retry semantics.
func IsRetryableError(err error) bool {
	var retryable RetryableError
	return errors.As(err, &retryable) && retryable.Retryable()
}

// CloneRequest returns an isolated request copy suitable for middleware boundaries.
func CloneRequest(value Request) Request {
	value.ModelOptions = cloneJSON(value.ModelOptions)
	value.Messages = CloneMessages(value.Messages)
	definitions := value.Tools
	value.Tools = make([]tools.Definition, len(definitions))
	for index, definition := range definitions {
		value.Tools[index] = tools.CloneDefinition(definition)
	}
	value.HostedTools = CloneHostedTools(value.HostedTools)
	return value
}

// CloneResponse returns an isolated response copy.
func CloneResponse(value Response) Response {
	value.ToolCalls = cloneToolCalls(value.ToolCalls)
	value.HostedToolCalls = CloneHostedToolCalls(value.HostedToolCalls)
	value.Artifacts = CloneArtifactRefs(value.Artifacts)
	value.Citations = append([]string(nil), value.Citations...)
	value.Usage = CloneUsage(value.Usage)
	return value
}

// CloneUsage returns an isolated usage observation.
func CloneUsage(value *Usage) *Usage {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

// CloneMessages returns isolated message and Tool Call slices.
func CloneMessages(values []Message) []Message {
	result := make([]Message, len(values))
	for index, message := range values {
		result[index] = message
		result[index].ToolCalls = cloneToolCalls(message.ToolCalls)
	}
	return result
}

// CloneHostedTools returns isolated hosted Tool activations.
func CloneHostedTools(values []HostedTool) []HostedTool {
	result := make([]HostedTool, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Target = cloneJSON(item.Target)
	}
	return result
}

// CloneHostedToolCalls returns isolated hosted Tool facts.
func CloneHostedToolCalls(values []HostedToolCall) []HostedToolCall {
	result := make([]HostedToolCall, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Input = cloneJSON(item.Input)
		result[index].Output = cloneJSON(item.Output)
		result[index].Error = cloneJSON(item.Error)
	}
	return result
}

// CloneArtifactRefs returns isolated artifact metadata.
func CloneArtifactRefs(values []ArtifactRef) []ArtifactRef {
	result := make([]ArtifactRef, len(values))
	for index, item := range values {
		result[index] = item
		result[index].Metadata = cloneJSON(item.Metadata)
	}
	return result
}

func cloneToolCalls(values []tools.Call) []tools.Call {
	result := make([]tools.Call, len(values))
	for index, call := range values {
		result[index] = tools.CloneCall(call)
	}
	return result
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
