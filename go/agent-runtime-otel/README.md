# Agent Runtime OpenTelemetry Adapter

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-otel` converts the
Runtime's content-safe `observability.Event` stream into OpenTelemetry spans.

The adapter deliberately owns **no** OpenTelemetry SDK, exporter, global
provider, collector, or shutdown lifecycle. The host creates a
`trace.TracerProvider` and injects it:

```go
telemetry, err := runtimeotel.New(tracerProvider)
if err != nil {
    return err
}

runner, err := agent.New(agent.Dependencies{
    Runtime: runtime,
    // ...
    Telemetry: []observability.Recorder{telemetry},
})
```

Run spans are parents of model, tool, workflow-effect, A2A, and Harness spans
when their start observations occur in the same process. A terminal observation
without a matching in-memory start still produces a span using the reported
duration, which keeps recovery paths observable after process restarts.

The adapter exports structural IDs, lifecycle state, duration, usage, model
provider/model/response identity, and error codes. It does **not** export
prompts, completions, tool arguments/results, workflow input/output, arbitrary
attribute maps, or other content.

For production, configure the host-owned OpenTelemetry SDK with a batching span
processor and an OTLP exporter/Collector. Standard OpenTelemetry environment
variables such as `OTEL_EXPORTER_OTLP_ENDPOINT` remain host concerns. The
adapter never calls `otel.SetTracerProvider`, so it does not override a host's
global provider or interfere with zero-code/eBPF instrumentation choices.

See [observability guidance](../../docs/observability.md) for the span model,
attribute policy, and operational boundary.
