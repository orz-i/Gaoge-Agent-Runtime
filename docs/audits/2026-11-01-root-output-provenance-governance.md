# 2026-11-01 — Round 3: root Turn output provenance

**Branch:** `governance/root-output-projection-20261101`
**Base:** `main@d61daad` (PR #85 already merged)
**Scope:** ownership of the existing `Snapshot.Output` projection, without introducing another result Store or execution authority.
**Status:** local implementation candidate; not yet pushed, merged or released.

## Demonstrated failure on the unchanged baseline

The public Harness `Load` returns a Turn snapshot with the root
Invocation's Kernel result. However `RefreshInvocation` and
`ResumeInvocation` take a read-only safety path when a child Invocation's
resolved Host business callback still has no verified effect receipt.
Those paths passed the observed **child** `kernel.Snapshot` into the shared
`loadSnapshot` assembler. Prior to this change, it unconditionally used a
non-nil supplied snapshot when constructing the **root Turn's**
`Snapshot.Output`, without matching its Run ID against the top-level
Invocation.

A new public API regression reproduced the bug before production edits:
the parent Kernel Run was still Running with no output; the child Kernel
Run had a `"child-only-output"` result; the child Invocation held an
unverified resolved Host callback. Ordinary `runner.Load(turnID)`
returned nil root output, but both `RefreshInvocation(childID)` and
`ResumeInvocation(childID)` returned the child's result as
`Snapshot.Output`. The initial test failed in both subtests (exit 1).
This is an **incorrect output owner projection**, not evidence of
additional physical effect execution, original Call ID duplication or a
broken Kernel CAS.

## Minimal correction and non-goals

The existing `Runner.loadSnapshot` checks the supplied Runtime Run ID
against the Turn's unique top-level Invocation `ExecutionRefID`. A
missing/different root identity causes it to ignore the foreign snapshot
and reload the **original root** from the bound Kernel Runtime. A supplied
root snapshot with a matching Run ID retains the existing fast path.

This one internal guard covers all child observation callsites, including
the Host callback safety fences, without duplicating checks at individual
Feature/Interaction entrypoints. The protected child callback remains
outcome-unknown and must still be reconciled through its existing physical
receipt protocol. Results of child Invocations remain owned by their own
lifecycle Item/Artifact; the Turn output is **only** its top-level result.

## Independent P1 audit and bounded follow-up fix

An independent review of the initial fix discovered that **equal Run IDs
are not sufficient output authority**. Using temporary Go overlays (never
written to this source tree), two further public/physical paths reproduced
cross-owner Result exposure before correction:

1. A successfully completed `StartApplicationTurn` in tenant A has no
   Kernel Run. When another tenant's Run was created under the same
   deterministic `ExecutionRefID`, public `Load`, `Refresh` and
   `FinalizeHostOutput` returned the foreign Result. Host finalization
   additionally could persist the wrong output content hash in an immutable
   terminal message Item. All three entrypoints failed the pre-fix overlay.
2. An admitted Agent root whose physical Run had not yet been created could
   read a foreign Run of a different Kind, Actor or Kernel Thread occupying
   the same ID. The ordinary public Load result was incorrectly populated.
   The pre-fix overlay and later committed red regression tests reproduced
   Kind/Actor/Thread mismatch and missing frozen-input cases.

**Correction:** `ExecutionApplication` explicitly skips all Kernel Result
lookup in the shared Turn assembler and in `terminalRuntimeSnapshot`;
its original durable `Invocation.OutputRefs` are unaffected. Any physical
Kernel-backed Result must pass `validateRootRuntimeOwnership`, verifying
its actual `Run.ID`, `Run.Kind`, `Run.Actor` and `Run.Thread` against the
original frozen root Invocation input. A mismatch is `ErrConflict`, never a
fallback to Session/HostThread authority. Root runtime synchronization
validates a Result **before** writing a Turn/Invocation status, Run Item or
terminal Host Item. `terminalRuntimeSnapshot` validates before hashing Host
output. For read-only legacy recovery with no physical Result, the existing
state inspection is preserved; the unverified state is not allowed to
produce display output.

The positive public-path regressions also establish that ordinary root
output remains available when Kind/Actor/Thread match, and that valid
Application `OutputRefs`, terminal Host ACK idempotency and pending child
callbacks are preserved. A narrowly scoped fixture for the new root-output
test uses actual frozen Actor/Thread instead of weakening the validation or
rewriting established recovery fixtures. No new public API, status enum,
State Store, implicit retry, Kernel mutation, SQL/DDL, persisted version
or compatibility bridge is introduced.

## Second independent audit — result-free status, frozen Goal and dispatch

The subsequent independent code audit found further gaps in the first P1
correction. The old guard ran only when `runtimeSnapshot.Result != nil`, so
an unrelated result-free **Failed** Kernel Run with a colliding root Run ID
could cause `Runner.Refresh` to commit the original Harness Turn and
Invocation as Failed, importing the foreign error code. `FinalizeHostOutput`
could also append a terminal Host Item from that same result-free foreign
Run. A different physical Run with matching ID, Kind, Actor and Thread but
a different original Goal could be accepted and supply its completed Result.
All three scenarios were reproduced through temporary independent Go test
overlays and then ported into the permanent root-ownership test suite as
**RED tests before the follow-up fix**.

**Final correction:** `syncRuntimeSnapshotWithCancellationProof` verifies
root physical ownership **on every physical observation**, regardless of
whether Result exists, and before any status CAS or lifecycle Item. The
existing `terminalRuntimeSnapshot` also verifies every loaded physical Run
before Host ACK, even without a Result. The shared validator checks the
Goal in the original hash-verified frozen Invocation Input in addition to
ID, Kind, Actor and Thread. A result-free historical `Load` may remain
read-only and visible, but it cannot prove a state transition. A genuinely
absent Run after a documented no-effect admission failure still uses the
existing synthetic terminal projection, not an invented physical receipt.

Another targeted RED regression identified that `ResumeTurn` previously
could call `Agent.Resume` for a Run with matching ID/Kind/Session Actor but
wrong frozen Thread or Goal; it now uses the same owner check **before**
that physical entrypoint. The successful owned result-free failure and
idempotent terminal Host ACK remain permitted. First-attempt RequestID is
not stored as an extra field in Invocation Input. The later independent
audit closed this gap using the existing canonical Invocation ID and
Attempt-aware RequestID derivation, without new persisted identity state.

Two shared legacy *test fixtures* (the root Agent fixture used for
Interaction/cancellation regression tests, and the PR #82 Handoff parent)
were first corrected to seal their true original Input/Hash, rather
than use an arbitrary hash or empty Input while claiming to represent
a real admitted physical root. The final RequestID hard cut additionally
updated the affected synthetic test roots to canonical Invocation IDs
and adjusted their test references. Their physical Run state, execution
protocol and behavioral assertions remain unchanged; the original
missing-frozen-Input scenario is still checked by the dedicated
fail-closed/read-only regressions. Production did **not** add a legacy
identity bypass or compatibility shim.

## Third independent audit — physical Cancel, top-level Replay and RequestID

The third independent pass found that a foreign Run with the same ID,
Kind, Actor and Thread but a different Goal could be **physically cancelled**
by public root `Runner.Cancel` before the final root projection rejected its
metadata. A repeated top-level `StartTeamTurn` could likewise call the
Feature's `Resume` after an initial wrong-Goal Start was rejected and only
later report `ErrConflict`. The first physical RequestID was not verified,
so `Refresh` could accept a different original request with the same
ID/Kind/Actor/Thread/Goal as the root and terminalize the Turn.
Each finding was reproduced as an independent temporary Go overlay,
then persisted as a **RED-before-fix** regression in the repository.

**Fix:** `validateRootRuntimeOwnership` now proves Attempt-bound RequestID
in addition to frozen ID, Kind, Actor, Thread and Goal. For Attempt 1,
`InvocationID(TurnID, "", CapabilityKey, snapshot.Run.RequestID)` must
equal the frozen Invocation ID. For a later Attempt, physical RequestID
must exactly match the existing `stableID("hretry", invocation.ID,
attemptString(Attempt))`; the original RetryInvocation Start path already
uses this identity. `replayTopLevelFeatureStart` validates a loaded root
before invoking any Feature Resume. `cancelRuntimeRun` receives an explicit
optional pointer to the **original root Invocation** from root `Cancel`
and validates on its final pre-control read before any native/custom
physical Cancel or terminal acceptance. Child and RunRelation cancellation
callers deliberately supply nil, so a foreign root from another Turn cannot
lend authority to a remote child. An invalid root is classified as a
direct, proven no-effect read rejection for the existing cancellation-intent
recovery; the earlier Host intent and bounded settlement contract are
unchanged.

A trial early physical read *before* the Host Cancel intent disrupted
bounded cancellation recovery and existing read-fault injection sequencing,
so it was removed. The final fix verifies immediately before physical
control without inventing another read phase. Existing tests with
synthetic root IDs were updated to use canonical first-attempt identity,
not rescued via an accepting legacy branch. A fresh positive regression
verifies that original `RetryInvocation` Attempt 2 succeeds with its
`hretry` RequestID and that a wrong retry RequestID is rejected.

**Final integration fixture correction:** The first full repository/real
PostgreSQL gate on this final candidate exposed a related test-only root
fixture in `go/agent-runtime-harness-postgres/pr82_remote_handoff_cancellation_test.go`
which had arbitrary root Invocation ID and no frozen Input. It caused the
new strict root check to return `ErrConflict` before the expected
`ErrHandoffCancellationUnverified` Handoff cleanup path. The fixture now
uses `InvocationID(TurnID, "", CapabilityAgent, "root")` and its real
hash-sealed Actor/Thread/Goal Input, matching the existing physical Run's
RequestID. All original remote Handoff cancellation and same-ID collision
assertions were retained. The first attempt's failed gate is preserved in
logs, not misreported as a code-success result.

The **final unchanged production/source candidate** was then verified by
both a fresh whole-repository `make check` (**PASS, exit 0**, Go unit/Race/
Lint, TS lint/typecheck/46 tests/build, evaluation/benchmark Smoke, OTel,
A2A, beta.24 clean consumer) and the full real PostgreSQL 16/Redis 8
`node scripts/run-integration.mjs --external-services` (**PASS, exit 0**)
with isolated dynamic host ports. Real Postgres cross-connection,
Handoff-root cancellation, native no-effect recovery and Redis continuation
suites passed. The isolated test containers were automatically removed;
pre-existing user development containers and OpenAI probe scripts were
untouched. No source-schema, SQL/DDL, external Provider or beta publication
was tested or changed.

## Fourth independent audit — native child execution ownership

A final independent review of the root owner hard cut identified two further
P1 child ownership failures. Native `ResumeInvocation` called a Team `Resume`
for a same-ID but wrong-Goal child Run, and `RefreshInvocation` persisted an
unowned completed child Result as an immutable `ItemArtifact` and terminal
Invocation. Separately, native child admission trusted an unverified parent
Kernel Run's Actor/Thread: a root `StartAgentTurn` correctly returned
`ErrConflict` for a foreign-Goal or foreign-tenant root, yet a subsequent
`StartTeamInvocation` still physically started a child. All four cases were
reproduced independently using temporary Go overlays, then added as permanent
**RED-before-fix** public API regressions.

**Correction:** `childInvocationContext` now verifies the exact frozen root
Run before admitting native children. A narrow
`validateChildRuntimeOwnership` checks the existing hashed child Input's
frozen root/ancestor Attempt bindings, validates the original physical root
owner and compares the native child Run's ID, Kind, Actor, Thread, Goal and
Attempt-specific RequestID. `ResumeInvocation` and the shared `resumeFeature`
entry check before physical continuation; `syncChildInvocationSnapshot` checks
before status CAS, diagnostic/approval facts and Result Artifact projection.
The parent dispatch grant now rechecks root and intermediate native parent
physical identity immediately before physical Start/Resume, preventing an
intervening replacement from being accepted as authority.

This validation is **not** applied as a generic route inference for Handoff
children. The original fixed ChildRunner and delegation receipt checks remain
authoritative. Existing cancellation may legitimately observe an Accepted
child for which *no Kernel Run was ever created*: only the already-verified
cancellation synchronization path may project its synthetic Cancelled state;
normal Refresh/Resume never accepts a synthetic Run or foreign result as
execution authority. Tests constructing artificial already-admitted native
roots/children were corrected to freeze true Goals, canonical original
Invocation IDs and Attempt RequestIDs rather than adding a permissive
production fallback. The full Harness suite and new child P1 tests passed
under Race. The post-fourth-audit final candidate passed:

- Four independent public API RED-before-fix regressions now GREEN:
  `TestIndependentChildTeamResumeRejectsForeignGoalBeforeEffect`,
  `TestIndependentChildStartRejectsWrongRootBeforePhysicalFeature`,
  `TestIndependentForeignRootActorCannotAuthorizeChildStart`, and
  `TestIndependentChildResultRequiresOriginalOwnerBeforeProjection`.
  Original child cancellation-route proof also confirmed no physical
  effect occurred for a custom Team lacking verified local ChildRunner.
- Child owner, root Attempt, nested cancellation and original root
  projection security regressions: **PASS, `go test -race -count=10`**.
- Complete Harness module `go test -race -count=1 ./...`: **PASS**;
  Harness `golangci-lint run ./...`: **0 issues**.
- Full repository `make check`: **PASS (exit 0)** after the final
  implementation. This includes all Go unit/race/lint/vet, TypeScript
  lint/typecheck/46 tests/build, evaluation/benchmark smoke, OTel/A2A and
  beta.24 local-cohort clean-consumer checks.
- Real PostgreSQL 16 + Redis 8
  `node scripts/run-integration.mjs --external-services`: **PASS (exit 0)**
  on isolated dynamic host ports. Original Handoff and cross-connection
  cancellation/continuation suites passed. Temporary containers removed;
  existing development services and unrelated untracked OpenAI probe scripts
  were left untouched.
- `gofmt`, docs relative links and `git diff --check`: **PASS**.

Some older synthetic test roots/children had arbitrary Invocation IDs,
incorrect physical RequestIDs or empty Goal Inputs that were incompatible
with the already-established frozen identity protocol. Those fixtures were
corrected, without a production compatibility adapter. A proven no-Run
cancellation of an Accepted child remains the **one explicit internal
exception** to physical child snapshot validation, and only when it is
projected by the existing verified cancellation path; it cannot be reached
from ordinary Refresh/Resume or child Artifact projection. No new database
state machine, DDL, Host intent router or schema version was introduced.

## Fifth independent audit — local native cancellation and Subtask read authority

Independent review after the fourth repair found that
`verifyLocalRelationChain` still returned success as soon as a non-Handoff
native Invocation matched a physical Run ID, without validating that the
actual Kernel Run belonged to the frozen Invocation. An original child
Agent Invocation with Goal `original authorized child task` was used to
cancel a same-ID physical Run with Goal `different physical task`:
`CancelInvocation` **physically cancelled** the foreign Run before
subsequent Harness synchronization returned `ErrConflict`. Separately,
public `Runner.Load` exposed another tenant's same-ID physical child Run
under `Subtasks`, including the foreign Goal, `completed` status and the
literal private Result. Both independent tests failed under `-race`
using temporary Go overlays before any production edit, and were then
added as permanent regression tests in
`local_relation_owner_regression_test.go`.

**Bounded fix:** The original `verifyLocalRelationChain` owner branch
now invokes `validateRootRuntimeOwnership` or
`validateChildRuntimeOwnership` for ordinary native Invocations. It retains
raw Feature RunRelation edge priority, Handoff's independently bound
ChildRunner proof, the existing no-effect cancellation settlement, and
read-only persisted relation facts when a physical Run is unverified. No
new execution-owner registry, Store record, public API or schema is added.
The same call path protects both native Kernel physical Cancel and public
Subtask Goal/status/Result projection (and existing local approval checks).

The additional original-root `runtime.Load` required for child proof also
runs during read-only `findSubtask`. Two pre-existing fault-injection tests
explicitly counted Kernel reads before a cancellation's Store CAS. They
were moved by one read **only for `CancelSubtask`** so they continue to
inject at exactly the original *post-cancellation-intent, pre-physical-
control* boundary. The tests still assert no physical effect, exact
native no-effect evidence, bounded release of the original cancellation
intent and success of a single subsequent safe retry. Neither callback
ordering nor the Host cancellation protocol was modified.

The two new P1 regressions are **RED before fix and GREEN after fix**;
repeated `go test -race -count=10` passed, together with the complete
Harness module Race suite and zero Harness lint issues. The final
post-fix whole-repository `make check` completed **PASS (exit 0)**,
including Go unit/Race/Lint/vet, TypeScript lint/typecheck/46 tests/build,
evaluation/benchmark smoke, OTel, A2A and beta.24 local clean consumers.
The real PostgreSQL 16/Redis 8 `node scripts/run-integration.mjs
--external-services` suite completed **PASS (exit 0)** on isolated dynamic
host ports, including cross-connection cancellation, Handoff and Redis
continuation recovery. Temporary test containers were removed, and existing
developer services were left untouched. This full candidate remains an
uncommitted local implementation; no files were staged, committed, pushed
or merged.

## Sixth independent merge audit — cross-Turn capability relation owner

The subsequent independent merge review found a remaining P1 scope boundary:
`KindCapability` RunRelation accepted a valid Kernel Run belonging to an
entirely different Harness Turn/tenant. It was enough to add an unrelated
Capability edge from Turn A's root to Turn B's legitimate physical Agent
root. `Runner.Load(turnA)` then disclosed B's completed Run Goal, status and
private Result through A's `Subtasks`. On the same forged edge while B's
Run was still Running, public `CancelSubtask(turnA, ...)` physically
**cancelled B's Run and returned success**. This was not a forged Run ID
alone: B's own frozen Invocation, Actor, Thread, Goal and original RequestID
were all valid, but its parent/Turn relationship was not. Both failures
were reproduced under `go test -race` using read-only temporary overlays,
then added as permanent **RED-before-fix** public API regressions in
`cross_turn_capability_relation_test.go`.

**Minimal hard cut:** `verifyCapabilityRelationOwner` in the existing local
RunRelation proof verifies that only an original **native child** can own a
`KindCapability` edge. It binds the immutable child's `ExecutionRefID` to
`ChildRunID`, `invocationRelationOwnerID(child)` to `OwnerNodeID`, and the
original hash-verified root Attempt's `ExecutionRefID` to `ParentRunID`.
Root and child must be from the same Turn. The verifier runs before
`verifyLocalRelationChain` accepts even a valid native owner, blocking
cross-Turn physical Cancel and read-only Subtask Result/Goal/status leakage.
`CancelSubtask` additionally rejects an edge against the explicitly
requested Turn using **Store metadata only, before creating a Host cancel
intent or touching any physical Canceller**. This preserves original
Kernel read-fault injection counts and no-effect recovery timing.
Original raw Feature-member relations take precedence, remote Handoff
retains its distinct fixed ChildRunner/receipt proof, and the original
persistent relation facts can remain observable without importing an
unverified physical Result. No Store API, SQL DDL, persisted schema,
Host intent router, compatibility shim or new execution authority was added.

The original two public API regressions now pass; their forged edges use
B's canonical Invocation OwnerNodeID, not merely an obviously invalid
arbitrary owner string. `CancelSubtask` also verifies that no cancellation
intent was opened for the foreign Turn. An additional positive-and-negative
regression checks a legitimate native child Capability relation and rejects
wrong OwnerNodeID, ParentRunID and ChildRunID.

**Final post-sixth-audit verification:** the two independent public API
regressions were **RED before repair, GREEN after repair**, then passed
with the legitimate relation-owner test and root/child security regressions
under `go test -race -count=10`. The complete Harness module passed
`go test -race -count=1 ./...`, and Harness `golangci-lint` reported
**0 issues**. The full repository `make check` completed **PASS,
exit 0** after the final source changes: all Go unit/Race/Lint/vet,
TypeScript lint/typecheck/46 tests/build, OTel, A2A, benchmark/evaluation
smoke and beta.24 local clean consumers. Real PostgreSQL 16/Redis 8
`node scripts/run-integration.mjs --external-services` completed **PASS,
exit 0** on isolated dynamic host ports, including cross-connection
cancellation and native/Redis continuation recovery. Ephemeral test
containers were removed; existing developer services and the two unrelated
untracked OpenAI probe scripts were left untouched. Final `gofmt`, docs
local links and `git diff --check` also passed. No staged commits,
repository push, PR, merge, DDL, wire/schema change, compatibility bridge
or new authorization state were introduced.

## Verification

- Red test before the fix:
  `TestPendingChildObservationNeverProjectsChildResultAsTurnOutput` failed
  on both public child APIs with `CHILD RESULT LEAK`.
- Green test after the fix: root Running without output and root physically
  Completed with its own `"root-only-output"` both return exactly the same
  root output as an ordinary `Load`, even after the physical child has
  completed with a different result. Both child `RefreshInvocation`
  and `ResumeInvocation` pass. The unknown business callback stays
  unresolved, its handler is called only once, and root Run Revision stays
  stable. **PASS, `go test -race -count=10`** (original round 3).
- Independent P1 regression: new `TestRootApplicationNeverProjectsForeignKernelOutput`
  uses actual `StartApplicationTurn`, another tenant's completed Run under
  the same execution-ref ID, and public Load/Refresh/FinalizeHostOutput.
  Output remains nil, original Host `OutputRefs` remain intact, and no
  foreign content hash is written to an acknowledged terminal message.
- `TestRootNativeOutputRequiresFrozenKindActorThread` covers a matching
  Agent root (positive control), wrong Kind, wrong tenant/Actor, wrong
  Kernel Thread, public Load, Refresh and Host finalization. For invalid
  roots, `ErrConflict` happens before foreign physical or terminal Items,
  or root status updates. Missing frozen authority fails closed under
  `TestRootOutputMissingFrozenIdentityFailsClosed`. These tests were **red
  before the P1 fix and green afterward**, then repeated **5 times under
  `-race`** with the original child-output regression.
- Full repository `make check` on the final P1 candidate: **PASS, exit 0**
  after fixing two static lint findings (`errors.Is` in the new test and
  explicit exhaustive `ExecutionApplication` switch case). Covers Go
  module unit/race/lint, metadata/format/tidy/vet, TypeScript
  lint/typecheck/46 tests/build, evaluation and benchmark smoke, OTel
  smoke, A2A checks and beta.24 local-cohort clean consumers.
- Real PostgreSQL 16 / Redis 8 integration: **PASS, exit 0**. The first
  `make integration` attempt could not bind the fixed Redis host port
  `56379` (occupied), so it never started tests. An isolated pair of
  PostgreSQL 16 / Redis 8 Docker containers on dynamic host ports then
  executed the same repository integration suites through
  `node scripts/run-integration.mjs --external-services` with a fresh
  `TEST_POSTGRES_DSN` and `TEST_REDIS_ADDR`. Real engine Race,
  cross-connection, cancellation, continuation and physical recovery tests
  all passed. Temporary containers were removed automatically; existing
  developer containers were not modified. No test script or Compose file
  was changed to work around the environment conflict.
- Follow-up red-to-green regressions added permanently:
  `TestRootResultFreeForeignRunCannotProjectRootFailure`,
  `TestRootResultFreeForeignRunCannotFinalizeHostMessage`,
  `TestRootMatchingKindActorThreadButDifferentGoalFailsClosed`,
  `TestRootOwnedResultFreeFailureStillProjectsAndFinalizes` and
  `TestResumeTurnRejectsWrongFrozenThreadOrGoalBeforeAgentDispatch`.
  The three original audit defects and unsafe Agent Resume were reproduced
  before the fix, then all passed. Existing Harness complete tests also
  passed after original frozen test fixtures were corrected.
- **Post-second-audit verification:** all new root-owner/foreign-Run,
  frozen Goal, result-free terminal Host ACK, safe Agent Resume and
  original child-output regressions passed **10 repetitions under
  `go test -race`**. The complete Harness module passed with `-race`,
  `golangci-lint` reported **0 issues**, and legitimate top-level retry,
  cancellation and verified callback recovery passed **three further
  `-race` repetitions**. The full repository `make check` completed
  **PASS (exit 0)** after the final changes, including all Go modules,
  TypeScript 46 tests, OTel/A2A, evaluation/benchmark smoke and beta.24
  local-cohort clean consumers. The real PostgreSQL 16/Redis 8
  `node scripts/run-integration.mjs --external-services` suite completed
  **PASS (exit 0)** on isolated dynamic host ports, including real SQL
  cross-connection, cancellation and continuation recovery; both test
  containers were removed, existing development services untouched.
- `gofmt`, documentation local links and `git diff --check`: **PASS**.
- No live Provider/gateway, mobile rollout, `make beta` or release tag
  was included.

## Release / compatibility boundary

This is a pre-stable semantic correction: Hosts that unintentionally used
`Turn.Output` as a child result must read the specific child
Invocation Item/Artifact, not rely on the parent Turn being overwritten.
No historical data migration is required: the error arose at assembly
time from an incorrectly supplied in-memory observation, not from an
incorrect persisted root Result. Existing host authorization and frozen
Invocation/Turn admission checks are unchanged.

**Follow-up rule:** Any future optimization that accepts a supplied
Runtime snapshot for top-level result presentation must preserve the
original root Invocation's execution identity and cannot treat the
most-recently observed child as the parent outcome.
