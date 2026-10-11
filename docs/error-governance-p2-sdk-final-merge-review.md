# Agent Runtime Error Governance P2 — Final Merge Review

- Date: 2026-10-11
- Branch: `feat/error-governance-p2-sdk`; base `origin/main@1669ffdc` (published Beta.16)
- Initial P2 candidate: `5227886`; merge-review fix: `e3dd5bf`; Beta.17 full-cohort preparation: `516bdd1`
- Outcome: **LOCAL GO for protected PR merge**; full `make beta` PASS, GitHub PR #40 required checks must still pass. No merge, tag or GitHub prerelease has been created as of this update.
- References: [ADR-0007](adr/0007-classified-failures-and-error-diagnostics.md) and [implementation candidate](error-governance-p2-sdk-candidate-audit.md)

## Scope and changes

Model adapters can wrap a real, typed Provider/Gateway failure with `model.NewClassifiedError` without changing `errors.Is`, durable retries or logical invocation/usage accounting. Agent stores the allowlisted stable model-failure code only at a terminal boundary; historical generic failures remain generic. HTTP users who own a Run still lack diagnostic authority: Run/Workbench/Harness/Feed and workflow traces emit safe summaries and bounded codes, without modifying internally persisted `ErrorDetail`.

## Final merge audit blockers

| ID | Original P2 candidate defect | RED case | Resolution |
| --- | --- | --- | --- |
| MB-01 | Recursive diagnostic scrub unconditionally deleted generic JSON `error`/`failure`, corrupting legitimate tool and business-result payloads | `TestPublicDiagnosticProjectionRetainsLegitimateBusinessFailurePayload` | Preserve generic business errors under explicit input/output/result/items fields; continue deleting SDK diagnostic-only fields and untrusted top-level error objects |
| MB-02 | Workbench `hash`, section hashes and topology hash were calculated before HTTP error-detail sanitization, then returned stale | `TestPublicWorkbenchRehashesChangedVisibleProjection` | Workbench-owned `RehashProjection` computes hashes over actually visible projection without changing original detail |
| MB-03 | Workflow state has structured `failure.status/code/detail` and activation/effect failure metadata. Removing the entire `failure` discarded correctness fields; leaving an arbitrary `errorCode` leaked untrusted bytes | `TestPublicWorkflowStateKeepsFailureStatusAndCodeButNotPrivateDetail` | Preserve status; bound code; omit raw detail/error strings; preserve normal business input result payloads |

Each case was observed failing on `5227886` and passing after targeted changes, along with existing SDK HTTP, Workbench and Agent tests and lint.

## Important limits

The SDK HTTP edge enforces **known diagnostic-field** projection. It does **not** assert that every arbitrary provider-produced string inside legitimate user content/tool results or feature-defined opaque state is safe or free of sensitive text. Those are separate intentional content channels subject to host object authorization and feature contracts. Field filtering is not a DLP or credential-scanning engine. Any future admin/debug read must require host-provided role-plus-object authorization and a separate audited endpoint; no owner query flag or implicit escalation is introduced.

An authoritative Run still has an opaque, feature-owned state object; P2 retains meaningful state and masks known SDK error fields, rather than replacing state with a second model or rebuilding recovery. The internal Kernel/Harness storage remains unchanged. SDK usage and physical retries retain their original truth and P1's failure/usage separation.

## Release gate

Publish only a new coherent Beta cohort (root tag + all nine Go module tags + GitHub TypeScript archive) after:
1. **PASS** `make beta` at code/release HEAD `516bdd1` (Exit 0), covering complete `make check`, Race, Go coverage 72.52% (minimum 70%), TypeScript coverage 79.16%, Go vulnerability report with 0 affected vulnerabilities, npm audit, reviewed JSON fixtures and packed client contract;
2. **PASS** the coordinated Beta.17 `VERSION`, all nine Go module requirements, TS metadata, release boundary, README, changelog, release notes and frozen lockfile. `pnpm install --frozen-lockfile` and Go module tidy-check passed;
3. **PASS** real PostgreSQL/Redis race-backed integration and OpenTelemetry Collector Trace/Metric E2E during local `make beta`; evidence `.cache/error-p2-beta17-gate.log` and `.exit`, matching `HEAD=516bdd1`;
4. **PENDING** protected-branch [PR #40](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/40) checks and confirmed merged GitHub SHA;
5. **PENDING** atomic tag push, release workflow success, checked artifact availability.

If any gate fails: **NO-GO**, do not create or repoint tags. Gaoge must not import an unpublished SDK candidate. After publication, Gaoge independently upgrades its committed pin and runs end-to-end Text Agent classification, HTTP security and desktop/tablet/mobile regressions.