# PR #76 independent admission correction — Application parent fence

- **Date:** 2026-10-30
- **Initial PR Head:** \`21a0b878afa56b52d1b9cf835e262495aa7e11d7\`
- **Base:** \`main@c0f331a7520fcca019bbb1ae254cdee5863af9d0\`
- **Scope:** Host-only Application child retries and parent liveness across
  separate Harness/Kernel Store reads and durable Effect claims.

## Two reproduced P1 admission windows

The original PR #76 fixes the root Actor/Thread identity fallback and checks
parent liveness before retrying the durable Attempt. Both changes remain
necessary, but Application effects are executed directly by a Host callback
and do not use Kernel-backed \`withPhysicalInvocationDispatch\` grants.

**Window A — after Retry Attempt CAS.** A controlled Store adapter pauses
immediately *after* committing a child Application's new
\`InvocationAccepted\` attempt but before returning it. The test cancels the
root Kernel Run while the retry is paused. On the initial PR Head, the
resumed \`executeApplicationInvocation\` still called
\`ExecuteApplicationCapability\` under the dead parent and completed,
even though the initial Retry preflight had observed a live parent.

**Window B — after Application effect claim.** A separate controlled Store
adapter pauses immediately after \`UpdateInvocation\` commits
\`Accepted -> Running\`, before the callback is invoked. The root Kernel Run
then terminates. The initial PR Head again executed the business callback
under a dead parent.

These are deterministic interleavings, not inferred vulnerabilities. The
tests are \`TestRetryChildApplicationRejectsParentTerminatedAfterAttemptCAS\`
and \`TestRetryChildApplicationRejectsParentTerminatedAfterEffectClaim\`.
Both failed before the correction and passed afterward.

## Minimal repair

- \`prepareApplicationExecution\` verifies exact Item / owning Harness
  Invocation / Kernel parent ancestry before claiming \`Running\` in the
  Harness Store. An already-retired parent leaves a new accepted attempt
  untouched for explicit recovery; no physical callback is invoked.
- \`executeApplicationInvocation\` verifies ancestry **again after**
  \`prepareApplicationExecution\` returns its committed claim and
  **immediately before** the Host executor boundary. If this read rejects,
  no callback has occurred. The claimed Invocation is failed with
  \`ErrApplicationNoEffect\` joined with the original conflict, preserving
  a committed no-effect classification rather than a fabricated business
  receipt or permanently ambiguous \`Running\` status.
- Existing \`ExecutionClass\` membership, Kernel/Store authority, tool/
  approval receipts, and retry execution identity remain unchanged.

Independent negative tests plus the valid child Application retry and
existing Application/Retry suites must pass \`go test -race\` with repeated
iterations. The real PostgreSQL/Redis continuation suite must also pass
on the *final* Head before admission.

## Residual boundaries

An Application child uses a Harness Invocation CAS as its effect claim; it
does not have a Kernel Run or a global cross-Store transaction. A parent
completing **after** the last pre-Executor check can still race the physical
callback. No automatic physical exactly-once guarantee is claimed.
A transient ancestor Store read failure after claiming \`Running\` is treated
as provably no callback from that attempt; further retries must pass the
normal frozen admission and parent-liveness checks, not reuse an old grant.

The correction does not add another status ledger, Job queue, FeatureDriver,
schema migration or compatibility bridge. Publishing coordinated beta.24
remains a separate user-authorized task.
