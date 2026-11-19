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
