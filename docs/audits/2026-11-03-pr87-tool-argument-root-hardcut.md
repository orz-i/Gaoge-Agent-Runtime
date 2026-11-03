# PR #87 — independent Provider Tool-argument object-root P1 audit

**Date:** 2026-11-03
**Reviewed immutable PR Head:** `f4798ec7905952dc3d51f8e6e5e811c428d8625f`
**Disposition:** confirmed P1 reproduced and corrected locally; new-Head CI and independent merge admission required.

## Proven physical authorization gap — RED on reviewed source

Both adapters required local Tool arguments to contain *valid* JSON and
rejected duplicate object keys, but did not enforce the Provider wire contract
that OpenAI `function_call.arguments` (decoded JSON string) and Anthropic
`tool_use.input` must each be a JSON **object**. A registered local Tool with
the valid permissive JSON Schema `{}` can also accept an array, `null`,
boolean or scalar value. Consequently, malformed *physical* Provider Tool
input was projected into an executable local `ToolCall`.

New direct-provider tests proved that OpenAI and Anthropic Unary and SSE
responses containing `[1]`, `null` or `true` were accepted with
`err=nil`, an executable ToolCall and even successful terminal SSE
completion. A second independent public **Agent → mock Provider HTTP SSE →
Kernel Memory Store → registered Tool Handler** test supplied a valid first
model reply followed by non-object `[]` Tool input on the second call. Under
the unchanged reviewed PR Head, **OpenAI executed one registered local
Handler (`effects=1`) and Anthropic executed one (`effects=1`)**. Both
requests had real completed Provider IDs and cumulative Usage. The mocks and
handlers were entirely synthetic; no live Provider/gateway credentials or
production tools were used.

## Minimal fail-closed correction

The common `providerhttp.ValidateUniqueJSONObject` requires exact
duplicate-member/depth validity **and** a non-null JSON object at the
physical Tool argument root. Empty `{}` and nested objects remain valid;
arrays, `null`, strings, booleans and numbers are forbidden even if the
registered Tool schema could accept them.

- **OpenAI:** apply root gating during completed function-item verification,
  `response.output_item.done` observation, shared output/replay projection
  and outbound assistant Tool-call encoding.
- **Anthropic:** apply root gating during completed Tool decoding, final SSE
  Tool-block reconstruction, verified signed replay and outbound assistant
  Tool-call encoding. Initial `content_block_start` still validates JSON
  member uniqueness without prematurely classifying a later conflicting
  `input_json_delta`: the already-protected initial `input:null` plus
  replacement fragments is a **framing failure with no invented completion
  receipt**, not a paid semantic rejection.

A completed but semantically invalid local Tool reply with verified cumulative
Usage preserves only its genuine Provider ResponseID/Usage as
`model.post_generation_failed`. No executable ToolCall, transcript content,
opaque replay or successful terminal SSE event survives. Missing/invalid
Usage and broken framing retain their original fail-closed classification.
No Host/router, Agent, Kernel, schema migration, replay version, retry policy,
provider fallback or backward-compatibility bridge was introduced.

## Permanent tests and gates

- `openai/tool_argument_root_test.go` and
  `anthropic/tool_argument_root_test.go`: valid object and invalid root
  types in Unary/SSE, optional and mandatory Tool contexts; Anthropic also
  covers `input_json_delta` producing a nonobject root.
- `internal/providerhttp/json_keys_test.go`: object-root shape and
  duplicate-member rejection, independent of the higher-level adapters.
- `tool_argument_root_agent_test.go`: both Providers exercise a genuine
  registered local Handler and Kernel accounting through public Agent APIs,
  with a permissive JSON Schema. Before fix each branch is RED
  (`effects=1`); after fix each is GREEN (`effects=0`).
- The preexisting Anthropic SSE physical-content and delta-substitution
  regressions stay GREEN, confirming no relaxation of framing/billing rules.

**Final local gates on corrected source (all PASS):**

- Complete Provider `go test ./... -count=1` and `go test -race ./...
  -count=1`, including all new direct and public Agent tests.
- Full repository `make check` **exit 0**: all Go unit/Race/vet/lint
  (strict Provider lint **0 issues**), TypeScript lint/typecheck/**46 tests**/
  build, scenario corpus, benchmarks, OTel/A2A and beta.24 clean-consumer
  release cohort.
- Real PostgreSQL/Redis integration `node scripts/run-integration.mjs`
  **exit 0**; all test containers and the network were removed after the run.
- `make metadata release-check` **exit 0**, `gofmt` and
  `git diff --check` clean.
- The preexisting untracked OpenAI probe scripts remain SHA-256 identical
  and excluded from all commits. No external Provider/gateway secrets, live
  API compatibility or production Tool effects were exercised.

Fresh GitHub CI/security checks on the **new pushed Head** and a distinct
immutable-Head merge admission audit remain mandatory. This repair does
**not** authorize merging PR #87 or publishing beta.24.
