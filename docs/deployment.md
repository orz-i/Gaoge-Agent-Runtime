# Deployment boundary

Gaoge Agent Runtime is an SDK and set of composable adapters, not a standalone
server distribution. The repository therefore does not publish a canonical
application Docker image: there is no single executable, provider credential
model, worker topology, or migration lifecycle that would be correct for every
host.

A production host is responsible for composing and deploying:

- the HTTP adapter and whichever feature modules it exposes;
- model/tool/provider implementations and outbound network policy;
- PostgreSQL migrations and connection lifecycle when durable SQL storage is
  used;
- Redis queue/feed clients and continuation workers when those capabilities are
  enabled;
- OpenTelemetry SDK/exporter/Collector configuration;
- process shutdown, health/readiness, secrets, autoscaling, and rollout policy.

The repository's `docker-compose.test.yml` is test infrastructure only. It
starts PostgreSQL and Redis for real-engine verification and is not a production
deployment manifest.

## Containerized hosts

Containerized applications should build the **host executable** in the host
repository, not wrap this SDK in a generic image. Pin every Agent Runtime Go
module to the same release cohort, run migrations from a host-controlled
migration job, and keep old/new workers from processing the same durable work
unless that mixed-version topology has explicit compatibility evidence.

For telemetry, inject the host's OpenTelemetry provider into
`go/agent-runtime-otel` and export to an OTLP endpoint/Collector. For state and
recovery requirements, follow the
[integration contract](integration-contract.md) and
[reliability evidence map](reliability.md).
