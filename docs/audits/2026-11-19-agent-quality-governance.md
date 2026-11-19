# Agent-quality governance — scoped delivery evidence

Date: 2026-11-19. Base: `origin/main@8b5ac865`. Code head:
`32918cf7f486b41d0fa7ecaa07cd73b57e6d7122`. PR: #132.
This is a scoped implementation/self-review record, not an independent audit,
release certification, or measured real-model quality improvement.

## Delivered in separate pushed commits

1. `003e1cf9`: explicit Host read-tool exploration policy, copied at composition
   and frozen in each new Run. Default/legacy suppression remains unchanged.
2. `35824ccf`: optional bounded local-tool Session composition over the existing
   Agent/Harness, with explicit policies and the exact scoped archive reader.
3. `19be7a78`: offline task regressions and read-only outcome review using the
   original strict baseline report reader, without a new report schema/engine.
4. `32918cf7`: self-review hardening for lossy UTF-8 and mismatched catalog Tool
   identities, reverse-default restart coverage and exact JSON argument identity.

## Execution and compatibility boundaries

- No Kernel/Store/RunRelation, grant, cancellation, Retry, shared-budget,
  Provider, HTTP or TypeScript implementation changes.
- The only new opaque Agent state field is optional. Nil/default Runs preserve
  old behavior. Recovery uses the original policy even if the new Runner's
  default changes in either direction. No pending model request is reprojected.
- The exploration guard applies only to a new call after original recovered
  receipts and unknown-dispatch checks. It cannot retry a missing receipt or
  enlarge selected tools/approval/budget authority.
- New facade tools must have explicit local policy and `ApprovalMode=never`;
  unsupported capabilities and unknown model capacity fail closed. Actual read
  semantics, identity, transcript and persistence remain Host-owned.
- The facade does not silently opt into managed Host-transfer recovery or
  reinterpret a context output reservation as a Provider output ceiling.

## Observed regression evidence

- During implementation, the new tests failed when the policy was not frozen
  and when the repeat detector incorrectly relied on a completed trailing batch
  despite a pending assistant call. Both were corrected; detection now counts
  only matched settled Tool results and handles partial pending batches.
- A dedicated negative test reproduced invalid UTF-8 key acceptance before
  validation was added. Catalog identity mismatch is also rejected.
- Object key order/whitespace preserve argument identity; distinct large integer
  values and different array orders do not collapse together.
- Existing full Agent and Harness suites pass with the race detector.
- A real bounded-Context fixture archives oversized output, removes the deep
  fact from inline context, reads the original scoped archive and completes the
  answer. This is scripted mechanism evidence, not model reasoning evidence.

## Parallel PR #131 check

PR #131 head checked: `5a26b43bbe471588681338394ba5aea53b6d2c76`.

- `git merge-tree --write-tree` found no textual conflict against the code head.
- The merged tree `0442cb74f3904147e3fa2e100142d334e224580b` was archived to an
  owned temporary directory; **full Agent and Harness race suites passed there**.
- No merge commit/ref or changes were made on either feature branch by this
  integration check. The temporary source directory was automatically removed.
- This tests those two exact heads and packages only. Later #131 updates and
  complete combined PostgreSQL/Redis behavior still require their own CI/review.

## Local verification

Completed:

- `go test -race ./go/agent-runtime/agent ./go/agent-runtime-harness -count=1`.
- `make agent-quality-regression` (rerun included in final verification).
- `make agent-baseline-test`: **159 tests passed**, including ten new outcome
  review tests. Pilot/partial/dirty-source gaps, malformed/legacy reports,
  read-only behavior, extension privacy and incomplete Usage are covered.
- `make agent-quality-calibrate`: **40/40 scripted passes**, 160 fixture Model
  calls / 120 Tool calls. The default benchmark profile remains unchanged.
- `node scripts/check-release.mjs`: clean external Go/TypeScript consumer gate,
  including 46 TypeScript tests and the new facade signature.
- `make agent-quality-anchor-check`: **2/2 public anchors**, with private evidence
  verification explicitly false. No original private report is certified here.
- `git diff --check` at each staged implementation commit.

The full local `make check` attempt passed metadata, Python protocol tests,
formatting/tidy checks, integration compilation, vet, **all workspace Go tests
and all workspace race tests**. It then stopped at staticcheck QF1003 in the new
test's if/else chain. That test is changed to a tagged switch in this follow-up;
all workspace lint and subsequent TypeScript lint/typecheck/test/build,
evaluation/benchmark smoke, OTel smoke, A2A product and release-consumer targets
then passed in a separate invocation. Every constituent check target has thus
passed across the two attempts; no single uninterrupted `make check` success is
claimed. Final PR Head CI and any remaining coverage/security/real-engine gates
must be recorded as actual results, not inferred from focused checks.

## Remaining effectiveness work

No paid Provider calls were made. No historical report or untracked Provider
probe in the original worktree was read or edited. No new complete clean-source
real-model score, broad task-family benchmark, statistically significant uplift
or cross-product ranking is claimed. Completion-time evidence acquisition,
approval/delegation convenience, coding-task Oracles and controlled live A/B
sampling remain separately scoped work.
