// Package runtimeotel adapts content-safe Agent Runtime observations to
// OpenTelemetry spans without owning SDK, exporter, or global provider setup.
package runtimeotel

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	recorderName        = "opentelemetry"
	instrumentationName = "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel"
)

var ErrInvalidTracerProvider = errors.New("invalid OpenTelemetry tracer provider")

type spanKey struct {
	scope        observability.Scope
	runID        string
	operationID  string
	attempt      int
	compensation bool
}

// Recorder translates structural runtime observations into spans. It never
// records prompts, completions, tool arguments/results, workflow payloads, or
// arbitrary user content because those values are absent from observability.Event.
type Recorder struct {
	tracer trace.Tracer

	mu         sync.Mutex
	spans      map[spanKey]trace.Span
	runParents map[string]trace.SpanContext
}

// New creates a Recorder from a host-owned TracerProvider. The adapter does not
// register a global provider or choose an exporter so hosts retain control over
// sampling, batching, resource attributes, OTLP configuration, and shutdown.
func New(provider trace.TracerProvider) (*Recorder, error) {
	if provider == nil {
		return nil, ErrInvalidTracerProvider
	}
	return &Recorder{
		tracer:     provider.Tracer(instrumentationName),
		spans:      make(map[spanKey]trace.Span),
		runParents: make(map[string]trace.SpanContext),
	}, nil
}

func (*Recorder) Name() string { return recorderName }

func (recorder *Recorder) Record(ctx context.Context, event observability.Event) {
	if recorder == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(event.RunID) == "" {
		return
	}
	switch event.Phase {
	case observability.PhaseStarted:
		recorder.start(ctx, event)
	case observability.PhaseCompleted, observability.PhaseFailed, observability.PhaseCancelled:
		recorder.finish(ctx, event)
	}
}

func (recorder *Recorder) start(ctx context.Context, event observability.Event) {
	key := keyFor(event)

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if _, exists := recorder.spans[key]; exists {
		return
	}

	parent := recorder.parentContextLocked(ctx, event)
	options := []trace.SpanStartOption{trace.WithAttributes(eventAttributes(event)...)}
	if !event.ObservedAt.IsZero() {
		options = append(options, trace.WithTimestamp(event.ObservedAt))
	}
	_, span := recorder.tracer.Start(parent, spanName(event.Scope), options...)
	recorder.spans[key] = span
	if event.Scope == observability.ScopeRun {
		recorder.runParents[event.RunID] = span.SpanContext()
	}
}

func (recorder *Recorder) finish(ctx context.Context, event observability.Event) {
	key := keyFor(event)

	recorder.mu.Lock()
	span, exists := recorder.spans[key]
	if exists {
		delete(recorder.spans, key)
	} else {
		parent := recorder.parentContextLocked(ctx, event)
		startedAt := event.ObservedAt
		if event.Duration > 0 {
			startedAt = event.ObservedAt.Add(-event.Duration)
		}
		options := []trace.SpanStartOption{trace.WithAttributes(eventAttributes(event)...)}
		if !startedAt.IsZero() {
			options = append(options, trace.WithTimestamp(startedAt))
		}
		_, span = recorder.tracer.Start(parent, spanName(event.Scope), options...)
	}
	if event.Scope == observability.ScopeRun {
		delete(recorder.runParents, event.RunID)
	}
	recorder.mu.Unlock()

	span.SetAttributes(eventAttributes(event)...)
	setTerminalStatus(span, event)
	if event.ObservedAt.IsZero() {
		span.End()
		return
	}
	span.End(trace.WithTimestamp(event.ObservedAt))
}

func (recorder *Recorder) parentContextLocked(ctx context.Context, event observability.Event) context.Context {
	if event.Scope == observability.ScopeRun {
		return ctx
	}
	if parent, ok := recorder.runParents[event.RunID]; ok && parent.IsValid() {
		return trace.ContextWithSpanContext(ctx, parent)
	}
	return ctx
}

func keyFor(event observability.Event) spanKey {
	return spanKey{
		scope: event.Scope, runID: event.RunID, operationID: event.OperationID,
		attempt: event.Attempt, compensation: event.Compensation,
	}
}

func spanName(scope observability.Scope) string {
	switch scope {
	case observability.ScopeRun:
		return "agent.runtime.run"
	case observability.ScopeModelInvocation:
		return "agent.runtime.model"
	case observability.ScopeToolInvocation:
		return "agent.runtime.tool"
	case observability.ScopeWorkflowEffect:
		return "agent.runtime.workflow.effect"
	case observability.ScopeA2AInvocation:
		return "agent.runtime.a2a"
	case observability.ScopeHarnessTurn:
		return "agent.runtime.harness.turn"
	default:
		return "agent.runtime.operation"
	}
}

func eventAttributes(event observability.Event) []attribute.KeyValue {
	attributes := make([]attribute.KeyValue, 0, 24)
	attributes = append(attributes,
		attribute.String("agent.runtime.scope", string(event.Scope)),
		attribute.String("agent.runtime.run.id", event.RunID),
	)
	if event.RunKind != "" {
		attributes = append(attributes, attribute.String("agent.runtime.run.kind", string(event.RunKind)))
	}
	if event.Revision > 0 {
		attributes = append(attributes, attribute.Int64("agent.runtime.run.revision", int64(event.Revision)))
	}
	if event.Status != "" {
		attributes = append(attributes, attribute.String("agent.runtime.status", event.Status))
	}
	if event.OperationID != "" {
		attributes = append(attributes, attribute.String("agent.runtime.operation.id", event.OperationID))
	}
	if event.Operation != "" {
		attributes = append(attributes, attribute.String("agent.runtime.operation.name", event.Operation))
	}
	if event.Attempt > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.attempt", event.Attempt))
	}
	if event.Compensation {
		attributes = append(attributes, attribute.Bool("agent.runtime.compensation", true))
	}
	if event.ChildRunID != "" {
		attributes = append(attributes, attribute.String("agent.runtime.child_run.id", event.ChildRunID))
	}
	if event.ErrorCode != "" {
		attributes = append(attributes, attribute.String("error.type", event.ErrorCode))
	}
	if event.Duration > 0 {
		attributes = append(attributes, attribute.Float64("agent.runtime.duration", event.Duration.Seconds()))
	}

	usage := event.Usage
	if usage.LLMCalls > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.usage.llm_calls", usage.LLMCalls))
	}
	if usage.ToolCalls > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.usage.tool_calls", usage.ToolCalls))
	}
	if usage.OutputBytes > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.usage.output_bytes", usage.OutputBytes))
	}
	if usage.StateBytes > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.usage.state_bytes", usage.StateBytes))
	}
	if usage.ChildRuns > 0 {
		attributes = append(attributes, attribute.Int("agent.runtime.usage.child_runs", usage.ChildRuns))
	}
	if usage.CostUnits > 0 {
		attributes = append(attributes, attribute.Int64("agent.runtime.usage.cost_units", usage.CostUnits))
	}

	if event.Scope == observability.ScopeModelInvocation {
		if event.Provider != "" {
			attributes = append(attributes, attribute.String("gen_ai.provider.name", event.Provider))
		}
		if event.Model != "" {
			attributes = append(attributes, attribute.String("gen_ai.request.model", event.Model))
		}
		if event.ResponseID != "" {
			attributes = append(attributes, attribute.String("gen_ai.response.id", event.ResponseID))
		}
		if usage.InputTokens > 0 {
			attributes = append(attributes, attribute.Int64("gen_ai.usage.input_tokens", usage.InputTokens))
		}
		if usage.OutputTokens > 0 {
			attributes = append(attributes, attribute.Int64("gen_ai.usage.output_tokens", usage.OutputTokens))
		}
		if usage.CacheReadTokens > 0 {
			attributes = append(attributes, attribute.Int64("gen_ai.usage.cache_read.input_tokens", usage.CacheReadTokens))
		}
		if usage.CacheWriteTokens > 0 {
			attributes = append(attributes, attribute.Int64("gen_ai.usage.cache_creation.input_tokens", usage.CacheWriteTokens))
		}
		if usage.ReasoningTokens > 0 {
			attributes = append(attributes, attribute.Int64("gen_ai.usage.reasoning.output_tokens", usage.ReasoningTokens))
		}
	}
	return attributes
}

func setTerminalStatus(span trace.Span, event observability.Event) {
	switch event.Phase {
	case observability.PhaseStarted, observability.PhaseCompleted:
		return
	case observability.PhaseFailed:
		code := event.ErrorCode
		if code == "" {
			code = "runtime_failure"
			span.SetAttributes(attribute.String("error.type", code))
		}
		span.SetStatus(codes.Error, code)
	case observability.PhaseCancelled:
		code := event.ErrorCode
		if code == "" {
			code = "cancelled"
			span.SetAttributes(attribute.String("error.type", code))
		}
		span.SetStatus(codes.Error, code)
	}
}
