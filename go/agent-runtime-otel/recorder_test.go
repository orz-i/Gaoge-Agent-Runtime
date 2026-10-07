package runtimeotel_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	runtimeotel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel"
	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewRequiresTracerProvider(t *testing.T) {
	t.Parallel()
	if _, err := runtimeotel.New(nil); !errors.Is(err, runtimeotel.ErrInvalidTracerProvider) {
		t.Fatalf("New(nil) err = %v", err)
	}
}

func TestRecorderBuildsRunTreeAndContentSafeAttributes(t *testing.T) {
	t.Parallel()
	recorder, spans := newRecorder(t)
	started := time.Date(2026, time.October, 6, 12, 0, 0, 0, time.UTC)

	recorder.Record(t.Context(), observability.Event{
		Scope: observability.ScopeRun, Phase: observability.PhaseStarted,
		RunID: "run-1", RunKind: kernel.RunKind("agent"), Revision: 1,
		Status: string(kernel.RunStatusRunning), ObservedAt: started,
	})
	recorder.Record(t.Context(), observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseStarted,
		RunID: "run-1", RunKind: kernel.RunKind("agent"), OperationID: "model-1",
		Operation: "generate", Provider: "openai", Model: "gpt-test", Attempt: 1,
		ObservedAt: started.Add(time.Second),
	})
	recorder.Record(t.Context(), observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseCompleted,
		RunID: "run-1", RunKind: kernel.RunKind("agent"), OperationID: "model-1",
		Operation: "generate", Provider: "openai", Model: "gpt-test", ResponseID: "resp-1", Attempt: 1,
		ObservedAt: started.Add(3 * time.Second), Duration: 2 * time.Second,
		Usage: runtimebudget.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10, ReasoningTokens: 1},
	})
	recorder.Record(t.Context(), observability.Event{
		Scope: observability.ScopeRun, Phase: observability.PhaseCompleted,
		RunID: "run-1", RunKind: kernel.RunKind("agent"), Revision: 2,
		Status: string(kernel.RunStatusCompleted), ObservedAt: started.Add(4 * time.Second),
		Duration: 4 * time.Second,
	})

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(ended))
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, span := range ended {
		byName[span.Name()] = span
	}
	run := byName["agent.runtime.run"]
	model := byName["agent.runtime.model"]
	if run == nil || model == nil {
		t.Fatalf("span names = %v", spanNames(ended))
	}
	if model.Parent().SpanID() != run.SpanContext().SpanID() {
		t.Fatalf("model parent = %s, run span = %s", model.Parent().SpanID(), run.SpanContext().SpanID())
	}
	attrs := attributeMap(model.Attributes())
	if got := attrs["gen_ai.request.model"].AsString(); got != "gpt-test" {
		t.Fatalf("model attribute = %q", got)
	}
	if got := attrs["gen_ai.usage.input_tokens"].AsInt64(); got != 7 {
		t.Fatalf("input tokens = %d", got)
	}
	for key := range attrs {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"prompt", "completion", "arguments", "result", "message", "delta"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("attribute %q exposes forbidden content category %q", key, forbidden)
			}
		}
	}
}

func TestRecorderSynthesizesMissingStartFromDuration(t *testing.T) {
	t.Parallel()
	recorder, spans := newRecorder(t)
	endedAt := time.Date(2026, time.October, 6, 13, 0, 0, 0, time.UTC)

	recorder.Record(context.Background(), observability.Event{
		Scope: observability.ScopeToolInvocation, Phase: observability.PhaseFailed,
		RunID: "run-recovered", OperationID: "tool-1", Operation: "weather",
		ErrorCode: "tool_error", ObservedAt: endedAt, Duration: 1500 * time.Millisecond,
	})

	ended := spans.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	if got := ended[0].StartTime(); !got.Equal(endedAt.Add(-1500 * time.Millisecond)) {
		t.Fatalf("start = %s", got)
	}
	if got := ended[0].EndTime(); !got.Equal(endedAt) {
		t.Fatalf("end = %s", got)
	}
	if got := attributeMap(ended[0].Attributes())["error.type"].AsString(); got != "tool_error" {
		t.Fatalf("error.type = %q", got)
	}
}

func TestRecorderIgnoresDuplicateStarts(t *testing.T) {
	t.Parallel()
	recorder, spans := newRecorder(t)
	event := observability.Event{
		Scope: observability.ScopeWorkflowEffect, Phase: observability.PhaseStarted,
		RunID: "run-1", OperationID: "effect-1", Attempt: 2, ObservedAt: time.Now().UTC(),
	}
	recorder.Record(t.Context(), event)
	recorder.Record(t.Context(), event)
	if got := len(spans.Started()); got != 1 {
		t.Fatalf("started spans = %d, want 1", got)
	}
}

func newRecorder(t *testing.T) (*runtimeotel.Recorder, *tracetest.SpanRecorder) {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown tracer provider: %v", err)
		}
	})
	recorder, err := runtimeotel.New(provider)
	if err != nil {
		t.Fatal(err)
	}
	return recorder, spans
}

func attributeMap(values []attribute.KeyValue) map[string]attribute.Value {
	result := make(map[string]attribute.Value, len(values))
	for _, value := range values {
		result[string(value.Key)] = value.Value
	}
	return result
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}
