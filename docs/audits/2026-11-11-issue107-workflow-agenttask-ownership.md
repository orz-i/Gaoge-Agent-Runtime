# Issue #107 — Workflow AgentTask child ownership proof (2026-11-11)

> Historical remediation and audit evidence only. For the frozen scope and actual acceptance criteria use [EA-1 execution authorization contract](../issue107-execution-authorization-contract.md), [the fixed phase/entrypoint matrix](../issue107-execution-acceptance-matrix.md), and its per-cell CSV ledger. New generic audit rounds are not an implicit release gate.

## Reproduction and trust boundaries

Runtime base: `b3e496a50895d0f52a1dc37ea5c2a3979b05cebd`.
Gaoge hosted integration: `TestTargetWorkflowChildMCPInheritsFrozenApprovalBeforeFirstDispatch`.
Before this change, a Workflow AgentTask child may be started and persist its
`KindWorkflowEffect` relation, but `workflow.Runner.VerifyLocalChild` accepts
only two Workflow Run kinds plus an `EffectClassSubworkflow`. Harness's
`materializeSubtask` correctly leaves the unverified relation queued, with no
actionable approval. After fixing that, approval requires a second proof:
`harness.verifyLocalApprovalRun` must establish that the same original
concrete Agent will handle `ResolveApproval`.

Boundary sequence:

1. Workflow's committed Effect/Activation and compiled AgentTask node are the
   logical authority; a relation row alone is not authority.
2. The Host's original *immutable* `StaticEffectRouter` class route chooses
   an executor, whose `LocalAgentTaskOwner` binds an exact `*agent.Runner`,
   one relation recorder and the Host's pure typed Effect-input origin proof.
3. Both concrete pointer identities are captured once when the route is
   constructed. Changed pointers, unregistered/custom executors, mismatched
   relation index or foreign Kernel are rejected.
4. Harness recursively proves the entire Run's parent Invocation/Feature chain,
   then checks that its own `RunRelationSource` is the Workflow's exact recorder
   before reading Agent child state.
5. A physical approval still requires the **same** original Agent pointer,
   checkpoint ID, frozen Turn admission and physical dispatch Grant. The Host's
   first Workflow-child Run middleware verifies the private, per-call
   `VerifiedSubtaskApprovalDispatch` context after the Harness persisted the
   original Grant. The transient capability is atomically invalidated after
   the original physical callback returns, including abnormal unwinding:
   a Context retained by middleware cannot replay the original checkpoint.
   Direct `Agent.ResolveApproval` cannot substitute for Harness.
6. Physical Cancel with an ordinary native Agent still requires the Agent
   Kernel-owned cancellation fence. A Host-prepared Agent's opt-in is strictly
   limited to `QuiescentApprovalCancellation`: one pending Tool checkpoint
   with a valid immutable binding, no unfinished Model/Tool dispatch and an
   original static executor that certifies all approval admission through the
   same Harness Store. Cancellation of Running or uncertain Host effects fails
   before minting a new cancellation intent. The Kernel's original revision
   CAS and Harness's Dispatch Grant/cancel-intent arbitration remain mandatory.
7. An already terminal original child is read only for idempotent
   settlement; its committed Run status cannot execute a second Cancel.
   A Completed child without a valid result fails closed.
8. A mixed Host Canceller can attest only the original native Agent or
   Workflow RunKind bound to its exact Kernel. A2A, foreign/missing Runners
   and other unbound kinds stay remote
   or unverified. Post-write errors (including a misleading ErrConflict) are
   outcome-unknown, never a no-effect certificate.

## Positive and adversarial coverage

- Valid direct AgentTask with first and later attempts, specific owner node,
  exact Run ID, RequestID, tenant/actor/thread and effect source.
- Compiled Definition/Activation/Effect identity and unique matching nodes,
  duplicate effect/activation/node, bogus class and type.
- Foreign Agent (including a different Agent in the same Kernel), foreign
  Kernel and recorder, forged copied relation, missing static route.
- Foreign tenant/actor/thread, child Kind, parent Kind, relation owner/parent,
  stale Attempt, mismatched persisted ChildRunID, malformed state and Goal.
- Exact same-runner Harness approval through a real checkpoint, rejected
  stale/replayed checkpoint, Tool executes once on Resume, Workflow finishes.
- Harness refuses approvals from a same-values alternate relation registry.
- Native child `CancelSubtask` requires a valid physical owner; pending
  approval Grant blocks concurrent per-child cancellation. A Host with
  Run/Model/Tool middleware can cancel only before approval when it
  owns the original per-child physical admission fence. Subsequent Running
  approval/Tool dispatch is not a blanket cancellation grant.
- Exact Agent state matrix rejects corrupted checkpoint, wrong call, unknown
  Model/Tool dispatch, earlier/later checkpoint, terminal state and cross-Kernel.
- Parent EffectCompleted requires child RunStatusCompleted, original ReceiptID
  and semantically matching JSON Output; mismatched/foreign child denied.
- AgentTask-only Workflow does not depend on the separate nested DefinitionRegistry.
- 40 concurrent cancellation/approval races verify at most one physical
  admission; a read-only denial leaves the waiting approval actionable.
  Retained middleware Context is invalid after its exact callback ends.
  Repeated cancel after successful native/Host cancellation reconciles the
  terminal state without re-dispatch or an unknown intent.
- Real Gaoge Hosted fixture (with its Context, Model/Tool and Budget middleware)
  demonstrates approval, safe preapproval child/root cancellation, direct
  approval rejection, no foreign-A2A local ownership, and post-commit ACK loss
  never reclassified as no-effect.
- Historical Subworkflow child proof is exercised independently, unchanged.

Tests:
`go -C go/agent-runtime test ./workflow -run 'LocalAgentTask|VerifyLocalChildOnlyBuiltInSubworkflow'`;
`go -C go/agent-runtime-harness test . -run Issue107`.
Both affected modules have also passed race-enabled package suites locally.
Full-cohort source and required CI gates must run on the eventual exact PR Head.
A non-default Host Canceller must implement `LocalKernelRunKindCanceller`
only when its Cancel implementation actually delegates that RunKind to the
same Kernel; this is a strict contract, not a permission inferred by SDK.
The Host must not accept direct Agent approval before the first middleware
has checked the Harness-minted physical Dispatch context.

## Host integration and outstanding gates

The Host must implement all `LocalAgentTaskOwner` methods on the original
statically registered AgentTask effect executor and must verify a loaded
child's typed origin *before* accepting it, especially on retries or collisions.
Gaoge's integration candidate was exercised with an isolated test-only
workspace using this Runtime worktree and a matching Host opt-in. The original
test moved **past** its previous missing-approval assertion and accepted the
first physical approval. It then failed later at Agent Resume /
`Harness.Refresh` with `target provider replay state conflict`, separately
seen along the still-unfinished Gaoge canonical/replay migration. The full
Gaoge test is **not PASS**, and this work does **not** authorize merging
Gaoge #69 or the hosted cutover.

No database migration, whole-Run erasure, replay of unknown effects,
production deployment, tag or backwards-compatibility shim is part of this fix.

## Independent P1/P2 remediation after the final audit (2026-11-12)

The next independent audit reproduced three previously missed variants against
Runtime `e6023f7` and Gaoge `d9a9572`, with runnable adversarial cases
preserved under the Issue #107 regression files:

1. Parent Workflow has already committed `cancelled` or `failed` in
   Kernel; an older Harness Turn still exposes child approval. Previously
   `ResolveSubtaskApproval` returned success, and a subsequent native child
   Resume executed a Tool after parent cancellation.
2. One approved Tool is already executing. Kernel terminal Cancel commits,
   but the Tool dispatch lacks an authoritative completion receipt. Previously
   `CancelSubtask` returned `confirmed` while the Tool still ran; its late
   Kernel CAS conflict proved nothing about the physical callback.
3. A Host Effect freezes child limits of one LLM call and one Tool call, but
   a same-ID child already persists wider limits and matching public labels.
   Previously the Host's origin proof accepted it, including via
   `targetWorkflowAgentEffects.Execute`.

### Restored invariants

- **Execution authority:** `verifyLocalApprovalRun` walks raw Feature ancestry
  and rejects any terminal Kernel parent. A second verification occurs after
  the original physical Harness approval Grant is committed, before entering
  Agent middleware. A no-effect rejection seals the Grant; it does not leave
  an unknown physical dispatch. Gaoge's original Workflow-child Run middleware
  checks the parent again on Start/Approval/Resume. A trusted direct
  `Agent.Resume` cannot use a child whose parent has already terminated.
- **Physical settlement:** `agent.TerminalPhysicalEffectsSettled` reads the
  original validated Agent state, not just `RunStatus`. Unknown Tool dispatch,
  active Model invocation, Tool wait, deferred children and unconsumed external
  signals prevent declaring a local Workflow AgentTask cleaned. Cancellation
  retains the unknown-effect intent until authoritative reconciliation; it does
  not retry the Tool or infer cleanup from a failed late CAS.
- **Feature ownership isolation:** These additional physical requirements
  apply to the original local Workflow AgentTask route only. Remote Handoff,
  Team, GroupChat and A2A retain their own dedicated cancellation proofs.
  The existing PR82 descendant cleanup and retry tests are mandatory.
- **Frozen child policy:** The original Agent Runner, not a Host duplicate
  implementation, computes and verifies effective hard Limits with the same
  `resolveRunLimits` admission logic. A reused child must match its original
  Start ID, RequestID, Goal, Actor/Thread, Model, Tool Keys and ModelOptions,
  and initial task transcript. Replaced or widened Run state fails closed.

### Tests and outstanding scope

The independent P1/P2 tests are pinned in
`go/agent-runtime-harness/issue107_independent_audit_regression_test.go`,
`go/agent-runtime/workflow/issue107_independent_audit_regression_test.go`
and Gaoge's matching
`backend/internal/bootstrap/mainapi/issue107_independent_audit_regression_test.go`.
An additional Harness regression terminates the parent exactly after approval
Grant persistence but before middleware and verifies safe child cleanup.
State-matrix tests cover physical settlement and frozen Agent admission, with
all existing Host/native cancellation evidence tests retained.

The original Gaoge MCP end-to-end replay test remains separately blocked at
`target provider replay state conflict`; this audit patch does not declare
the hosted hard cut complete. A full Gaoge `backend/internal/bootstrap/mainapi`
package run also fails in older delegation/model, browser Tool outcome-verifier
and A2A cleanup paths. Selected representative failures were reproduced against
the **prior** Gaoge `d9a9572` commit with the same working Runtime cohort, so
those samples predate this remediation; the remaining failures were not all
independently classified. The complete Gaoge package is therefore **NO-GO**,
not an implied success from the passing Issue #107 focused tests.
Standalone Gaoge currently still pins an earlier Runtime beta.24 source
cohort and must update its SDK gitlink *together with* the future Runtime PR
head. No production drain, external billing proof or cross-repository CI
acceptance has been claimed here.


## Fourth-review atomic arbitration and crash-recovery remediation (2026-11-12)

Against Runtime `e4e6fb0` / Gaoge `67d6d2b`, the independent review
reproduced (1) the parent-cancel/child-Approval last-read CAS window,
(2) parent-cancel/approved Tool dispatch after the Host status read,
(3) relation loss permitting direct Agent approval without Harness Grant,
and (4) mutually exclusive frozen `isolatedContext` values accepted by
the same child. Evidence:
`/tmp/issue107-fourth-independent-evidence` on the isolated Mac.

The remediation replaces repeated liveness reads as permission with a
transactional `ChildExecutionStore` physical Grant. A persisted
`Run.ParentRunID` binds AgentTask and nested Subworkflow to every Kernel
ancestor. Store `BeginChildExecution` acquires/validates the complete
ancestor chain; parent terminal `Apply` / `ApplyControl` and child Approval
CAS must compete with the original Grant in the *same* persistence lock /
transaction, including across independent PostgreSQL connections. A direct
missing-relation child has no Root fallback. Frozen
`OriginFingerprint` binds the exact serialized parent Effect input
including privacy Context isolation; the Host original Runner verifies
the same immutable initial Model/Tool policy. Parented approvals must
pass a pure Host `ApprovalControlPreflight` of the real Harness dispatch
Grant before any Run middleware, physical Store Grant or Model/Tool call.

A further independent negative test uncovered a restart race:
a recovered Agent could previously see clean child state and retire a
**still active** approval's physical Grant before its callback returned,
allowing the parent Cancel to win and exposing the same TOCTOU. The fix
requires a distinct **durable post-callback settlement marker** tied to
the original Grant/revision and validated inside the Store transaction.
The Agent alone can mint its opaque proof after successfully returning
and verifying pending physical Model/Tool effects. The Store rejects zero,
forged, stale and premature settlement permits. Reconciliation is
read-only until this marker exists. Unknown outcomes stay blocked.

The previously separate Harness Turn budget cancellation path also needed
an exact source-restricted no-effect `ChildGrantBusy` fact. Without it,
a budget already cancelled by the first parent cancellation prevented
a second request from resuming even after the original Tool completed.
The deferred witness retains the **same** outstanding Harness cancel intent
and never releases the budget or grants a second side-effect authorization.
Invalid/duplicate/foreign proof Items are rejected.

**Positive and adversarial verification:**
- 80 in-memory competing parent terminal / child Begin operations.
- Same parent/child immutable origin, tenant, thread and fresh Attempt.
- Memory, GORM PostgreSQL and native SQLite cross-Store Grant suite.
- 30 real PostgreSQL cases with independently connected transactional
  cancellation versus Grant admission; the isolated server did not
  exercise production traffic.
- PostgreSQL/SQLite Grant survives restart, rejects premature retirement,
  and supports recovery after a persisted settlement marker with lost ACK.
- Original #107 Hosted Approval/Tool races and relation-loss/Context
  fingerprint adversarial fixtures (repeated five times).
- Active approval callback remains uncancellable by fake clean-state
  reconciliation; shared-budget active Tool rejects the first parent
  cancellation and permits safe retry after trusted receipt.
- Full Core, Harness, PostgreSQL and SQLite test suites, Race and lint
  must be rerun on the eventual exact locked PR head.

**Exit status:** This remediation is still not an independent final
acceptance. Gaoge #69 full Hosted ProviderReplay and other existing
integration failures, the outdated `sdk` gitlink, required GitHub CI,
operational migration and release proof remain NO-GO. Unknown external
effects cannot be administratively erased by an API call; a dedicated
authoritative external reconciliation process is required for any such
historical Grant.

## Fifth independent audit and sixth governance: complete physical admission (2026-11-12)

The fifth independent audit of Runtime `3e2a2739` and Gaoge
`7a01b9db` found three deterministic P1 variants. A nested Workflow
Effect executed during parent Cancel; a nested Workflow Wait resumed
and dispatched a fresh Effect AFTER its parent had already cancelled;
a parented Agent Start middleware executed a synthetic external Host
effect before child Create/Grant; and native PostgreSQL read child
Running before parent locking, permitting stale Grant admission after
an independent child Cancel. The original minimum failure tests are
retained as mandatory regressions in this Runtime source tree.

**Contract changes:**

- `Workflow.StartRun/Resume/ResolveWait` require Store physical
  admission around the whole interpreter segment for parent-bound Runs,
  with a distinct Workflow Feature settlement proof. Completed external
  effects require original receipts; unverified remote Pending retains
  its durable Grant until trusted physical reconciliation. A verified
  local pending Subworkflow child can safely release the intermediate
  synchronous callback when its own physical Grant remains authoritative.
- Agent Start now creates its immutable child, acquires the original
  physical Grant and THEN enters all Host Run middleware. For Host
  middleware on parented Runs, a pure `StartControlPreflight` must
  verify the frozen parent/Effect/relation before Create. The Gaoge
  Host verifies the same persisted source again on actual RunStart.
- All Run/Model/Tool `Next` callbacks are bounded by their original
  middleware call. Saved `Next` is rejected after return; a previously
  admitted asynchronous Next must finish before the outer call returns.
  Host callbacks that spawn detached effects without Next remain
  outside the SDK's physical settlement authority.
- PostgreSQL `BeginChildExecution` locks root-to-leaf ancestors and
  finally locks/revalidates the child state/identity. A child cancelled
  during parent lock wait cannot receive a new physical Grant.

**Test matrix:** 3 original fifth-audit P1 tests (Workflow effect,
Workflow Wait and Agent Start) plus real PostgreSQL stale-child test;
unknown remote external Pending, quiescent Wait, nested local child
Pending, stale/cross-tenant child identity, deferred and stored
Middleware Next, and full original Host approval/cancellation lineage.
The same actual Workflow Runner conformance executes on Memory,
GORM PostgreSQL, native SQLite and real isolated PostgreSQL 16.
The real PG stale-child interleaving has been repeated with two
independent physical connections; no production database was used.

**Remaining NO-GO:** this is local remediation, NOT a final independent
merge acceptance. No GitHub PR Head checks, production migration or
cutover have run. Gaoge's `sdk` gitlink is still pinned to the
pre-#107 cohort, and the original Hosted MCP test still reports
`target provider replay state conflict`. Those gates must remain
explicitly blocked rather than inferred from passing Issue #107 tests.

## Sixth independent review / seventh governance: exact revision and Root authorization (2026-11-12)

The sixth independent audit locked Runtime `30ad120` and Gaoge
`c7759dc`, discovering four connected P1 failures: (1) a stale
Workflow.Resume could send an external Effect at Run revision R even
though another worker committed R+1 before physical Grant admission;
(2) two valid Workflow ResolveWait continuations could cause a stale
pure checkpoint CAS to pin an UNKNOWN Grant without any Tool/Model call;
(3) Root Workflow omitted the physical Grant and permitted two queue
workers to issue the same Effect before CAS; and (4) a fully completed
Workflow could not recover its original durable post-callback settlement
marker after an ACK loss and a new process/Store instance. Tests were
preserved under `/tmp/issue107-sixth-independent-evidence` on the Mac.

**Remediation:** Kernel `BeginChildExecution(ctx,runID,expectedRevision)`
now binds the frozen original Run version. All three Stores atomically
check it with current status and parent identity. Root Workflow obtains
its own durable Grant and first-party scoped per-revision mutation
permit, replacing the parentless bypass. The original Root permit is
never inherited by a nested Subworkflow; that child gets its own Grant.
An external Effect Pending without authoritative receipt remains
QUARANTINED, not automatically replayable. Trusted original
`EffectResult.PhysicalNoEffect` permits a failed/compensating/retrying
Effect to yield safely only when no physical request was issued.
`Workflow.ReconcileChildPhysicalGrant` recovers an exact already-marked
Root or Child Grant without replaying any external operation. PostgreSQL
checks immutable StartRevision during settlement and release, not merely
its Grant ID; SQLite v3 follows the same exact transaction predicate.

**Required test matrix:** original independent R/R+1 Effect and Wait
races, duplicate Root Resume/Cancel, durable ACK lost and DB reopen,
invalid child/tenant/foreign revision, unknown external Pending,
original no-effect retry, Memory/GORM PostgreSQL/SQLite conformance,
real PostgreSQL two-connection locks and revision interleavings,
Agent/Host approval and child cancellation, Race, lint and external SDK
consumer cohort. No future-release or production migration is implied
by these local test results.

**Still NO-GO:** independent final review of the eventual locked commit,
Gaoge #69 full Hosted ProviderReplay and pinned SDK cohort, exact PR Head
required CI and safe production migration/drain have not been performed.


## Seventh independent review / eighth remediation: self-owned physical Grant (2026-11-12)

Against the locked Runtime `71bad5d` / Gaoge `c7759dc` heads, the
seventh independent reviewer found four P1 invariants missing and
preserved exact reproduction sources in the Mac-only
`/tmp/issue107-seventh-independent-evidence/` directory.

1. A Workflow root's public Cancel could dispatch an external
   `resource.release` compensation while Kernel.Cancel of that Run
   independently committed terminal state; no original physical Grant
   existed during the external callback.
2. A parented child Agent/Workflow with an outstanding physical Grant
   could independently Kernel.Cancel or Apply to its **own** Run,
   although its ancestors correctly rejected Cancel. The Grant owner
   index contained only ancestor IDs, not the executing child itself.
3. The first Attempt's genuine `PhysicalNoEffect=true` persisted after
   scheduling Attempt 2; an ambiguous external send on Attempt 2 was
   later acknowledged as no-effect by the stale marker, permitting
   Attempt 3 to duplicate the external physical request.
4. A Root Agent Start with an already-used Run ID could call arbitrary
   Host RunMiddleware and perform a synthetic external effect BEFORE
   Kernel.Create returned `ErrAlreadyExists`.

### Protocol changes

Root/child Workflow and parented Agent share an opaque
`<Feature>_physical_owner` and per-revision `<Feature>_physical_apply`
permit. Every Store now indexes the original physical Grant against
its own executing Run ID and full immutable ancestry. Kernel Apply
and first-party Control can mutate an active Run only with this
exact Grant/revision/Feature proof; a direct ownerless call fails
without a Store commit. Rejected direct Control must also preserve
the current legacy Agent ExecutionLease identity and epoch.
No helper can retire an unknown callback on elapsed time alone.

Workflow.Cancel now uses `withChildPhysicalGrant` around the entire
failure/compensation/terminal segment. For pending unknown external
Undo the Grant remains quarantined; completed trusted receipts
permit first-party settlement and later recovery without replay.
A new attempt-bound persisted `physicalNoEffectAttempt` records
the original Executor's affirmative pre-dispatch failure; scheduling
a later Attempt cannot convert this into proof for that new callback.

The Root Agent Start entrypoint now creates the Run before entering
Host RunMiddleware. A middleware error with no native continuation
cannot borrow any nested private no-effect proof to discharge
the Harness physical dispatch grant. Root Agent's original
ModelInvocation/ToolDispatch/ExecutionLease remains the authoritative
long-lived Model/Tool recovery mechanism; this phase does not
replace it with a second unverified root Grant.

### Verification and remaining risk

- All four seventh-audit minimal adversarial reproductions have
  dedicated tests in the checked-out Runtime source.
- A common self-physical-fence state machine checks Memory, SQLite,
  GORM PostgreSQL and native PostgreSQL, including rejection of
  direct self Cancel/Apply and preservation of original Agent
  ExecutionLease after a rejected Cancel.
- First-party Run Progress/Approval, frozen Host starts,
  Workflow compensation and Attempt-specific no-effect are tested
  across normal, cancelled and unknown external outcomes.
- Independent final review, exact PR Head GitHub CI, external
  ProviderReplay and production migration have **not** been
  performed. Gaoge #69's original Hosted MCP test and outdated
  `sdk` gitlink remain explicitly blocked; no unverified
  success/GO should be claimed.
