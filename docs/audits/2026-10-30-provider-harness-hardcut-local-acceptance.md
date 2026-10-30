# 2026-10-30 — Provider / Harness architecture hard-cut local acceptance

- **Review scope:** source branch `governance/provider-harness-hardcut-20261030`,
  Core/Harness/Provider architecture and coordinated beta.24 candidate
- **Verified implementation HEAD:** `f96278c` (four atomic code, API,
  design and lint-remediation commits since `main@8eb21cf`)
- **Decision:** **GO for protected PR / independent merge audit**;
  **NO-GO for direct release or unsupervised Host upgrade**
- **Source of truth:** [ADR 0012](../adr/0012-host-selected-capabilities-and-external-model-providers.md),
  [breaking changes](../breaking-changes.md),
  [beta.24 candidate notes](../releases/v0.1.0-beta.24.md)

## Verified architecture cut

1. **Provider module ownership:** moved Router, OpenAI Responses and
   Anthropic Messages adapters to independent
   `go/agent-runtime-provider`; the previous Core Provider import path
   does not exist. Core and the public Harness module do not depend on the
   concrete Provider module. Vendor-backed task-agent sample has its own
   non-published development Go module, explicitly composing Provider.
2. **Explicit selection:** `harness.Runner.Start` has been removed.
   `StartAgentTurn` is the typed Agent entry; Team, GroupChat, Workflow
   and Application have their own entry points. External Go consumer and
   regression test assert this public boundary. `SessionClient.Prompt`
   remains explicitly Agent-specific.
3. **Durability retained:** `ModelInvocation` and Feature/Tool/cancel
   intents, receipts, Store epochs, authoritative Kernel CAS, frozen Context
   and Provider replay are not silently rewritten. `ExecutionClass` is an
   exhaustive recovery discriminator, not a product intent classifier.
   No generic FeatureDriver registry, implicit intent router, compatibility
   forwarding package, SQL DDL or new persisted migration protocol.
4. **Version safety:** existing beta.23 tags are immutable; this is the
   **unreleased** beta.24 source cohort (eleven public Go modules and one
   developer-only example). No remote tags or release created.

## Local test evidence

| Gate | Verified result |
| --- | --- |
| `make check` | **PASS, exit 0**, after the final Go import-format remediation. Includes all 12 source Go module test/race/lint, formatting/tidy/vet, 59 baseline Python tests, TypeScript 47 tests, evaluation/benchmark smokes, A2A/OTel smoke and clean external consumers. |
| `make coverage` | **PASS**, Go statement coverage **73.51%** versus 70% requirement; TypeScript statements **88%**. |
| `make security` | **PASS**, 0 reachable vulnerabilities in examined Go code; Go reported 3 dependency-module vulnerabilities without identified calls. `pnpm audit --audit-level high`: no known vulnerabilities. |
| `make integration` | **PASS**, real Docker PostgreSQL 16 / Redis 8: crash/restart, cancellation, durable grants/receipts, Store/queue integration and continuation E2E. Containers removed after run. |
| `make integration-otel` | **PASS**, actual pinned OpenTelemetry Collector trace/metric emission. Container removed. |
| `node scripts/check-release.mjs` | **PASS**, 11 public Go module source-cohort clean consumers under `GOWORK=off` with temporary test-only replacements; TypeScript package build/packed consumer. Not published-tag verification. |

`make beta` as a single aggregate command was not invoked; every listed
prerequisite (`check`, `coverage`, `security`, `integration`,
`integration-otel`) returned 0 independently. The first single-command
`make check` request was interrupted by the remote tool, then a subsequent
complete invocation returned 0. No independent GitHub CI result is claimed.

## Conditions before publishing

- Protected PR review must independently inspect import-boundary changes,
  capability selection and recovery semantics. CI, security checks and
  clean-consumer gates must pass on the **reviewed exact Head**.
- Coordinate root and all 11 Go module tags to the same accepted immutable
  commit. Release automation must run `check-release.mjs --published`
  **without** filesystem replacements; no beta.24 tags exist at this review.
- Hosts must explicitly migrate imports and `StartAgentTurn` call sites,
  inventory pending Runs, and drain or quarantine incompatible Provider
  bindings/replay. Do not swap a named Provider binding to a different
  endpoint/credential-policy/implementation in-place.
- Real credentials/live gateway compatibility, provider-owned physical
  exactly-once effects and product-specific authorization remain outside
  local fixture/integration proof. Unknown external outcomes require
  trusted reconciliation; they are never permission to blind retry.

Two unrelated pre-existing, untracked local OpenAI probe scripts were
preserved without staging or editing.
