package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimeotel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel"
	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("OpenTelemetry Collector E2E emission completed")
}

func run() error {
	endpoint := strings.TrimSpace(os.Getenv("TEST_OTEL_HTTP_ENDPOINT"))
	if endpoint == "" {
		endpoint = "127.0.0.1:54318"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
		otlptracehttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(traceExporter))
	defer tracerProvider.Shutdown(context.Background())

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
		otlpmetrichttp.WithTimeout(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("create OTLP metric exporter: %w", err)
	}
	reader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(time.Hour))
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer meterProvider.Shutdown(context.Background())

	traceRecorder, err := runtimeotel.New(tracerProvider)
	if err != nil {
		return err
	}
	metricRecorder, err := runtimeotel.NewMetrics(meterProvider)
	if err != nil {
		return err
	}
	started := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	events := []observability.Event{
		{
			Scope: observability.ScopeRun, Phase: observability.PhaseStarted,
			RunID: "otel-e2e-run", RunKind: kernel.RunKind("agent"), Revision: 1,
			Status: string(kernel.RunStatusRunning), ObservedAt: started,
		},
		{
			Scope: observability.ScopeModelInvocation, Phase: observability.PhaseStarted,
			RunID: "otel-e2e-run", RunKind: kernel.RunKind("agent"),
			OperationID: "otel-e2e-model", Operation: "generate", Provider: "fixture", Model: "fixture-model",
			ObservedAt: started.Add(time.Second),
		},
		{
			Scope: observability.ScopeModelInvocation, Phase: observability.PhaseCompleted,
			RunID: "otel-e2e-run", RunKind: kernel.RunKind("agent"),
			OperationID: "otel-e2e-model", Operation: "generate", Provider: "fixture", Model: "fixture-model",
			ResponseID: "otel-e2e-response", ObservedAt: started.Add(2 * time.Second), Duration: time.Second,
			Usage: runtimebudget.Usage{InputTokens: 8, OutputTokens: 2, TotalTokens: 10},
		},
		{
			Scope: observability.ScopeRun, Phase: observability.PhaseCompleted,
			RunID: "otel-e2e-run", RunKind: kernel.RunKind("agent"), Revision: 2,
			Status: string(kernel.RunStatusCompleted), ObservedAt: started.Add(3 * time.Second), Duration: 3 * time.Second,
		},
	}
	for _, event := range events {
		traceRecorder.Record(ctx, event)
		metricRecorder.Record(ctx, event)
	}
	if err = tracerProvider.ForceFlush(ctx); err != nil {
		return fmt.Errorf("flush traces: %w", err)
	}
	if err = meterProvider.ForceFlush(ctx); err != nil {
		return fmt.Errorf("flush metrics: %w", err)
	}
	return nil
}
