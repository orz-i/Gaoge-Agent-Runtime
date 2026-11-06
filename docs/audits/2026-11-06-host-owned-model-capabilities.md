# 2026-11-06 — Host-owned ModelProfileSource hard cut: security and convergence record

## Problem, scope, and source of authority

The previous beta.24 OpenAI and Anthropic adapters embedded multiple exact-model capability tables. These went stale when the upstream vendors added models (e.g. new GPT models), forcing a runtime SDK update for ordinary catalog maintenance. A separate Anthropic forced-Tool compatibility list even inferred unsupported behavior from model-name prefixes. Model naming and feature availability are product/Host metadata, **not** Agent Runtime authority.

**This change implements the user's hard-cut directive:** the SDK exports only the provider-neutral `model.ModelProfileSource` interface, `model.StaticModelProfiles` and `model.Capabilities` value; both Provider adapters accept `Config.ModelProfiles` as externally supplied Host data. There are **zero production vendor model-ID allowlists** and no built-in catalog fetcher, secondary capability registry, persistence mirror or automatic discovery mode.

## Safety invariants and trust boundaries

| Invariant | Boundary / verified behavior |
|---|---|
| M1: the Host alone selects/certifies exact-model abilities | The SDK never infers structured output, effort or forced Tool support from an exact name, date suffix or prefix. Source may come from a Host-owned JSON/YAML file, DB or discovery API. Missing capabilities remain unverified. |
| M2: the Provider snapshots the imported data once | `New` retrieves `SnapshotModelProfiles` once, bounds input count/ID/effort vocabulary, sorts copies, never retains caller-owned slices. `Capabilities` exposes an additional copy. Unsupported/malformed metadata fails before an HTTP attempt. |
| M3: immutable selected-model binding | The secret-backed Provider HMAC covers transport identity. A second model-specific HMAC covers *the selected request model* and its exact certified feature set, including omitted-effort sampling and OpenAI/Anthropic forced-Tool restrictions. An unrelated catalog update does not invalidate an unchanged in-flight model. |
| M4: no silent request-policy downgrade | Explicit unsupported effort or strict schema is rejected before either Unary or SSE HTTP. OpenAI non-none explicit effort and Anthropic any explicit effort reject sampling; default omitted-effort restrictions are enforced **only** when Host provided. A Host-certified forbidden mandatory Tool (`any` in Anthropic or `required` in OpenAI) fails pre-dispatch; automatic Tools remain unaffected. |
| M5: pending grant is tied to original request | A modified selected profile, endpoint, secret or Host binding returns existing `ErrProviderBindingMismatch` without dispatching/retrying the physical ModelInvocation. Restore the original profile to resume the same pending attempt. |
| M6: historical provider fingerprint is a hard cut | Old pre-change beta.24 Provider fingerprints are deliberately different, **including when the new catalog is empty**; an old pending attempt cannot be silently replayed or reclassified. No legacy compatibility bridge, run schema upgrade, or duplicated execution loop. |
| M7: billing identity remains independent | No code change to physical Provider ResponseID, SSE/Unary envelope validation, Tool validation, typed rejection receipts, known/unknown Usage, ModelInvocation Kernel CAS, or ledger semantics. Existing suites cover these. |

**Trusted Host boundary:** The SDK validates that a supplied capability is well-formed and bound to the selected model. It cannot independently verify an external provider supports what the Host claims. An unsupported upstream feature must return its HTTP failure without automatic lowering/dropping options. Host must authenticate and verify its source; a malicious Host-supplied catalog is outside this authority model, not a reason to scatter model names in the runtime.

## Implementation

- `go/agent-runtime/model/model_profile_source.go`: interface, function adapter and immutable-by-copy map adapter. `model.Capabilities` now includes `SupportsSamplingWhenEffortOmitted` and `SupportsForcedToolChoice`.
- `go/agent-runtime-provider/internal/providerhttp/model_profiles.go`: bounded exact-ID and per-protocol effort validation; one snapshot. `binding.go` now fingerprints the selected model, avoiding global-catalog invalidation.
- `openai/client.go` and `anthropic/client.go`: same shared interface, frozen exact profiles and unchanged wire-protocol projection. Remove `openai/effort.go`, `anthropic/effort.go`, Anthropic `forcedChoiceUnsupported` family-prefix inference, and both legacy `StructuredOutputModels` / `EffortModelLevels` Config fields.
- `task-agent`: Host keeps its explicitly selected Schema/effort certification and may import a model profile source; no automatic public-model recognition.

## Invariant × path × unexpected input test matrix

- Host model catalog: nil source, valid `StaticModelProfiles`, `ModelProfileSourceFunc` called once, immutable after Host mutation, sorted effort vocabulary, duplicate/unknown level, malformed ID, 256 model bound.
- Model identity: exact selected ID, private route, 512 seeded prefix/dated-ID variants. No public default capabilities without Host certification; named future models can be introduced by Host alone.
- OpenAI and Anthropic: Unary and SSE; valid explicit effort, true/false Structured Outputs; omitted effort + sampling, explicit incompatible effort + sampling, unsupported level, duplicate JSON; no paid HTTP on rejected combinations.
- OpenAI/Anthropic forced-Tool: Host-forbidden exact model rejects both transports before HTTP; adjacent/prefix-alike name does **not** inherit an unsupported restriction. Original Tool/Replay authority remains unchanged.
- Pending persistence: original Provider HMAC and request model preserved across retry, restarted Runner and added unrelated profile; selected-model profile change returns `ErrProviderBindingMismatch` with no second physical HTTP, restoration permits exactly one physical continuation.
- Baseline: real loopback Host-selected model, native schema preflight, Tool/reasoning replay, fixed two sample cases, encrypted replay, distinct private Usage and original Context/Tool recovery.
- Core: existing Provider ResponseID consistency, typed refusal receipts, Usage accounting and Kernel CAS coverage retained; no new billing identity code.

## Breaking changes and limitations

The old Config fields `StructuredOutputModels` and `EffortModelLevels` are **removed**. The new single field `ModelProfiles model.ModelProfileSource` must be supplied by the Host when effort/strict JSON/output restrictions are needed. Old beta.24 pending ModelInvocations cannot resume under the new per-model fingerprint; Hosts must explicitly re-create or migrate them according to their own rollout policy. There is no SQL schema change and no SDK compatibility bridge. Public model IDs may be kept in **test fixtures** and examples of external Host data, but none are compiled into SDK feature-dispatch logic.

The SDK does not fetch live model metadata, validate external catalog signatures, certify model cost/context windows or guarantee real third-party gateways accept requested options. Source and billing/replay validation beyond offline mocks require external live verification, separate from the deterministic CI gate.

## Verification (local, implementation commit `7cb33bd38f72507fcc1d6f2e972935a065b0deaa`)

- All **12** Go workspace modules: full `go test ./...`, `go test -race ./... -count=1`, and Format/Tidy/Vet/Lint gates PASS. GolangCI-Lint reported **0 issues** for each module.
- TypeScript lint/typecheck/46 tests/build PASS; `make agent-baseline-test eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` PASS, including the beta.24 **clean-consumer** installation/build. The task-agent Host-imported Schema preflight, encrypted Tool replay and two real loopback task/recovery cases PASS with both absent and explicit effort.
- `make coverage`: PASS. Go statement coverage **75.38%** (minimum 70%), TypeScript statement coverage **88.04%**.
- Actual Docker PostgreSQL/Redis-backed integration: PASS for the complete four suites, each with a real successful process exit and Race enabled: `go/agent-runtime-postgres`, `go/agent-runtime-harness-postgres`, `go/agent-runtime-redis`, and `integration/continuation-e2e`. The initial aggregate `make integration` connection dropped during the fourth suite, so each suite was explicitly rerun to completion. The two dedicated Docker services, volumes and network were removed with `docker compose ... down -v --remove-orphans`.
- `govulncheck` on all 12 workspace modules: no code-reachable vulnerabilities reported; 3 advisories in required dependency modules not exercised by code. `pnpm audit --audit-level high` PASS with 1 moderate advisory. The aggregate `make security` tool connection / remote vulnerability-index fetch interrupted; these subtargets were verified per-module rather than asserting a successful aggregate exit.
- `make check` began and passed metadata, complete Go unit suites and Core/Provider/Harness/task-agent Race checks before the Mac Pi connection dropped. Every remaining Race workspace module and each subsequent check target was successfully rerun separately; **do not** claim an uninterrupted `make check` exit code.
- Previous frozen/selected-ModelInvocation replay tests, binding drift tests, prefix/capability tests, both Unary/SSE fail-closed checks, and seeded 512-case unknown-name property test PASS.

This is a **locally tested pre-stable hard-cut candidate**, not a release. GitHub PR-Head CI, third-party live billable Provider tests and a fresh post-change independent audit are **not executed**. The Host owns migration policy for old pending beta.24 ModelInvocations; unsupported/changed routes remain fail-closed, not silently replayed.

## Follow-up — default-deny authorization convergence

The subsequent independent code audit on `456543c7` found two P2
model-authorization regressions and reproduced both with Go overlays.
This audit's original beta.24 hard-cut implementation did **not** close
them. See [separate P2 remediation](2026-11-06-host-owned-model-capabilities-p2-remediation.md)
for the positive, default-deny capability change, the Host effort
self-certification fix, and fresh post-fix validation evidence.
