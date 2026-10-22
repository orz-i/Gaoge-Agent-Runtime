# PR #58 — Provider adapters merge-admission remediation

- Date: 2026-10-21
- Branch: `feat/model-provider-adapters`
- Baseline: PR #58 `f0e71b2`, targeting `main`
- Scope: the two P1, two P2 and one protocol-evolution finding from the merge admission review
- Kernel/Store schema changes: **none**

## Resolved findings

| Severity | Finding | Remediation and regression evidence |
| --- | --- | --- |
| P1 | OpenAI omitted/null/partial Usage was interpreted as zero consumption | Parse usage as optional, require explicit input/output counts, retain nil if incomplete; both direct and streaming paths tested, including a budgeted Agent Run that can no longer falsely complete |
| P1 | Anthropic default Thinking and unsupported forced Tool modes | Preserve signed and redacted Thinking blocks in an opaque provider-neutral replay envelope; persist the ordered original assistant blocks on Agent Tool intent and validate visible Tool IDs/names/arguments before replay. Known models that reject forced `tool_choice=any` now fail before HTTP dispatch, never silently downgrade |
| P2 | OpenAI refusal content was discarded | Preserve `refusal` text as the terminal assistant response, covering unary and SSE terminal decoding |
| P2 | Router rejected provider-internal model namespaces | Route by the first slash; allow nested vendor model IDs while rejecting blank path segments/unknown providers |
| Protocol | New Anthropic SSE event types caused hard failures | Skip unknown **top-level** SSE events, but strictly reject unknown content-block fields/types/deltas that could compromise signed replay correctness |

## Boundaries and security

- No provider SDK dependency or dynamic registry is added. Only the existing `model.Client` contract is used.
- `model.ProviderReplay` is optional, versioned and provider-scoped; it is deep-cloned across request/response boundaries and stored only in trusted feature state for reliable continuation.
- The public `agent.ViewState` strips provider-private replay evidence from messages and invocation projections. It is not interpreted as a permission or a Tool receipt.
- The Anthropic adapter checks replayed Tool names, IDs and arguments against the Agent's validated intent before dispatch. Malformed/unknown block formats and cross-provider replay are rejected.
- The Anthropic API may still reject replay when a Host changes the original signed prompt/Tool prefix. There is no attempt to bypass signature validation.
- Provider-specific forced-tool support is not universal. Known models that do not support it return `ErrUnsupported`; unknown aliases remain subject to provider-side validation.
- OpenAI reasoning-plus-function-call replay, arbitrary multimodal payloads and Hosted Tools remain outside the declared text/local-Tool profile.

## Local verification

- `make metadata fmt-check tidy-check go-test`: PASS across all ten Go modules.
- `make go-race`: PASS across all ten Go modules.
- `make go-lint go-vet`: PASS, zero issues.
- `make coverage`: PASS, Go 71.06% (floor 70%), TypeScript 44/44.
- `make security`: PASS on Go 1.27.3, no reachable Go vulnerabilities, pnpm high+ audit clean.
- `make ts-lint ts-typecheck ts-test ts-build eval-smoke a2a-product-check release-check otel-e2e-smoke`: PASS.
- `GOWORK=off GOTOOLCHAIN=go1.26.10 go test -count=1 ./provider/... ./agent ./model`: PASS, proving SDK compatibility on the documented older Go line.
- New tests: OpenAI nil/incomplete Usage and token-gated Agent; Anthropic signed Thinking unary/SSE/Agent Tool replay, tampering and privacy; forced-tool capability gating; nested model router names and refusal propagation.

## Remaining production acceptance

**Live credentialed OpenAI and Anthropic endpoint conformance has not been executed.** Keep production Provider support explicitly gated on those tests, especially version/alias capability checks. A successful GitHub CI run against the new PR Head is still required before merge.
