# P2 SDK final merge and release audit

- Date: 2026-10-10
- Baseline: `main@73197d3` (immutable Beta.14)
- Reviewed feature implementation: `feat/durable-auto-allowance-p2@0e14d7a`
- Release candidate: `0.1.0-beta.15`; exact final SHA to be recorded after version preparation
- Decision: **GO for protected PR after version parity and revalidation; NO-GO for tag until PR CI is green**
- Design: [ADR 0006](./adr/0006-runtime-owned-auto-allowance-segments.md) / [implementation audit](./execution-allowance-p2-implementation-audit.md)

## Merge-blocker review

1. **Scope & ownership:** Automatic renewals are an *optional, immutable-per-Run* Agent state policy and bounded Kernel CAS, not a new generic queue, model-selected permission or Conversation/Billing ledger. Harness merely forwards the policy from a trusted direct-Agent start. The HTTP grant interface stays explicitly authorized as in P1; model/Tool results cannot change grants.
2. **Call-site consistency:** The policy is checked only at a fresh logical model admission or before executing the next persisted Tool in a batch; already dispatched/unknown effects follow existing receipt/recovery logic, without reissue on allowance transition. A successful CAS emits a durable wakeup; hosted `DeferResumption` yields for the established Scheduler/Worker. Standalone SDK Runner can proceed synchronously.
3. **Hard ceiling & lifecycle:** Partial final steps are clamped to the Run's *frozen* hard limits; a hard cap still terminates. Invalid initial policy cannot dispatch model/Tool calls. P1 manual grant remains available after exhausted auto renewals, subject to owner, public Turn revision and private Kernel CAS. Tool waiting approval and cancelled/failed terminal Runs cannot be revived automatically.
4. **Repeat-work safety:** Settled work only, Tool key and canonical arguments/result; volatile IDs ignored, arbitrary JSON integers preserved, meaningful result whitespace retained. The matcher is intentionally conservative and cannot be used as an authorization or hard-stop oracle. No plaintext/fingerprint is written to public events.
5. **Context and child topology:** Harness Context Window V2 checkpoint is neither reset nor duplicated by automatic grants; child Team/GroupChat/Workflow Runs do not inherit the direct-Agent policy. Existing shared Turn charges, Context hard bounds, deadlines and provider/tool permission checks remain authoritative.
6. **Durability & idempotency:** Memory and production GORM-backed SQLite adapters reload the same Run/receipts after a worker restart; a revision race admits one winner, a persistent outbox transition awakens the existing Scheduler+Worker; no new attempt/replayed side effect is introduced.
7. **Compatibility:** No OpenAPI/HTTP/TypeScript surface changes. New Go-only opt-in fields, persisted as optional Agent state. Beta.14 data without policy still decodes and runs in manual mode. No Beta.14 tag rewritten, no SQL migration, no compatibility bridge. Prohibit mixing old Runtime workers with P2-active Runs during adoption.
8. **Publication:** All nine Go modules, TypeScript metadata, docs and OTel E2E consumer must reference one immutable Beta.15 release cohort. GitHub protection requires PR checks. Tagged workflow must attach packaged TypeScript client, and separately verify live integration. Gaoge remains on published Beta.14 until verified release.
9. **Unsupported evidence:** Tests do not cover paid model APIs, live production concurrency, P95/P99 costs or true *P2-specific* PostgreSQL multi-process crash under load. Do not claim these.

## Test evidence

The exact implementation candidate passed two full local `GOTOOLCHAIN=go1.27.2 make beta` runs (latest `artifacts/p2-final-verified-beta.exit=0`). Includes nine-module Go unit/race/lint/coverage, TS tests/build/clean consumer, zero reachable vulnerabilities, real PostgreSQL/Redis integration, OTel Collector pinned-image emission/metrics/traces, and cleanup of integration containers. Additional focused AutoAllowance, Harness/approval/context, SQL reload and Scheduler/Worker tests passed with Race. Full final release metadata/tagged workflows must be rerun **after** version changes and through protected CI.

## Host gate

Gaoge P2 documentation lives on `feat/agent-execution-continuity-p2-host@c2fa4dcf`. The host has **not** adopted AutoAllowance and must wait for a published Beta.15, pin module/gitlink/TS archive coherently, enable only direct Agent with bounded steps, and run host `make check`, targeted Go Race and Chat recovery tests before merge.

## Final release gate record

Pending: create Beta.15 versioned metadata, validate full `make beta`, open protected PR, receive green CI, merge and tag/publish. Do not present this audit as evidence that those steps were already done.
