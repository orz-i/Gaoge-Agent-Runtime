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
8. A mixed Host Canceller can attest only the local Agent RunKind bound to its
   exact Kernel. A2A, foreign/missing Runners and other RunKinds stay remote
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
