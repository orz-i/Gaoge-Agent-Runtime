# Issue #107 — Workflow AgentTask child ownership proof (2026-11-11)

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
