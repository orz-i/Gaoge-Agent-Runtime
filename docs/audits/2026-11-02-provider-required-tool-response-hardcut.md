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

## PR #87 independent follow-up P1 — Anthropic SSE terminal-phase Tool injection

**Reviewed immutable PR Head:** `363797338e28ea0e5f4bf7d35e4cb66ad15b226f`
(after the prior Tool-free billing and sanitization repair). The full GitHub
check set, including CI/CodeQL, was green. This follow-up review independently
identified a **new P1** in Anthropic `GenerateStream` event ordering.

**RED before correction:** The adapter accepted `content_block_start` and
`content_block_stop` even after a final `message_delta` with
`stop_reason=tool_use` and cumulative Usage. A syntactically complete
`message_stop` therefore authorized a physically *late* local ToolCall,
ProviderReplay and successful SSE terminal event. It also accepted
interleaved/out-of-order blocks, missing block index (defaulting to index 0),
no actual `message_delta` when an initial envelope claimed a stop reason,
contradictory final stop reasons and decreasing cumulative `output_tokens`.
The last case could undercount a completed provider response. This violated
the official [Anthropic streaming event order](https://platform.claude.com/docs/en/build-with-claude/streaming):
content blocks precede one or more top-level `message_delta` events, then
`message_stop`; final token observations are cumulative.

Read-only `/tmp` Go test overlays initially reproduced the late Tool start,
late Tool stop and duplicate terminal state acceptance under Race, without
modifying the immutable review Head. Permanent
`anthropic/sse_terminal_phase_test.go` added a wider 17-case unary-independent
SSE matrix that **failed before** the Provider edit: actual executable
ToolCalls, ProviderReplay and a terminal success event were observed for
late Tool events, while a missing `message_delta` or missing index was still
accepted. A second public Agent → Anthropic SSE → Kernel Store test reproduced
this under an independent `go test -overlay -race` mutation restoring the
original Provider file: with either late Tool order, the registered local
Tool Handler **actually executed once** (`effects=1`) despite malformed SSE.
No external Provider credentials or real business Tools were used.

**Minimal correction:** The Anthropic adapter now tracks the one active
content block and whether the final `message_delta` phase began. Content
blocks must have an explicit index, be contiguous in start order and close
without overlap *before* the first final message delta. No later content
start/stop/delta may be reconstructed into an executable Tool. At least one
`message_delta` must explicitly provide the final stop reason, and all
observed final stop reasons must agree. Multiple final deltas remain valid
per the upstream protocol; cumulative output Usage can only stay equal or
increase. `message_stop` must follow that verified phase. Malformed streams
fail the existing Provider protocol boundary with no ToolCalls, replay,
trusted completed Usage receipt, or successful terminal SSE event. Already
supported valid Tool calls, bounded post-completion semantic rejection and
normal cumulative Usage remain unchanged. This adds no Agent/Kernel code,
retry behavior, persistence schema, host router or compatibility shim.

**Verification before delivery:** The direct 17-case terminal-phase suite,
public Agent side-effect test and the existing Anthropic/OpenAI/Provider
regressions passed `go test -race`; focused negative/positive and public
Agent tests passed ten consecutive Race runs. A code-overlay mutation with
the original Head reproduced RED again, including the real Agent Tool
Handler execution; the corrected Provider passed GREEN. This is repair
evidence, **not merge approval**. Merge requires checks on the new immutable
Head and an independent merge-admission audit.

**Final P1 source verification on the repaired branch:**

- **PASS, exit 0:** full `make check`, including Go workspace unit/Race/
  vet/Lint, TypeScript lint/typecheck/**46** tests/build, benchmark/evaluation
  smoke, OTel/A2A and beta.24 clean-consumer local-cohort gate.
- **PASS, exit 0:** real PostgreSQL/Redis Docker integration via
  `node scripts/run-integration.mjs`, including cancellation, restart,
  cross-connection recovery and continuation; temporary containers/network
  removed after completion.
- **PASS:** full Provider Race; direct terminal-phase and public Agent
  side-effect regressions repeated **10** Race runs; independent old-Head
  code-overlay mutation remains RED; gofmt and `git diff --check`.
- Original two untracked OpenAI probe scripts excluded and SHA-256-identical.
  Live Provider/gateway compatibility, GitHub required CI on the newly
  pushed Head, merge approval and beta.24 publication remain unverified.

## PR #87 independent P1 — outer Provider Message/Responses ownership before local Tool authorization

**Reviewed immutable PR Head:** `5dd19ec2011aa0c276845295360490ae03ed20bb`.
The GitHub required unit/race, coverage, CodeQL and other applicable checks
were green, but a new read-only independent audit identified an unverified
provider-owner boundary in **both** standalone HTTP adapters.

**Anthropic Messages:** The adapter decoded `id`, `stop_reason`, Usage and
`tool_use` without checking the enclosing Message `type` and `role`. A
complete unary Message or SSE `message_start` with explicit `role=user`,
`role=system` or `role=tool`, or `type=tool_result`, was accepted as an
assistant's own local Tool call, with `err=nil`, ProviderReplay and an
SSE terminal success event. Absent, null, empty or wrong-typed role/type
also silently omitted authority evidence while still authorizing a Tool.

**OpenAI Responses:** The parallel `decodeResponse` boundary accepted any
outer resource with `status=completed`, supported function-call output and
valid Usage. Its documented invariant `object="response"` was never checked.
A foreign `object="chat.completion"` or `object="image"`, absent/null/empty
object or unsupported scalar could therefore reach the same Tool execution
path, despite not representing an OpenAI Responses resource.

These are protocol-owner validations, not assumptions about provider model
text, tool names or Host routing. The official wire schemas identify
[Anthropic output Messages](https://platform.claude.com/docs/en/build-with-claude/working-with-messages)
as `type=message`, `role=assistant`, and
[OpenAI Responses resources](https://developers.openai.com/api/reference/typescript/resources/responses/methods/create)
as `object=response`. Their fields are mandatory, not optional legacy
compatibility flags.

**Independent RED before source edits:** Read-only `/tmp` Go `-overlay` tests
against the exact original Head reproduced successful unary/SSE local
ToolCalls and SSE success receipts for explicit foreign owner fields. Two
permanent Provider-level test matrices
(`anthropic/message_owner_test.go`, `openai/response_owner_test.go`) then
reproduced failures for foreign, absent, null, empty and nonstring identities.
A public Agent → HTTP/SSE → Kernel Store mutation audit restoring the old
Provider source confirmed **one actual local Tool Handler effect** for
foreign Anthropic user/tool/type/missing-owner envelopes and foreign OpenAI
chat/image/missing-object envelopes. No real provider credentials or
external business Tools were used.

**Minimal hard cut:** Anthropic `messageBody` now carries its outer `type`
and `role`, verified at the unary completed-message receipt boundary and at
SSE `message_start` **before any content block can create Tool evidence**.
OpenAI `responseBody` carries `object`, verified as `response` at the
single shared unary/SSE final decode boundary. Invalid/missing authority
returns a nonretryable Provider/protocol error and **no** local ToolCall,
ProviderReplay, SSE terminal success, synthetic billable completion or
Agent Tool side effect. A malformed outer resource is not proof of a
settled assistant response, even if it contains an ID and apparent Usage.

All offline HTTP fixture envelopes across the Provider tests were updated
to actual provider wire shapes (the prior simplified mock objects omitted
mandatory fields); no runtime compatibility adapter, mode branch, schema
migration, Store/Kernel/Agent change, retry change or Host-owned route was
added. Positive ordinary text, genuine assistant Tool, safety refusal,
stateless reasoning replay, incomplete response and required-Tool rejection
paths remain covered.

**Permanent RED → GREEN evidence:**

- Provider unary/SSE negative matrices reject all invalid owner values;
  valid `message/assistant` and `object=response` controls still authorize
  their legitimate local Tools.
- Public Agent durable Run regressions reject forged owner envelopes with
  zero Tool effects, zero completed second ModelInvocation/Usage receipts
  and no persisted or emitted ProviderReplay, while the genuine optional
  first response is consumed normally.
- The old-Head source mapped through isolated Go test overlays turned the
  exact new tests RED again, including the public Agent handler effects
  `effects=1`; corrected source passed the complete Provider Race and
  targeted owner/Agent Race checks in ten consecutive runs.

This local repair evidence does not approve the new PR Head. Full release
and PostgreSQL/Redis integration gates, GitHub CI, a fresh independent
immutable-Head merge admission review and beta.24 publication remain
separate explicit stages.

**Final local gates after this ownership hard cut:**

- **PASS, exit 0:** full `make check` after correcting all affected
  offline Provider and task-agent baseline fixture envelopes; includes Go
  unit/Race/vet/Lint, TypeScript lint/typecheck/**46 tests**/build,
  benchmark/evaluation smoke, OTel/A2A and beta.24 clean-consumer local
  cohort. The first pass exposed only outdated ownerless task-agent mock
  responses; the real production validation remained strict, and the
  corrected test fixtures passed on the second complete run.
- **PASS, exit 0:** `node scripts/run-integration.mjs` against isolated real
  PostgreSQL/Redis, with cross-connection cancellation, recovery and
  continuation; all test Docker resources were removed.
- **PASS:** full Provider Race, focused Provider owner and public Agent
  side-effect regressions under Race `-count=10`, isolated original-Head
  production-source mutation RED, gofmt and diff checks.
- Two preexisting untracked OpenAI probe scripts remain unchanged and are
  explicitly excluded from every commit. No live OpenAI/Anthropic/gateway
  credentials or published beta.24 tag were used.

Fresh GitHub-required CI on the **new** PR Head and independent merge-admission
review remain separate gates; this repair does not authorize merge or release.

## PR #87 next independent P1 — unphased OpenAI message ownership and status

**Immutable audited Head:** `83a00b88b9ab302db21bd3eab7666863ae91158f`.
All applicable GitHub required checks were green. A fresh read-only audit
found a separate local Tool authorization gap after the outer Responses
`object=response` repair: `openai/projectOutput` checked an explicit foreign
`message.role` only when `phase != nil`, and did not fence explicit
`message.status` at all. A completed response with a fully valid physical
`function_call` plus a sibling unphased `role=user/system/tool` message, or an
assistant message with `status=in_progress/incomplete`, was treated as a
successful executable Tool batch. The resulting final ResponseID/Usage and
ToolCalls were accepted in both unary and SSE.

**Independent RED:** an isolated `/tmp` Go `-overlay` on the unmodified Head
added public Agent→HTTP/SSE→Kernel cases for the four primary scenarios.
Each scenario triggered exactly one *real local Tool Handler execution*
(`effects=1`) despite the invalid sibling, instead of settling a rejected
paid completion. No external provider or business handler was involved.

**Bounded repair:** `projectOutput` now validates physical `message` role and
status with presence-sensitive `json.RawMessage` before processing the rest
of the output. If present, `role` must equal `assistant` and `status` must
equal `completed`, whether the message is phased/unphased, comes before/after
a local Tool, or Tool choice was optional/mandatory. Explicit null, empty,
foreign, unknown or nonstring values invalidate the entire response. Truly
omitted legacy fields remain supported. An independently completed envelope
with verified final Usage and any physical `function_call` returns only the
existing sanitized `model.post_generation_failed` ID/Usage rejection: zero
executable ToolCalls, text, phase replay or successful SSE terminal event.
No second dispatch loop, retry, Host routing, Agent/Kernel, Store schema or
compatibility bridge changed.

**Permanent RED→GREEN tests:** `openai/message_item_authority_test.go` covers
positive legacy/explicitly-completed assistant messages and negative role/
status combinations for optional and forced Tool, unary and SSE, and both
sibling orders. `completed_tool_receipt_test.go` adds public Agent → OpenAI
SSE → Kernel Store cases proving two consumed ModelInvocations, correct
34/10/44 cumulative tokens, zero Tool side effects and a minimal rejected
response. Replacing only `openai/phase.go` with the original immutable-Head
file via isolated `go test -overlay -race` turns these same tests RED,
including `effects=1` in the public Agent Handler; the repaired source is
GREEN. Focused and full Provider Race tests pass.

Fresh GitHub CI on the newly pushed Head and an independent immutable-Head
merge admission audit are still required. This repair is **not approval**
to merge or publish beta.24.

**Final local source gates for the OpenAI message-item P1 repair:**

- **PASS, exit 0:** full repository `make check` on the repaired source
  (Go unit/Race/vet/Lint, TypeScript lint/typecheck/46 tests/build,
  benchmark/evaluation, OTel/A2A, beta.24 clean-consumer gate).
- **PASS, exit 0:** `node scripts/run-integration.mjs` against isolated
  real PostgreSQL/Redis with cancellation, cross-connection recovery and
  continuation; test Docker resources were removed.
- **PASS:** focused Provider/Agent Race `-count=10`, complete Provider Race,
  mutation against the immutable original Head RED, gofmt and diff checks.
- Two preexisting untracked OpenAI probe files were excluded and remain
  hash-identical. No live third-party gateway/provider credentials were used.
  New GitHub CI and independent immutable-Head merge gate remain required.

## PR #87 next independent P1 — physical Tool Call IDs must survive Agent normalization

**Immutable reviewed Head:** `f177172aef0c168d47aacd656d070b29d236cf3b`.
The full applicable GitHub CI/security check cohort passed on that Head.

**Independent RED, OpenAI and Anthropic:** a physically completed response
with valid Tool name/arguments/Usage but a `function_call.call_id` or
`tool_use.id` consisting only of a space, tab or Unicode nonbreaking space
was accepted as an executable local Tool. IDs with leading/trailing spaces
were also accepted. The public Agent's `queueToolCalls` uses
`strings.TrimSpace(call.ID)`, and even creates a fresh local Tool ID when
trimming yields empty. Thus a missing stable physical Provider identity
was silently promoted to a new Tool authorization, or an original ID was
changed before continuation. This bypass also occurred on *optional*
Tools and in both unary/SSE Provider modes.

Read-only `/tmp` Go `-overlay -race` tests confirmed the gap before any
repository edit. The original Head executed the local OpenAI Tool Handler
**once per invalid-ID case** (`effects=1`, `requests=2`), and Anthropic
Unary/SSE accepted the same invalid local Tool calls with `err=nil` and
ProviderReplay. The later permanent Anthropic public Agent test explicitly
removed all other malformed siblings and again demonstrated **one actual
Tool Handler execution** in both whitespace-only and padded-ID cases when
the original Provider source was restored via a separate mutation overlay.
No real Provider credentials or business Tool handlers were used.

**Minimal repair:** OpenAI rejects `function_call.call_id` when it is empty
or `strings.TrimSpace(id) != id` both in its completed physical Tool item
fence and in the shared output projection used for replay verification.
Anthropic enforces the same invariant at `parseContentBlock` for `tool_use`
so direct decoded content, bounded SSE reconstruction and signed replay
share one fail-closed rule. A known outer completion with final, valid Usage
returns only the existing sanitized `ResponseRejectedError` receipt with
`model.post_generation_failed`: exact original ResponseID/Usage but no Tool,
text, replay or successful SSE terminal event. Incomplete/missing/negative
Usage cannot mint completed billing evidence. Legitimate stable ID requests
remain executable; no Agent/Kernel, Store schema, Host, retry mechanism,
ID-rewrite compatibility shim or additional abstraction was introduced.

**Permanent RED→GREEN regression coverage:**

- `openai/tool_id_identity_test.go`: direct unary and SSE, optional and
  forced Tool, valid-control and whitespace/leading/trailing/Unicode invalid
  IDs, missing Usage and precise rejected receipt semantics.
- `anthropic/completed_tool_receipt_test.go`: direct Unary and SSE identity
  failures, with final Usage present and checked.
- `completed_tool_receipt_test.go` and
  `completed_anthropic_mixed_tool_receipt_test.go`: real public Agent → mock
  Provider SSE → Kernel persistent Store, requiring two consumed physical
  invocations, **34 input + 10 output = 44** tokens, zero Tool effects,
  no executable calls/ProviderReplay and classified `model.post_generation_failed`.
- A post-fix mutation test replacing only OpenAI `client.go`/`phase.go` and
  Anthropic `replay.go` with the original immutable-Head sources made the
  new tests fail again with `effects=1` in both Agent paths. Corrected
  Provider tests were GREEN under Go Race, retaining legitimate Tool output
  behavior and exactly-once billing.

This local repair evidence is not independent new-Head merge admission.
Fresh GitHub CI/security checks and an independent code/security review on
an immutable new PR Head are still required before merge or beta.24 release.

**Final local gates for exact physical Tool ID validation:**

- **PASS, exit 0:** entire `make check` with Go unit/Race/vet/lint,
  TypeScript lint/typecheck/**46 tests**/build, benchmarks, OTel/A2A,
  beta.24 clean-consumer local-cohort release gates.
- **PASS, exit 0:** real isolated Docker PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs`, including cross-connection cancellation,
  restart/recovery, continuation and budgets; test containers/network removed.
- **PASS:** complete Provider Race; focused ID-integrity and public Agent
  regression suites repeated **10** Race runs; independent restoration of the
  original immutable-Head Provider source turns them RED with actual local
  Tool Handler execution; corrected sources GREEN. `gofmt` and diff checks
  passed.
- **Excluded:** live Provider/gateway credential checks, Host integration,
  beta.24 publication, and the two pre-existing untracked OpenAI probe scripts
  (which remain SHA-256-identical). Required GitHub CI and an independent
  immutable-Head merge gate are separate from this local repair.

## 2026-11-03 — PR #87 independent P1: OpenAI SSE done-item substitution

**Reviewed immutable PR Head:** `94910b66540eb2fb5c9a8392cbebfd8919248641`.
All applicable GitHub required CI/security checks were green on this Head.

**Independently reproduced P1:** OpenAI `GenerateStream` originally treated
`response.output_item.done` as trusted evidence **only** for reasoning
items. It ignored the same terminal event for a physically generated
`function_call` or assistant `message`, and later authorized whatever local
Tool appeared in `response.completed`. A gateway could send a Tool done
item with `status=incomplete` or `in_progress`, or a different Call ID,
name, arguments, output index or item type, then replace it with an
apparently completed valid Tool in the final response. The adapter returned
`err=nil`, an executable local Tool and terminal SSE ID/Usage. A duplicated
completed item was likewise ignored. A completed final envelope alone did
not reconcile previously observed physical completion evidence.

The [OpenAI Responses SSE event schema](https://developers.openai.com/api/reference/resources/responses/streaming-events)
labels `response.output_item.done` as the event for an item marked done,
with its absolute output index and physical item. Such a frame is not an
ignorable progress hint when authorizing an external Tool effect.

**READ-ONLY RED:** A temporary Go `-overlay` test against the unchanged
review Head showed six contradictory Tool cases accepted, while matching
done and final-only positive controls passed. Permanent test matrices then
reproduced this across incomplete/in-progress/null statuses, differing
physical Call IDs, Tool names/arguments/item IDs, wrong or omitted indices,
duplicate done events, a message done substituted with a Tool, conflicting
message role/status, and an unknown/null done item. On the original source
these cases returned unauthorized successful ToolCall responses.

The public Agent → actual mocked HTTP/SSE Provider → Kernel Memory Store
regression was also tested by restoring only the original
`openai/client.go` and `openai/stream_replay.go` into an isolated Go overlay.
**Five** contradictory SSE scenarios each resulted in **one actual local
Tool Handler execution** (`effects=1`, `requests=2`) instead of rejecting
the untrusted completion, while the repaired implementation executed **zero**.
The old source did this despite previously green CI. No external Provider
credentials or real business Tools were involved.

**Bounded hard-cut repair:** `completedReasoningItems` becomes
`completedOutputItems`: the same bounded, absolute-indexed collector now
also observes completed local `function_call` and `message` output items.
Observed done items reconcile against the final response on type, exact
identity, content/arguments, index and completion status before Tool
execution or ProviderReplay authorization. Repeated, missing-index,
unknown or contradictory done events are Provider protocol failures, not
paid semantic rejections. Consequently the Agent persists **no second
completed ModelInvocation/Usage receipt** and executes no Tool when that
SSE completion is untrustworthy. The original completed first invocation
and its Usage remain intact. Genuinely final-only response streams,
matching done/final frames and missing optional completed status for Tool/
Message remain accepted. Existing narrower Reasoning encryption/status
reconciliation is unchanged; `response.output_item.added` still grants no
authority. No Agent/Kernel, database schema, Host router, automatic retry,
or compatibility rewrite was introduced.

**Permanent regression coverage:**

- `openai/stream_tool_done_authority_test.go` — **21-case** direct SSE
  verification with valid final-only and consistent-done controls, optional
  status omissions, and malformed/conflicting done identity/status/content.
- `openai_sse_done_agent_test.go` — **five** public Agent→Provider→Kernel
  side-effect and persistence cases, each requiring no Tool effects, no
  fabricated second billing receipt, no authorized replay and no terminal
  success. Genuine first invocation still consumes exactly 17/5 tokens.
- Source mutation restores only the two original Provider files and turns
  the exact tests RED, including actual Agent Tool Handler `effects=1`;
  corrected source is GREEN. Focused suites and complete Provider Race
  pass. Required checks on the new PR Head and independent merge admission
  are still separate gates; this repair is **not merge or release approval**.

**Final source verification for the 2026-11-03 SSE item-completion hard cut:**

- **PASS, exit 0:** complete `make check` including all Go unit/Race/vet/lint,
  TypeScript lint/typecheck/46 tests/build, benchmark/evaluation smoke,
  OTel/A2A and beta.24 clean-consumer local-cohort release checks.
- **PASS, exit 0:** actual isolated Docker PostgreSQL/Redis integration using
  `node scripts/run-integration.mjs`, covering cancellation, recovery,
  restart and continuation; all test containers and network removed.
- **PASS:** focused direct SSE/Agent regression Race **10 consecutive runs**;
  complete Provider Race; original immutable-Head two-file source mutation
  RED (actual local Tool Handler `effects=1`), repaired source GREEN;
  `gofmt` and diff integrity checks.
- Preexisting two untracked OpenAI probe scripts excluded from all commits
  and SHA-256-unchanged. No external OpenAI/Anthropic/gateway credential
  probes were performed. GitHub CI on the **new** Head and a fresh independent
  merge-admission review remain mandatory; no merge/release is approved.

## 2026-11-03 — PR #87 independent P1: unfinished OpenAI reasoning item can dispatch a local Tool

**Reviewed immutable PR Head:** `4f9862b669bc049de70df4521a4de8784448cc10`.
All applicable GitHub required CI/security checks had completed successfully
on that immutable Head before this independent audit.

**Independently reproduced P1:** OpenAI `projectOutput` had presence-sensitive
completion checks for physical `message` and `function_call` items, but
accepted *any* physical `reasoning` item without verifying its own explicit
`status`. A `response.completed` outer envelope could contain an inner
`reasoning` item with `status=in_progress`, `incomplete`, `failed`, `null`,
empty, unknown or nonstring status, plus nonempty `encrypted_content` and
a valid completed local `function_call`. The Provider returned an executable
ToolCall and authorized encrypted replay despite the unfinished reasoning
item. The gateway's outer completion did not establish that this physical
reasoning item was complete.

**Read-only RED on original Head:** A fresh temporary `/tmp` Go test overlay
added a public Agent → mocked HTTP/SSE OpenAI response → Kernel Store
regression for five abnormal statuses. All five cases produced exactly
**one real registered local Tool Handler execution** (`effects=1`,
`requests=2`), while the expected minimal paid rejection did not occur.
No external provider credentials or real business side effects were used.

**Minimal hard cut:** In the shared OpenAI `projectOutput` projection,
validate reasoning-item status using presence-sensitive `json.RawMessage`.
If genuinely omitted, retain the existing stateless Responses legacy path;
if present, only the exact string `completed` is permitted. A foreign,
unfinished, null, empty or nonstring status invalidates the whole output
before any ToolCall, replay or SSE terminal event may be authorized. An
independently completed outer response with verified final cumulative Usage
and a physical Tool retains **only** the original ResponseID/Usage via the
existing typed `model.post_generation_failed` rejected receipt. An unknown
final Usage cannot mint a completed receipt. The same projection is used
for unary, SSE and durable stateless ProviderReplay verification.

**Permanent RED→GREEN coverage:**

- `openai/reasoning_item_status_test.go` checks positive omitted/explicitly
  completed statuses and eight invalid/unknown-Usage cases, in optional and
  mandatory Tool modes and both Unary/SSE transport modes. Valid responses
  still provide their original encrypted ProviderReplay and ToolCall.
- `completed_tool_receipt_test.go` tests six abnormal reasoning statuses
  in both sibling orders through the real public Agent → mock OpenAI SSE →
  Kernel Store. On a completed but semantically rejected second response,
  the Agent consumes exactly two ModelInvocations and **34 input + 10 output
  = 44 total tokens**, with **zero local Tool Handler executions** and no
  leaked ToolCalls, content or replay.
- Replacing *only* production `openai/phase.go` with the exact immutable
  original-Head source via isolated `go test -overlay -race` turns these
  permanent tests RED again, including actual Agent `effects=1` in every
  invalid status case. The fixed source passes the same tests, with all
  previous OpenAI/Anthropic Provider Race checks remaining GREEN.

This repair does not alter Agent/Kernel, schema, middleware, Host routing,
retry policy, provider-side storage or compatibility shims. GitHub CI and
an independent merge-admission audit must be repeated on the **new actual
PR Head**; this evidence is not merge or beta.24 publication approval.

**Final local gates for OpenAI Reasoning-item completion hard cut:**

- **PASS, exit 0:** full repository `make check` on the repaired source,
  including all Go unit/Race/vet/lint, TypeScript lint/typecheck/46 tests/build,
  evaluation/benchmark smoke, OTel/A2A and beta.24 clean-consumer cohort.
- **PASS, exit 0:** isolated real Docker PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs`; containers and test network cleaned.
- **PASS:** complete Provider Race; focused Provider and public Agent Race
  `-count=10`, original immutable-Head production `phase.go` overlay RED
  (`effects=1`), corrected source GREEN; `gofmt` and `git diff --check`.
- The preexisting two untracked OpenAI probe scripts were excluded and their
  SHA-256 remain unchanged. No live external gateway/provider credentials
  were tested. Fresh GitHub checks and independent merge-admission audit
  on the **new** PR Head remain separate gates, not automatically approved.

## PR #87 independent P1 — ambiguous Provider JSON owner/Tool fields

**Immutable audited PR Head:** `22b6d40459792b68b0021e2b73cce4a1f78c81ab`.
All applicable GitHub CI, security and coverage checks were green on that
Head before this audit began.

**Independent RED on unchanged Head:** Standard Go `encoding/json` accepts
repeated members of one JSON object and silently applies a last-key-wins
interpretation. Both Provider adapters decoded the physical JSON first, then
validated the overwritten value. A gateway could send an OpenAI Responses
body with `object="chat.completion", object="response"`, outer
`status="incomplete", status="completed"`, duplicated local
`function_call.call_id`/status, or inconsistent token Usage; the adapter
returned an executable Tool and successful terminal receipt. Equivalent
Anthropic Messages failures included a physical `role="user",role="assistant"`
owner, conflicting stop reasons and a duplicate `tool_use.name`/ID.

An isolated read-only Go `-overlay -race` of the immutable original Head
reproduced **7 direct Tool-admission failures** across the two Provider
adapters. After introducing permanent tests, a second mutation restored
only the original three Provider/HTTP source files (without reverting the
new tests) and turned the exact test suites RED. In six real public
**Agent → mocked Provider SSE HTTP → Kernel Memory Store** cases,
**one actual local Tool Handler executed** for each ambiguous response
(`effects=1`, `requests=2`). This was a real local side effect in a synthetic
test handler; no external gateway, credentials or business effects were used.

**Bounded hard cut:** The shared Provider HTTP boundary now validates
object-member uniqueness at every level of bounded JSON before a unary
response is unmarshalled. Each OpenAI/Anthropic SSE callback performs the
same check on its bounded JSON event **before** type/role/status/Tool parsing.
Unicode JSON escape-equivalent names are treated as duplicates. Repeated
names in *different* objects and ordinary escaped content strings are valid.
A bounded nesting limit protects the structural walk. The generic SSE
framer remains data-agnostic, preserving non-JSON transport event support;
OpenAI's permitted `[DONE]` terminator is unaffected. A duplicated member
is **`providerhttp.ErrProtocol`**, not verified completed billing evidence:
no ToolCall, ProviderReplay, SSE final ResponseID/Usage or second completed
ModelInvocation is authorized. The first genuine ModelInvocation and its
Usage remain untouched. No Provider-specific compatibility bridge, new
Agent/Kernel schema, retry policy, Host router or additional dispatch state
is introduced.

**Permanent RED→GREEN regressions:**

- `internal/providerhttp/json_keys_test.go` verifies distinct sibling
  objects, escaped content and scalar arrays are accepted; duplicate outer,
  nested and escape-equivalent names, malformed JSON, trailing documents,
  overly deep nesting and ambiguous `ReadJSON` are rejected.
- `openai/duplicate_authority_test.go` and
  `anthropic/duplicate_authority_test.go` verify positive/negative unary/SSE
  responses for foreign owners, stop/completion state, duplicated local
  Tool identity, nested input/Usage and SSE event type.
- `duplicate_json_authority_agent_test.go` proves six real public
  Agent→Provider SSE→Kernel Store failures with **zero** Tool effects,
  **two** physical Provider requests but **only one** completed and charged
  ModelInvocation (17 input + 5 output tokens), and no forged second Tool,
  ProviderReplay or completed billing receipt.
- Read-only mutation restored the three original source files via Go
  `-overlay -race`, turning these same tests RED with `effects=1` in all
  six Agent paths; the corrected source runs GREEN. Focused suites repeated
  ten times under Race, and complete Provider Race passed.

No live provider/gateway compatibility is claimed. Independent CI on the
new actual PR Head and immutable-Head merge admission remain separate gates.

**Final local gates for duplicate Provider JSON authority P1:**

- **PASS, exit 0:** full repository `make check` on repaired source
  (Go unit/Race/vet/lint, TS lint/typecheck/**46 tests**/build,
  evaluation/benchmark, OTel/A2A and beta.24 clean-consumer local cohort).
- **PASS, exit 0:** isolated real PostgreSQL/Redis integration with
  `node scripts/run-integration.mjs`; temporary Docker containers and test
  network were cleaned.
- **PASS:** complete Provider Race and focused direct/Agent Race suites
  `-count=10`; original three-file immutable-Head mutation RED with actual
  local Tool `effects=1` across all six public Agent paths; fixed source
  GREEN; `gofmt` and diff checks.
- Two existing untracked OpenAI probe scripts were excluded from commits
  and remain SHA-256 identical. No live upstream Provider/gateway credentials
  were tested. Fresh GitHub CI and independent new-Head merge-admission audit
  are still required; **no merge or beta.24 publication is approved**.

## 2026-11-03 — PR #87 independent P1: duplicate-key JSON inside encoded Tool input

**Immutable reviewed Head:** `805bca134173f29f862741f3e4f76114b3888835`.
All relevant GitHub CI/security/coverage checks were green at audit start.

**Independent RED, both providers:** The preceding shared Provider HTTP JSON
uniqueness fence validates outer OpenAI/Anthropic JSON and SSE frames, but
cannot inspect another JSON value stored **as a string** inside those frames.
OpenAI `function_call.arguments` is a string containing complete JSON;
Anthropic SSE `content_block_delta.delta.partial_json` is a string whose
fragments reconstruct `tool_use.input`. Both were accepted using
`json.Valid`, so duplicate keys such as `{"x":1,"x":2}`, nested duplicate
keys and Unicode escape-equivalent member names could still produce a local
executable ToolCall. The model/Tool schema check cannot itself certify that
a duplicate-name object has unambiguous physical meaning.

Two isolated `/tmp` test-only overlays of the unchanged Head proved that
OpenAI's ambiguous encoded arguments led to **one actual local Tool Handler
execution** (`effects=1`, `requests=2`) on the public Agent→Provider SSE→
Kernel Store path; Anthropic accepted a ToolCall from an SSE `partial_json`
reconstruction containing duplicate keys. Neither used a live Provider or
production business handler. The full permanent regression suites were
subsequently tested via read-only `go test -overlay -race` with only the four
original Provider source files restored. Both Provider suites turned RED;
**both** public Agent paths executed exactly one real test Tool Handler
(`effects=1`) for duplicate root, nested and escaped-equivalent JSON keys.
Valid controls remained GREEN.

**Minimal repair:** Reuse the existing bounded
`providerhttp.ValidateUniqueJSONKeys` directly on the physical inner JSON.
OpenAI rejects ambiguous `function_call.arguments` before execution in
`verifyCompletedFunctionCallItems`; the shared `projectOutput` and SSE
completed-item observer also require the same property, protecting stateless
ProviderReplay and all streaming states. Anthropic enforces the invariant
when parsing the reconstructed `tool_use.input` content block, so unary,
SSE final reconstruction and signed replay share one unambiguous input rule.
For an otherwise verified completed Provider response with cumulative Usage,
a physical Tool is settled only as a sanitized `model.post_generation_failed`
ResponseID/Usage receipt. No ToolCall, content, ProviderReplay or successful
terminal SSE event survives; absent/invalid final Usage still cannot mint a
completed billing receipt. Valid nested JSON with fields of the same name
in separate sibling objects remains supported. No Agent/Kernel, Store schema,
Host route, retry mechanism or compatibility bridge changed.

**Permanent RED→GREEN regressions:**

- `openai/encoded_tool_arguments_test.go` checks valid/invalid JSON strings
  under optional and required Tool choice, Unary and SSE, nested and escaped
  duplicate keys, known Usage and unknown Usage.
- `anthropic/encoded_tool_input_delta_test.go` exercises valid reconstructed
  SSE `input_json_delta`, optional/required Tool use, nested/escaped repeated
  fields, full and missing final Usage.
- `completed_tool_receipt_test.go` adds public Agent→OpenAI SSE→Kernel Store
  duplicate-key Tool payloads. `completed_anthropic_mixed_tool_receipt_test.go`
  covers the same physical Handler path with Anthropic `partial_json`.
  Invalid completed Tool invocation is consumed once with the original
  trusted Usage: two ModelInvocations / 34 input + 10 output = 44 tokens,
  **zero Tool effects** and no executable replay.
- Focused direct/Agent Race passed **10 consecutive runs** and full Provider
  Race passed. Restoring the exact original Head Provider implementation with
  a read-only overlay turns the same new tests RED (`effects=1` in both
  Agent paths); repaired source is GREEN.

This local repair is **not approval** to merge PR #87 or publish beta.24.
Fresh checks and independent merge admission on the new actual PR Head remain
necessary. External live gateway/Provider compatibility was not tested.

**Final local source gates (double-encoded Tool input P1):**

- **PASS, exit 0:** complete repository `make check` on the final code
  commit including all Go unit/Race/vet/lint, TypeScript lint/typecheck/
  **46 tests**/build, evaluation/benchmark, OTel/A2A and beta.24
  clean-consumer local-cohort release gates.
- **PASS, exit 0:** isolated real PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs`; test containers and network removed.
- **PASS:** full Provider Race and focused direct/Agent Race for both
  Provider adapters, `-count=10`; exact original-Head Provider source mutation
  RED with `effects=1` in both public Agent paths, corrected source GREEN.
- The initial local `make check` found two `goimports` format issues in new
  tests, corrected before final code commit. The first full recheck hit one
  A2A scenario corpus threshold failure under Race outside changed Provider
  code; isolated A2A Race **10/10 passed**, and the subsequent full
  repository `make check` passed. No A2A code or baseline was changed to
  conceal the intermittent observation.
- Existing two untracked OpenAI probe scripts remain SHA-256-identical and
  excluded from commits. No external live Provider/gateway credentials used.
  Fresh GitHub checks and independent merge-admission review on the new PR
  Head are required; this is not authorization to merge or publish beta.24.

## 2026-11-03 — PR #87 independent P1: case-folded JSON aliases bypass exact-member uniqueness

**Immutable independently audited PR Head:** `16ea6116ffe6c719a64c6dc7816e1a5366b62dcb`.
All applicable GitHub CI/security/coverage checks were green when this
review began. The worktree contained only two known, excluded untracked
OpenAI probe scripts.

**Independent READ-ONLY RED:** The preceding JSON transport hard cut rejected
repeated object keys *only when their strings were identical*. However,
Go's `encoding/json` matches JSON object member names to struct fields using
case-insensitive Unicode simple folding. Therefore an outer OpenAI Responses
object with `"Object":"chat.completion","object":"response"`, or
`"Status":"incomplete","status":"completed"`, could pass the exact-key
check and be decoded as a valid response with an executable local Tool.
The same bypass applied to `"Type":"message","type":"function_call"`
inside OpenAI output; Anthropic Messages accepted both `Role`/`role` and
`Stop_Reason`/`stop_reason` with contradictory identities or stop reasons.
The distinction was not safely preserved by the subsequent struct decoder.

A temporary `/tmp` Go `-overlay -race` added direct tests without changing
the reviewed Head. **Four OpenAI** Unary/SSE cases and **two Anthropic**
Unary cases were proven to return `err=nil` and executable ToolCalls despite
conflicting physical JSON keys. The initial candidate patch then added
permanent direct/Agent tests. A second isolated source mutation restoring
*only* the original Head's `internal/providerhttp/json_keys.go` with those
new tests left in place turned them all RED again. Six public
**Agent → mocked Provider HTTP/SSE → Kernel Memory Store** cases each
executed **one actual local Tool Handler** (`effects=1`, `requests=2`)
under the original code: three OpenAI and three Anthropic casefolded
owner/status/Tool type or Usage aliases. This was a local synthetic Handler;
no live Provider/gateway or production business Tool was invoked.

**Minimal hard cut:** Extend the existing bounded shared JSON uniqueness
walk, without creating another transport layer, by Unicode simple-folding
all member names to a deterministic canonical representative before checking
one object's seen-name set. Go's Unicode simple-fold equivalences include
ASCII uppercase/lowercase, long-s `ſ`/`s`, Kelvin `K`/`K` and JSON Unicode
escape-equivalent names. Hash-based per-object validation retains linear
work in input size; recursion depth remains bounded. The checks already run
in the shared unary `ReadJSON`, both Providers' SSE JSON frame consumers,
OpenAI's double-encoded `function_call.arguments` and Anthropic's final
reconstructed `tool_use.input`/replay paths. Thus a physical owner, stop
reason, item type, Tool identity or Usage cannot be silently overwritten
via case aliases before local Tool authorization.

**Intentional breaking boundary:** Although general JSON objects distinguish
case-sensitive names such as `x` and `X`, the Provider wire/Tool input
contract now rejects **case-fold equivalent keys in the same object** to
avoid decoding ambiguity. Sibling objects may reuse those names freely.
There is no silent casing rewrite, automatic retry or legacy compatibility
bridge. Ambiguous physical Provider responses fail `providerhttp.ErrProtocol`
before producing any ToolCall, ProviderReplay, terminal SSE success or
invented completed ModelInvocation/Usage receipt. The original first
unambiguous ModelInvocation and its token accounting remain intact.
No Agent/Kernel logic, database schema, Host route or Tool execution engine
is changed.

**Permanent RED→GREEN tests:**

- `internal/providerhttp/json_keys_test.go` covers ASCII and Unicode casefold
  collisions (including escaped Unicode and nested fields), case-alias
  response ownership, distinct sibling objects, malformed/trailing JSON and
  existing depth guards.
- `openai/duplicate_authority_test.go` and
  `anthropic/duplicate_authority_test.go` cover valid and conflicting
  physical role/resource, type/status, Tool Call ID/name and Usage in both
  Unary and SSE; the OpenAI SSE frame event `Type`/`type` alias is included.
- `duplicate_json_authority_agent_test.go` adds six real public Agent→SSE→
  Kernel cases with **zero physical Tool effects**, **only one consumed
  ModelInvocation / 17 input + 5 output tokens** and a second pending
  invocation without fabricated usage, ToolCalls or ProviderReplay.
- A read-only source mutation against the actual immutable old Head makes
  these new tests RED with `effects=1` in all six public Agent scenarios;
  the corrected candidate passes the same tests and complete Provider Race.

No live upstream compatibility is claimed. New Head GitHub CI/security and
a separate immutable-Head merge-admission audit remain required; this is
**not merge or beta.24 release authorization**.

**Final local gates for case-folded JSON alias P1:**

- **PASS, exit 0:** full repository `make check` on the repaired source
  (Go unit/Race/vet/lint, TypeScript lint/typecheck/**46 tests**/build,
  evaluation/benchmarks, OTel/A2A and beta.24 clean-consumer local-cohort
  release gates).
- **PASS, exit 0:** real isolated Docker PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs` including cross-connection recovery,
  cancellation and continuation; all temporary containers/network removed.
- **PASS:** focused direct Provider/Agent Race **10** repetitions; full
  Provider Race; isolated restoration of immutable original Head's
  `internal/providerhttp/json_keys.go` turns new tests RED with real local
  Agent Handler `effects=1` in all six scenarios; corrected source GREEN.
  Strict Provider `golangci-lint` returned **0 issues**, `gofmt` and diff
  checks passed. A test-only `staticcheck` suggestion about switch
  formatting was corrected before committing; no runtime behavior changed.
- Two preexisting untracked OpenAI probe scripts were excluded and remain
  SHA-256-identical. No live upstream Provider/gateway credentials tested.
  Fresh GitHub CI/security checks and a new independent immutable-Head
  merge-admission audit are still required; no merge or beta.24 publication
  has been authorized by this source-level review.

## 2026-11-03 — PR #87 independent P1: ambiguous persisted ProviderReplay re-emitted upstream

**Immutable PR Head independently reviewed:**
`2159b85af690d03b5c13ec420bdc6f56c1135b52`. All relevant GitHub
CI/security/coverage checks on this Head were green when auditing began.

**Independent read-only RED:** The existing strict JSON uniqueness guard
covered incoming physical OpenAI/Anthropic response/SSE frames and
newly decoded Tool input but did **not** validate the raw persisted
`model.ProviderReplay.Data` before outbound continuation. Go's struct
unmarshalling can interpret a mutated message with `"Role":"user"` followed
by `"role":"assistant"` as an assistant replay, just as it can interpret
contradictory `Status`/`status`, `Phase`/`phase` or `Type`/`type`. The
OpenAI replay projector compared its **last-key-wins normalized** result
against the approved Agent transcript and returned the original ambiguous
bytes unchanged. Anthropic signed replay suffered the same gap for exact
duplicate Tool type/ID/name or signature fields: `parseContentBlock` decoded
only the final field, verified against the Agent transcript, and then
returned the original conflicting signed/Tool JSON for retransmission.

On the unchanged reviewed Head, two independent temporary `go test -overlay
-race` test suites first generated valid ProviderReplay through public
`Generate` and then mutated only the stored raw replay bytes. In **four
OpenAI** phase/owner/status/item-type cases and **four Anthropic** Tool/
signed-Thinking cases, the Provider adapters accepted the tampered replay,
**sent a second physical HTTP request** to the upstream mock and returned
`err=nil`. This is an outbound authority/provenance integrity failure;
these tests did **not** assert new local Tool Handler effects, because
there was no subsequent Agent Tool dispatch. No live Provider or business
actions were performed.

**Minimal hard cut:** At each of the two existing stateless
`verifiedReplay` entrypoints, validate the entire bounded replay JSON with
the already-shared `providerhttp.ValidateUniqueJSONKeys` **before**
`json.Unmarshal`, any projection or signed-block parsing, and prior to
physical `Provider.Post` dispatch. That same validator rejects exact,
Unicode-escape and simple-fold equivalent duplicate keys at every nesting
level. The Agent transcript remains the authority for visible text and
Tool ID/name/arguments; the new guard additionally certifies that the raw
serialized evidence has only one JSON interpretation. Invalid persisted
replay returns `ErrUnsupported` with **no physical model request, no new
Tool or billing receipt, no replay and no automatic retry**. Authentic
phased assistant messages and signed/encrypted thinking/tool replay retain
their original opaque bytes and normal continuation behavior. No durable
format/schema bump, migration, Host route, new adapter or compatibility
bridge is introduced.

**Permanent RED→GREEN evidence:**

- `openai/phase_test.go`: direct response → stored phase replay mutation
  covering contradictory role, status, phase and item type. Each tampering
  attempt must fail **before** a second Provider HTTP request. Existing
  positive phased final/tool replay tests remain unchanged.
- `anthropic/regression_test.go`: actual signed Thinking + local Tool replay
  mutation covering duplicate block type, Tool ID/name and thinking
  signature, all rejected before second HTTP request. Existing valid
  signed replay and Tool continuation tests remain unchanged.
- A separate isolated Go `-overlay -race` restored **only** the original
  reviewed Head's `openai/replay.go` and `anthropic/replay.go`, leaving these
  permanent tests in place; all eight tampered replay cases turn RED and
  dispatch upstream. Corrected source is GREEN. Focused tests passed
  **10 consecutive Race repetitions**, full Provider Race and strict
  Provider lint **0 issues**.

This is not approval to merge PR #87 or publish beta.24. New actual Head
GitHub CI/security and fresh independent merge admission remain required;
no live gateway replay compatibility is claimed.

**Final local gates for persisted replay JSON integrity P1:**

- **PASS, exit 0:** full repository `make check` including Go
  unit/Race/vet/lint, TypeScript lint/typecheck/**46 tests**/build,
  evaluation/benchmarks, A2A/MCP/OTel and beta.24 clean-consumer.
- **PASS, exit 0:** real isolated Docker PostgreSQL/Redis integration with
  `node scripts/run-integration.mjs`; all test containers/network removed.
- **PASS:** focused direct replay Race `-count=10`, full Provider Race and
  strict Provider `golangci-lint` (0 issues), original two-file source
  mutation RED (8 upstream replay dispatches), repaired source GREEN;
  gofmt/git diff --check.
- The two preexisting untracked OpenAI probe scripts are excluded from all
  commits, with unchanged SHA-256. No external live gateway/Provider
  credentials or tools were used. Fresh GitHub CI/security and immutable-
  Head merge admission still required; the repair itself approves neither
  merge nor beta.24 release.

## 2026-11-03 — PR #87 independent P1: Anthropic SSE message_start content substitution

**Immutable PR Head independently reviewed:**
`0abcb92c2604f0cae6badb8383414f8de9b534ea`. All applicable GitHub
CI, security and coverage checks completed successfully on this Head before
this audit began. Existing two untracked probe scripts were excluded.

**Read-only RED on unchanged Head:** `Anthropic.GenerateStream` decoded and
validated the outer `message_start.message` type and assistant role, then
silently reset `body.Content = nil` without checking the originally supplied
`message_start.message.content`. A gateway could send a nonempty initial
physical Tool item with ID `other-call`, then an inconsistent later
`content_block_start`/`stop` Tool item with ID `call-one`; the adapter
accepted the latter as a completed executable local Tool with verifiable
Usage. Similarly, a preexisting text block or explicit JSON `null` content
could be erased to authorize a replacement local Tool. The real Anthropic
SSE content is generated by later indexed block events; an initial content
array must be empty, not silently replaced by different physical evidence.

A temporary `/tmp` Go test overlay on the **unchanged** reviewed Head sent
three malformed `message_start` envelopes followed by otherwise-completed
Tool streams. Direct Provider tests returned `err=nil`, executable ToolCall,
ProviderReplay, ResponseID and Usage in **all three** cases. A separate
permanent public **Agent → mocked Anthropic SSE HTTP → Kernel Memory Store**
test registered a real synthetic Tool Handler: restoring only original
`anthropic/client.go` via `go test -overlay -race` yielded **one actual local
Tool Handler execution (`effects=1`, `requests=2`)** for each of the three
conflicting initial-content cases. No live Provider or production business
Tool was invoked.

**Minimal hard cut:** When handling the initial `message_start`, validate
the original physical `message.content` before any start-state assignment or
content reset: genuinely omitted legacy content and explicit empty `[]` are
valid; `null`, non-array types and any nonempty array are a
**`providerhttp.ErrProtocol` framing failure**. This is not a completed
provider response and cannot mint billing proof. Do not forward its Tool,
text, signed replay or terminal SSE ResponseID/Usage and do not retry by
reinterpreting later content blocks. All later normal content-block ordering,
Tool identity, cumulative Usage and signed Thinking rules are unchanged.
The change is only in the existing Anthropic SSE event handler, with no
additional adapter, Agent/Kernel code, schema, Host route or compatibility
shim.

**Permanent RED→GREEN regression coverage:**

- `anthropic/message_start_content_authority_test.go` tests direct physical
  SSE acceptance of empty `[]` and truly omitted content as positive controls,
  plus nonempty Tool/text arrays, explicit `null` and non-array content as
  protocol failures. All invalid cases require **no ToolCall, ProviderReplay,
  ResponseID, Usage or terminal SSE success**.
- `anthropic_sse_start_content_agent_test.go` exercises the real public
  Agent→Anthropic SSE→Kernel store path in three negative cases, including
  real registered Tool Handler effects. Repaired implementation consumes only
  the first valid ModelInvocation (17 input + 5 output tokens) and leaves the
  second unverified, with **zero Tool effects**, no fabricated second completed
  billing receipt and no replay.
- Isolated restoration of exact original reviewed-Head `anthropic/client.go`
  using a read-only Go overlay turns the same permanent direct and Agent
  tests RED with `effects=1` in every public Agent negative case; fixed
  source is GREEN. Focused tests passed ten consecutive Race repetitions,
  complete Provider Race passed, strict Provider lint reported zero issues.

No upstream live gateway compatibility claim or release approval is implied.
Fresh GitHub CI on the new actual PR Head and a distinct immutable-Head
merge-admission audit remain required; this review does **not** authorize
merging PR #87 or publishing beta.24.

**Final local gates for Anthropic SSE message_start content P1:**

- **PASS, exit 0:** full repository `make check`, including all Go
  unit/Race/vet/lint, TypeScript lint/typecheck/**46 tests**/build,
  evaluation/benchmarks, A2A/MCP/OTel and beta.24 clean-consumer cohort.
- **PASS, exit 0:** real isolated PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs`; all temporary Docker containers and
  test network cleaned.
- **PASS:** focused direct/Agent Race `-count=10`, complete Provider Race,
  strict Provider lint (0 issues), original immutable-Head source mutation
  RED with actual `effects=1` for all three Agent cases; repaired source
  GREEN. `gofmt` and `git diff --check` passed.
- The two previously existing untracked OpenAI probe scripts were excluded
  and remain SHA-256 identical. No live upstream Provider credentials were
  exercised. New actual-Head GitHub CI/security plus a separate independent
  merge-admission audit remain required; no merge/beta.24 publication is
  authorized by this local repair.

## 2026-11-03 — PR #87 independent P1: Anthropic SSE Tool input delta substituted physical start arguments

**Immutable audited PR Head:** `ad65aba8265d6315a7fa8d3548bb45e33f4fefae`.
All applicable GitHub CI, security and coverage checks were completed
successfully when this independent review began. The only preexisting
untracked files were two OpenAI probe scripts, excluded from this change.

**Read-only RED on the unchanged Head:** The existing Anthropic SSE
`content_block_start` handler preserved the physical `tool_use.input` on
its parsed block. However, if later `input_json_delta` frames arrived,
`content_block_stop` unconditionally replaced that original input with
`chunks[index].String()`. Thus a gateway could send a nonempty original
Tool input `{"x":1}` followed by a later `partial_json` encoding
`{"x":2}`, or a nested input `{"nested":{"approved":false}}` followed
by `{"nested":{"approved":true}}`, and the adapter would accept and
authorize the latter as the executable local Tool's arguments. An initial
`input:null` followed by a valid Tool JSON fragment was likewise silently
replaced. The outer SSE framing, Provider identity and final Usage remained
valid, so none of the prior uniqueness/Role/Tool ID checks noticed the
conflicting physical Tool argument evidence.

An isolated `/tmp` Go `-overlay -race` test on the original Head confirmed
**three invalid argument substitutions** returned `err=nil`, an executable
ToolCall, ProviderReplay and completed ResponseID/Usage. Two positive
controls already passed: physical initial `input:{}` plus later JSON delta,
and a populated initial JSON object **with no later delta**.

**Minimal repair:** In the existing `content_block_stop` Tool reconstruction,
when a nonempty accumulated `input_json_delta` is present, require the
initial physical `tool_use.input` to be a valid **empty JSON object**.
If initial input has members, is `null`, or is not an object, fail as
`providerhttp.ErrProtocol` **before** replacing any Tool input, returning
local Tool authorization, saved replay or successful terminal SSE Usage.
An empty JSON object plus a streamed fragment and a complete object with no
fragment remain supported, including nested JSON. This treats conflicting
initial/final physical Tool arguments as a malformed SSE stream, not a
completed paid response; the untrusted stream does not mint completion or
billing evidence. No new provider adapter, Agent/Kernel logic, Host route,
persistence schema, migration, automatic retry or compatibility bridge was
added.

**Permanent regression and independent source mutation evidence:**

- `anthropic/tool_input_delta_authority_test.go`: direct Anthropic SSE tests
  exercise 3 conflicting Tool input substitutions with valid final Usage,
  plus 2 positive cases preserving legitimate empty-input streaming and
  complete initial input without deltas.
- `anthropic_sse_start_content_agent_test.go`: extends the public
  **Agent → mocked Anthropic SSE HTTP → Kernel Memory Store** regression
  with the 3 conflicting input cases alongside prior message-start content
  checks. Corrected source proves **zero local Tool Handler effects**,
  **two** physical Provider requests and only the **first** unambiguous
  ModelInvocation consumed (17 input + 5 output tokens); no unverified
  second completion/Usage receipt or Tool replay is fabricated.
- Isolated original-Head mutation using `go test -overlay -race` restored
  only `anthropic/client.go`, leaving all permanent tests in place. The
  three direct invalid cases turned RED, and **all three public Agent cases
  actually executed a registered local Tool Handler once** (`effects=1`,
  `requests=2`). This was a synthetic local handler, never an external
  business Tool or live Provider. Valid controls remained GREEN.
- On repaired source both direct and public Agent suites passed 10
  consecutive Race repetitions; full Provider Race and strict Provider
  golangci-lint (0 issues) passed.

Live gateway/provider compatibility was not tested. Fresh GitHub CI and
separate independent merge-admission audit on the new actual Head remain
required. This repair is **not** authorization to merge PR #87 or publish
beta.24.

**Final local quality gates for Anthropic SSE Tool input substitution P1:**

- **PASS, exit 0:** full repository `make check` on the repaired source
  (all Go unit/Race/vet/lint, TypeScript lint/typecheck/**46 tests**/build,
  evaluation/benchmarks, A2A/MCP/OTel and beta.24 clean-consumer cohort).
- **PASS, exit 0:** real isolated PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs`, with all test containers and network
  removed after the run.
- **PASS:** focused direct/Agent Race `-count=10`, full Provider Race,
  strict Provider lint **0 issues**, original reviewed-Head source mutation
  RED with **three actual Tool Handler executions** in the public Agent
  negative paths, repaired source GREEN. `gofmt` and `git diff --check` passed.
- Both previously existing untracked OpenAI probe scripts remain SHA-256
  identical and were excluded from commits. No live upstream Provider or
  gateway credentials were tested. GitHub checks on the **new actual PR Head**
  and a separate immutable-Head merge-admission audit remain mandatory.
  Neither merging PR #87 nor publishing beta.24 is authorized by this fix.
