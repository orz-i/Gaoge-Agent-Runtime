# 2026-11-05 — Three-part Agent governance: independent-audit remediation

## Scope and immutable review baseline

- Independent review base: `governance/agent-provider-quality-20261105@b4d99761b6cc24380480cdbe977ceee58dbd048a` (4 prior commits relative to `main@f2999a784ab272728ed5a6ee1eee680de28ee855`).
- Independent finding evidence: `/private/tmp/gaoge-threepart-independent-audit.V9DBCu/` on the Mac Pi device (temporary files; not committed).
- Original findings: **P1 x 1, P2 x 3**. This document records narrow corrective code and regression tests, not a new kernel/feature scheduling protocol.
- User-owned untracked OpenAI Responses probe scripts are intentionally not staged.

## Invariants and trust boundaries

1. A named Provider's previously committed `BindingFingerprint` must remain byte-for-byte identical when no new Host effort policy is configured; a changed actual policy still invalidates the old invocation.
2. The original Model/Tool request, original Tool IDs, Provider ResponseID, authenticated binding, and opaque replay must remain the same across retry or process reconstruction. A restored pending ModelInvocation cannot mint a new authorization from a mismatched binding.
3. ModelOptions are validated in their *raw* JSON form before an immutable Harness `ConfigSnapshot`/Role is canonicalized, hashed, or used for request preparation. Duplicate or case-fold-equivalent keys must not silently change their meaning.
4. For a given exact Provider/model ID, an effort setting may not be combined with incompatible sampling parameters and must not be widened by a Host override of an already known public model. Invalid configurations cannot reach physical Unary/SSE transport; accepted configurations must preserve their parameters.
5. Optional baseline `reasoningEffort` is either absent or one valid exact string; `null`, `""`, duplicate, mixed-case shadow and unknown keys are rejected before preflight. Legacy reports with unrelated extra non-execution metadata remain readable, but Python does not silently ignore effort aliases.
6. No new root/child execution identity, Kernel lease/Epoch, Tool effect authorization, Store CAS, SQL DDL, Run status, compat bridge, model/Tool loop or Host intent router is introduced.

## P1 — Legacy Provider HMAC is unchanged when effort is omitted

The new optional `EffortModelLevels` in `internal/providerhttp.BindingSpec` now carries `json:",omitempty"`. The unconfigured map is absent from the serialized HMAC material (including an empty map), matching the previous layout **exactly**; nonempty Host-certified policy still changes the fingerprint, using cloned/sorted level values.

Permanent regression tests:
- `TestUnconfiguredEffortPreservesOldPersistedProviderBinding` compares legacy-HMAC reference code against the current binding for OpenAI default, OpenAI strict-schema, and Anthropic default and rejects policy alias/order drift.
- `TestHistoricalPendingNamedProviderBindingSurvivesEffortUpgrade` creates a genuinely pending `agent.ModelInvocation` after a retryable physical error, asserts its fingerprint equals the **old** HMAC, reconstructs a different Runner/Provider from the same store, advances the original retry gate and consumes the same original ModelInvocation successfully. OpenAI and Anthropic are both covered.
- The existing changed-binding and final physical-dispatch fence suites remain required; there is no fallback allowing an altered binding.

## P2 — One raw-JSON uniqueness authority before freezing ModelOptions

The original bounded, Unicode simple-fold-aware Provider JSON-member validator was moved without semantic expansion into Core `jsoncontract`. Provider retains its original `ErrProtocol` transport classification through tiny wrappers. The same Core validator guards `canonicalJSON` in Harness before any `json.Unmarshal` can erase keys. It applies to both root `ConfigSnapshot.ModelOptions` and `RoleSnapshot.ModelOptions`.

Permanent tests:
- `TestUniqueObjectRejectsDuplicateAndFoldedMemberNames`: invalid roots, nested shadowing, Unicode/case-fold aliases, trailing JSON, nesting cap and valid distinct entries.
- `TestModelOptionsAreUniqueBeforeFrozenConfigHash` and `TestModelOptionsRequireObjectBeforeSealing`: 6 adversarial configurations in both Host and Role scopes, plus valid canonical material.
- Existing Provider JSON/Tool-root parity suites still pass.

## P2 — Model-aware effort/sampling, all physical paths

OpenAI rejects sampling when an explicitly selected effort is not `none`, and on exact known reasoning models whose omitted effort defaults to non-`none`. A supported explicit `none` can retain sampling, as can existing nonreasoning models; unknown gateway routes are not inferred from prefixes. Anthropic rejects sampling alongside a requested effort and on the documented adaptive-thinking model IDs even when effort is omitted. For both adapters a Host may certify exact *unknown/private* IDs, but may not override the known public model table (including falsely enabling Astra `none`).

Permanent tests:
- `TestOpenAIReasoningSamplingCombinationsBeforeUnaryAndStream` rejects high+temperature, high+top_p, omitted non-none default+sampling and unsupported `none`, verifying no HTTP request; it preserves supported explicit `none`+sampling and nonreasoning sampling on Unary and SSE.
- `TestAnthropicSamplingCompatibilityBeforeUnaryAndStream` rejects Sonnet/Fable/Opus invalid combinations before Unary/SSE, while preserving older gateway-only omitted-effort behavior.
- Existing `TestExactModelEffortProjectedBeforePhysicalOpenAI` now uses a supported high-effort request without sampling.
- Private override restrictions covered by existing effort tests.

References: [OpenAI model compatibility](https://developers.openai.com/api/docs/guides/latest-model) and [Anthropic thinking compatibility](https://platform.claude.com/docs/en/build-with-claude/thinking).

## P2 — Baseline stdin preserves requested effort

The Go baseline input scanner now requires exact known top-level member names and a valid nonempty JSON string for a *present* `reasoningEffort`. Go's case-folded struct aliases and `null` string zero values are rejected instead of being reinterpreted. No HTTP is sent on invalid input. Python preserves historical report metadata, but rejects any `reasoningEffort` case-fold alias rather than silently stripping it.

Permanent tests: `TestBaselineInputRejectsShadowedAndExplicitlyEmptyEffort` and the Python launcher tests cover null, empty, aliases, duplicates, unknown keys, omitted effort and explicit valid effort. Existing report retry and comparison tests remain authoritative for historic reports.

## Verification status

- Original five adversarial audit overlays turned GREEN (3 old-HMAC configurations, 3 OpenAI cases including public-model override, 2 Anthropic cases, 3 Host-config duplicate cases, 4 baseline-stdin cases).
- Core `go test -race ./... -count=1` — PASS.
- Provider `go test -race ./... -count=1` — PASS.
- Harness `go test -race ./... -count=1` — PASS.
- Task-agent `go test -race ./examples/task-agent -count=1` — PASS.
- Python baseline launcher — 62 tests PASS.
- Whole workspace `fmt-check`, `tidy-check`, `go-vet`, `go-lint` — PASS (0 lint issues).
- Other module race, TypeScript/evaluation/integration/clean consumer and final full `make check` are separate, later release/merge-admission gates; do not report unrun checks as passed.
- No live paid OpenAI/Anthropic/gateway request or external production/real power-failure test was run. Local HTTP and retryable in-process persistent Store fixtures do not prove provider-side billing/exactly-once.

## Exit criterion

The initial P1 and three P2 have concrete regression tests and passing affected-chain checks. This is a **local repair candidate**, not a new independent final-code verdict; an independent final review must inspect the full corrected chain on the eventual locked PR Head, and all required GitHub CI must pass before merge. An unresolved new P1/P2 from that review reopens the gate.
