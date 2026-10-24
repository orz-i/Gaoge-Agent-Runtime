# Unreleased note — interactive real-model baseline

This development-tool change adds an interactive real-model baseline for the
existing task-agent example. It changes no exported SDK API, persistence schema,
published module version or provider protocol.

Run `python3 scripts/run-agent-baseline.py` from a source checkout. The script
builds the example and asks for a base URL or complete endpoint, exact model,
explicit token limits and a hidden API key. Interactive root and `/v1` base URLs
are expanded to the selected protocol's endpoint and shown before key entry;
custom complete paths and historical report addresses retain their meaning. It supports the existing OpenAI Responses and
Anthropic Messages adapters. Unknown structured-output models require an
explicit exact-model opt-in followed by a native-schema probe; no capability
fallback is introduced.

Two fixed data variants reuse the existing evidence/draft/recovery oracle.
Reports retain failed and incomplete observations, identify their dataset and
measurement profile, and separate task outcomes from recovery observations.
Token usage includes coverage; a missing receipt is never reported as free
consumption. The probe is measured separately from task samples.

The launcher writes JSON and Markdown reports under the ignored
`artifacts/agent-baselines/` directory and can compare compatible JSON reports
offline. Keys travel through stdin to the built binary and do not enter command
arguments, environment fallback or report configuration. Raw provider errors and
request/response bodies are excluded from reports. Safe preflight diagnostics
include an observed numeric HTTP status and a fixed response-kind classification
when available. Console and Markdown output explain the failing stage, skipped
samples and the next configuration check. Reports without these optional fields
remain readable; no report schema version or SDK contract change is required.

Contributor setup now requires Python 3.9+ for the standard-library launcher
tests in `make check`. No third-party Python dependency is added.
`make agent-baseline` starts the interactive runner;
`make agent-baseline-test` runs its offline checks.

No real-model result is bundled or implied by offline tests. The live task uses
in-memory stores and a controlled lease clock, as before; real process recovery
continues to be established by the separate durable-store integration suite.
See the [baseline guide](../agent-model-baseline.md).
