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

The current measurement profile uses an 8,192-token service input ceiling,
revised from 6,144 after full native phase/replay fixtures demonstrated the old
ceiling was insufficient. The large dossier must still be archived and queried.
The saved profile and dataset hash include the changed budget; historical reports
retain their recorded constraints, and comparisons across these profiles are
rejected. See the [phase support note](unreleased-openai-response-phases.md).

The launcher writes JSON and Markdown reports under the ignored
`artifacts/agent-baselines/` directory and can compare compatible JSON reports
offline. Keys travel through stdin to the built binary and do not enter command
arguments, environment fallback or report configuration. Request bodies,
authorization headers and raw network errors are excluded from reports. Failed
preflights now include bounded, credential-redacted response body content and
the SDK's extracted answer in `responseDebug`, displayed directly in the console
and preserved in JSON/Markdown without an extra flag. Body and answer truncation
and incomplete captures are explicit. Existing HTTP diagnostics include an
observed numeric status and a fixed response-kind classification when available. Console and Markdown output explain the failing stage, skipped
samples and the next configuration check. Reports without these optional fields
remain readable; no report schema version or SDK contract change is required.

Failed task samples likewise record the last actual provider HTTP response,
physical request count, last observed status/type and a bounded redacted body.
This is the observed last response, not necessarily the direct cause when a
later call fails locally before dispatch. Opaque encrypted reasoning evidence
is never written into task diagnostics. A one-repetition `retry` override allows
a limited diagnostic run without changing the prior report. There is no new
provider request, SDK API or persistence schema; replay failures still fail
closed and remain distinguishable from generic unsupported responses.

For a failed OpenAI strict-schema probe, optional `wireCheck` observations now
compare the actual outbound native format with the original response message
boundaries and normalized answer. Observation is bounded; `wireCheck` contains
fixed classifications, booleans and counts, while `responseDebug` separately
retains redacted response text for inspection. The observer adds no network call
or body drain, does not alter pass/fail, and cannot assign an upstream failure
to a particular gateway or model implementation. Partial captures remain
explicitly inconclusive. Earlier reports without response text cannot recover
that text after the original process exits.

`python3 scripts/run-agent-baseline.py retry <report.json>` reuses a validated
report's public settings, displays them, checks current execution requirements,
and builds before asking for a fresh hidden key. It creates a new report and
runs the original preflight plus task sequence. It never rewrites or resumes
samples from the previous report, guesses another endpoint, or retrieves a
credential from a report.

The shared provider HTTP adapter now preserves typed dispatch timeouts with the
existing `model.upstream_timeout` code while keeping the raw network cause
private and preserving retryability and caller Context handling. The launcher
uses this code to recommend connectivity checks when no HTTP response was
observed. It does not infer a network phase from elapsed time, change timeout
budgets, or rewrite historical failure codes.

Contributor setup now requires Python 3.9+ for the standard-library launcher
tests in `make check`. No third-party Python dependency is added.
`make agent-baseline` starts the interactive runner;
`make agent-baseline-test` runs its offline checks.

No real-model result is bundled or implied by offline tests. The live task uses
in-memory stores and a controlled lease clock, as before; real process recovery
continues to be established by the separate durable-store integration suite.
See the [baseline guide](../agent-model-baseline.md).
