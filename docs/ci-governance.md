# CI governance: P0

P0 preserves the complete validation scope. It does not introduce affected-only
tests, race sharding, test-result reuse, or a new development branch.

The pre-change sample (100 CI runs, November 13–19, 2026) contained 44 successful
PR runs with a 14m50s median wall time. In ten recent successful PRs, median job
times were race 14m43s, quality-main 10m21s, coverage 8m34s, and real-engine
integration 3m44s. These are workflow/job observations, not benchmark thresholds.
An [example run](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44520256900)
shows that serial Go tests, not the roughly 16-second consumer check, dominate
the quality step. P0 establishes target/module evidence before P1 scheduling
changes; it does not claim the baseline wall time is already reduced.

## Safely separating PR description checks

`pr-description.yml` reports **Pull request Markdown description** with a
separate concurrency group and read-only permissions. It needs no Go toolchain,
workspace dependencies, database services, or publication credentials.

This first stage deliberately retains `edited` and the description validator in
`ci.yml`. Removing either before branch protection requires the new check would
allow a malformed description edit to bypass the old required quality gate.
Consequently, **editing a description still reruns heavy CI during this stage**.

An administrator performs the cutover only after this PR is merged:

1. Update existing open PRs to the accepted default branch and wait for the new
   description check to succeed on their current test-merge commits. The script
   does not modify PR branches or bodies for you.
2. Inspect a read-only readiness and branch-protection plan:

   ```bash
   node scripts/activate-pr-description-check.mjs orz-i/Gaoge-Agent-Runtime
   ```

3. After reviewing it, explicitly authorize the additive protection update:

   ```bash
   node scripts/activate-pr-description-check.mjs orz-i/Gaoge-Agent-Runtime --apply
   ```

   The script requires the workflow on the default branch, paginates open PRs,
   rejects missing/failed/skipped/cancelled description checks, retains every
   existing required check and app binding, and preserves strict protection.
   No repository settings are changed without `--apply`.
4. In a follow-up PR, remove `edited` and the legacy description-validation step
   from **only** `ci.yml`, and update the staged-rollout assertion in
   `activate-pr-description-check.test.mjs`. Keep `edited` in
   `pr-description.yml`. At that point, editing a description reruns only the
   independent required check and no longer cancels the code-validation run.

Do not use top-level path filtering to skip required workflows, and do not
rename existing required checks during this cutover. Rollback restores the old
CI trigger/validator before removing the independent required check.

## Static checks and the required quality gate

`make check`, `make check-no-race`, and the release gate retain every pre-P0
target. Their static targets now run before behavioral tests. CI exposes the timed
equivalents of `make check-static` and `make check-execution-no-race` as separate
steps on the same runner, so a lint/type error is visible without waiting for full
unit tests.
No additional Go cache or runner is introduced. Do not run tidy and tests
concurrently in the same checkout: tidy temporarily edits module files.

The existing **Unit, race, lint, and release consumers** required-check name is
unchanged. Its `always()` aggregator now also requires coverage, vulnerability
audit, Windows command safety, and real-engine integration to succeed. Collector
E2E remains push-only: its skipped result is accepted only on a PR, and it must
succeed on main pushes. Missing, failed, cancelled, unexpectedly skipped, or
unrecognized dependencies fail closed. CodeQL, dependency review, and the
independently required integration check are unchanged.

CI jobs have explicit timeouts (3–30 minutes, depending on the job). These are
hang bounds, not performance assertions or reasons to retry a failed test.

## System dependencies

All four existing Linux CI users of SQLite and the release job share
`.github/actions/setup-sqlite`. A successful `dpkg-query` reporting
`install ok installed` skips both `apt-get update` and installation. Missing or
partially removed packages still require a successful update, install, and
post-install verification. Network/install failures are never converted into
success. This does not change the dependency version policy or runner image.

## Timing baseline

The normal CI workflow sets `CI_TIMING_DIRECTORY` to an ignored
`artifacts/ci-timings` directory. `make ci-static`, `make ci-execution-no-race`,
`make ci-race`, and `make ci-coverage` execute the same authoritative Make target
lists, serially, through a shell-free runner. Other heavy targets and the Windows
command check use the same recorder. `go-workspace.mjs` additionally records each
module operation (format/tidy/vet/lint/test/race/coverage/audit).

Each measured job publishes its summary and a small JSONL artifact even after
failure, with seven-day retention and a distinct name per job/run attempt. An
operation records its label, mode, start timestamp, monotonic duration, exit code,
and signal; it does not copy raw argv, environment, logs, Provider content, or
credentials. Failed operations remain visible and stop subsequent targets.
Timing storage/publication is best-effort: a reporting outage cannot fail a
successful check or replace/mask an actual test failure. The correctness and
security steps themselves do not use `continue-on-error`.

Target durations include nested module durations: **do not sum both levels**.
The summary shows target/command times and the slowest 20 module operations;
the artifact retains all records. Package durations remain in normal Go logs.
No wall-time threshold or performance gate is introduced, and ordinary local or
release commands write no timing files unless explicitly opted in:

```bash
CI_TIMING_DIRECTORY="$PWD/artifacts/ci-timings" make ci-static
CI_TIMING_DIRECTORY="$PWD/artifacts/ci-timings" node scripts/ci-timings.mjs --summary
```

P1 should use repeated comparable runner baselines to choose sharding or scope
changes. P0 does not claim the previously proposed 3–8 minute feedback targets
have been achieved.
