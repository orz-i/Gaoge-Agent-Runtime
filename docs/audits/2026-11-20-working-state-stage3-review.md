# Stage 3 review and repair

## Scope

Review of the Stage 3 changes before their implementation/review commit on
`codex/agent-session-output-limits`, based on `5c34b2f`. The review covers
WorkingState provenance, versions, branch isolation, admission and recovery,
Tool authority and legacy data. The review itself did not commit, push, merge
or advance a later stage; the subsequent commit was separately authorized.

## Confirmed defect

**P1, repaired: an admitted nil update could inherit future WorkingState.**

The original implementation preflighted and pinned the source only for an
explicit state update. A full-path Turn with `WorkingState == nil` selected its
base again when building its first checkpoint. With an injected failure after
Turn admission but before that checkpoint commit, a later Turn could update
state at the same source boundary. Replaying the original Turn with the exact
same ID and input then used that later state for its first model dispatch.

Both cases were reproduced before repair using the existing checkpoint-commit
fault fixture through a temporary Go overlay:

- Original state V1, later state V2: replay incorrectly carried V2.
- Originally no state, later state V1: replay incorrectly acquired V1.

The before-repair evidence is retained locally under
`C:/Users/mouta/AppData/Local/Temp/gaoge-working-state-nil-review-2te9w0/`
(`result.log`, `result-absent.log` and their overlay fixtures).

## Repair contract

`ConfigSnapshot.ContextBase` is optional derived, hash-bound metadata for the
actual Context Open plan. It freezes the previous checkpoint's four source
fields, full/delta mode, cache-reset choice and original expected active head.
A non-nil empty binding records that no previous checkpoint existed. This
freezes both carry and absence without converting either into an explicit
state update or advancing its version.

New admissions select and validate the plan before creating a Turn. Host input
cannot inject that binding. Retries compare the original Host admission and
configuration without derived metadata, then reuse the original sealed plan.
Restoration validates checkpoint identity, content, scope and coverage again;
delta plans also require the original static fingerprint. The original Store
commit detaches the old Turn if the active head has advanced. No second Store,
execution owner, transcript or model call is introduced.

Legacy Config snapshots without the field retain their original hash. Existing
checkpoints and explicit delta bases remain usable. An old accepted full-path
Turn without a checkpoint/binding fails closed if it would now inherit a state
whose presence was not frozen; the original facts are not rewritten.
An existing Turn whose original Session or Config cannot be loaded is rejected
before new source selection or persistence, rather than treated as a missing
Turn eligible for fresh admission.

## Other review conclusions

- Four-field provenance and a strictly increasing version are enforced against
  the selected immutable base. A new explicit source-delta branch may select
  an older base; versions are relative to that branch, not globally relative
  to an unrelated active head.
- State remains a separate user-role projection. Capture checks its exact
  prefix, and compaction preserves it outside ordinary Entries and raw
  transcript Artifacts.
- A focused Tool probe supplied state text claiming authorization for an
  unselected `admin_write` Tool and a larger output ceiling. The model attempted
  that call, but its declared Tools remained empty, the cap remained 128 and
  the handler executed zero times. Text references grant no additional rights.
- The original acceptance table counted the three invoice model calls only.
  The state fixture has one additional warmup generation that creates its
  source checkpoint. The table and byte totals now explicitly exclude that
  warmup instead of implying equal complete-fixture cost.

## Revalidation

Pinned Go 1.27.4 on Windows/amd64; isolated consumers and the real PostgreSQL
fixture use `GOWORK=off`, while module checks use this worktree's `go.work`.

- Harness full `go test -race ./... -count=1` passed (11.902 s). Final focused
  `TestContextBase` and Tool-authority race tests passed after all new tests
  were present (1.583 s), including four carry/absence/delta recovery modes,
  detached-head behavior, legacy compatibility, hash/clone integrity, caller
  metadata injection and invalid-source rejection.
- HTTP module full race tests passed, including Agent and Harness routes.
- Harness `golangci-lint run ./...` passed with zero issues after the final
  test files were added. Changed Go formatting and `git diff --check` passed.
- Existing isolated Agent/Harness consumer gates passed with the current
  cohort, without adding required Store methods.
- Original PostgreSQL explicit/facade crash recovery and WorkingState recovery
  all passed with `-race` (31.156 s). They retain the exact pending request,
  source/state, archived evidence and one business effect. Invoice call/byte
  counts are unchanged from the corrected Stage 3 acceptance table.
- An independent same-Turn race probe passed: one request paused after choosing
  an old head, another request with identical Host input chose a newer head and
  won admission. The loser received a conflict; its original-input retry reused
  the winning Config/checkpoint/state without another model dispatch.
- The pre-Stage 3 HEAD was independently built in an isolated temporary module.
  Nil seed JSON, admission hash and absent-binding Config hash/ID match the
  permanent golden test exactly. The Config hash is
  `70325c06acad5e02a683484e9701863a4add40ea13941b7ccf8326ab873f12c1`.
- Core source is unchanged by this review. The prior full Core tests, Context
  race tests, Context lint and legacy Window golden remain valid and were
  reused rather than rerun.

No further blocking finding remains in the reviewed scope. The temporary
PostgreSQL container was stopped. At review completion the worktree HEAD was
`5c34b2f` with Stage 3/review changes uncommitted; the original `main` checkout
remained clean at `19151c9`. The later commit packages this reviewed scope and
the [upgrade note](../releases/unreleased-working-state.md).

The Windows SQLite `dir.Sync(): Access is denied` baseline limitation from the
[Stage 0/1 review](2026-11-20-agent-session-stage01-review.md) remains applicable.
No SQLite source or durability policy changes are part of this review.
