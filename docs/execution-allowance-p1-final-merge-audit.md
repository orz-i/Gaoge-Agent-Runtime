# P1 Durable Allowance — Final Merge Audit

- Date: 2026-10-09
- Candidate: `feat/durable-execution-allowance-p1@135f153`
- Baseline: `v0.1.0-beta.13@af144a3`
- Initial release decision: **NO-GO** pending public-contract and versioned-release fixes.

## Findings

1. **Release blocker:** `contracts/agent-runtime/v1/openapi.yaml` does not include the new `POST /harness/turns/{turnID}/allowance` endpoint, its strict JSON contract, the nonterminal `paused_budget` status, or `turn.paused_budget`. Go and TypeScript otherwise advertise these values. Update the canonical OpenAPI and its tests before release.
2. **Release blocker:** Repository `VERSION`, nine Go module requirements, `go.work`, boundary metadata, TypeScript package/lock, and `check-beta.mjs` still identify published beta.13. Prepare new coordinated beta.14 release; never move an immutable tag.
3. **Release gate:** `make check` has passed, but `make beta` with real PostgreSQL/Redis, security, and OTel Collector must pass before creating tags. If unavailable, report a blocker instead of claiming a published release.

## Verified

- Kernel/Agent/Harness states: pause is nonterminal; direct Agent resumes same Run, with no repeated model or completed Tool calls; concurrent grant CAS one-winner and frozen hard limits enforced.
- HTTP: owning Turn, public Turn Revision; tenant-opaque 404, strict bad request and conflict handling.
- Handoff and Workflow: paused children are pending; Continuation only resumes after explicit grant.
- SQLite-backed GORM persistence and SDK all-module tests/race/lint/types/build passed; this is not live-provider or real PostgreSQL multi-process evidence.

## Host adoption boundary

Only after an immutable new SDK release should Gaoge pin it. The host must set an initial bounded allowance, expose authorized continuation from paused Turn only, preserve absolute ceilings/Context Window/Tool approval, and prove no side effects duplicated. P2 auto-renewal/P3 richer UX remain separate.
