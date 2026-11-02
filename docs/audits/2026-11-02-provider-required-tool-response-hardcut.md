# 2026-11-02 — Provider required Tool response hard cut (local governance)

- **Repository:** `orz-i/Gaoge-Agent-Runtime`
- **Branch:** `governance/provider-required-tool-20261102`
- **Base:** `main@340bdafe31dbf021003b64b6db297b7b80fbebc1` (PR #86 merged)
- **Scope:** OpenAI Responses / Anthropic Messages standalone adapters, unary + SSE
- **Disposition:** Local repair candidate only; independent audit, PR and merge are separate gates

## Finding and independent RED

`model.Request.RequireToolCall` was forwarded to the upstream API as
OpenAI `tool_choice="required"` or Anthropic `tool_choice.type="any"`.
Both `Generate` and `GenerateStream` then trusted any successfully
decoded terminal response, without verifying that it contained a Tool call.
A gateway or mock provider that ignores the outbound force option could
return a text-only `completed` / `end_turn` response and receive
`err=nil`. The SSE path also emitted a terminal `ResponseID` / usage
event, incorrectly suggesting the requested Tool obligation was fulfilled.

Two public, offline HTTP/SSE tests in
`openai/forced_tool_response_test.go` and
`anthropic/forced_tool_response_test.go` initially failed in both
unary and streaming modes:

- forced Tool + ordinary text: unexpectedly successful, with nonempty
  `ResponseID`; streaming also emitted one final receipt
- expected behavior: fail closed, no accepted Result/terminal receipt
- non-forced text and recognizable safety refusals were kept as explicit
  positive cases; real local Tool execution was added as a positive case

Both tests were **RED on the original code** before the production edit.
Fixture data contains no API key, real network calls, external Tools or
user conversations.

## Minimal correction

At the already-established final response decode boundary, both adapters
now require `len(response.ToolCalls) > 0` for
`RequireToolCall && !response.Refused`, **before returning success
or emitting a terminal stream receipt**. A missing call is a nonretryable `ErrUnsupported`, not a synthetic Tool
call or blind provider fallback. If trustworthy cumulative Usage and
ResponseID are present, it retains the original Model Response under the
existing `model.ResponseRejectedError` protocol, classified with the
established `model.post_generation_failed` code. Agent then commits and
consumes that exact known-completed, rejected ModelInvocation and Usage once.
Without trustworthy Usage, the adapter still returns empty Response plus
plain `ErrUnsupported`; there is no fabricated completion. This is
restricted to the Provider adapters and the shared internal cumulative-Usage
validator; Core ModelInvocation, Kernel, Agent Loop, durable intents/receipts,
Context, OpenAI encrypted reasoning replay and Anthropic signed reasoning
remain unchanged.

An explicit provider refusal remains a final
`Response{Refused:true}` rather than being misrepresented as a successful
Tool execution or being silently turned into an automatic retry.
A valid local function call is still accepted, including when
`RequireToolCall` is set. Incremental SSE text deltas may already
have been emitted while waiting for the terminal frame; they are
observational only, not durable completion evidence.

There is no new configuration, model family routing, compatibility bridge,
Store schema, SQL DDL or second execution loop. The stricter result
contract is intentionally a pre-stable beta.24 breaking correction,
recorded in `docs/breaking-changes.md`.

## First independent P1 audit and bounded fix — completed rejection accounting

A subsequent independent audit found a **P1**: after an upstream completed
HTTP/SSE response supplied `ResponseID` and valid cumulative Usage but ignored
mandatory Tool choice, both adapters returned **empty `model.Response`** plus
plain `ErrUnsupported`. Agent therefore failed the Run but left the second
`ModelInvocation` in **pending**, with its completed provider attempt and
billable Usage neither persisted nor consumed.

Two independent Provider-layer tests and a public Agent → routed Provider →
Kernel regression reproduced this under `go test -race` using only local
HTTP fixtures. The Agent fixture sends an optional first request (which
receives ordinary text), then a mandatory Tool request (which the gateway
ignores). Both completed physical requests consumed 17 input and 5 output
tokens, but the old implementation recorded only 1 Model call and 22 tokens
rather than 2 Model calls and 44 tokens. The Run became `failed` with its
second completed physical attempt still marked `pending`.

**Remediation:** The existing final Tool-result proof runs before emitting
terminal SSE success. When the provider response is fully decoded but
has no required Tool call, a small shared internal validity check confirms
that cumulative `model.Usage` is present, nonnegative and nonoverflowing.
For known completion, the adapter returns the original `ResponseID` and
Usage **with** `model.NewResponseRejectedError` and the existing
`model.post_generation_failed` failure code. Agent's first-party receipt
protocol already handles it: durable terminal receipt, Usage accounting
once, `ModelInvocationConsumed`, and terminal failure without local Tool
execution or automatic blind retry. No Agent/Kernel changes or new
persisted fields are required. Invalid/missing Usage fails closed with plain
`ErrUnsupported` and an empty response; interim SSE deltas never become
successful terminal receipts. Explicit safety refusals and legal Tool Calls
continue through the original path.

The permanent tests cover real completed usage and no-usage/partial/negative/
overflow cases in unary and SSE for both providers, the cumulative-usage
validity predicate, and the public Agent two-request accounting path. The
P1 tests were **RED before repair** and **GREEN after repair**. The independent
audit also tested an isolated what-if overlay of this exact contract change
before any production edits; all three provider/Agent packages passed Race
under that overlay. The final implementation passed all scoped and full gates.

**Final P1 repair verification (post-code-fix):**

- The two permanent Provider regression suites passed, including real
  completed `ResponseID`/Usage and valid Tool/refusal positive paths for
  both unary and SSE.
- Missing, partially reported, negative or overflowing cumulative Usage
  cases passed for both providers and modes; the shared internal Usage
  validator independently passed zero/valid/negative/overflow edge cases.
  Missing proof never creates a `ResponseRejectedError` or SSE terminal ACK.
- The public Agent + real mock-HTTP Provider end-to-end test passed under
  `go test -race`. The test reloads the committed Kernel Run and verifies
  **two** exactly-once `ModelInvocationConsumed` records, one execution
  attempt each, two Provider HTTP requests, **34 input + 10 output = 44**
  counted tokens, no local Tool execution, and terminal failure code
  `model.post_generation_failed` (not an unconsumed pending invocation).
- **PASS:** focused provider/Agent replay and rejection tests under
  `go test -race -count=10`; existing Agent terminal-repair/restart tests
  under Race; standalone Provider module unit tests and Lint (0 issues).
- **PASS, exit 0:** complete repository `make check` on the final source,
  including Go ordinary/Race/Lint/vet, TypeScript lint/typecheck/**46 tests**,
  benchmark/evaluation smoke, OTel, A2A and beta.24 clean-consumer gate.
- **PASS, exit 0:** real PostgreSQL 16 / Redis 8
  `node scripts/run-integration.mjs --external-services` on isolated Docker
  services and dynamic local ports, including cancellation and cross-process
  continuation/restart; temporary containers were removed.
- **PASS:** Go formatting, relative document links, `git diff --check`.

These are **local** verification results; they do not substitute for a fresh
independent code audit or required GitHub CI on a PR Head.

## Second independent P1 audit — OpenAI phase-scoped terminal refusal

The next independent audit found a separate **P1** in the forced-Tool
exception for OpenAI Responses. `projectOutput` deliberately sets
`Response.Refused=true` whenever **any** assistant message has a refusal,
including a `commentary` message preceding a different `final_answer`.
The new forced-Tool verifier originally treated this global safety flag as
authorization to return success **without a Tool call**, even when the
selected `final_answer` was ordinary text. The public OpenAI unary client
returned `err=nil`, while streaming additionally emitted a terminal
successful `ResponseID`/Usage event. Both were reproduced **RED under Race**
using a temporary audit overlay without modifying production files.

**Minimal fix:** The original global `Response.Refused` remains unchanged
so no safety evidence is hidden. The private `terminalSafetyRefusal` helper
checks the **already decoded and phase-validated original provider Output**:
only the selected final message containing exclusively explicit refusal
parts may waive the required Tool call. A prior commentary refusal or a
final response mixing refusal and ordinary text cannot grant this
exception. For unphased legacy output, use the last visible message as the
terminal one. Non-qualifying responses preserve the previous P1 repair's
known-completion Usage/`ResponseID` and existing `ResponseRejectedError`
classification when Usage is trustworthy, without emitting a successful
terminal SSE event. Unknown/invalid Usage still fails closed as before.
No new public field, persisted type, Agent policy, execution loop or
compatibility adapter was added.

The two independent unary/SSE audit reproducers are now permanent
regressions in `openai/forced_tool_phase_owner_test.go`: commentary refusal
followed by ordinary final text must be rejected, while a real final
refusal must remain allowed. Additional negative/positive cases cover
mixed refusal and ordinary text in one final message, unphased legacy
refusal followed by ordinary text, and a legitimate terminal legacy
refusal. The failing pair was **RED before fix / GREEN after fix**, with
complete Provider module tests passing. This patch was also validated
independently in a temporary alternative-code overlay before the real
source edit; the relevant phase, refusal and forced-Tool tests passed
there under Race. The terminal classification is restricted to the OpenAI
Responses adapter; Anthropic Messages has its separate explicit
`stop_reason=refusal` proof and was left untouched.

**Final verification after the second P1 repair:**

- **RED before correction / GREEN after correction:** two independent
  public OpenAI Unary/SSE reproducers for commentary refusal plus ordinary
  `final_answer`, with a distinct valid terminal-refusal control case.
- **PASS:** additional positive/negative legacy-last-message, final mixed
  refusal-and-text and commentary-plus-final-refusal cases under Race.
- **PASS:** forced Tool, completed-rejection/Usage accounting, OpenAI
  encrypted reasoning replay, Anthropic signed replay and Agent terminal
  repair/restart regressions under repeated `go test -race -count=10` for
  the focused Provider set and `-count=3` for Agent repair tests. Standalone
  Provider complete unit tests passed; Provider Lint **0 issues**.
- **PASS, exit 0:** full repository `make check` on the final source,
  including all Go unit/Race/Lint/vet, TypeScript lint/typecheck/**46 tests**
  /build, OTel, A2A, benchmark/evaluation smoke and beta.24 clean consumers.
- **PASS, exit 0:** isolated PostgreSQL 16 / Redis 8
  `node scripts/run-integration.mjs --external-services` on dynamic local
  ports, including cross-connection cancellation, execution recovery and
  Redis continuation. Temporary Docker test containers were removed.
- **PASS:** `gofmt`, documentation links, whitespace and `git diff --check`.
  The two pre-existing untracked OpenAI probe scripts remain unchanged.

Both full gates were **rerun after** the terminal-refusal production edit;
this result does not rely on the earlier passing run from before the second
P1 fix. The branch remains an uncommitted local implementation: independent
PR Head review and GitHub-required CI are separate admission stages.

## Third independent P1 audit — terminal refusal role/status binding

The subsequent read-only audit identified a further **P1 ownership gap**
in the same OpenAI terminal-refusal exception. A provider `message` with
no phase and an explicitly foreign `role` (`user`, `system`, `tool`) could
be projected as a refusal and thus permit a Tool-free completion under
`RequireToolCall`. Likewise, a `final_answer` refusal item with explicit
`status=in_progress` or `status=incomplete` was accepted when the outer
Responses envelope reported completion. Both Unary and SSE returned success;
SSE additionally emitted the misleading terminal success receipt. These
cases were reproduced under `go test -race` through independent read-only
test overlays with **no modification to the candidate source**.

**Bounded correction:** Keep the existing global `Response.Refused` safety
evidence and phase projection unchanged. The private OpenAI
`terminalSafetyRefusal` helper now uses the raw selected message to prove
that, **if present**, `role` is `assistant` and `status` is `completed`.
Explicit `null`, empty, non-assistant role or unfinished status are not
proof, whereas genuinely omitted fields retain the established unphased
legacy behavior. `json.RawMessage` preserves field presence and avoids
conflating an absent key with a present-but-empty value. This check only
authorizes the specific forced-Tool safety refusal exception; ordinary
non-forced outputs keep their original decoding semantics.

The independent role and status tests were copied into permanent
`openai/forced_tool_phase_owner_test.go` and reproduced **RED before
repair**. After the role/status fix they passed **GREEN under Race**, along
with the earlier commentary/final-answer and legitimate refusal cases.
Additional negative tests for explicitly `null` and empty field values
were first **RED under the initial role/status prototype**, then **GREEN**
after the presence-sensitive proof. Valid assistant/completed and actually
omitted legacy fields remain allowed. Missing Tool completions still use
existing `ResponseRejectedError` with original ResponseID/Usage when
verified; SSE never emits a terminal *success* event on a rejected output.

No public Model API, persisted schema, new status machine, compatibility
adapter or Host-level router was added. The two pre-existing untracked
OpenAI provider probe scripts are excluded and their SHA-256 hashes are
checked unchanged.

**Final post-third-audit local verification:**

- Independent role/status regression tests reproduced **RED before patch**,
  then **GREEN** with both Unary and SSE. A second presence-sensitivity
  check reproduced **RED** for explicit null/empty role/status under the
  initial prototype, then **GREEN** when raw field presence was bound.
  Legacy omitted fields, valid `assistant`/`completed` terminal refusals,
  earlier commentary refusal protection and mixed final text guards pass.
- **PASS:** Provider/Agent forced-Tool, completed-rejection and replay
  regressions under `go test -race -count=10`, with existing Agent
  rejection/restart tests at Race count 3; full Provider unit suite and
  Lint (0 issues).
- **PASS, exit 0:** final full repository `make check` on the post-fix
  source: Go unit/Race/vet/Lint, TypeScript lint/typecheck/**46 tests**/build,
  OTel, A2A, Benchmark Smoke and beta.24 local clean consumers.
- **PASS, exit 0:** isolated real PostgreSQL 16 / Redis 8
  `node scripts/run-integration.mjs --external-services`, including
  cross-connection cancellation, recovery and Redis continuation.
  Temporary Docker services were removed after completion.
- **PASS:** `gofmt`, documentation relative links/whitespace,
  `git diff --check` and exact uncommitted change-scope verification.

The two full gates were **rerun after** the presence-sensitive source
change. These local results do not replace a subsequent independent
code review, PR Head verification or GitHub-required CI.

## Fourth independent P1 audit — physical function_call item status

The final read-only audit identified a separate **P1 unauthorized physical
Tool execution**: OpenAI `projectOutput` admitted `function_call` items by
name, CallID and JSON arguments alone, ignoring explicit per-item
`status=in_progress` or `status=incomplete`, even when the enclosing
Responses object was `completed`. Both Unary and SSE reported success;
SSE emitted terminal success evidence. More importantly, an independent
public Agent + mock-HTTP Provider test showed one real local Tool Handler
execution and a `completed` Agent Run for each unfinished item status.
The same vulnerability reproduced on the unchanged `main@340bdafe` base,
so it predates this provider governance branch. An independent, isolated
what-if patch was tested under Race before any repair.

**Bounded repair:** In the OpenAI adapter, validate **every** physical
`function_call` item, whether or not `RequireToolCall` is set, before
`projectOutput` parses executable Tool arguments or Agent can queue Tool
execution. An item with explicit `status=completed`, or genuinely omitted
status for legacy output, is allowed. Explicit `in_progress`, `incomplete`,
null/empty, unknown or otherwise non-completed status invalidates the
entire candidate and releases **zero ToolCalls, arguments, contents or
ProviderReplay**. On a finished response with verifiable Usage, return
only the original `ResponseID` and cumulative Usage with existing typed
`model.post_generation_failed` / `model.ResponseRejectedError` evidence for
one durable accounting event. With unknown or invalid Usage, return empty
Response with plain nonretryable `ErrUnsupported`; no invented completion
or terminal SSE success. This check intentionally precedes Tool argument
JSON validation, so even unfinished malformed arguments cannot discard a
known paid completion. No Agent, Kernel, SQL, public API or retry protocol
was changed.

The two direct tests and the public Agent side-effect test were copied
from the independent audit into permanent regressions. All were **RED on
the unmodified candidate** and **GREEN after repair**. Further checks
cover explicit null/empty/unknown item statuses, optional and forced
local Tool choice, mixed completed/incomplete calls, unfinished invalid
JSON arguments, unknown Usage and both transports. The real-store Agent
test verifies that two physical model requests lead to two durable and
consumed receipts, 34 input + 10 output = 44 Token accounting units,
zero Tool execution and no unauthorized replay payload. Valid completed
and omitted-status legacy Tool calls still succeed.

This is a pre-stable beta.24 hard cut, documented in
`docs/breaking-changes.md`; no compatibility shim or new execution route.
**Final post-fourth-audit verification:**

- **RED on original `main` and pre-repair candidate:** explicit
  `function_call.status=in_progress` / `incomplete` was accepted in unary
  and SSE. The public Agent test actually executed a local Tool Handler
  once and returned a completed Run. Optional Tool calls reproduced the
  same unsafe authorization. No real Provider or external business Tool
  was used.
- **GREEN after bounded repair:** direct optional/mandatory Tool output
  tests, public Agent side-effect regression, explicit null/empty/unknown
  status, mixed valid/unfinished calls and unfinished malformed arguments.
  Valid completed and omitted-status legacy Tool calls remain accepted.
  For known Usage, the response retains only original ResponseID/Usage and
  a typed rejected completion; public Agent reloads the durable Kernel
  Store and confirms two exactly-once consumed ModelInvocations and
  **34 input + 10 output = 44** counted tokens, with zero Tool execution.
- **Independent mutation audit:** an isolated Go overlay that removed the
  item-status fence caused the direct, partial-argument and public Agent
  safety tests to fail. The unmodified candidate passed those same tests.
- **PASS:** focused OpenAI, Anthropic, common Usage and Agent/Provider
  replay tests under `go test -race -count=10`; durable Agent rejection
  and restart tests under Race count 3; Provider Lint 0 issues.
- **PASS, exit 0:** full repository `make check` on the final source,
  including all Go unit/Race/vet/Lint, TypeScript lint/typecheck/**46 tests**
  /build, benchmark smoke, OTel/A2A and beta.24 clean-consumer gate.
- **PASS, exit 0:** real PostgreSQL 16 / Redis 8 isolated external-service
  integration with cross-connection cancellation, persistence/recovery and
  Redis continuation. Test containers were removed afterward.
- **PASS:** Go formatting, documentation local links/whitespace,
  `git diff --check` and original untracked OpenAI probe integrity checks.

These are local results and a read-only independent pre-PR code audit, not
GitHub CI or approval of an uncreated PR. No commit/push, branch cleanup or
release operation is included in this governance slice.

## Verification

- **RED baseline:** `go test -count=1 -run
  '^TestForcedToolResponseMustActuallyContainToolCall$'
  ./openai ./anthropic`, both adapters failed for Unary and SSE
  forced/plain-text responses, with an invalid terminal receipt on SSE
- **GREEN after correction:** same tests passed, covering forced/real Tool,
  non-forced text and forced/explicit safety refusal
- **PASS:** `go test -count=1 ./...` in the Provider module
- **PASS:** `go test -race -count=4 ./openai ./anthropic`, and repeated
  targeted Provider negative/positive tests under `go test -race -count=10`
- **PASS:** Provider `golangci-lint run ./...`, 0 issues
- **PASS, exit 0:** final full-repository `make check`: Go workspace
  unit/race/lint/vet, TypeScript lint/typecheck/46 tests/build, OTel,
  A2A, benchmark/evaluation smoke and beta.24 clean-consumer checks
- **PASS, exit 0:** `node scripts/run-integration.mjs --external-services`
  against isolated PostgreSQL 16 and Redis 8 with dynamically assigned host
  ports: cross-connection cancellation, Handoff, continuation and Redis
  recovery suites; temporary services were removed after the check
- **PASS:** `gofmt`, documentation local links and `git diff --check`.
  Only the two existing untracked OpenAI probe scripts were left untouched.

**Unverified:** Real OpenAI/Anthropic credentials, third-party gateways,
production Hosted/mobile deployment, beta.24 publication/tagging.
These require separate explicit live compatibility acceptance.

**Worktree discipline:** The two original untracked
`scripts/probe_openai_responses.py` and
`scripts/test_probe_openai_responses.py` are excluded from this work.
No commit/push/PR is part of this local governance slice.

## PR #87 independent post-CI P1: completed malformed Tool still loses paid Usage

A later independent read-only merge gate reproduced a remaining P1 despite
all required GitHub checks passing. On PR Head `51390b5`, a completed OpenAI
Responses envelope with valid ID and cumulative Usage, and a physical
`function_call` whose own status was `completed` but arguments were malformed
JSON, failed downstream `projectOutput()` parsing. The adapter returned an
empty response and plain `ErrUnsupported`, leaving a paid ModelInvocation
pending. A public Agent + mock SSE + real in-memory Kernel Store test observed
two completed physical model calls but only the first consumed and accounted:
17 input/5 output (22 total) rather than 34 input/10 output (44 total).
The invalid local Tool did not execute. See the superseding
[blocking PR audit](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/87#issuecomment-6570010105).

**Follow-up fix on the same PR branch:** After validating an independently
completed outer provider response and its cumulative Usage, a shared internal
post-generation rejection helper now sanitizes **invalid physical Tool items**
to original `ResponseID` plus cloned Usage only, with typed
`model.post_generation_failed`. OpenAI catches completed malformed Tool
arguments and unrecognized Tool names **before** output projection. Other
non-Tool and encrypted replay errors retain their previous classification.
Anthropic analogs were independently reproduced for malformed Tool IDs and
unknown Tool names: completed unary/SSE messages must preserve their known
billing even when final stream block reconstruction fails. The Anthropic
adapter now retains minimal receipts across these post-completion failures.
Unknown Usage, unfinished envelopes, malformed pre-completion SSE and invalid
final Usage never mint a completed receipt. No ToolCalls, arguments, content,
replay or successful SSE terminal event are emitted on these rejections.

**Permanent RED→GREEN evidence:**

- `TestCompletedMalformedToolStillConsumesPaidAgentInvocation`: 2 physical
  OpenAI model completions; exactly 2 consumed durable ModelInvocations and
  34/10/44 token totals, zero Tool effects, rejected failure code.
- OpenAI `TestUnfinishedFunctionItemWithMalformedArgumentsNeverExecutesOrLosesReceipt`:
  completed malformed item with and without Usage, unary and SSE. The
  before-fix test failed for both transports; corrected implementations pass.
- Anthropic `TestCompletedMalformedToolRetainsAnthropicUsageReceipt`: missing
  Tool ID and unknown Tool name in unary/SSE, with and without cumulative
  Usage; before-fix known-Usage cases failed, corrected implementations pass.
- Shared `TestRejectCompletedResponseNeverExposesInvalidProviderOutput`:
  only valid final identity/Usage survive; no invented completion or
  mutable Usage alias.

**Final-source quality gates:** Full `make check` passed (exit 0), including
Go workspace unit/Race/Vet/Lint, 46 TypeScript tests, benchmark, A2A, OTel
smoke and beta.24 clean-consumer tests. Real PostgreSQL/Redis integration via
`node scripts/run-integration.mjs` passed (exit 0), including independent
connections, cancellation, recovery and Redis continuation; the dedicated
Docker Compose containers and network were removed after verification.

Any subsequent pushed Head requires a fresh independent merge-admission audit
and GitHub required checks on that exact Head. This repair is not permission
to merge or publish beta.24.

## PR #87 follow-up P1 — completed mixed Tool output still loses paid receipt

An [independent review of Head `1f4ee3a`](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/87#issuecomment-6571095423)
found a separate paid completion gap. OpenAI `decodeResponse` correctly
validated a completed physical `function_call` with valid ID/name/JSON but
then returned an **empty** Response for `projectOutput` failures caused by a
contradictory sibling message (refusal or `final_answer`) or unrecognized
output item. For the completed second Provider request with 17 input/5
output tokens, the public Agent stored only the first consumed invocation
(22 tokens), leaving the second already-paid invocation pending and its
Usage uncharged. The independent temporary Unary/SSE and Agent/Kernel
fixtures reproduced this under Race without modifying the PR source.

**Repair:** Keep the independently completed outer ID and cumulative Usage
separate from untrusted projected content. After an error in physical
Tool verification, output projection or replay serialization, reject a
Tool-bearing candidate using the existing shared
`providerhttp.RejectCompletedResponse` helper: only the cloned, valid
ResponseID/Usage can survive, classified `model.post_generation_failed`.
No Tool call, partial arguments, content, refusal state, ProviderReplay or
successful terminal SSE event becomes authoritative. If final Usage is
missing/negative/overflowing, the response remains empty with ordinary
`ErrUnsupported`. Text-only unsupported output and missing encrypted
reasoning evidence retain their original Provider error classifications.
This is restricted to OpenAI Provider; no Agent/Kernel, persisted schema,
Host route, bridge or retry mechanism changes.

**Permanent regression evidence:** `openai/completed_mixed_tool_receipt_test.go`
includes both Unary/SSE for completed Tool+refusal (both orders), Tool+final,
Tool+unknown/ill-typed output item, Tool+invalid text part, unknown/invalid
Usage, unchanged text-only classification and missing-encrypted-replay
classification. It also proves an oversized physical Tool replay cannot
silently discard known paid Usage. `completed_tool_receipt_test.go` adds five
public Agent→HTTP/SSE Provider→Kernel tests with persisted consumed
invocations, exactly two Provider requests, 34/10/44 tokens and zero Tool
side effects. Independent tests were RED before the fix (including the
Agent pending-receipt case) and GREEN after. Focused Provider/Agent tests
passed repeated Race runs. **Final-source quality gates:** `make check`
passed (exit 0), including Go unit/Race/Vet/Lint, TypeScript tests/build,
benchmarks, OTel/A2A and beta.24 clean-consumer tests. Isolated real
PostgreSQL 16/Redis 8 integration via `node scripts/run-integration.mjs`
passed (exit 0), including cross-connection cancellation, recovery and
Redis continuation; temporary Docker containers/network were removed.
Formatting and `git diff --check` passed. These local results are not GitHub
checks or an independent immutable-Head merge-admission review; neither
merging nor beta.24 publication is authorized by this repair.

## PR #87 independent P1 repair — Anthropic mixed completed Tool receipt

A fresh [independent BLOCK audit of Head `93e29ba`](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/87#issuecomment-6571870290)
identified an analogous Anthropic Messages problem. A completed response
with a valid physical `tool_use` and separately invalid content (rich text
citations, missing Thinking signature/redacted data, or unsupported blocks)
returned an empty Response whenever the invalid sibling was decoded first
or after a Tool. The prior code only preserved a paid rejection receipt if
the **failing** block itself had type `tool_use`. During final SSE
reconstruction, an invalid Thinking block alongside a valid Tool returned
plain `ErrProtocol`, losing original ID and final cumulative Usage.

Independent read-only Unary/SSE Race overlays reproduced the bug; the
public Agent → Anthropic HTTP/SSE → Kernel Store repro observed two
completed Provider requests but only the first `consumed`: budget
incorrectly recorded **1 LLM call / 22 tokens** instead of **2 / 44**.
The second invocation remained `pending` without Usage and no local Tool
side effects occurred.

**Bounded implementation:** Preserve a separate, trusted outer
`completedMessageReceipt`. On a post-completion decode or replay failure,
scan the *original* content for a physical `tool_use`, independent of
partially decoded calls and content ordering. Reject Tool-bearing candidates
with `providerhttp.RejectCompletedResponse`, retaining only original ID
and validated cloned Usage; never authorize content, ToolCalls, refusal
flags, replay, or SSE terminal success. For SSE, perform the same check
across all already-observed blocks after `message_stop` and final Usage
validation, including block reconstruction/index failures. Earlier
incomplete/invalid streams, non-Tool responses and unverified cumulative
Usage cannot mint a completed receipt. No Kernel/Agent/Host, persisted schema,
retry policy, compatibility bridge or second execution loop was added.

**Permanent RED → GREEN tests:**

- `anthropic/completed_mixed_tool_receipt_test.go`: Unary both orders
  of valid Tool and invalid text/Thinking/redacted/unknown sibling; SSE
  invalid signed blocks before and after a valid Tool; known, missing,
  negative and overflowing Usage; text-only and truncated completion
  exclusions. Tests were RED on the previously reviewed Head and GREEN
  after the narrowly scoped provider correction, under `go test -race`.
- `completed_anthropic_mixed_tool_receipt_test.go`: public Agent/Router/
  Anthropic-SSE/Kernel Store path for both block orders. Initially RED
  with `agent.model_failed` and one pending paid invocation; after the
  correction it asserts two consumed durable ModelInvocations, exactly
  34 input + 10 output = **44** tokens, two physical requests, zero Tool
  side effects, and `model.post_generation_failed` on the second.
- Existing OpenAI/Anthropic valid Tool, refusal, forced-Tool, missing
  Usage and signed/reasoning replay regressions remain required gates.

**Final source verification:** `make check` passed (exit 0), covering Go
workspace unit/Race/Vet/Lint, TypeScript lint/typecheck/46 tests/build,
benchmark smoke, OTel/A2A, and beta.24 clean-consumer release checks.
`node scripts/run-integration.mjs` also passed (exit 0) with real
PostgreSQL/Redis cross-connection, cancellation/restart and continuation
checks; isolated test containers and network were cleaned. The focused
new Anthropic Provider and public Agent/Kernel tests passed 10 successive
Race runs. `gofmt` and `git diff --check` passed. Existing untracked
OpenAI probe scripts were excluded and remained hash-identical.

This is a repair candidate, **not independent approval of the new Head**.
GitHub CI, independent immutable-Head review, merge and beta.24 release
remain separate explicit gates.

## PR #87 independent P1 — Anthropic SSE preterminal semantic rejection

An additional [read-only independent BLOCK audit of `79080f3`](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/87#issuecomment-6572384691)
identified a remaining gap in Anthropic `GenerateStream`. The previous
repair only retained paid Tool receipts when validation failed during
post-`message_stop` reconstruction. A structurally valid
`content_block_start` with unsupported rich `citations` or an unknown
content type instead returned immediately from `parseContentBlock`,
before subsequent completed `message_delta` and `message_stop`. The
independent read-only Race overlays reproduced both Tool→rich Text and
rich Text→Tool orders. The public Agent→Anthropic SSE→Kernel Store path
recorded two completed physical calls but only one consumed invocation,
**22 tokens instead of 44**, with the second left Pending and no Tool
side effects.

**Bounded correction:** The streaming adapter now separates content
semantics from SSE transport/framing. When a content block's JSON
envelope has a structurally readable nonempty type but its content
violates the supported local profile, it irreversibly marks the response
semantically rejected, retains *only* a block type for physical Tool
detection, and continues validating start/delta/stop framing and
aggregate size. Subsequent deltas are not emitted or interpreted as
authorized content. A completed `message_stop`, supported final stop
reason, original response ID and final verified cumulative Usage are
required before returning **only** a sanitized
`model.post_generation_failed` ResponseID/Usage receipt, and only when
a physical `tool_use` was present. This never emits terminal SSE
success, replay or executable ToolCalls. Missing final Usage, negative
Usage, premature EOF/stop, malformed framing (including noncontiguous
content block indices) and oversized streams still fail closed with no
invented completion. The correction is in
the Anthropic Provider only; no Agent/Kernel/Host changes, migration,
alternate route, retry mechanism or compatibility bridge.

**Permanent RED → GREEN regression evidence:**
- `anthropic/completed_mixed_tool_receipt_test.go` extends the SSE matrix
  with unsupported cited text and unknown blocks on both sides of a
  physical Tool. Known final Usage now produces a minimal typed paid
  rejection; missing/negative final Usage, `max_tokens`,
  missing `message_stop`, mismatched frame event/type, gapped block
  indices and text-only semantic rejection never mint it. Further
  rejected text deltas cannot be emitted as authorized content.
- `completed_anthropic_mixed_tool_receipt_test.go` adds both cited-text
  orders through real mock HTTP/SSE, Router, public Agent and the in-memory
  Kernel Store. Initially RED with one Pending paid invocation, now
  verifies exactly two consumed invocations and **34 input + 10 output =
  44 tokens**, two Provider calls, zero Tool side effects, with
  `model.post_generation_failed` on the second.
- Focused Provider and Agent Race checks run repeatedly; the existing
  Anthropic signed replay, valid Tool, streaming and OpenAI regressions
  remain included in the repository-wide quality gates.

**Final-source verification:** `make check` passed (exit 0)
including Go workspace unit/Race/Vet/Lint, TypeScript lint/typecheck/
46 tests/build, benchmark smoke, OTel/A2A and beta.24 clean-consumer
release checks. Isolated real PostgreSQL/Redis integration via
`node scripts/run-integration.mjs` passed (exit 0), including
cross-connection cancellation/restart and continuation, with test
containers/network removed. Targeted Anthropic Provider and public
Agent/Kernel regressions passed repeated `go test -race -count=10`
and complete Provider Race tests passed. `gofmt` and
`git diff --check` passed; the two preexisting untracked OpenAI probes
were excluded and remained unchanged.

Local passing tests are repair evidence only, **not independent merge
approval**. After push, GitHub CI and a separate immutable-Head
merge-admission review must be completed before merge or beta.24 release.

## PR #87 subsequent independent immutable-Head P1/P2 — mandatory Tool-free receipts

The [independent BLOCK audit of `c9fe284`](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/87#issuecomment-6573721016)
confirmed two additional gaps across OpenAI Responses and Anthropic Messages.
Both were reproduced without editing the reviewed Head, using isolated
`go test -overlay -race` public Agent → mock HTTP/SSE Provider → Kernel Store
fixtures. The code change below remains on the existing PR branch and is
not a separate merge approval.

**P1 — completed mandatory Tool-free unsupported output loses billed Usage.**
A provider can ignore `tool_choice` and return a completed response with
verified cumulative Usage but only unsupported text/content. OpenAI
`projectOutput` failed before mandatory Tool verification; Anthropic
`parseContentBlock`, preterminal semantic SSE rejection and final stream
reconstruction likewise required a physical Tool before recording a paid
rejection. Two real physical requests (17 input + 5 output each) became only
one `consumed` ModelInvocation and **22 instead of 44** stored tokens, with
the second already-paid request left `pending` and the Run classified
`agent.model_failed`. No Tool handler executed.

**P2 — rejected mandatory Tool-free *valid text* survived in durable state.**
The final forced-Tool verifier returned the original decoded Response with
`ResponseRejectedError`. Agent correctly consumed the charged completion
but persisted its rejected `Response.Content` (and potentially phase replay)
in `ModelInvocation.Response`. A rejected completion must contain only
billing evidence, not unauthorized model text.

**Bounded repair:** Both adapters now pass the frozen `RequireToolCall`
obligation into their completed-response decode boundary. After valid outer
completion, any semantic decode failure on a mandatory Tool request uses
`providerhttp.RejectCompletedResponse` with only original ResponseID and
validated cumulative Usage; this includes a Tool-free unsupported message.
The Anthropic bounded, well-framed post-`message_stop` SSE semantic rejection
path makes the same distinction. Fully decoded responses with no required
Tool likewise use that sanitizer rather than returning their text/refusal/
replay. The genuine selected terminal safety-refusal exception and executable
valid local Tools retain their prior behavior. Missing/invalid final Usage,
truncated responses, malformed SSE framing, unavailable encrypted reasoning
replay and optional Tool-free unsupported output remain fail-closed under
their existing classifications. No Agent/Kernel/Host changes, persistence
schema, retry policy or compatibility path were added.

**Permanent regression evidence:** `openai/required_tool_semantic_receipt_test.go`
and `anthropic/required_tool_semantic_receipt_test.go` test unary/SSE
completed unsupported text, plain text, omitted/negative Usage, incomplete
outer responses, optional Tool-free errors and legitimate terminal refusals.
`completed_tool_receipt_test.go` and
`completed_anthropic_mixed_tool_receipt_test.go` assert the public Agent
reloads exactly **two consumed ModelInvocations**, **34 input + 10 output =
44** tokens and **zero Tool effects**, with rejected `Response.Content`,
ToolCalls and ProviderReplay all empty. Prior phase-refusal and Anthropic
rich-text regressions were updated to require sanitized paid rejection
receipts, rather than persisting rejected content or silently dropping
verified completed Tool-free Usage. The new public Agent cases were **RED
before the production repair** and **GREEN after** under Go Race. The
Provider direct tests and full Provider/Agent Race suites passed; focused
regressions passed ten consecutive Race executions. A fresh independent
immutable-Head review and GitHub-required checks remain separate merge gates.

**Final local quality gates after the P1/P2 correction:**

- **PASS, exit 0:** full repository `make check`, including Go
  unit/Race/vet/Lint, TypeScript lint/typecheck/46 tests/build, benchmark,
  OTel/A2A and beta.24 clean-consumer local-cohort release gates.
- **PASS, exit 0:** `node scripts/run-integration.mjs` with isolated real
  Docker PostgreSQL/Redis, covering cross-connection cancellation,
  continuation, budgeting and restart/recovery. Test containers and network
  were removed.
- **PASS:** full Provider Race, focused Provider/Agent Race repetitions (10),
  Agent Race, `gofmt` and `git diff --check`.
- **Exclusions:** no real external OpenAI/Anthropic/gateway credentials,
  hosted production deployment, PR merge or beta.24 publication. Both
  preexisting untracked OpenAI probe scripts remain hash-identical and are
  excluded from commits. Required GitHub CI and a new independent Head
  merge-admission review remain separate gates.
