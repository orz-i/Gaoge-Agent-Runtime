# 2026-11-01 — Agent/Host intent terminology boundary: local acceptance

- **Repository:** `orz-i/Gaoge-Agent-Runtime`
- **Branch:** `governance/agent-intent-boundary-20261101`
- **Base:** `main@c738b12e3141cb76459a92c591b27b58c4f38599` (PR #82 merged)
- **Target:** Unreleased beta.24 cohort; beta.23 tags remain immutable
- **Decision:** **Local GO for two atomic commits and later independent PR admission**, not permission to merge/release
- **Design:** [ADR 0016](../adr/0016-agent-loop-and-intent-boundary.md)

## Source-derived ownership audit

The current production Go/HTTP/TypeScript Contracts do **not** contain a
generic `IntentRouter`, `PrepareIntent`, `DirectMode`, or
`Runner.Start`. `harness.StartAgentTurn` and its four typed sibling
start entrypoints are explicitly Host-selected. `SessionClient.Prompt`
delegates to the Agent-specific start and is not a task classifier.
`ExecutionClass` is the durable execution owner discriminator;
Provider Router and HTTP cancellation routes perform explicit concrete
execution dispatch, not semantic user-intent selection.

Old `Direct Agent` terminology remained on the *same* Agent loop in
private Harness admission/replay/context helpers, an example variable,
active comments and journal messages. We renamed these to Agent-named
implementations without preserving aliases, removed an unused replay
helper argument and refreshed the public-facing descriptions. New Agent
journal **message text** now reads `Agent loop started/completed`; the
event **types** (`agent.started`, `agent.completed`) and prior durable
journal entries remain unchanged.

The requested `Prepare Intent` has no separate implementation to
remove. **Do not confuse it with execution safety preparation:**

- `PrepareModelRequest` and `ModelRequestPreparer` construct a
  model-visible request before one durable `ModelInvocation` hash/CAS.
  This freezes the Context/Role/Capability Tool projection and original
  Tool Catalog authority. Recovery must never prepare a fresh request
  for a committed pending invocation.
- `PrepareRun` admits an already-selected Agent Run before physical
  Model/Tool dispatch, for deferred child continuation. It has no
  independent user-intent or Direct-mode state machine.
- Agent/Kernel/Feature-owned execution intents, ProviderReplay, original
  Tool Call IDs, cancellation/dispatch grants, and unknown-outcome
  reconciliation remain unchanged and cannot migrate to Host routing.

## Verified gates

| Gate | Result | Evidence on Mac Pi |
| --- | --- | --- |
| `make check` on final code | **PASS, exit 0** | `/tmp/gaoge-agent-intent-hardcut-check-20261101.log` |
| Real Docker PostgreSQL/Redis `make integration` | **PASS, exit 0** | `/tmp/gaoge-agent-intent-hardcut-integration-20261101.log` |
| Agent pre-commit model request frozen across crash/replay | **PASS** | `TestContextProjectionIsSealedBeforeModelIntentAndNotRepeatedOnRestart` |
| Deferred `PrepareRun` invokes no physical Model until Resume | **PASS** | `TestPrepareRunDefersModelUntilIndependentResume` |
| Public Agent and Harness APIs exclude Host intent/mode routing | **PASS** | `TestAgentAPIHasNoHostIntentOrDirectMode`, `TestExplicitAgentStartDoesNotExposeLegacyGenericStart` |
| Portable renamed benchmark smoke | **PASS** | `make benchmark-smoke` and output `BenchmarkHarnessStartAgentTurnMemory` |
| Baseline rekey integrity | **PASS** | `benchmarks/baseline.json` retains `samples=5, nsPerOp=47976, bytesPerOp=62257, allocsPerOp=315` |
| Diff whitespace and symbol audit | **PASS** | `git diff --check`; no old production `DirectAgent` helpers |

The baseline entry rekey is purely a measurement-label change. The
unchanged older reference values are **not** a claim that current
candidate latency/allocations match those values; this pass did not
rebaseline benchmarks or certify a performance improvement. The
`make check` benchmark suite is smoke/report-only, while Race,
Lint, TypeScript and external clean consumer checks are release gates.

## No silent contract changes

- No persisted enum or schema, SQL DDL, Kernel Run state, ModelInvocation
  fingerprint, ProviderReplay, Tool execution receipt, cancellation
  intent, execution epoch or Run identity changed.
- No generic Agent/Direct start alias, Host mode registry, router,
  `PrepareIntent` compatibility facade or second execution loop.
- The only altered event payload is human-readable diagnostic message
  text for **new** Agent started/completed events. Existing journal rows
  are not rewritten, and consumers should use stable event types
  rather than parse display text.
- A historical document may still mention `Direct` in a previous
  milestone name or audit title. This is intentional: ADR 0016 governs
  the **current** SDK contract, not a rewrite of historical evidence.
- The two preexisting untracked Responses probes remain untouched and
  are excluded from the atomic commits.

Independent GitHub PR Head review, strict protected-branch CI and
product Host adoption remain later gates; this document does not
authorize a merge or beta.24 release.
