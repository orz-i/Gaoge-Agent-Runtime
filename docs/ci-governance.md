# CI governance and rollout

## CI execution profiles

Draft task feedback and merge authorization are distinct facts. The existing
required check names and branch protection are unchanged.

| Profile | Trigger | Evidence |
| --- | --- | --- |
| Fast | Draft PR | `Fast CI (Draft task feedback)`: shared static/consumer/security checks, TS tests, complete non-Memory Core tests, focused Harness and Memory/native SQLite/actual PostgreSQL contracts, targeted Race, additional affected optional modules |
| Full | Ready PR and every main push | Both existing required CI aggregates, complete Go/Race matrices, coverage union and floor, all four real-engine suites, Windows, consumers and security; Collector additionally on main |
| Full milestone | Explicit `workflow_dispatch` on the selected branch | Same full test scope as Ready PR, distinctly named **non-merge** aggregates; does not validate the PR merge context or replace required checks |
| Release | Immutable Beta tags | Existing release gate, unchanged |

Draft **intentionally leaves both required Full contexts non-success**. GitHub
may treat skipped jobs as passing required checks; the original `always()`
aggregates run and explicitly block Draft instead of accepting a skip.
A Draft workflow can therefore report overall failure with the Fast job green.
Use that exact Fast job's actual success, not the overall conclusion or any
skipped result, for intermediate task evidence. On Ready, every Full dependency
must actually succeed; a prior Fast result, cancelled run, partial coverage,
or manual milestone cannot satisfy the required merge contexts. Converting a
PR back to Draft also triggers the two profiles' reevaluation. Full validates
the PR description; until the separate required-description rollout below,
`edited` remains a trigger.

The Fast floor runs even for docs and conservative impact fallback. The existing
impact collector adds affected optional modules for scoped changes, or known
direct optional modules on Full fallback; it cannot remove that floor. Core and the native three-engine profile run in parallel. The new NativeTransitionCommit
family contributes one original commit/readback cut to Fast; its complete fault matrix
is retained in the affected task and Full. Core's long real-time Memory/Store phases,
complete adapter, Windows and E2E matrices
are deferred, not certified by Fast. Required native test identities must emit
actual PASS events, including real PostgreSQL and selected subtests; an empty
filter or skipped service is not success. Full and Release never consume a Fast
plan, and no coverage denominator or test assertion is reduced.

Real-engine Full shards run the unchanged Kernel PostgreSQL, Harness PostgreSQL,
Redis and detached continuation-E2E commands in parallel with independent service
containers. Their existing required integration aggregate fails unless the
matrix succeeds. `make integration-test` and Release still execute all four
commands by default; `--suite=kernel|harness|redis|continuation` is only a
partition of that same suite.

### QUAL handoff for PR #157

Roll out only after the independent CI PR is reviewed and merged, and the
existing architecture PR updates its base to that accepted main. GAR-4 remains
on its original Full gate. For GAR-5–GAR-8 use an exact candidate SHA, the
card-specific local/actual three-engine contract, production/test size evidence,
the actual successful Draft Fast job, and the existing strict independent
review/Done Gate. This is task acceptance, **not merge admission**. Record
any deferred checks and preserve failure evidence; no change to architecture
acceptance criteria, dependency order, test assertions, or Qual review policy.

Run a Full milestone after GAR-7 if desired (actual exact-SHA evidence, not a
required merge context). GAR-9 and GAR-11 still require actual Full validation
of the cumulative candidate with current main/base and independent review;
GAR-12 retains controlled merge authorization. If a Full run finds a defect in
an earlier accepted card, reopen/remediate that scope before merging. Do not
reuse Full evidence after code/base changes.

Initial baseline: GAR-3 CI run 45935872091 took 13m24s on the critical path
and approximately 79 accumulated job-minutes (not billing minutes). The
Fast target of 2–4m and the Full speedup must be measured on actual hosted runs;
local warm-cache test time is not a hosted CI result. No repository permissions
or branch-protection settings are changed by this profile implementation.

## Historical P0–P2 baseline

P0 front-loads static feedback and repairs gate coverage; P1 reuses unit coverage
and partitions full suites; P2 introduced an advisory impact report. The complete
coverage union and Release gate remain unchanged in the new profiles.

| Accepted PR run | Workflow wall time | Accumulated CI job time |
| --- | ---: | ---: |
| [P0](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44533727890) | 16m51s | 46m32s |
| [P1](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44539358109) | 6m42s | 41m54s |
| [P2 shadow](https://github.com/orz-i/Gaoge-Agent-Runtime/actions/runs/44544001832) | 6m49s | 43m13s |

These are individual runs, not p50/p90 or billing minutes. The original PR wall
median was 14m50s, so the roughly 60% P0-to-P1 reduction must not be described as
a 60% reduction in normal compute usage. All three denominators were 45,193 Go
statements. Main/warm-cache and representative-PR validation remains pending.

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

## Required checks and advisory reports

The existing **Unit, race, lint, and release consumers** name is unchanged. Its
`always()` aggregator requires static/consumer, complete coverage/race matrices,
coverage floors, vulnerability audit, Windows safety, and real-engine checks.
Missing, failed, cancelled, unexpected skipped, or unknown dependencies block.
Only Collector E2E may be skipped for non-main Full runs; it must pass on main pushes. Independent
integration, CodeQL, and dependency-review protection remains unchanged.

Timing and runtime shadow reporting are **non-blocking observations**. The
shadow job has job-level `continue-on-error` and is not a quality dependency.
Checkout/report/artifact outages remain visible but cannot block otherwise valid
code. Its policy regression tests remain mandatory in `metadata`; removing a
real correctness check is not an allowed fallback. Coverage artifacts, unlike
observability artifacts, are mandatory. Timeouts bound hangs, not performance.

## System dependencies and release isolation

Linux CI uses `.github/actions/setup-sqlite`. An installed package skips apt;
otherwise update, install, and verification must succeed. Release checks out an
immutable tag that may predate this action, so its workflow contains the same
bootstrap **inline**. A regression test requires behavioral parity and executes
that inline code in a checkout fixture with no repository-local action/script.
This removes the new path dependency; it does not prove that every historical
tag passes all modern release gates. No publishing operation is used as a test.

## Timing evidence

`CI_TIMING_DIRECTORY` opts into content-free JSONL records (label/mode, start,
monotonic duration, exit code/signal). No argv, environment, Provider content,
or credentials are copied. Summaries and per-job/shard/attempt artifacts retain
seven days; failures stop subsequent validation targets, not timing publication.
Nested target/module times overlap: **do not sum both levels**. No duration gate
is introduced. To record a local static baseline:

```bash
CI_TIMING_DIRECTORY="$PWD/artifacts/ci-timings" make ci-static
CI_TIMING_DIRECTORY="$PWD/artifacts/ci-timings" node scripts/ci-timings.mjs --summary
```

## P1: complete suites and coverage

`scripts/go-workspace-layout.mjs` partitions all 14 workspace modules exactly
once in each full coverage/race matrix, with `fail-fast: false` and isolated
runners. Core, nested task-agent, and independent Transport remain mandatory.

| Shard | Modules |
| --- | --- |
| core | Core, Postgres, Redis, MCP, OTel, A2A |
| sqlite | Kernel SQLite, Harness SQLite |
| adapters | Provider, Harness, task-agent, Harness-Postgres, HTTP, Transport |

All use `./... -count=1`, plus the original race or atomic `-coverpkg=./...`
flags. Coverage execution replaces the extra plain Go pass; Vitest coverage
replaces the extra TS pass. `ci-consumers` retains build, isolated/packed
consumers, eval/benchmark/A2A smoke. Full static, detached E2E compile, real-engine,
security, and local/release `make check`/`make coverage` scope is unchanged.
Never run tidy alongside tests in one checkout: it edits module files temporarily.

Successful coverage shards upload exact module/commit manifests. The final gate
requires this run's three artifacts and 14 profiles, rejects stale/incomplete/
unexpected/malformed data, then computes the existing source-block union and
**70% statement floor**. Duplicate blocks count once; zero-execution statements
stay in the denominator; shard percentages are never averaged. TS floors are
unchanged. Failed/skipped matrices or missing artifacts cannot pass the gate.
Retries replace only their shard's reports within the same run.

`GOMODCACHE` is shared by dependency hash. Build caches isolate toolchain,
mode/shard, dependency hash and commit; fallback never crosses mode/shard.
Only trusted main pushes refresh build snapshots; PRs restore only. Until a main
push seeds the new caches, PR builds may remain cold. No cache is test evidence.

Local reproduction (coverage shards own `coverage/go`, so run sequentially or
use separate worktrees):

```bash
node scripts/go-workspace.mjs race --shard=sqlite
node scripts/go-workspace.mjs coverage --shard=core
make ci-static
make ci-consumers
```

Rollback must restore both P0's ordinary test invocations and unsharded workflow;
never just remove the coverage union/required dependencies.

## P2: advisory impact analysis

The optional read-only shadow job publishes `plan.json` and a Markdown summary
with seven-day retention. It exports **no outputs, skip flags or matrix**. Plans
remain advisory for Full; the Fast script invokes the same collector directly
to add optional-module tests to its fixed floor, never to waive Full checks.

Candidate scopes include ordinary docs, TS/packed-client README, independent
Transport, Provider plus task-agent, Harness reverse consumers (including HTTP,
TS and continuation E2E), and OTel plus its E2E. Module READMEs retain their owner.
Core/conformance/persistence, shared or executable contracts, CI/tooling,
dependency/release metadata, mixed high-risk changes and unknowns recommend full.
Main always recommends full. A candidate is not proof an unexecuted test passes.

The collector validates full SHAs/checkout and a unique merge base, then reads a
NUL-delimited `--no-renames` diff: old/new rename paths and deletions are retained,
without API file-count caps. Declared internal require/replace graphs cover all
workspace modules and both detached E2Es across base/merge-base/head/checkout;
old edges remain in the reverse closure, and the longest module path owns a file.
Incomplete/unknown/ambiguous data falls back to full. No PR body, source contents,
raw Git error, environment or credentials are reported. This over-approximation
is not a whole-program or cross-language contract proof.

```bash
# Full SHAs, with the analyzed head checked out; writes ignored evidence only.
node scripts/ci-impact.mjs --base <full-base-SHA> --head <full-head-SHA>
node --test scripts/ci-impact.test.mjs
```

## Remaining rollout and acceptance

- [ ] Merge this PR only after review and current required checks pass; no auto-merge.
- [ ] Observe main's complete run, including Collector and new build-cache saves.
- [ ] Follow the staged description-check cutover above; until then `edited`
      still reruns heavy CI. Do not claim this benefit has shipped.
- [ ] Measure representative later PRs: warm/cold cache, p50/p90 feedback,
      accumulated runner time, queueing and cancelled/edited-triggered work.
- [ ] Compare real docs/TS/Transport/Harness/high-risk shadow reports with full
      results. A failure outside suggested scope is a potential false negative,
      not a reason to waive the test. Current CI-only changes validate full fallback,
      not all low-risk classifications.

The new Draft profile is a separate stage, not a hidden affected-only toggle
for Full. Slow-test clock/lease/process semantics and paid Provider runs are
outside this CI-only cutover. Keep unresolved items explicit instead of counting
them done.
