package runtimeotel_test

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimeotel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel"
	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNewMetricsRequiresMeterProvider(t *testing.T) {
	t.Parallel()
	if _, err := runtimeotel.NewMetrics(nil); !errors.Is(err, runtimeotel.ErrInvalidMeterProvider) {
		t.Fatalf("NewMetrics(nil) err = %v", err)
	}
}

func TestMetricsRecordsTerminalOperationsWithoutIdentityCardinality(t *testing.T) {
	t.Parallel()
	metrics, reader, shutdown := newMetrics(t)
	defer shutdown()

	metrics.Record(t.Context(), observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseStarted,
		RunID: "run-secret", OperationID: "invocation-secret", RunKind: kernel.RunKind("agent"),
		ObservedAt: time.Now().UTC(),
	})
	metrics.Record(t.Context(), observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseCompleted,
		RunID: "run-secret", OperationID: "invocation-secret", RunKind: kernel.RunKind("agent"),
		Provider: "openai", Model: "gpt-test", Duration: 2 * time.Second,
		Usage: runtimebudget.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
	})
	metrics.Record(t.Context(), observability.Event{
		Scope: observability.ScopeToolInvocation, Phase: observability.PhaseFailed,
		RunID: "run-other", OperationID: "tool-secret", RunKind: kernel.RunKind("agent"),
		ErrorCode: "tool_error", Duration: 500 * time.Millisecond,
	})

	collected := collectMetrics(t, reader)
	operation := metricByName(t, collected, "agent.runtime.operation.count")
	sum, ok := operation.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("operation data = %T", operation.Data)
	}
	var operationCount int64
	for _, point := range sum.DataPoints {
		operationCount += point.Value
		assertMetricAttributesSafe(t, point.Attributes.ToSlice())
	}
	if operationCount != 2 {
		t.Fatalf("operation count = %d, want 2", operationCount)
	}

	duration := metricByName(t, collected, "agent.runtime.operation.duration")
	histogram, ok := duration.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("duration data = %T", duration.Data)
	}
	var durationCount uint64
	var durationSum float64
	for _, point := range histogram.DataPoints {
		durationCount += point.Count
		durationSum += point.Sum
		assertMetricAttributesSafe(t, point.Attributes.ToSlice())
	}
	if durationCount != 2 || durationSum != 2.5 {
		t.Fatalf("duration count/sum = %d/%v, want 2/2.5", durationCount, durationSum)
	}

	tokens := metricByName(t, collected, "agent.runtime.model.token.usage")
	tokenHistogram, ok := tokens.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("token data = %T", tokens.Data)
	}
	if len(tokenHistogram.DataPoints) != 2 {
		t.Fatalf("token datapoints = %d, want 2", len(tokenHistogram.DataPoints))
	}
	seen := map[string]int64{}
	for _, point := range tokenHistogram.DataPoints {
		attrs := attributeMap(point.Attributes.ToSlice())
		seen[attrs["gen_ai.token.type"].AsString()] = point.Sum
		assertMetricAttributesSafe(t, point.Attributes.ToSlice())
	}
	if seen["input"] != 7 || seen["output"] != 3 {
		t.Fatalf("token sums = %#v", seen)
	}
}

func TestMetricsIgnoresCancelledContextAndZeroDuration(t *testing.T) {
	t.Parallel()
	metrics, reader, shutdown := newMetrics(t)
	defer shutdown()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	metrics.Record(cancelled, observability.Event{
		Scope: observability.ScopeRun, Phase: observability.PhaseCompleted, RunID: "ignored",
	})
	metrics.Record(t.Context(), observability.Event{
		Scope: observability.ScopeRun, Phase: observability.PhaseCompleted, RunID: "recorded",
	})

	collected := collectMetrics(t, reader)
	operation := metricByName(t, collected, "agent.runtime.operation.count")
	sum := operation.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("operation points = %#v", sum.DataPoints)
	}
	if metricExists(collected, "agent.runtime.operation.duration") {
		t.Fatal("zero duration unexpectedly produced duration metric")
	}
}

func newMetrics(t *testing.T) (*runtimeotel.Metrics, *sdkmetric.ManualReader, func()) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	metrics, err := runtimeotel.NewMetrics(provider)
	if err != nil {
		t.Fatal(err)
	}
	return metrics, reader, func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown meter provider: %v", err)
		}
	}
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	return collected
}

func metricByName(t *testing.T, collected metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return metric
			}
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func metricExists(collected metricdata.ResourceMetrics, name string) bool {
	for _, scope := range collected.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return true
			}
		}
	}
	return false
}

func assertMetricAttributesSafe(t *testing.T, values []attribute.KeyValue) {
	t.Helper()
	attrs := attributeMap(values)
	for _, forbidden := range []string{
		"agent.runtime.run.id",
		"agent.runtime.operation.id",
		"gen_ai.response.id",
	} {
		if _, exists := attrs[forbidden]; exists {
			t.Fatalf("metric attributes contain high-cardinality identity %q", forbidden)
		}
	}
}
