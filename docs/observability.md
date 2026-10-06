# Observability and OpenTelemetry

Agent Runtime separates **structural observations** from telemetry vendors. The
core `observability.Event` type contains lifecycle identity, status, duration,
usage, and error facts but deliberately has no prompt, completion, tool
argument/result, workflow payload, message, or arbitrary attribute field.

## OpenTelemetry adapter

The optional `go/agent-runtime-otel` module converts those events into spans.
It accepts a host-owned `trace.TracerProvider`; it does not create an SDK,
select an exporter, register globals, or own shutdown.

This preserves three boundaries:

1. Runtime correctness never depends on telemetry export success.
2. Hosts keep sampling, resource, batching, OTLP credentials, Collector, and
   provider lifecycle under deployment control.
3. Content remains off by construction unless a future, separately reviewed
   content policy introduces an explicit opt-in path.

### Span model

| Runtime observation | Span name | Parent |
| --- | --- | --- |
| Run | `agent.runtime.run` | Incoming host span when present |
| Model invocation | `agent.runtime.model` | Active Run span when available |
| Tool invocation | `agent.runtime.tool` | Active Run span when available |
| Workflow effect | `agent.runtime.workflow.effect` | Active Run span when available |
| A2A invocation | `agent.runtime.a2a` | Active Run span when available |
| Harness turn | `agent.runtime.harness.turn` | Active Run span when available |

Span names are intentionally low-cardinality. Run IDs, operation IDs, run kind,
attempt, status, and related facts are attributes rather than span-name
components.

The adapter also emits the current OpenTelemetry GenAI attribute names for model
provider/model/response identity and token usage. It does not emit GenAI content
attributes. GenAI semantic conventions remain under active evolution, so these
keys are treated as adapter behavior rather than a new Runtime persistence or
wire contract.

### Recovery behavior

The recorder keeps only active span handles and Run span contexts in process
memory. When it receives a terminal event without the matching start event
(for example after recovery in a new process), it creates and immediately ends
a synthetic span whose start time is `observedAt - duration`. This preserves
latency/error evidence without pretending that an unavailable parent context
survived the restart.

## Host setup

Production hosts should configure an OpenTelemetry SDK and pass its
`TracerProvider` to the adapter. Prefer batched export and an OpenTelemetry
Collector/OTLP pipeline rather than synchronous network export on Runtime
execution paths. Configure service/resource identity, sampling, exporter
endpoints, authentication, redaction, retention, and provider shutdown in the
host.

The adapter intentionally does not set a global provider. This keeps it
compatible with hosts that already instrument HTTP/database clients and avoids
overriding zero-code/eBPF provider choices.

## Metrics and logs

The first adapter surface is tracing. Runtime events already carry the
structural duration and usage facts needed for a later metrics adapter without
changing Kernel state. Logs remain a host concern; correlate host logs with the
active OpenTelemetry context rather than adding arbitrary log payloads to the
Runtime event contract.
