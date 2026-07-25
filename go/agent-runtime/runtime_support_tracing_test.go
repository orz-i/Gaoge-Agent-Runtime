package agentruntime

import (
	"context"
	"testing"
)

type traceContextTestKey struct{}

type traceContextTestTracer struct {
	injected  TraceContext
	extracted TraceContext
}

func (t *traceContextTestTracer) TraceID(context.Context) string { return "trace-id" }

func (t *traceContextTestTracer) Start(ctx context.Context, _ string, _ ...LogField) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (t *traceContextTestTracer) Inject(context.Context) TraceContext { return t.injected }

func (t *traceContextTestTracer) Extract(ctx context.Context, value TraceContext) context.Context {
	t.extracted = value
	return context.WithValue(ctx, traceContextTestKey{}, value.TraceParent)
}

func TestEnginePersistsAndRestoresTraceContext(t *testing.T) {
	want := TraceContext{TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", TraceState: "vendor=value"}
	tracer := &traceContextTestTracer{injected: want}
	engine := &Engine{tracer: tracer}
	if got := engine.captureTraceContext(t.Context()); got != want {
		t.Fatalf("captured trace context = %#v", got)
	}
	ctx := engine.restoreTraceContext(t.Context(), want)
	if tracer.extracted != want || ctx.Value(traceContextTestKey{}) != want.TraceParent {
		t.Fatalf("restored trace context = %#v value=%v", tracer.extracted, ctx.Value(traceContextTestKey{}))
	}
}
