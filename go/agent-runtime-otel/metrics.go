package runtimeotel

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const metricsRecorderName = "opentelemetry.metrics"

var ErrInvalidMeterProvider = errors.New("invalid OpenTelemetry meter provider")

// Metrics converts terminal structural runtime observations into low-cardinality
// OpenTelemetry metrics. Run and operation IDs are deliberately excluded from
// metric attributes to avoid unbounded timeseries cardinality.
type Metrics struct {
	operations metric.Int64Counter
	duration   metric.Float64Histogram
	modelToken metric.Int64Histogram
}

// NewMetrics creates a metrics recorder from a host-owned MeterProvider. The
// host remains responsible for SDK readers/exporters, views, resources, and
// shutdown.
func NewMetrics(provider metric.MeterProvider) (*Metrics, error) {
	if provider == nil {
		return nil, ErrInvalidMeterProvider
	}
	meter := provider.Meter(instrumentationName)

	operations, err := meter.Int64Counter(
		"agent.runtime.operation.count",
		metric.WithDescription("Number of terminal Agent Runtime operations."),
		metric.WithUnit("{operation}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create operation counter: %w", err)
	}
	duration, err := meter.Float64Histogram(
		"agent.runtime.operation.duration",
		metric.WithDescription("Duration of terminal Agent Runtime operations."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("create operation duration histogram: %w", err)
	}
	modelToken, err := meter.Int64Histogram(
		"agent.runtime.model.token.usage",
		metric.WithDescription("Input and output token usage for completed model invocations."),
		metric.WithUnit("{token}"),
	)
	if err != nil {
		return nil, fmt.Errorf("create model token histogram: %w", err)
	}
	return &Metrics{operations: operations, duration: duration, modelToken: modelToken}, nil
}

func (*Metrics) Name() string { return metricsRecorderName }

func (metrics *Metrics) Record(ctx context.Context, event observability.Event) {
	if metrics == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(event.RunID) == "" || !terminalPhase(event.Phase) {
		return
	}

	attributes := metricAttributes(event)
	options := metric.WithAttributes(attributes...)
	metrics.operations.Add(ctx, 1, options)
	if event.Duration > 0 {
		metrics.duration.Record(ctx, event.Duration.Seconds(), options)
	}
	if event.Scope != observability.ScopeModelInvocation || event.Phase != observability.PhaseCompleted {
		return
	}

	modelAttributes := append([]attribute.KeyValue(nil), attributes...)
	if event.Provider != "" {
		modelAttributes = append(modelAttributes, attribute.String("gen_ai.provider.name", event.Provider))
	}
	if event.Model != "" {
		modelAttributes = append(modelAttributes, attribute.String("gen_ai.request.model", event.Model))
	}
	if event.Usage.InputTokens > 0 {
		metrics.modelToken.Record(ctx, event.Usage.InputTokens, metric.WithAttributes(append(
			modelAttributes, attribute.String("gen_ai.token.type", "input"),
		)...))
	}
	if event.Usage.OutputTokens > 0 {
		metrics.modelToken.Record(ctx, event.Usage.OutputTokens, metric.WithAttributes(append(
			modelAttributes, attribute.String("gen_ai.token.type", "output"),
		)...))
	}
}

func terminalPhase(phase observability.Phase) bool {
	switch phase {
	case observability.PhaseStarted:
		return false
	case observability.PhaseCompleted, observability.PhaseFailed, observability.PhaseCancelled:
		return true
	default:
		return false
	}
}

func metricAttributes(event observability.Event) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		attribute.String("agent.runtime.scope", string(event.Scope)),
		attribute.String("agent.runtime.phase", string(event.Phase)),
	}
	if event.RunKind != "" {
		attributes = append(attributes, attribute.String("agent.runtime.run.kind", string(event.RunKind)))
	}
	if event.Status != "" {
		attributes = append(attributes, attribute.String("agent.runtime.status", event.Status))
	}
	if event.ErrorCode != "" {
		attributes = append(attributes, attribute.String("error.type", event.ErrorCode))
	}
	if event.Compensation {
		attributes = append(attributes, attribute.Bool("agent.runtime.compensation", true))
	}
	return attributes
}
