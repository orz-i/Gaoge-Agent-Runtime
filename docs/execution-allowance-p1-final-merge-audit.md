# P1 Durable Allowance — Final Merge Audit

- Date: 2026-10-09
- Candidate: `feat/durable-execution-allowance-p1@135f153`
- Baseline: `v0.1.0-beta.13@af144a3`
- Initial release decision: **NO-GO**; all documented blockers remediated. **Final SDK merge and tag decision: GO**, subject to GitHub release workflow verification.

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

## Final SDK gate evidence / merge decision

- Accepted SDK candidate before this audit-only commit: `37588d6`.
- `GOTOOLCHAIN=go1.27.2 make beta`: **PASS, exit 0**.
- Full quality: metadata, fmt/tidy, Go vet/test/race/lint, TypeScript
  lint/typecheck/test/build, deterministic evaluation, benchmarks, coverage
  and clean consumer gate passed.
- All nine `govulncheck` modules: **zero reachable vulnerabilities** on
  patched Go 1.27.2 and x/net 0.60.0. `pnpm audit` found no known
  vulnerabilities meeting the security gate.
- External database integration: true PostgreSQL and Redis Docker integration
  tests passed and test-owned containers/networks were removed.
- OpenTelemetry Collector image pinned at digest
  `sha256:0fba96233274f6d665ac8831ad99dfe6479a9a20459f6e2719c0d20945773b46`.
  End-to-end traces and metrics were emitted and confirmed, and the
  Collector test container/network was removed.
- OpenAPI Beta.14 allowance endpoint, status enums and strict schema tests
  passed. Version and release notes across nine modules, TypeScript and
  integration module are aligned. The release workflow will re-run tests at
  the immutable root tag before attaching its prerelease archive.

**GO for SDK fast-forward merge + coordinated immutable beta.14 tag push.**
DO NOT declare the SDK published until the GitHub release workflow passes,
the prerelease exists and the TypeScript archive can be retrieved. Separate
Gaoge host branch/pin adoption must pass its own validation.

## CI merge-blocker correction (2026-10-10)

PR #36 initially failed the GitHub Actions quality job: the checkout selected
Go 1.27.2 correctly, but `.github/workflows/ci.yml` installed
`golangci-lint v2.12.2`, which cannot decode Go 1.27 export-data format
(version 5 vs maximum 4). This is a mismatched lint toolchain, not an
application/kernel test failure. Official golangci-lint support for Go 1.27
starts at v2.13.0; this branch adopts the locally validated v2.14.0 in
**both** CI and immutable release workflows. No linter is disabled and the
quality gate must re-run on GitHub to completion before merging this PR.
