# PR #58 — P1 HTTP ProviderReplay public projection remediation

Date: 2026-10-21. Branch: `feat/model-provider-adapters`. Scope: public HTTP JSON only.

## Finding and independent reproduction

The optional `model.ProviderReplay` carries provider-owned signed Thinking or
encrypted reasoning evidence for lossless stateless Tool continuation. Core
`agent.ViewState` intentionally strips this field, but the generic HTTP
`SnapshotResponse` previously passed raw Feature State through
`PublicDiagnosticPayload`, which did not recognize the private
`providerReplay` key.

On the pre-fix PR Head `09ae779`, a real in-memory Kernel/Agent/Tool run
with a provider replay fixture confirmed that the HTTP `POST /agent-runs`
response and authorized `GET /runs/:id` both leaked the opaque provider
payload, while the typed `agent.ViewState` did not. A separate JSON/Workbench
fixture reproduced the same bug in nested messages, model invocation requests
and responses, business-content arrays, and generic public projections.

This is a **P1 confidentiality boundary failure for authorized Run readers**,
not an unauthenticated Run access bypass.

## Minimal repair

Add `providerreplay` to the unconditional private-key deletion in
`go/agent-runtime-http/public_diagnostics.go`, using the existing case-insensitive
hyphen/underscore normalization. The recursive scrubber applies to every nested
object (even inside `result`, `content`, `input` and other business data) and
therefore covers the shared HTTP Run snapshot, Agent StartRun snapshot, public
Workbench sections/timeline/topology, generic Run Feed data and Harness
diagnostic projection paths that already use it.

This does **not** change authoritative Kernel Feature State, signed Thinking
bytes, OpenAI encrypted reasoning, Provider API contracts, Tool authorization,
the HTTP schema, or persistence formats. Ordinary user/model text, Tool results
and Run metadata remain readable.

## Regression contract

- `TestProviderReplayRemovedFromPublicStateAndNestedBusinessPayloads`: verifies
  original JSON remains unchanged, all spelling variants are filtered at every
  depth and regular content survives.
- `TestProviderReplayScrubbedFromAllPublicDiagnosticSurfaces`: verifies public
  Workbench sections/topology/timeline and Run Feed; internal source data is
  unchanged.
- `TestAgentHTTPCreateAndAuthorizedReadRedactProviderEvidence`: runs an actual
  Kernel/Agent/Tool loop for both Anthropic and OpenAI replay fixtures,
  POSTs through the Agent HTTP endpoint and GETs the completed Run through
  the authorized core HTTP endpoint. Both public responses exclude private
  Thinking/signatures/encrypted reasoning while preserving visible final text
  and a completed, reloadable Kernel state.

All new tests reproduced the confidentiality failure **before** the code fix
and passed **after** it. Full CI and merge admission must be reassessed on the
new Head; prior Head success does not apply automatically.

## Scope limit

Official OpenAI/Anthropic vendor endpoint production conformance and the
reliability of third-party proxy model aliases are independent acceptance
questions. No live credentials are required for these confidentiality tests.
