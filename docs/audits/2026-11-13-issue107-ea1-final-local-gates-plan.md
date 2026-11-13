# EA-1 final local evidence plan (not results)

After H-AC and MW-NEXT source closure, freeze a clean source SHA and prohibit concurrent source edits. Existing batch PASS records remain historical until real same-source execution replaces them. Six H-FROZEN cells stay UNRUN under independently blocked WP-GAOGE. The unchanged strict checker must still report NO-GO; there is no Runtime-only strict exemption.

## Planned gates

1. Record exact tools, clean Git state, free space and isolated service configuration. Never use production services.
2. Run `make check-no-race` (includes ordinary full-module tests and repo CHECK_TARGETS except race). Provide dedicated test services rather than accepting skipped integration tests as proof.
3. Enumerate the twelve modules from `scripts/go-workspace.mjs`; serially run `go -C <module> test -race -count=1 -v ./...`. This supplies verbose full-race evidence instead of a redundant opaque `make go-race`. Provide the dedicated PostgreSQL DSN to both Kernel and Harness PostgreSQL modules.
4. Union historical ledger real-PostgreSQL selectors by module, including the final H-AC and MW-NEXT suites, and run two additional repetitions of those suites only. The full-race first run plus these yields three actual runs. Preserve the historical five-repeat stale-admission requirement (`TestAuditIssue107PostgresBeginCannotBindCallerExpectedChildRevision`) and three-repeat Workflow-attempt requirement (`TestEA1P0WorkflowAttemptLocalNoEffectAcrossRootAndChild`) with the needed additional repetitions, not blanket repeated full suites.
5. Run `make coverage`, `make security`, `make integration-test`, and `make integration-otel` with their actual isolated dependencies. The integration-test `^TestRealPostgres` selector does not cover all `TestEA1RealPostgres` tests and cannot replace matrix-directed evidence.

CHECK_TARGETS includes metadata, agent baseline, formatting, tidy, vet, tests, race, lint, TypeScript lint/typecheck/test/build, eval and benchmark smoke, otel smoke, a2a product and release checks. Release checks exercise external Go consumers with GOWORK=off plus local cohort replacements and packed TypeScript consumers. Do not use old published tags as proof for the current unpublished source. Do not run paid agent-quality/probe/pilot work.

## Evidence migration and limits

Each log records the exact source, command and real exit status; durable normalized log bytes receive SHA256 manifests. Every migrated cell must map its actual test suite and required subcases to RUN/PASS lines, not just a whole-process exit0. Detect SKIP and missing selectors explicitly. Preserve all old diagnostic/failed-source logs and the historical batch reports. Only cells with complete actual final evidence change source SHA.

The Mac cannot certify Windows/Linux CI runners or required GitHub PR-head checks. User instructions prohibit push/PR/merge, so those remain unexecuted authorization limits. H-FROZEN is not relabeled N/A to make strict green. Local Runtime evidence completion and overall release GO are distinct outcomes.

## Read-only environment preflight

Observed before B20 implementation: Go 1.27.4 darwin/arm64, Node 24.21.0, pnpm 11.22.0, golangci-lint 2.14.0. Task-owned isolated PostgreSQL and Redis containers are healthy. No Collector was running and this worktree had no node_modules; installation/service setup remains to be done through repository-supported commands. Free disk was 8.9 GiB. Complete gates can add several GiB, so run serially and monitor between modules. Only unused, task-owned regenerable caches may be cleaned; preserve source/evidence and other users' data. No cleanup has occurred in this preflight.
