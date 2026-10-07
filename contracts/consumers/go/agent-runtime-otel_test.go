package consumer

import (
	"testing"

	runtimeotel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var (
	_ func(trace.TracerProvider) (*runtimeotel.Recorder, error) = runtimeotel.New
	_ func(metric.MeterProvider) (*runtimeotel.Metrics, error)  = runtimeotel.NewMetrics
)

func TestOpenTelemetryAdapterPublicConstructors(t *testing.T) {
	t.Parallel()
	if runtimeotel.ErrInvalidTracerProvider == nil || runtimeotel.ErrInvalidMeterProvider == nil {
		t.Fatal("OpenTelemetry adapter validation errors must remain exported")
	}
}
