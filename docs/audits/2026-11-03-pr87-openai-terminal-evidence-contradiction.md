# PR #87 independent P1 — OpenAI completed response terminal-evidence contradiction

**Date:** 2026-11-03
**Immutable audited PR Head:** `0eb08d962a8885aec1f11bb5d507121b31f79fbf`
**Scope:** OpenAI Responses Unary/SSE physical completion proof before local Tool authority or durable usage.

## Confirmed finding: contradictory completed envelope authorizes local Tools

The original adapter required `object=response` and `status=completed` but
discarded two documented physical terminal fields: `error` (model generation
failure) and `incomplete_details` (truncation evidence). These fields are
normally `null` in a completed Responses object; non-null values conflict
with its claimed completed status. A gateway could return `status=completed`
together with `error:{"code":"server_error"}` or
`incomplete_details:{"reason":"max_output_tokens"}`, a valid local
`function_call`, and final cumulative Usage. Before the fix, both
`Generate` and `GenerateStream` returned executable ToolCalls, verified
Usage and successful terminal SSE events despite contradictory physical
completion evidence.

**Independently reproduced RED on the unchanged reviewed Head:** new
`openai/terminal_contradiction_test.go` tests confirmed invalid non-null
error/truncation fields (including `{}` and invalid scalars) were accepted in
Unary and SSE; legitimate absent or explicit `null` fields remained valid.
A new public **Agent → mocked OpenAI HTTP SSE → Kernel Memory Store →
registered Tool Handler** test used a genuine first completed text response
followed by a contradictory Tool response. In both `error_object` and
`incomplete_details` cases, the original immutable Head actually invoked
the registered local Handler once (**effects=1**, **requests=2**). The Run
then entered the uncertain Tool-result path due to the synthetic Handler's
intentional empty result. These were isolated local fixtures; no real
credentials, production Tools or live gateway requests were used.

A separate, read-only Go `-overlay -race` test restored **only** the
original Head's `openai/client.go` while retaining the new tests. Both
direct and public Agent tests turned RED on the immutable source. Repaired
source passed the same focused Race tests.

## Minimal hard cut

The OpenAI outer `responseBody` now retains `error` and
`incomplete_details` as presence-sensitive raw JSON. A physical completed
response is valid only when both are genuinely absent or explicitly JSON
`null`. Any non-null value is a contradictory terminal proof and returns
`ErrUnsupported` with **no** ResponseID, Usage, executable ToolCall, replay
or successful SSE terminal event. The check happens in the already shared
`decodeResponse` path before projecting any Tool output and therefore covers
both Unary and SSE. Legitimate completed response IDs, Tool IDs, arguments,
encrypted replay and nullable status fields remain unchanged.

No automatic correction, synthesized completion receipt, retry-policy
change, Host/Agent/Kernel modification, provider fallback, SQL schema change,
replay-format version or compatibility bridge was added. Status `incomplete`
continues to produce the existing `ErrIncomplete` classification.

## Regression and merge-admission evidence

- `go/agent-runtime-provider/openai/terminal_contradiction_test.go`:
  direct Unary/SSE positive and negative terminal-evidence cases.
- `go/agent-runtime-provider/openai_terminal_contradiction_agent_test.go`:
  public Agent/Kernel test that requires zero registered local Tool effects,
  no second ModelInvocation completed receipt/Usage, and only the first
  genuine **17 input / 5 output** tokens accounted.
- The original immutable-Head overlay is RED and repaired source GREEN,
  including focused `-race` runs.

**Final local verification PASS:** complete repository `make check` exit
**0** (Go unit/Race/vet/strict Lint **0 issues**, TypeScript
lint/typecheck/**46 tests**/build, scenario corpus, benchmarks, OTel/A2A
and beta.24 clean-consumer release cohort); independent real PostgreSQL/
Redis integration exit **0**, temporary Docker resources removed;
post-document `make metadata release-check` exit **0**, gofmt and
`git diff --check` PASS. Focused Provider/Agent regression Race passed
**10 consecutive repetitions**. Both preexisting untracked OpenAI
probe scripts remain SHA-256 identical and excluded from all commits.
No live third-party Provider or production Tool was used.

New-Head GitHub CI/security results and a separate immutable-Head
merge-admission audit are required before any merge decision. PR #87
remains open and beta.24 unreleased.
