# PR #87 — independent Anthropic rejected-content SSE delta framing P1

**Date:** 2026-11-03
**Immutable reviewed PR Head:** `b178d8170d777c8dc76b8c11d22c0da4e22f8a0f`
**Scope:** Provider completion and billing authority after a prior SSE content semantic rejection. No PR merge or release authorization is implied.

## Finding — proven P1, immutable Head RED

PR #87 already protected Anthropic SSE Tool ownership, original input/delta
substitution, block ordering and cumulative Usage, and drained unsupported
rich content for a legitimate *completed but rejected* paid receipt. However,
once `rejectedContent` became true, the `content_block_delta` handler
counted raw bytes and skipped **all** physical delta-type ownership checks.
A subsequent `text` block could emit `input_json_delta`, `signature_delta`
or an unknown delta, and a rejected `tool_use` could emit `text_delta`.
The final `message_stop` with cumulative Usage then incorrectly promoted
these malformed frames into a trusted `model.post_generation_failed`
completed ResponseID/Usage receipt. If earlier content was rejected, a
later, otherwise valid `tool_use` with initial `input:{"x":1}` could also
receive a contradictory JSON delta `{"x":2}` without the existing
input-ownership check noticing it: the discarded delta was never recorded
by the state machine.

**Independent RED evidence:** a new direct SSE test failed on multiple
mismatched delta/Tool input-substitution cases while accepting positive,
semantically rejected but well-framed paid completions. A read-only
`go test -overlay -race` restored only the exact original
`anthropic/client.go` from the immutable reviewed Head, retaining the new
tests. Both public **Agent → synthetic Anthropic HTTP SSE → Kernel Memory
Store** negative cases failed independently: `wrong_delta_owner` and
`later_tool_input_substitution`. The unchanged original Head persisted
the malformed second model invocation as `consumed` with
`ProviderResponseID=paid-2`, `model.post_generation_failed` and Usage,
causing **LLMCalls=2, input=34, output=10**, instead of one valid first
model invocation (**LLMCalls=1, input=17, output=5**). No local Tool Handler
executed in these cases; the impact is completion/billing authority, not
demonstrated unauthorized Tool effects. All endpoints and Tool handlers
were synthetic local fixtures; no external Provider credentials were used.

## Minimal correction

- Even under irreversible content rejection, known physical block types
  must still accept only their corresponding delta types: `text_delta`
  for `text`, `input_json_delta` for `tool_use`, signed
  Thinking-specific deltas for `thinking`, and no deltas for
  `redacted_thinking`.
- Track only minimal non-content metadata for rejected Tool JSON and
  Thinking signatures. A nonempty Tool JSON delta still requires the
  physical initial Tool input to be an empty object; a duplicate or
  out-of-phase Thinking signature cannot certify completed framing.
  The original initial Tool input is retained solely to validate conflicts
  after a semantic block parse rejection; it is never authorized,
  emitted as text, or stored as ProviderReplay.
- Unsupported *future* block types stay semantically rejected. Known
  properly framed but unrepresentable content continues to return only
  verified ResponseID/Usage as a paid post-generation rejection. Contradictory
  known-type framing fails as `providerhttp.ErrProtocol` with **no
  completed billing receipt**, no ToolCalls, no replay and no terminal SSE
  success. Preexisting bounded frame parsing and final Usage checks remain.
- No Agent/Kernel/Host modifications, provider fallback, retry-policy
  changes, durable schema migration, replay protocol version or
  backward-compatibility bridge.

## Permanent tests and verification

- `anthropic/rejected_stream_delta_framing_test.go`: invalid
  post-rejection text/Tool delta ownership and Tool input replacement,
  with valid rejected-content positive controls.
- `anthropic/rejected_thinking_delta_framing_test.go`: Thinking signature
  duplication, late Thinking after signature, signed input conflicts,
  redacted-block delta rejection and valid unknown future-block semantics.
  A read-only original-Head overlay reproduces four false paid completions;
  corrected focused Race is GREEN.
- `anthropic_sse_rejected_delta_agent_test.go`: independent public
  Agent→HTTP SSE→Kernel checks of persisted invocation/Usage and zero
  Tool side effects for both RED cases.
- Original immutable Head was proven RED via a Go source overlay; fixed
  source runs all focused tests and complete Provider Race GREEN.

**Completed local gates — PASS:** full repository `make check` exit **0**
(Go Unit/Race/Vet/strict Lint 0 issues, TypeScript lint/typecheck/**46**
tests/build, scenario corpus, benchmarks, A2A/OTel and beta.24 clean
consumer cohort); full Provider Race `go test -race ./... -count=1` exit
**0** after all new regressions were added; `make metadata release-check`
exit **0**; real isolated PostgreSQL/Redis integration exit **0** with
all Docker test containers and network removed; `gofmt` and
`git diff --check` PASS. The two original untracked OpenAI probe scripts
remain SHA-256 identical and excluded from commits. No live Provider
credentials or production Tool were used.

Fresh GitHub CI/security checks on the **new pushed Head** and a separate
independent immutable-Head merge-admission audit remain mandatory. No
PR merge or beta.24 release is authorized by this local fix.
