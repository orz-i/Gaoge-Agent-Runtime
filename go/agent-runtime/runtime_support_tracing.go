package agentruntime

import "context"

type noopSpan struct{}

func (noopSpan) End()                      {}
func (noopSpan) SetAttributes(...LogField) {}
func (noopSpan) RecordError(error)         {}

func (s *Engine) startSpan(ctx context.Context, name string, fields ...LogField) (context.Context, Span) {
	if s != nil && s.tracer != nil {
		return s.tracer.Start(ctx, name, fields...)
	}
	return ctx, noopSpan{}
}

func (s *Engine) traceID(ctx context.Context) string {
	if s != nil && s.tracer != nil {
		return s.tracer.TraceID(ctx)
	}
	return ""
}

func recordSpanError(span Span, err error) {
	if span != nil && err != nil {
		span.RecordError(err)
	}
}
