# Explicit WorkingState and Context admission

Stage 3 adds optional, bounded Host-authored `ContextSeed.WorkingState`, the
corresponding independent `Window.WorkingState`, and a Session source-boundary
lookup. State is user-role task data with exact checkpoint provenance and a
version relative to its selected branch. It cannot grant Tools, approvals,
budget or a larger model output ceiling. Automatic summarization remains a
later stage. See the [integration guide](../working-state.md).

## Upgrade and compatibility

New Context admissions freeze their actual Open plan in optional, hash-bound
`ConfigSnapshot.ContextBase` metadata before creating a Turn. This includes a
known absence of a previous checkpoint/state, the original source and expected
head. Retrying an interrupted admission uses that plan and the original Host
input; it cannot acquire later WorkingState. When the head has advanced, the
existing Store commit preserves the recovered Turn as a detached branch.
Host-supplied base metadata is discarded.

Upgrade every worker/reader that will handle the new Config or checkpoint
fields before admitting them. Older executables cannot reconstruct their
hashed semantics safely; rolling back to such an executable must not continue
these newly admitted Turns. No automated conversion or hash rewriting is
provided. No required Store method or SQL column changes: the existing JSON
payloads carry the optional fields.

Absent fields preserve legacy Window, ContextSeed, admission and Config hashes.
Legacy Turns with an existing checkpoint restore normally. An explicit source
delta still names its original immutable base. One old Accepted-Turn corner
cannot be recovered safely: a full-path Turn with neither a checkpoint nor a
base binding is rejected with a conflict if its selected base would now supply
WorkingState. The original admission did not freeze that state's provenance.
The Turn remains Accepted without model dispatch; trusted Host reads remain
available to inspect its original facts. Legacy full-path admissions that do
not encounter such state continue to work. Deployments with outstanding
pre-binding admissions can complete them before starting WorkingState updates.

## Validation and known limitation

Harness/HTTP race tests, final Context-base/authority regressions, independent
Agent/Harness consumer checks and PostgreSQL cross-process recovery passed.
The [review record](../audits/2026-11-20-working-state-stage3-review.md) details
the reproduced nil-carry defect, repair, compatibility vectors and scoped
validation. Its invoice call/byte counts exclude the separately identified
warmup source Turn.

The [Windows SQLite baseline failure](../audits/2026-11-20-agent-session-stage01-review.md)
is retained: both baseline and worktree failed directory `Sync()` with
`Access is denied` in the pinned Windows environment. This change does not
modify SQLite or weaken durability. The scoped passes above do not mean the
complete Windows SQLite integration/release gate passed.
