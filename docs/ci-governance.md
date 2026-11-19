# CI governance: P0, P1, and P2 shadow analysis

Both phases preserve the complete validation scope. P0 established safe gates
and timing; P1 below schedules full suites in three shards and reuses coverage
execution as ordinary unit validation. Neither introduces affected-only tests,
test-result caching, a new development branch, or weaker release gates.
P2 adds an advisory impact report only; it deliberately does not enable
affected-only execution or change any existing job's conditions.

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
   description check to succeed on the commit evaluated by GitHub. Normally
   Actions checks are attached to the PR head; if the test-merge commit has
   checks or statuses, it takes precedence. The script inspects both sources
   instead of requiring a nonexistent test-merge check or accepting stale head
   evidence. It does not modify PR branches or bodies for you.
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
equivalents of `make check-static` and the smoke/consumer subset as separate steps
on the same runner, so a lint/type error is visible without waiting for full unit
tests. Full ordinary unit validation now lives in the coverage jobs described
below. Do not run tidy and tests concurrently in the same checkout: tidy
temporarily edits module files.

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

All Linux CI users of SQLite and the release job share
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

## P1: full-suite reuse and balanced scheduling

P0's final [run 44533727890](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44533727890)
took 16m51s including queue time. Its race command took 937s, ordinary Go tests
464s, and Go coverage 540s. P1 does **not** turn that single sample into a timing
gate or a guaranteed speedup.

The authoritative `scripts/go-workspace-layout.mjs` catalog partitions every
`go.work` module exactly once for each of two independent matrices:

| Shard | Modules | P0 race time (approximate) |
| --- | --- | ---: |
| core | Core, Postgres, Redis, MCP, OTel, A2A | 280s |
| sqlite | Native SQLite | 330s |
| adapters | Provider, Harness, task-agent, Harness-Postgres, HTTP, independent Transport | 328s |

Both matrices use `fail-fast: false` and isolated runners. Every module still
uses `./...`, `-count=1`, and the original race or atomic `-coverpkg=./...` flags.
No test-name selection or ordinary-PR skip is introduced. The full Core module,
nested task-agent module, and independent Transport module remain mandatory.
Invalid shards and unknown CLI arguments are errors, not empty successful runs.

`ci-consumers` removes only `go-test`, `go-race`, and `ts-test` from the ordinary
CI phase. The first is now performed with coverage instrumentation, the second
by the complete race matrix, and TypeScript tests by `vitest run --coverage`.
Static checks, build, isolated/packed consumers, eval/benchmark smoke, A2A
boundaries, detached E2E compile, real-engine integration and security remain.
The packed TypeScript consumer still deliberately runs its own package tests.
All existing local/release Make targets, including `make check`, `make coverage`
and the full published-consumer release workflow, retain their old scope.

### Complete coverage, not per-shard averages

A successful Go coverage shard uploads its profiles and a manifest recording
its exact module set and `GITHUB_SHA`. A failed shard produces no success
manifest/artifact. `Coverage gates` runs only after all Go coverage shards pass,
downloads artifacts **from this run**, and requires exactly the expected three
directories and 13 profiles. Wrong commits, missing/extra files, changed module
assignments, invalid profiles, and wrong coverage modes fail closed.

Only then does it compute the existing source-block union and enforce the
unchanged repository **70% statement floor**. Duplicate blocks are counted once,
zero-execution blocks remain in the denominator, and percentages are never
averaged across shards. TypeScript thresholds are unchanged. Coverage artifact
upload is mandatory; timing publication alone remains best-effort.

The stable required quality aggregator depends on both the coverage matrix and
the final coverage gate, as well as the race matrix. A failed/cancelled/skipped
matrix, skipped dependent gate, or missing artifact cannot turn the PR green.
Artifact names include the shard; retries replace only that shard's report in
the same workflow run. New run attempts cannot collide in timing artifact names.

### Cache scope and measurement

The setup action separates shared, content-addressed `GOMODCACHE` downloads
(dependency hash, no per-commit suffix) from `GOCACHE` builds (toolchain,
mode/shard scope, dependency hash, commit). Build restore fallback never crosses
the mode/shard scope. Only trusted pushes to `main` refresh build snapshots;
PRs restore them without uploading six new build snapshots on every commit.
Download caches retain their normal branch isolation. No cache is test evidence,
and no secret is cached. The first PR run after this cache migration may be
colder; steady-state measurements require main to seed the new caches.

P1 is accepted based on unchanged scope, failure-path tests, real CI results,
and comparable target/module/job durations. Collect total runner time as well
as wall time: more isolated runners can reduce waiting while increasing setup
cost. Do not promise the earlier 3–8 minute target before measuring it.

Local reproduction (run coverage shards sequentially in one checkout, or use
separate worktrees; each shard owns `coverage/go`):

```bash
node scripts/go-workspace.mjs race --shard=sqlite
node scripts/go-workspace.mjs coverage --shard=core
make ci-static
make ci-consumers
```

To roll back scheduling, restore P0's unsharded workflow and ordinary CI target
invocations together. Do not drop the coverage union/required dependencies while
leaving out ordinary tests. The staged description-check cutover above remains
unchanged and is not activated by P1.

## P2: impact analysis in shadow mode

P1's accepted [run 44539358109](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44539358109)
completed in 6m42s versus P0's 16m51s. Accumulated CI runner time was 41m54s
versus 46m32s. Both contained all 13 coverage/race modules and 45,193 statements
in the Go coverage denominator. This is a two-run comparison, not a long-term
performance guarantee. P2 does not claim additional savings while shadow-only.

The **CI impact analysis (shadow)** job runs alongside, not ahead of, P1's full
jobs. It needs only checkout, Node, and read-only repository access. It publishes
`plan.json`, a Markdown summary, and a seven-day artifact. The existing required
quality aggregator also requires this evidence job to succeed. Classification
uncertainty produces an explicit full recommendation, not an empty successful
selection; tests/report-upload failures are not ignored.

### Advisory scope and conservative fallbacks

`scripts/ci-impact-policy.mjs` reports candidate module/consumer boundaries:

| Change | Shadow recommendation |
| --- | --- |
| Ordinary Markdown docs | Metadata and protected checks; no focused Go candidates |
| TS source, packed-client README, Node lockfile | TS/static/packed-consumer candidates |
| Provider Transport source | Independent module and its consumer |
| Provider source | Provider plus the nested task-agent reverse consumer |
| Harness source | Harness, task-agent, Harness-Postgres, HTTP, detached continuation E2E, TS wire consumers |
| OTel adapter source | OTel plus detached OTel E2E |
| Core/conformance, persistence, continuation E2E | Full |
| Shared contracts, executable A2A docs, audit code fixtures | Full |
| CI/actions/scripts/global tooling, dependency/release metadata | Full |
| Unknown paths/statuses or unavailable/ambiguous analysis | Full |

Module READMEs are assigned to their owning module, not generic docs. The
packed-client README remains a TypeScript consumer input. Any mixed change
containing a full-risk path recommends full. Main pushes always recommend full.
This taxonomy is deliberately conservative and not a declaration that an
unexecuted test would pass.

The collector uses full commit SHAs from the event and verifies the actual
checkout. It finds a unique merge base, then uses Git's NUL-delimited
`--name-status --no-renames` diff with external diff/text conversion disabled.
A rename is represented by both the old deletion and new addition. Deleted
files stay in the analysis, and no GitHub API file-count/pagination cap can
truncate the change set. Missing history, malformed paths, ambiguous bases,
truncated diffs and unreadable module snapshots all fall back to full. Raw Git
errors, PR bodies, source contents, environment values and credentials are not
copied into the report.

The dependency graph over-approximates internal `require`/`replace` identities
from every workspace module plus **both detached E2E modules**. It unions graphs
from the merge base, event base, head and actual checkout; removing an old edge
cannot hide its reverse consumers. The longest module path owns a file, so the
nested task-agent module is not mistaken for Harness itself. Unknown identities
or incomplete graphs fall back to full. Existing full tidy/vet/contract checks
remain authoritative; this declared-module graph is not a whole-program proof
about dynamic behavior or future cross-language contracts.

### Shadow is not an activation switch

Every plan is explicitly `mode: shadow`, `execution: full`. The job exports **no
workflow outputs**, no skip flags, and no matrix. No validation job consumes its
report in an `if` or a dynamic matrix. The full 13-module coverage union, 70%
floor, full race matrices, real-engine suites, security, CodeQL, dependency
review, consumers and existing Collector trigger policy remain unchanged.

In particular, focused candidates cannot be substituted for the global coverage
denominator. The plan always states `complete-13-module-union-required`.
There is no `--apply`, repository variable, label, or environment toggle that
silently enables affected-only validation in this phase.

Review shadow reports against actual full results across representative docs,
TS, independent-module, Harness and high-risk changes before proposing a
separate activation PR. Any failure outside a suggested scope is a potential
false negative: retain full execution and fix the model, rather than waiving
the failing check. Activation must separately define legal skips, mandatory
fail-closed aggregation, contract ownership and complete coverage semantics.
Current CI/tooling changes correctly recommend full and are not evidence that
all low-risk classifications have been field-validated.

Local reproduction requires the analyzed head to be checked out, plus full SHA
arguments (not branch names). It only reads Git and writes ignored evidence:

```bash
node scripts/ci-impact.mjs --base <full-base-SHA> --head <full-head-SHA>
node --test scripts/ci-impact.test.mjs
```

P2 shadow does not alter clocks, lease/deadline assertions, or cross-process
store tests to manufacture faster results. Slow-test semantics and paid live
Provider quality runs remain outside this change.
