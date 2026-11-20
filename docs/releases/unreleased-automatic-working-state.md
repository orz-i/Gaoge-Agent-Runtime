# Optional native automatic WorkingState

An explicit `ConfigSnapshot.WorkingStateSummary` opts a Turn into one bounded
summary invocation before its first business model call. The original Agent
Run owns its pending/completed/consumed lifecycle, Provider binding, execution
lease, cumulative limits and per-call cap. This is an additional model call,
not a Provider call hidden inside Context preparation or transcript loading.
See the [integration guide](../automatic-working-state.md) and
[design/acceptance plan](../plans/working-state-auto-summary-stage4.md).

## Supported composition

The public Session integration supports ordinary Context recovery after callback
return. Dedicated `Context.SummaryNativeRecovery` additionally uses the original
Kernel PARK/ENABLE protocol for an interrupted native summary frame. It requires
the exact same Harness Store as the Runtime's `ManagedRecoveryHost` and a
restricted concrete native callback composition. This option and the existing
`ManagedRecovery` option are mutually exclusive. Shared-budget combinations
with summary remain rejected. No extra allowance, background worker, Kernel
RunKind, Store method or SQL column is introduced.

Host-provided WorkingState remains authoritative task data for this feature:
explicit or unknown-origin state is not automatically overwritten. Automatic
state carries a hashed `Derivation` locating its original summary receipt.
Only a verified same-Session automatic state with a compatible policy can be
updated automatically. Models cannot choose source/version/derivation or
create authorized evidence references. Tools remain subject to the original
selection, policy and scope checks.

## Upgrade and drain behavior

Nil policy/purpose/derivation fields retain their old serialization and hash
behavior. New policies are part of sealed Config and native Run identity;
reopening a handle cannot add or change an existing Run's policy.
The dedicated native recovery choice is also frozen in the original Run.
An ordinary Run cannot gain its recovery authority from a new handle, and a
native-recovery Run still requires that composition when reopened with nil
current summary policy. Explicit root Retry skips summary for the same Turn.
Admitted or terminal Turns without a Run retain the original Resume errors and
readable snapshots; a missing Run is never reconstructed for mode checks.

Upgrade every reader and worker that may encounter the new protocol before
admitting it. Summary purpose and source/recipe participate in the invocation
hash so an old reader cannot ignore them and consume an active summary as a
business answer. Old consumed requests are not reconstructed or rehashed;
the new Config/Window fields and coordinated reader deployment also govern
that boundary. No automatic hash rewriting or rollback execution bridge is
provided. Existing [legacy Accepted-Turn limits](unreleased-working-state.md)
continue to apply.

The original transcript remains available through exact Artifacts and the
existing authorized reader. Successful target application precedes Kernel
summary consumption. Recovery must recognize the original exact target after
a lost commit acknowledgement, rather than selecting today's head, issuing a
new summary or charging usage twice. Unknown physical Provider outcomes keep
the original unknown/no-effect boundary and cannot trigger a business fallback.
Dedicated recovery safe points are committed atomically with native summary
pending/completed/consumed state. The old physical callback is parked, not
declared returned. A dispatched request without a saved receipt has no reusable
pending safe point; no successor can resend it merely because the process died.
This dedicated protocol covers the original dispatch's first summary-phase
transfer and its selected safe-point repair. A later business intent gains no
new recovery authority. Another hard crash at a different phase after transfer
fails closed under the original immutable Host reservation; arbitrary chains
of cross-phase takeover are not supported.

This change also repairs a Stage 3 sampling clone omission: WorkingState and
its lists/derivation are now isolated across typed Context preparation ports,
while preserving the nil/empty distinctions required by their proof contract.

Review additionally counts the real summary invocation ID with exact counters,
includes the prospective business ID envelope, and retains typed Raw JSON size
for byte/fallback-token bounds even without a routed counter. Oversized active
summary requests fail closed on read. Source/target reconciliation also retains
the original Turn timestamp floor when the Host clock is ahead or moves back.
No existing request hash is rewritten.
The v1 recipe still requires legacy-compatible number literals and unambiguous
JSON keys. Older completed receipts with ineligible source data cannot install
derived state. A subsequent [archive codec fix](unreleased-context-archive-fidelity.md)
preserves structured numbers in new ordinary archives without rewriting old
artifacts, summary recipes, or their identities.

## Validation boundary

Acceptance separates summary/business/warmup logical calls, physical attempts,
actual usage coverage and request bytes. A smaller business request alone is
not a lower total-cost or task-quality claim. Offline fixtures and local
PostgreSQL are the implementation gates; paid live-model quality evaluation
requires a separately authorized run.

The [Windows SQLite baseline failure](../audits/2026-11-20-agent-session-stage01-review.md)
remains documented: directory `Sync()` returned `Access is denied` for both
baseline and worktree in the pinned Windows environment. SQLite publication and
fsync behavior are unchanged. The SQLite managed safe-point validator shares the new
finite lifecycle/event check with Memory and PostgreSQL; this does not change
directory synchronization. Scoped Context/Agent/PostgreSQL passes do not mark
the complete Windows SQLite integration/release gate green.

See the [Stage 4 acceptance record](../audits/2026-11-20-automatic-working-state-stage4.md)
for the actual hard-crash matrix, compatibility proof and paired costs.
