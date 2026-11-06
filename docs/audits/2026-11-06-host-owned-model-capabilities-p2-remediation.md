# Host-owned model capability authorization — P2 remediation and acceptance

**Date:** 2026-11-06
**Starting audit HEAD:** `456543c7aef12cc47bfb76fb7ea8e6e6f11a71dc`
**P2 implementation commit:** `39110322a1f0c40f066ede8eb5566c3794f5f02d`
**Scope:** `model.Capabilities`, OpenAI/Anthropic Provider adapters, task-agent Host baseline; no Kernel/Harness Store schema changes
**Independent audit evidence:** `/private/tmp/gaoge-host-profiles-fresh-audit-456543c/READONLY-INDEPENDENT-AUDIT.md`

## The two independently reproduced P2 root causes

1. The task-agent baseline accepted the user's `reasoningEffort` as its own verified `EffortLevels` without an independent Host model catalog. With a separate unknown-model JSON Schema opt-in the unverified `max` request reached both Providers' loopback HTTP endpoints.
2. The model capability interface had **negative** omitted-effort sampling and mandatory-Tool flags. Their zero values meant “not prohibited.” Removing built-in model lists therefore caused previously incompatible configurations to reach physical OpenAI/Anthropic Unary/SSE HTTP when the Host supplied no model entry.

These were locally simulated wire-dispatch violations, **not** evidence of actual upstream API acceptance, unauthorized native Tool execution, paid real charges or compromised trusted storage.

## Design decision (no model ID allowlists)

- The Host alone owns the exact model catalog. The SDK still contains **no OpenAI/Anthropic model name lists**.
- Replace `DisallowSamplingWhenEffortOmitted` and `DisallowForcedToolChoice` with **positive**, default-deny `SupportsSamplingWhenEffortOmitted` and `SupportsForcedToolChoice`. Missing, false, and unconfigured model profiles do **not** permit those advanced requests. The Provider enforces this before either Unary/SSE HTTP; explicit OpenAI `effort=none` plus sampling still requires positive verification of that effort level.
- Mandatory local Tools remain usable when positively Host-certified; normal text and *optional* local Tools do not require a mandatory-Tool grant. The Host cannot infer support from model name prefixes.
- The task-agent example's user-selected `reasoningEffort` stays a request preference only. It is **never copied into `EffortLevels`**. The unknown-model schema opt-in permits only a schema probe, **not** effort or forced Tools. The baseline now checks the necessary strict-schema, effort and forced-Tool capabilities in one preflight before paid HTTP.
- To preserve a runnable example, operators can provision a separately trusted model catalog through the **Host-only** `GAOGE_MODEL_PROFILES_FILE` environment variable, or pass an existing `model.ModelProfileSource` through Host code. No model catalog is downloaded by the SDK; user baseline JSON stdin cannot set this path or mint credentials.
- The Host example's JSON loader enforces a 256 KiB byte ceiling, unique/case-fold-distinct JSON members, an object root, known fields, and per-Provider effort vocabulary; errors do not echo file names, contents or keys. The Host is responsible for path permissions, authentication and catalog accuracy.

## Safety matrix and proof

| Security invariant | Successful/control path | Adversarial exception path | Proof |
|---|---|---|---|
| I1: user request cannot certify its own effort | independently supplied Host file certifies exactly the requested level | `max` requested with no Host file, or Host file only permits `low` | `TestBaselineUnverifiedUserEffortRejectedBeforeHTTP`, `TestBaselineHostFileRejectsUncertifiedEffort`; both Providers reject before HTTP |
| I2: missing profile denies advanced grants | Host positively enables mandatory Tools and omitted-effort sampling on exact model | absent model profile, `temperature` with no effort; `RequireToolCall` on Unary/SSE | `TestUncertifiedAdvancedModelFeaturesNeverCrossProviderHTTP`: 2 Providers × 2 features × 2 transports, zero HTTP |
| I3: explicit positive flags are exact, frozen | permission imported for one model; original ModelInvocation resumes after unrelated catalog changes | selected-model permission revoked or different Provider credential/scope | `TestPositiveAdvancedModelProfileIsBoundToSelectedModel` and original provider binding/recovery suites; changed profile rotates HMAC |
| I4: malformed Host metadata cannot silently mint authority | valid Host operator file / function-supplied snapshot | duplicate or case-folded feature keys, unknown fields, null, wrong types, trailing object, oversize file, invalid effort, nonexistent file | `TestHostModelProfileFileRejectsInvalidOrAmbiguousAuthority`; fail-closed without reporting path |
| I5: Provider completion/Usage/Tool receipts remain correct | exact, named Provider ResponseID and original Usage persisted after settled Tool/replay | SSE interruption, Tool response rejection, middleware mutation, cancellation and recovery | original Provider, Agent and task-agent full suites in Race mode, plus real Docker Postgres/Redis integration |

The original *three independent negative oracle families* were re-executed with their unchanged Go test overlays from `/private/tmp/gaoge-host-profiles-fresh-audit-456543c/`:
`TestAuditUnverifiedUserEffortCannotAuthorizePhysicalPreflight`,
`TestAuditMissingHostProfileMustNotSilentlyEnablePreviouslyForbiddenForcedTool`,
`TestAuditUncertifiedDefaultReasoningModelCannotLeakSamplingRequest`.
All changed **RED → GREEN** with `-race`. Their original negative output and final positive output remain outside the repository in `/private/tmp/gaoge-p2-fix-*-green.log`.

## Verification (implementation code locked at `39110322`; prior to documentation-only commit)

- `make go-test`: PASS for all **12 Go modules**.
- `make metadata fmt-check tidy-check go-vet go-lint`: PASS; Lint 0 issues.
- Complete `go test -race ./... -count=1` for all 12 Go workspace modules: PASS (two recorded groups, core/provider/harness and remaining eight).
- `make agent-baseline-test ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check`: PASS, including 46 TypeScript tests and clean beta.24 consumers.
- `make coverage`: PASS. Go statement coverage **75.38%** (minimum 70%); TypeScript statements **88.04%**.
- `make integration`: PASS, complete successful exit with real PostgreSQL, Redis and cross-process continuation E2E in Race mode. Dedicated Docker test resources cleaned.
- Go symbol-level vulnerability scanning: no code-reachable advisories (3 advisories in required dependencies not called); Node dependency scan `pnpm audit --audit-level high`: PASS with 1 moderate advisory. `make security` aggregate was interrupted by a Mac Pi connection; remaining Go module scans and pnpm scan were independently completed with successful exits.

## Hard cuts, residual risks, and delivery gate

**Protocol:** The renamed positive `model.Capabilities` flags change selected-model HMAC material. This is a pre-stable breaking change and intentionally has **no backwards-compatible model authorization bridge**. Old beta.24 pending invocation fingerprints still require explicit Host migration/restoration; a selected model's capability changes must never authorize physical continuation. No Runtime Agent loop, separate registry, Host intent router or SQL schema was introduced.

**Residual:** Host provisioner integrity and external vendor capability accuracy remain in the trusted Host boundary. Model/provider gateway live billing and paid reasoning replay were not tested. PR-head GitHub CI has not run for this post-fix branch; external independent review of the final commit remains a separate optional delivery step, not a repeated-patching requirement.

**Exit criterion:** No unresolved in-scope P0/P1/P2 supported by the reproduced matrix; all named local check families passed; modified tracked files remain limited to the selected capability contract, Host example/test fixtures and documentation. Merge requires actual locked PR-head CI when a PR is created; no automatic merge.
