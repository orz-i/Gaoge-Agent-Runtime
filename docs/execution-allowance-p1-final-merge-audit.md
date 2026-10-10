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

## Security gate remediation (2026-10-09)

- Official Go Vulnerability Database first exposed 8 reachable standard-library
  weaknesses on local Go 1.27.1. All 8 are fixed by Go 1.27.2; SDK
  `go.work` now selects Go 1.27.2 for local builds and GitHub setup-go jobs.
  No exceptions or exclusions are applied to the scanner.
- Under the patched standard library, the next scan identified four reachable
  `golang.org/x/net v0.55.0` HTTP/2 vulnerabilities in the HTTP module.
  Upgrade to `x/net v0.60.0` (the upstream fixed version) and refresh all
  consistent transitive crypto/sys/text constraints in the HTTP/Redis modules.
- Ran `GOTOOLCHAIN=go1.27.2 make security`: PASS, `govulncheck` on all nine
  modules reported zero reachable vulnerabilities, and `pnpm audit` reported
  no known high-level vulnerabilities. `make tidy-check` also passed.
- The full Beta gate (including external PostgreSQL/Redis and OTEL collector)
  must run again on the resulting exact commit before final GO/publishing.
