# Approval, Provider binding and Hosted Tool hard-cut — local acceptance

- **Date:** 2026-10-30
- **Scope:** branch `governance/approval-provider-binding-hardcut-20261030`
- **Reviewed implementation Head:** `4f1d565`
- **Base:** merged `main@0efd1f3` (PR #73)
- **Decision:** local **GO for protected PR and independent merge review**.
  Not a publication or unsupervised in-flight upgrade approval.
- **Version:** unreleased `v0.1.0-beta.24` candidate, with no new Tag,
  no SQL/schema version changes, no compatibility bridges.

## Three atomic changes

1. **Exact approval checkpoint, `3dea855`.** Harness Go, HTTP v1,
   OpenAPI and TypeScript all require the pending exact `CheckpointID`.
   Remove the old checkpoint-only Agent Tool approval inference; a legacy
   resolved approval cannot authorize a pending call without its committed
   `pendingApproval` binding. Stale and missing Turn-only approvals
   are rejected before execution. New negative Go/HTTP/TS tests cover a
   second pending approval, previous ID replay, missing ID and no Tool calls.
2. **Frozen named Provider identity, `c46c8b8`.** A named Provider now
   supplies a stable lowercase SHA-256 fingerprint and the
   `ModelInvocation` request hash incorporates it with Provider name.
   The Router requires nonsecret Host `Binding.Identity`; first-party
   OpenAI/Anthropic adapters HMAC their actual Endpoint, credential,
   protocol and effective policy. A custom HTTP client requires explicit
   `BindingScope`. Missing/changed fingerprints on pending recovery are
   rejected before another Model claim and immediately before dispatch.
   Negative cases include same-name Endpoint/credential/Host policy drift,
   missing proof, and an actual committed-pending-intent crash. No dynamic
   registry or alternate Provider execution engine.
3. **Explicit Hosted Tool terminal evidence, `4f1d565`.** Empty status
   no longer proves a completed native effect. Stable Tool Call IDs keep
   their held reservations until explicitly terminal; unidentified
   unfinished effects cannot be automatically reconciled. They return
   `budget.ErrInvalidUsage` and suspend/retain the original dispatched
   Model budget reservation instead of fabricating an ID, counting a
   completion or silently releasing quota. Restart and eventual
   terminal-receipt regression tests pass.

## Actual tests on implementation Head

| Gate | Result |
| --- | --- |
| `make check` | **PASS, exit 0**: all 12 Go source-module tests/race/lint, fmt/tidy/vet, 59 baseline-script tests, TypeScript tests, eval/benchmark/A2A/OTel smoke, clean source consumers |
| `make coverage` | **PASS**, Go statement coverage **73.55%** (required 70%); TS statements **88.08%** |
| `make integration` | **PASS** real PostgreSQL 16/Redis 8; Store replay, concurrency, cancellation and cross-process recovery. Containers cleaned |
| `make integration-otel` | **PASS** actual pinned Collector trace/metric E2E; container cleaned |
| `make security` | **PASS**, 0 reachable Go-code vulnerabilities and npm high severity audit clean. The Go scan reports 3 vulnerabilities in required modules, with no known reachable call path |
| `make go-test`, `make go-lint`, `make tidy-check`, `make release-check`, `make metadata` | **PASS** individually after implementation changes |

The full `make beta` aggregate command was not invoked. Its component
gates (`check`, `coverage`, `security`, `integration`,
`integration-otel`) were run and passed separately on the implementation
Head. A PR requires GitHub CI and independent review of its final exact Head.

## Deliberate remaining constraints

- A completely custom external Model implementation can hide its actual
  transport behind an unchanged Host-declared binding identity. The Host
  must rotate that identity for meaningful endpoint/transport changes.
  Unnamed local test Models are not covered by external Provider attestation.
- A third-party native Tool completing without an ID may be counted as an
  anonymous *explicit terminal fact*, but two identical anonymous facts
  cannot be safely deduplicated. Missing-ID **unfinished** effects stay
  unresolved; the resulting Model token usage may remain reserved until
  Host reconciliation. This is safer than blind retry or fabricated receipts.
- `beta.24` changes the pending ModelInvocation request hash, approval
  API, and native Tool settlement semantics; stop old workers and inventory
  all outstanding Runs/Turns. Drain or quarantine old pending work, and do
  not rehash, forge an approval/binding ID or replay uncertain physical
  effects. Published `beta.23` tags remain immutable.
- Source-cohort clean consumers are not evidence for `--published`.
  A future prerelease still needs protected merge review, coordinated tags,
  and actual published module checks with no replacements.

The two unrelated local OpenAI probe scripts were left untracked and were
not staged or changed during this work.
