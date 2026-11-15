# PR #112 — post-hard-cut CI real-engine integration repair

- Branch: `refactor/subagent-only-orchestration`
- Failed PR Head: `1cd3b662bce600c59474863bc943134bfa1b01ea`
- Job: `PostgreSQL and Redis integration`, GitHub Actions run `43662290347`
- CI error: `integration/continuation-e2e/recovery_test.go:68:21: undefined: runrelation.KindTeamMember`

## Root cause and contract

The SubAgent-only BREAKING CHANGE removed the retired Team `RunRelation` kind, but the detached `integration/continuation-e2e` module retained a Team member fixture. `go.work` omits that E2E module, so core-module unit, vet, lint, Race and clean-consumer gates did not compile the fixture. The expensive PostgreSQL/Redis CI job therefore reached its final E2E step before failing at Go compilation. This is an SDK test-consumer hard-cut omission, **not a database/Redis execution failure**.

The test is specifically meant to verify that one committed terminal child relation is queued, remains deduplicated across PostgreSQL and Redis adapter restarts, and is reprojected for a newer parent Run revision. Preserve those semantics with the supported `runrelation.KindDelegation` and `agent.RunKind` rather than reintroducing `KindTeamMember` or any legacy alias. On restart, explicitly verify the persisted relation's parent and original immutable delegation owner.

## Regression prevention

- `make integration-compile` now compiles all detached continuation E2E tests with `GOWORK=off` and no external services.
- The normal `make check` and `make check-no-race` matrices include this compile check, so an invalid SDK symbol fails even when the real-engine job is not run.
- Both `make integration` and `make integration-test` depend on that compile gate to fail early rather than after full PostgreSQL/Redis test suites.
- The two preexisting local untracked Python probe files are outside the changeset.

## Verification evidence

- The original E2E file reproducibly failed to compile locally with `undefined: runrelation.KindTeamMember`.
- After the change, `make integration-compile` passed with the production module replacements in `GOWORK=off` mode.
- `TestRealPostgresRedisSchedulerRestart` passed with `-race` against disposable real PostgreSQL 16 and Redis 8 containers; the containers were destroyed after the test.
- Full `make integration-test` and subsequent PR-Head CI are separate gates, evaluated at delivery. This repair does not imply that the PR may be merged without those checks.
