# PR #87 — independent P1: reused physical Provider response ID across model invocations

**Date:** 2026-11-03
**Immutable audited PR Head:** `e76f7854badb0742c546b0794add379bc99e3d15`
**Disposition:** P1 confirmed on original Head and repaired locally. New-Head CI/security and independent merge admission remain separate gates.

## Proven authorization and billing defect — original Head RED

Existing Provider adapters enforce an exact, nonempty physical completion
ResponseID before decoding local Tool calls, but Agent persisted each named
Provider's `ModelInvocation.ProviderResponseID` without checking whether a
*previous distinct invocation* under the **same provider binding** had
already consumed that physical response identity. An upstream gateway could
return a valid first text response, then reuse the *same* completed response
ID with a different, otherwise valid local Tool Call ID on the second
invocation. Agent would authorize the new local Tool and charge the reused
response as a second completion even though its physical identity did not
prove a new generation.

**Independent RED on the unchanged audited Head:** New public
**Agent → mocked OpenAI Responses or Anthropic Messages HTTP SSE →
Kernel Memory Store → registered local Tool Handler** regressions each
provided two physical HTTP responses with identical completed
`ResponseID="reused-provider-id"`, `17 input / 5 output` final tokens.
The second response returned a distinct `second-tool-id`:
**OpenAI actually executed one local Handler (`effects=1`) and Anthropic
actually executed one (`effects=1`)**. A second case for each Provider
reused the response ID on a semantically rejected mandatory-Tool reply:
no Tool side effect occurred but Agent consumed the second paid receipt,
double-counting **LLMCalls=2, input=34, output=10** instead of the first
trusted **LLMCalls=1, input=17, output=5**.

A read-only `go test -overlay -race` restored **only**
`go/agent-runtime/agent/model_invocation.go` from exact immutable Head
`e76f7854badb0742c546b0794add379bc99e3d15`, leaving the permanent
tests intact. Both providers' executable-Tool cases were RED
(`effects=1`) and both known-rejected cases were RED on duplicate billing.
No real Provider credentials, gateway requests, production Tools, or
external effects were used.

## Minimal correction — Agent ModelInvocation identity fence

Before accepting a successful or known-billed-rejection Model response,
Agent now checks its **already durable ModelInvocations in the same Run**.
If another completed or consumed invocation for the exact same named
Provider and immutable `BindingFingerprint` has already persisted an
identical normalized `ProviderResponseID`, the new result fails closed
as `ErrInvalidModelResponse`. It must **not** become a completed or
consumed second invocation, bill a second local Usage receipt, enter an
authorized local Tool batch or retain encrypted/signed ProviderReplay.
This checks the new response's identity before mutating feature state,
while retaining the original still-pending ModelInvocation. The existing
Agent failure handling returns `agent.model_failed`.

The check **does not** reject a retry of the *same* pending invocation,
an unrelated named Provider, a distinct Host `BindingFingerprint`,
unnamed local models without verifiable physical identity, or a genuinely
new provider response ID. IDs are not globally deduplicated across
separate Runs; no new global index, Kernel/Store schema, Host router,
Provider protocol, retry policy, compatibility shim or migration is added.
This is a targeted, pre-stable hard cut in **Agent-owned persisted receipt
authority**, not an HTTP adapter workaround.

## Permanent tests and validation

- `go/agent-runtime-provider/duplicate_physical_response_id_agent_test.go`:
  four public real HTTP SSE → Agent → Kernel/Tool cases
  (OpenAI/Anthropic × Tool execution/known rejection). Assert **zero**
  Tool side effects and only the first trusted **17/5** token receipt.
  Independent original-Head overlay reproduced each unsafe case.
- `go/agent-runtime/agent/model_invocation_response_identity_test.go`:
  internal identity-scope controls for same binding/different invocation,
  completed vs consumed history, retry of the same invocation, unrelated
  Provider/binding, unnamed/unverified local models and new/empty IDs.
- Existing synthetic `model_invocation_fault_test.go` now derives a
  stable physical response ID from `request.InvocationID`;
  `structured_agent_test.go` uses distinct HTTP response IDs per call;
  `examples/task-agent/baseline_http_test.go` generates a fresh physical
  response ID per outgoing mock HTTP response without changing the
  deterministic Task payload. The original completion-repair, restart and
  benchmark baseline/oracle tests retain their original assertions and pass.
  No production Host behavior or local test Tool implementation changed.
- Corrected focused public Agent/provider Race **10 successive repetitions**
  passed, and internal Agent identity Race passed.
- Full repository `make check` **exit 0** on corrected source:
  Go unit/Race/vet/strict lint (**0 issues**), TypeScript lint/typecheck/
  **46 tests**/build, evaluation/benchmark smoke, MCP/A2A/OTel and beta.24
  clean-consumer cohort.
- Real isolated PostgreSQL/Redis integration via
  `node scripts/run-integration.mjs` **exit 0**, temporary Docker containers
  and network removed.
- Post-document `make metadata release-check` **exit 0**, gofmt and
  `git diff --check` **PASS**.

The two original untracked OpenAI probe scripts are SHA-256 unchanged and
excluded from commits. No live Provider credentials or production Tools
were used. GitHub CI/security on the **new pushed PR Head** and a fresh
independent immutable-Head merge-admission audit remain necessary. Neither
merging PR #87 nor publishing beta.24 is authorized by this local fix.
