# Agent quality governance: bounded first delivery

Base: `origin/main@8b5ac865`. Independent of PR #131 (execution custody,
cancelled-Turn Retry and atomic budget admission). This branch must not change
Kernel, Store, grant, cancellation, Retry, shared-budget or physical-replay rules.

## Three independently reviewable stages

1. **Read-tool exploration.** Add an explicit Host-owned allowlist, copied at
   composition and frozen in each new Agent Run. Preserve legacy behavior when
   absent and on old Runs. An opted-in local read tool remains available for
   different arguments after failed searches; repeated identical settled calls
   are rejected before dispatch. Explicit capability denials, unknown outcomes,
   approvals, budgets and terminal-answer repair remain unchanged. No arbitrary
   callback, additional execution loop, automatic retry or authority expansion.
2. **Recommended tool-session composition.** Add an optional facade over the
   existing Agent/Harness runners, with explicit stores, authenticated identity,
   transcript, model window and per-tool policy. Wire bounded Context and its
   archive reader once. Reject unsupported capabilities rather than silently
   ignoring approvals/delegation/shared budgets. Keep the text-only facade and
   advanced composition unchanged.
3. **Task-outcome checks.** Provide repeatable offline task-outcome checks for
   the new exploration and composition paths, and a read-only review of existing
   real-model reports. Clearly separate scripted mechanics from real-model
   quality, incomplete pilots from complete scores, and unmatched experiments
   from valid comparisons. Reuse the existing baseline/report protocol instead
   of adding another benchmark engine. No paid Provider calls.

## Required evidence

- Focused regression and race tests, including default compatibility, immutable
  policy/restart behavior, exact-repeat refusal, unknown-effect preservation,
  hard-limit enforcement and facade input validation.
- Existing Agent/Harness tests, offline baseline protocol tests and synthetic
  calibration. Consumer and metadata checks for public API additions.
- Review the final diff against PR #131. Do not merge, rewrite, or push its branch.
- Each implementation stage is committed and pushed separately. The PR names
  actual local/CI evidence and unrun gates; no invented real-model uplift.

## Non-goals

No configurable general policy engine, workflow interpreter, semantic summary
algorithm, tool parallelism, steering, new provider, sandbox, schema migration,
model catalog, or automatic completion-time evidence acquisition. These require
separate evidence and scope. The first direction here addresses premature
read-tool suppression, not every possible Agent solving failure.

## Delivery status

All three implementation stages and a scoped self-review hardening commit have
been separately committed and pushed to PR #132. Local evidence, exact tested
heads, the non-mutating PR #131 integration check and remaining limitations are
recorded in [the delivery audit](../audits/2026-11-19-agent-quality-governance.md).

## User-authorized extension: directions 4 and 5

The subsequent user request extends this same PR beyond the original non-goals:

- Add root-Agent user input at model boundaries through an explicit Host log;
  freeze its binding and applied cursor, preserve pending requests/Tool receipts,
  and be explicit that queue acceptance is not atomic delivery at completion.
- Include the existing independent-child admission/recovery regression in the
  task gate. Ordinary native Tool parallelism remains serial until per-call
  dispatch/receipt/cancellation proof is designed; do not fake it with unmetered
  internal goroutines. See the dedicated input/parallel-read boundary guide.
- Correct the initial request-retention inference: consumed requests are already
  released. Optimize the verified discarded-replay copying in public history
  and consumption, backed by equivalence/isolation tests and a same-process
  replay-heavy microbenchmark. No new persistence format or inferred DB gain.

Extension code is pushed in `9a03d474` (projection allocation), `c53d0a7b`
(safe-point input), `bdf0d2ff` (queue capacity classification), and `6fa95dbf`
(callback-frame/ACK-loss hardening). Exact parallel-PR assertions are in the
input-boundary audit. Ordinary native Tool parallelism remains **not implemented**
in this delivery; the existing independent-child regression is not relabeled
as a new native parallel executor.

## Acceptance-driven correction after the completion reassessment

- `c3c08b69`: executable Host steering/late-input example, including next-Turn
  transcript preservation and duplicate-input detection; no new SDK interface.
- `ceff0354`: actual 100/500-step state-submission/reconstruction measurement.
  Final state grows about 5x while submitted aggregate JSON grows about 25x in
  that Memory/unbounded-history fixture; this is not measured disk/WAL.
- `48aede56`: fixed a real required-Tool bug discovered by the document task.
  Two regressions were reproduced RED on unmodified `ceff0354`, then passed:
  committed JSON lists/scalars were previously mistaken for failed calls.
- `83082b4f`: three independently checked code/document/approval deliverables,
  negative Oracles, actual artifacts, explicit profiles and a separately gated
  Provider path. Default fixture passes are not real-model quality evidence.

The acceptance follow-up expands the suite to seven
fixed variants, adds strict ordered case/family-count report validation, and
negative tests for stale public-test evidence, false denial claims, and
plausible unsigned-document distractors. It remains fixture-only evidence; the
real-model pilot remains explicitly deferred.

The final handoff keeps the baseline `review-quality` schema unchanged.
Acceptance validates its own JSON at the producer boundary and records ordered
planned cases, source/build identity, model/endpoint/profile identity for the
live path, and separate planned/processed family counts. Repeated fixture task
identity is stable; changing model/profile/endpoint/budget rotates it. No
general report-rendering framework was added.

Pinned combination `83082b4f125e5b1b2b0421f7b9597cd37ab14270` + PR #131
`5198b27d9afe5194c6d5aa2904e03e01caf5b634` produced temporary tree
`574b2550b9acdfb24b63557fd871a64b9173920d` without textual conflict. Agent,
Harness and HTTP race suites, the custody overlay assertion, and the new task
acceptance tests passed from that temporary source. No branch was merged.

Remaining: authorized actual-model repetitions/control-treatment comparison,
broader held-out workloads, persistent-backend/WAL/process-crash measurement,
and ordinary native Tool parallelism. No automatic completion claim is made.

Latest local delivery checks: full task-agent package tests and race tests;
focused artifact/negative-Oracle tests; full workspace lint; format/metadata;
expanded `agent-quality-regression`; integration compile; external Go/TS
consumers (46 TS tests); 159 Python report tests; unchanged scripted evidence
calibration 40/40. These are separate measured gates, not a new uninterrupted
whole-workspace or real-model effectiveness certification.
