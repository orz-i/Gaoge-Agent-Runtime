# Breaking changes (pre-stable Beta)

**Policy:** Before a stable release, incompatible architecture/contract changes are permitted when explicitly decided. They must remove old shims/fallbacks rather than retain parallel protocols, document deleted symbols and persistence impact, and pass SDK consumer and recovery tests. A published Beta tag is immutable; publish changes in a new prerelease.

## 2026-10-30 — Application child post-claim ancestor fence (beta.24 candidate)

**Independent PR #76 review correction:** In contrast to Team, Workflow or Agent,
a Host-only Application capability does not have a Kernel Run and does not
pass through the generic physical Feature dispatch grant. Consequently its
parent-liveness check before `RetryInvocation` CAS was insufficient: the
parent could terminate after the new Attempt was persisted, or after the
Application's `Accepted -> Running` claim was committed, yet the Host
Executor callback still ran under the stale child.

The Application effect boundary now checks exact parent Item/Invocation/
Kernel ancestry immediately before `Accepted -> Running` and **again after**
that claim, immediately before calling `ExecuteApplicationCapability`.
A pre-claim rejection leaves the original Accepted Attempt untouched;
a post-claim rejection has affirmative proof that no Host callback was
invoked, records that claimed Attempt as Failed with a no-effect cause,
and does not fabricate a business receipt or reissue an effect.

Two deterministic interleavings, paused after Retry Attempt CAS and
after Application effect-claim CAS respectively, reproduce incorrect
physical dispatch on the initial PR Head and pass with the correction.
The post-claim, pre-callback check still cannot be atomic with an unrelated
Kernel parent completing a moment later; Host policy must retain the
already documented cancellation and reconciliation boundaries. No new
Application protocol/SQL schema/Kernel status is introduced.

## 2026-10-30 — Child retry parent liveness before Attempt CAS (beta.24 candidate)

**Correction:** A failed/cancelled child `RetryInvocation` can no longer
rotate its persisted `Attempt` / `ExecutionRefID` while its parent Kernel Run
has already terminated, even when the product-level Turn still says Running.
The exact Parent Item ancestry and owning Harness Invocations must also
remain live: a terminal intermediate parent invalidates a descendant retry.

Harness now validates that ancestry before Store `RetryInvocation`, not only
later inside its durable physical-dispatch callback. A known-dead parent
returns `harness.ErrConflict` without consuming an Attempt, creating an
unreachable Accepted Invocation, or issuing a new physical Run. The
existing **post-grant** ancestry fence still rejects parent cancellation
racing the preflight; this separate Kernel/Host Store read is not asserted
to be an atomic cross-database transaction.

Application capabilities are Host effects without a Kernel Run. Child
Application retries still validate their Host Invocation ancestors, but
are **not** required to load an imaginary Application Runtime Run. Existing
valid child Team/Workflow/Application retries retain their input, retry
identity and parent relation contracts. No new public Driver, synthetic
parent status, SQL DDL, Schema version or old-protocol fallback is added.

## 2026-10-30 — Frozen root retry execution identity (beta.24 candidate)

**Removed:** Harness top-level `RetryInvocation` no longer reconstructs a missing
frozen Actor or Kernel Thread from `Session.Actor` / `Session.HostThread`.
A Host Thread is a product-level identity, not permission to replace a
persisted Kernel Thread during a new physical Run attempt.

A retryable root Invocation (Agent, Team, GroupChat, Workflow or Application)
must have valid, matching `Input`/`InputHash` evidence and a nonempty, valid
Actor and Kernel Thread. Missing, malformed or mismatched frozen evidence
fails closed with `harness.ErrConflict` **before** Store `RetryInvocation`
rotates `ExecutionRefID` or reopens the Turn. The typed Feature dispatch path
checks the same frozen fields after the attempt is accepted. A historical
pre-hard-cut Invocation without that identity must be inspected and
drained/quarantined; it is not silently upgraded from the Session.

No new persisted fields, schema number, Session lookup or compatibility
migration is introduced. Current valid typed root retries retain their
existing deterministic Run identities and execution behavior.

## 2026-10-30 — Hosted Tool terminal receipts fail closed (beta.24 candidate)

**Removed:** The empty string Hosted Tool status is no longer a legacy terminal
receipt. `model.HostedToolTerminalStatus("")` now returns false. Only known
terminal states such as `completed`, `failed`, `error`, `cancelled` and
`canceled` certify an ended provider-hosted effect; unknown and blank values
are unfinished. Providers must send an explicit terminal status.

**Durable budget safety:** With a stable provider-owned Tool Call ID, an
unfinished fact keeps its existing held hosted Tool reservation; a later
matching explicit terminal receipt may settle it. If an unfinished Tool has
**no provider-owned Call ID**, automatic reconciliation is impossible.
Harness does not mint a fake remote ID or silently refund quota. It rejects
the attempted native accounting with `budget.ErrInvalidUsage`, suspends
the dispatched Model reservation as unresolved and retains the original
Host budget claim. Restart/retry cannot reissue that logical Model request.
This intentionally sacrifices automated token settlement for unidentified
native outcomes rather than fabricate a completion or spend quota twice.

**Host action:** Drain or explicitly quarantine pending unnamed/unknown native
Tool outcomes before upgrading. Adapt third-party Model clients to emit an
actual terminal status and stable Call IDs for asynchronous native work.
Historical receipts are not rewritten, no SQL migration/new ledger type or
replay bridge is introduced, and published beta.23 tags remain immutable.

## 2026-10-30 — Provider binding identity hard cut (beta.24 candidate)

**Named Provider binding:** Agent now freezes an opaque SHA-256
`bindingFingerprint` into the durable ModelInvocation and `requestHash`.
The unary `model.Client` interface is unchanged, but all named external
Providers must implement `model.RequestBindingFingerprinter` with a
deterministic, nonsecret lowercase SHA-256 fingerprint. A missing or malformed
proof rejects a new intent before any physical dispatch. A changed Provider
name or a changed/missing binding fingerprint rejects a persisted pending
invocation before claim, then again immediately before dispatch.

**Pre-dispatch admission race closure (PR #75 independent review):**
Revalidate the frozen Provider name and binding fingerprint inside the final
Model dispatch guard **after** ModelMiddleware, Hosted Tool discovery and shared
concurrency-permit waits, but before the physical Provider call. A changed
binding now returns the distinct recoverable
`agent.ErrProviderBindingMismatch` error, not the permanent
`agent.model_dispatch_invalid` terminal Run status used for malformed
post-intent requests. The committed Pending ModelInvocation, attempt identity
and lease remain intact: only restoration of the exact original trusted
binding (after its claim expires) or explicit quarantine may proceed.
This avoids a changed Host configuration both silently dispatching to the new
target and incorrectly terminalizing evidence needed for trusted recovery.

**Host Router/API break:** The standalone Router now requires a nonempty
`provider.Binding.Identity`; two-field bindings can no longer construct a
Router. OpenAI/Anthropic HMAC their actual Endpoint, authenticated identity,
wire protocol and selected configuration. A custom `HTTPClient` must supply
a Host-owned `Config.BindingScope`, rotated on any meaningful transport/policy
change. Arbitrary custom Clients cannot be introspected: the Host is responsible
for changing its explicit binding identity when the destination changes.

**Upgrade:** Existing pending ModelInvocations use the previous request-hash
format, without binding proof; they are intentionally **not accepted** for
redispatch. Stop old workers, inventory all pending Agent Runs, drain or
quarantine unknown remote outcomes, and do not fabricate fingerprints, reset
attempts or retry physical effects. Historical consumed receipts remain
readable. There is no SQL DDL, schema-number churn, old-hash fallback, or
dynamic Provider registry. An unnamed local Client has no external binding
attestation; production Provider-backed Clients must declare a name and proof.
Published beta.23 tags remain unchanged; beta.24 is still unreleased.

## 2026-10-30 — Exact approval checkpoint required (beta.24 candidate)

**Deleted:** A Turn-only Harness approval request is no longer authorized.
`harness.ResolveApprovalRequest.CheckpointID` must contain the exact pending
approval ID (1–128 non-whitespace-trimmed bytes). Missing/blank/oversized
values fail before any physical dispatch; a changed ID returns a conflict.
The HTTP v1 `ResolveHarnessApprovalRequest` now requires both `checkpointID`
and `decision`. The TypeScript call is
`harness.turns.resolveApproval(turnID, checkpointID, decision, comment?, options?)`.
There is no deprecated Go entry point or optional TypeScript argument.

**Persisted Agent state:** The old checkpoint-only approval inference in
`agent.approvalForPendingCall` is deleted. The exact
`pendingApproval` Tool-call/checkpoint/digest binding must already be
committed; a resolved older checkpoint cannot grant execution merely by
matching its Tool payload. Native Agent `ResolveApproval` retains the required
Kernel Revision CAS binding; delegated Subtask approvals already require an
explicit checkpoint ID and remain unchanged.

**Adoption:** Stop old workers and inventory all unfinished approval Runs,
Harness Turns and delegated Subtasks. Drain or explicitly quarantine older
pending approvals that lack `pendingApproval`; do not invent a binding, reuse
an unverified Tool decision, or automatically resume an uncertain Tool effect.
New clients must read the displayed current `checkpointID`, send it with the
approval and handle stale/replayed approval as Conflict. No SQL DDL, schema
version churn or compatibility bridge.

## 2026-10-30 — Explicit Harness Agent entry (beta.24 candidate)

**Removed:** `harness.Runner.Start(context.Context, StartRequest)` is no
longer a public default-Agent selector. The single supported explicit Agent
entry is `harness.Runner.StartAgentTurn(context.Context, StartRequest)`.
Typed Team/GroupChat/Workflow/Application entry points remain separate.
`SessionClient.Prompt` remains a deliberately Agent-specific convenience
handle, internally calling `StartAgentTurn`; no automatic intent classifier
or hidden capability router is installed in Kernel or Harness.

**Scope:** Callsites must choose their capability before committing a Turn.
Existing persisted `ExecutionClass`, `ModelInvocation`, cancellation intent,
feature grant and receipt fields are **not** changed or silently reclassified.
No compatibility alias or fallback exists. In-flight work follows the same
failure-closed recovery rules as beta.23.

## 2026-10-30 — Provider module extraction (beta.24 candidate)

**Removed:** `github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider`,
including `provider/openai`, `provider/anthropic` and the private HTTP
support package, is no longer part of Core. The source and public Router API
now live under `github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider`;
the example also moves to `go/agent-runtime-provider/examples/providers`.
The existing Harness task-agent example is isolated as a non-published nested
module, so the public Harness module no longer requires the Provider dependency.
There is no deprecated Go import, alias, re-export or fallback path.

**Host action:** Replace imports with the standalone module path and add the
Provider module dependency explicitly. Keep constructor-owned credentials and
Host egress policy. Core and Kernel remain vendor-neutral. The serialized
`model.Request`, `ModelInvocation`, and `ProviderReplay` format do not
change in this extraction. A Run committed to an unavailable Provider or a
changed route must continue to fail closed; do not silently select another
Provider or retry an uncertain Tool/Model effect.

**Release:** Existing beta.23 tags are immutable, so the new eleven-module
source cohort and TypeScript client target beta.24. This is a prerelease
candidate, **not** proof that the new module/version is remotely installable.
Before deployment, stop and inventory in-flight Runs, drain/quarantine
unreplayable ones, and require clean external consumer, recovery and full CI
evidence. No new SQL DDL, Kernel state or migration bridge is introduced.

## 2026-10-29 — PR #72 second independent admission correction: Context and native Start preflight

**Context recovery:** Once the frozen Turn and its root Invocation are committed, a failed Context rebuild/read **before any physical dispatch grant** is a request-local error, not authority to mark that shared Turn `failed`. The same applies to the first Start and to replayed direct Agent, Team, Workflow, GroupChat and Application roots. Other workers may be preparing the *same immutable admission* concurrently; a transient Context Store failure must not destroy their execution. The persisted Turn remains `accepted` (or at its independently committed status), so an authorized caller may retry with the **identical frozen request**. A changed ContextSeed/admission still conflicts; genuinely unknown physical effects remain fenced. Operators may explicitly inspect/cancel an admission that is permanently misconfigured, rather than relying on a failed read to fabricate a terminal execution. No new context state, lease or compatibility path is added.

**Native Agent zero-effect proof:** The first-party Agent now prepares/validates its Start state *before entering any Run Middleware or Kernel Create*. This is a breaking ordering clarification: OutputContract capability inspection, selected Tool discovery/validation and allowance/limit checks occur before Run Middleware. Capability and catalog lookup implementations at that boundary **must be read-only**; they are not a business-effect dispatch. A direct failure at this boundary returns a private `agent.IsNativeStartNoEffect(err)` proof while preserving the original cause under `errors.Is/As`. Only the exact `*agent.Runner`, for a Harness physical `start`/`start-retry` grant, may consume this direct proof and write a bounded durable grant-completion Item; then and only then may it project the native no-effect failure. A Run Middleware may not manufacture this proof: any private native error forwarded after Middleware is wrapped and rejected by the exact-type gate. Arbitrary Model/Tool effects, Kernel Create errors (including a committed Create with a lost ACK), a missing or late grant receipt, custom Features/Agents and generic `ErrInvalidRequest`/`ErrConflict` are **not** proof of no effect. Existing custom Feature/Agent zero-effect behavior still requires `harness.FeatureStartNoEffect(cause)` under its explicit host contract; this public marker is **not accepted** for a concrete native `*agent.Runner` Start, because its Run Middleware may already have performed effects. Native Agent Start recognizes only its direct private pre-middleware proof.

**Proof and rollout:** `TestPR72DirectStartContextReplayReadFailureCannotTerminalizeOriginal`, `TestPR72WorkflowStartContextReplayReadFailureCannotTerminalizeOriginal`, `TestPR72OriginalContextReadFailureLeavesRetryableAdmission`, `TestRealPostgresPR72ConcurrentStartContextFailureDoesNotTerminalizeOriginal` (two real SQL handles), `TestPR72NativeAgentPreCreateInputErrorRetiresGrant`, `TestPR72NativeAgentNoEffectReceiptLostACKDoesNotInferFailure`, and native Agent tests for direct error provenance, malicious Middleware forwarding, invalid-input Middleware bypass and post-Create ACK loss. Before deploying the next unreleased Beta, inspect incomplete Turns and unresolved physical grants; do not reset uncertainty or synthesize a completion receipt. These changes reuse existing immutable Items and CAS without SQL DDL, Schema version churn, old-protocol shims or exactly-once claims.

## 2026-10-29 — PR #72 physical Start/retry and public diagnostics hard cut

**Feature Start outcome protocol:** A Start/Start-replay or typed child/retry that returns no authoritative Kernel Run snapshot can no longer change an admitted Turn/Invocation to `failed` merely because its physical callback returned an error, including `kernel.ErrConflict`. The original immutable physical-dispatch grant and `ExecutionRefID` remain the authority for reconciliation; a concurrent identical request may return `ErrConflict` without terminalizing the original executor. For a callback that has already entered physical execution, an unknown result returns `harness.ErrExecutionOutcomeUnknown` (HTTP 409 `harness.execution_outcome_unknown`), without disclosing the upstream error. There is no generic automatic remote Start retry.

**Explicit no-effect exception (breaking for custom Features):** Hosts that can prove their Feature `StartRun` created **no** Kernel Run and dispatched **no** external effect must return `harness.FeatureStartNoEffect(cause)`. Only after the existing dispatch-grant completion receipt has committed may Harness convert that no-effect result to a failed, explicitly retryable Invocation. A generic Feature error or a lost completion receipt must remain unresolved; do not use the no-effect marker for timeouts or outcome-unknown remote callbacks. Failure projection now competes atomically with a later physical dispatch grant on the same Turn/Invocation, on both Memory and PostgreSQL Stores. A root `RetryInvocation` also checks unresolved descendant dispatches before changing its identity. An unverified pending grant blocks `RetryInvocation` identity rotation, even for preexisting malformed `failed` Invocations. Manual reconciliation must preserve the original Run identity until its physical outcome is authoritatively known.

**Completion deadline and public projection:** Physical dispatch-grant completion receipts use an independent, **5-second bounded** Context after the caller disconnects. A timed-out/lost acknowledgment is **not** proof that the completion Item failed to commit; continue from the persisted Item, never blindly redispatch. Diagnostic Timeline Item payloads retain the internal Kernel IDs for Store arbitration, but public full-turn, paged `/items`, and Turn Feed projections now recursively strip `runID`, `physicalRunID`, and `executionRefID` fields (case and separator insensitive). No new SQL tables, schema numbers, parallel scheduler, backwards-compatibility bridge, or changes to published Beta tags are introduced. In-flight work from the previous unmerged candidate must be audited before attempting reconciliation.

**Verification:** `TestPR72ConcurrentStartReplayDoesNotTerminalizeActiveDispatch`, `TestPR72AmbiguousChildStartKeepsOriginalIdentityAndGrant`, `TestPR72ProvenNoEffectFailureCannotTerminateNewDispatch`, `TestPR72DispatchCompletionUsesBoundedIndependentContext`, the Memory/SQL `TestPR72RetryCASPreservesPendingPhysicalExecutionInMemoryAndSQL`, real PostgreSQL cross-connection `TestRealPostgresPR72PendingGrantPreventsFailureAndRetryAcrossConnections`, and HTTP Timeline/Feed redaction tests. Full race and real PostgreSQL/Redis integration are required release gates.

## 2026-10-25 — Harness admission and Application capability recovery hard cut

**Breaking admission contract:** Each new Harness Turn now seals an `AdmissionHash` into its immutable `ConfigSnapshot` and therefore its existing `ConfigSnapshotID` / Turn identity. The hash covers the normalized host Turn identity, Actor/Thread, RequestID/Goal, required/initial Tool selection, ContextSeed including its source content, host input/output message binding, CallAllowance, AutoAllowance, **the selected root execution class/capability/version and its frozen typed Invocation input hash**. Team/Workflow/GroupChat/Application input and cross-capability replay are rejected before the Turn can accept a different root. A retry that changes any frozen field returns `harness.ErrConflict` **before** Agent/Capability dispatch. The actual execution request is cloned during freezing; mutation of caller-owned pointers cannot change it afterwards. The raw ContextSeed is **not** persisted in ConfigSnapshot: only its hash and the two host message references are retained for recovery.

**Application effect safety:** An Application Invocation must win its one-time `accepted` → `running` Store revision CAS before `ExecuteApplicationCapability`. All session reads required to dispatch now precede that CAS. A cancellation may win while an Invocation is still `accepted`, but `Cancel`/child cancellation on an already-`running` Application fails with `ErrApplicationOutcomeUnknown`; it must not claim the effect was stopped or cancel the shared ledger prematurely. An already-running Invocation is **not** retried by `StartApplicationTurn`, `StartApplicationInvocation`, `Refresh`, `ResumeInvocation`, or `RetryInvocation`. When the external outcome is uncertain (including a generic executor error or lost completion commit), the Invocation remains `running` and returns `harness.ErrApplicationOutcomeUnknown`. HTTP projects this as 409 `harness.application_outcome_unknown` without exposing the Host error. The optional `harness.ApplicationCapabilityReconciler` may **read-only** verify the frozen Invocation/ExecutionRefID and return a known result; false/failed reconciliation stays blocked. Only an error explicitly wrapping `harness.ErrApplicationNoEffect` certifies that the Host definitely dispatched no effect, permitting the existing explicit failed-Invocation retry protocol. Do not wrap that sentinel for a timeout or ambiguous result.

**Recovery and reads:** Harness `Refresh`/admitted `Start`/`ResumeTurn` now repair deterministic host-message, Invocation, Context, terminal Run and Turn Item projections from previously committed facts. Repair must never re-enter Model/Tool/Application execution just to recreate Items. `Refresh` on an `accepted` Application Invocation is projection-only; on `running` it may invoke the explicitly read-only reconciler but never the executor. A repaired terminal Run may republish a missing retained TurnFeed terminal event (replay deduplication is best effort, not exactly-once transport). `Runner.LoadState`/`SessionClient.LoadState` are bounded read-only Session/Turn/config views; `Runner.ListTimeline`/`SessionClient.Timeline` use cursor paging (1–500 Items). Existing complete `Load` and `Refresh` remain explicit full-history projections, not compatibility shims. HTTP adds `GET /harness/turns/{turnID}/state` and bounded `GET /harness/turns/{turnID}/items?afterSeq=&limit=` (1–500). The TypeScript client exposes `harness.turns.state` and `harness.turns.items`. The `state` endpoint exposes **committed Harness status**, not proof that a host-bound output was finally acknowledged; use the full `get` endpoint where that distinction matters. Nonterminal Harness feed subscriptions now authorize without scanning complete Timeline Items. `LoadState` reports persisted Harness status and does not silently synchronize an independently advanced Kernel Run.

**Interaction business callback and cancellation hard cut (2026-10-26):** A durably resolved Interaction grants its external `InteractionResponseHandler` callback via the existing Invocation revision CAS (`waiting_input` → `running`) after preflight reads, across Application, Agent, Team and Workflow owners. An unverified resolved callback claim is authoritative independently of the parent Turn status (`waiting_input` **or** `running`). `Refresh`, `ResumeTurn`, Feature replay, child synchronization and all three cancellation entrypoints (`Cancel`, `CancelInvocation`, `CancelSubtask`) must respect that claim before allowing another Runtime effect or changing shared budget. Cancellation of waiting Interaction owners competes for the *same* Invocation revision: if cancellation wins, the callback cannot dispatch; if the callback wins before its physical effect is verified, cancellation returns `ErrConflict` (or `ErrApplicationOutcomeUnknown` for the original in-flight Application executor). Once verified, cancellation may proceed through the normal Runtime CAS. The Host callback or positive read-only reconciler persists a deterministic `ItemDiagnostic` success receipt *before* continuing Runtime execution; it is not a separate execution state machine or a claim that the Kernel continuation finished. A callback with an uncertain external result retains its durable dispatch claim and blocks **every** additional dispatch, including concurrent identical response replay; the API returns 409 `harness.interaction_outcome_unknown` until host reconciliation. Only an error wrapping `ErrInteractionNoEffect` (host proves **no external effect** was dispatched) allows a CAS back to `waiting_input` and an explicit safe retry. Hosts must still enforce idempotency by Interaction ID for their own effect/reconciliation boundary. After an ambiguous callback or a crash between host success and Harness completion, the host explicitly calls `Runner.ReconcileInteraction(ctx, turnID, interactionID)` with the optional `InteractionResponseReconciler` port. The reconciler is **read-only** and may return `known=true` with verified OutputRefs only after externally confirming success; otherwise the Invocation remains `running` and recovery fails closed with `ErrInteractionOutcomeUnknown`. Only the subsequently authorized Runtime continuation may run; the physical business handler is never replayed. `CancelSubtask` now checks the same Invocation execution claim before mutating budget or cancelling its Run, including a fail-closed fallback for related Runs without a standalone Harness Invocation. The completed Interaction itself is not a physical exactly-once receipt. The process-local post-callback success witness permits that same call stack to finish Runtime projection without allowing unrelated Refresh or retry calls to infer success. This changes the observable transient Invocation status from `waiting_input` to `running` after response acceptance; clients must not interpret it as resumed model execution while Turn remains waiting.

**Runtime continuation single-dispatch correction (2026-10-26):** A verified Interaction business receipt does **not** itself authorize re-entering Agent/Team/Workflow. The first caller atomically inserts a second, distinct deterministic `ItemDiagnostic` continuation-claim fact using the existing Harness Store's unique Item identity. If that claim already exists, `ReconcileInteraction` fails closed with `ErrInteractionOutcomeUnknown` unless the authoritative Kernel Run is already terminal and can be read-only projected. Sequential/concurrent positive host reconciliations may no longer invoke the same Runtime continuation twice; a lost or ambiguous continuation outcome must be reconciled against the Kernel Run/Feature-owned execution facts, not retried merely because the host business callback succeeded. Ordinary replay of an older resolved Workflow Interaction remains observational when a newer wait is active; an accepted response lacking verified business evidence cannot be acknowledged as completed. This is an append-only protocol change using existing Timeline Items and Kernel execution protections, **not** a new SQL table or parallel scheduler. Do not automatically replay unfinished Turns created by the earlier unmerged candidate without inspecting their callback/continuation evidence.

**Recursive cancellation hard cut (2026-10-26):** Before cancelling an Invocation or Subtask, Harness traverses all known RunRelation descendants and their persisted Invocation identities, then follows Item-parent ancestry for Application children that have no RunRelation. It rejects cancellation before touching Runtime or budget if any affected response handler has an unverified `running` claim or any affected Application executor is still `running`. Top-level `Cancel` also refuses uncertain child Application physical effects. Where complete ownership cannot be proven, cancellation fails closed for outstanding unknown Interaction effects in the Turn. This read-only preflight uses existing Store CAS for waiting Invocation cancellation; it is not an atomic cross-store transaction spanning Kernel, Harness and external business systems.

**Cancellation admission and continuation completion hard cut (2026-10-28):** Root, Invocation and Subtask cancellation first performs **read-only** effect inspection, atomically persists a Turn-scoped cancellation-intent `ItemDiagnostic`, then repeats effect inspection and competes for existing waiting-Invocation CAS before touching physical Runtime/remote cleanup or budget. Until a corresponding completion Item commits, Memory and PostgreSQL/SQLite Harness Stores reject *new* Invocation creation, Interaction creation/resolution, dispatch-grant transitions to `running` and retry attempts; these operations serialize with cancellation-intent insertion on the same Turn lock. This is deliberately a Turn-wide admission hold during the cancellation window, even for an otherwise unrelated new sibling; existing independent sibling cleanup is still permitted when effect ownership is proven. A definitive pre-effect rejection closes a fresh intent. A failed/unknown physical cancellation retains it fail-closed. A retry may adopt only the same scope with a **confirmed cancelled Kernel root Run** to finish descendant cleanup. A crash before that confirmation requires operator reconciliation rather than automatic re-dispatch. All evidence uses existing immutable Timeline Items; there is no new SQL DDL, execution state machine or compatibility bridge.

The continuation-claim Item is now separate from a persisted completion Item, written only after the single elected Agent/Team/Workflow continuation returned successfully. `ResumeTurn`, `ResumeInvocation`, typed Feature replay and the central resumer check the same outstanding continuation grant: only the original confirmed callback stack may enter its owned dispatch; other callers get `ErrInteractionOutcomeUnknown` rather than call Resume again. An old `ResolveInteraction` replay with a claim but no committed continuation completion, newer durable wait or terminal owner status no longer fabricates success from two `running` statuses. An interrupted claim with no authoritative progress is intentionally blocked for manual Kernel/Feature reconciliation; this changes replay behavior for incomplete pre-stable Turns and must not be bridged to the older permissive flow.

**Physical Start/Resume dispatch vs cancellation (2026-10-29):** Turn-wide cancellation admission alone is not enough: an already-`accepted` child may still be about to call `StartRun`, or a `running` Agent/Team/Workflow may invoke `Resume` with no Harness Invocation-status change. Harness now commits a physical dispatch grant as a unique `ItemDiagnostic` on the existing Turn Store boundary **before** a typed Agent, Team, GroupChat, Workflow Start/Resume, retry, or Workflow Wait-resume call. The same Store lock/transaction that creates cancellation intents rejects a fresh physical grant when cancellation is active, the Turn/Invocation is terminal, or the Invocation has an unfinished grant. Root and nested child Starts also verify the parent's persisted Item ancestry and Kernel Run authority *after* acquiring the grant; a child already accepted before its Team parent's cancellation may not physically create a new Run afterwards. Cancellation preflight inspects outstanding grants: an unresolved `Resume` or a `Start` without a persisted Kernel Run fails closed, while a `Start` whose Kernel Run already exists can be cancelled under Kernel's own CAS/epoch fencing. Grants remain open on ambiguous physical outcomes and require operator recovery. A returned authoritative Run snapshot or an explicit post-grant parent-admission rejection can close them; a generic Feature error (even one wrapping `kernel.ErrConflict` or `kernel.ErrNotFound`) **without a Kernel snapshot** is not no-effect evidence and must retain the grant. A crashed first-party Agent Start may leave an unfinished grant **after** its Kernel Run already acquired a sticky Execution Epoch. In that narrow case, and only for the concrete native Agent Runner on an execution-fenced Kernel Store, recovery delegates directly to the Kernel's authoritative `AcquireExecution` / lease / Epoch checks rather than waiting for a second Harness grant. While the old lease is live, Resume returns the native execution conflict without effects; after expiry, an Epoch takeover can continue the same frozen Run and then settle the orphaned Start grant. No custom or unleased Agent gets this recovery path. The Kernel CAS/epoch must arbitrate a concurrent cancellation. The grant/receipt Items are durable diagnostic Timeline facts, not extra Feature executions or a parallel scheduler.

**Observable breaking behavior:** A concurrent replay of an in-flight `StartRun` no longer interprets a still-running physical dispatch as permission for a second `Resume`, even if its Kernel Run already exists; it returns `ErrConflict` until the first physical call settles. Cancelling an `accepted` Invocation while its original `StartRun` has not yet created any Kernel Run likewise returns conflict instead of claiming an effect was stopped. Once a native Run exists, Kernel control can cancel it; a custom Feature must honor Kernel terminal/epoch fencing before any subsequent physical work. Custom Harness `Store.AppendItem` implementations MUST enforce these same Turn-serialized dispatch/cancellation rules (no compatibility bridge). A `RuntimeCanceller` may return `kernel.ErrConflict` only as proof that it performed **no remote cleanup at all**; remote-outcome-unknown errors must not be mislabeled as CAS conflict. An exhausted no-effect cancellation can retire its own intent and be retried only when authoritative Kernel state is still `running`, with no shared-budget mutation or Interaction side effect. Otherwise the intent stays fail-closed. These are breaking changes to the unpublished execution contract, **without new SQL DDL, migrations, or version-number churn**.

**Approval and allowance control arbitration (2026-10-29):** A pending Harness cancellation intent now also fences `ResolveApproval`, `ResolveSubtaskApproval` and `GrantCallAllowance`. These operations must acquire the same persisted physical-dispatch grant as Agent/Team/Workflow Start/Resume **before** they may commit an approval resume or Kernel budget-allowance wakeup. Grant creation serializes with cancellation intent in the existing Turn Store; an approval or allowance request denied by this guard must not issue a Model/Tool call or mutate a paused Kernel Run into `running`. For delegated Subtask Agents without their own Harness Invocation, the grant belongs to the existing root Invocation and its payload includes the exact physical child Run ID, so cancelling that Subtask also checks the outstanding child-specific grant. A missing/ambiguous physical callback result remains fail-closed; a verified matching Kernel snapshot closes its grant. No legacy control bypass, new SQL table or schema version is retained.

**Go SDK allowance validation correction (2026-10-29):** `Runner.GrantCallAllowance` now rejects negative Model/Tool increments and the zero/zero increment with `ErrInvalidRequest` **before** committing a physical-dispatch grant. This matches the native Agent and HTTP syntax guard. A malformed Go SDK request no longer leaves an unresolved dispatch Item that blocks a later valid allowance or operator cancellation. A genuine ambiguous physical callback remains fail-closed; no previous incomplete grant is automatically cleared and no persisted schema/version changes.

**Native Agent control pre-dispatch recovery (2026-10-29):** The first-party `agent.Runner` now marks failures before its `ResolveApproval` Run-middleware boundary or `GrantCallAllowance` Kernel `ApplyControl` boundary with a private native control proof that matches `agent.ErrControlNoEffect` via `errors.Is` while retaining the underlying cause. Read-only Kernel/Agent State-load errors, stale revisions, and definitive preflight rejections have **not** executed the current control attempt. Only the Harness physical-dispatch wrapper for those three native Agent control phases (`ResolveApproval`, delegated `ResolveSubtaskApproval`, and `GrantCallAllowance`) may consume this proof, and **only** when its Agent is the concrete first-party `*agent.Runner`; it commits a deterministic completion Item for the same dispatch grant. Harness requires the **direct private proof type** (checked through `agent.IsNativeControlNoEffect`), not merely a wrapped or returned public sentinel. A custom Agent adapter cannot forge that type, including by wrapping `*agent.Runner`; Host Run middleware returning `ErrControlNoEffect` cannot grant itself authority either. Failures after entering Run middleware/approval resolution or `ApplyControl`, including an uncertain CAS reply after commit, are not certified and remain fail-closed. A failed Harness completion-receipt write also leaves the grant unresolved. A crash after a successful no-effect receipt is committed can safely resume from a new PostgreSQL connection/process; a crash before that receipt commits remains unknown and must be reconciled, never automatically retried. No change to Harness/Kernel SQL schema, no independent execution state, no generic error-based release.

**Kernel control pre-Store no-effect proof (2026-10-29):** A valid native `GrantCallAllowance` could acquire a durable Harness dispatch grant and then fail when `Kernel.ApplyControl` reloaded the Run *before* entering `Store.ApplyControl`. The Kernel was unchanged, but the missing proof left both retry and operator cancellation blocked. `Kernel.ApplyControl` now returns a private direct no-effect error for failures strictly before entering the write-capable Store method: the initial read, revision check, pure mutation/control validation, and unsupported-store guard. `kernel.IsControlNoEffect(err)` recognizes only that direct Kernel-issued proof, preserving the underlying error for `errors.Is`/diagnostics. Native Agent allowance control promotes the proof to its existing private `agent.ErrControlNoEffect` result, allowing Harness to commit the existing dispatch-completion Item. Every error returned by `Store.ApplyControl` or fallback `Store.Apply` is instead wrapped as outcome-unknown (with its original `errors.Is` cause preserved), even if the Store forwards another Kernel operation's no-effect proof. A Store-side CAS conflict, committed-but-lost reply, failed Harness completion receipt, or crash before the completion receipt remains fail-closed. No new persistence object, SQL schema, fallback, or general effect reconciliation mechanism was introduced.

**No-effect child cancellation retries (2026-10-29):** `CancelInvocation` and `CancelSubtask` now apply the same carefully bounded no-effect cancellation release as root `Cancel`: an exhausted sequence of `kernel.ErrConflict` from a `RuntimeCanceller` may retire its own intent only if the target Kernel Run remains `running`, the Turn has no shared-budget ledger or Interaction mutation, and the error came from the **target** cancellation before any descendant cleanup. A failed Subtask attempt retains its historical requested/failed Timeline facts but may safely retry physical cancellation after the intent is closed. A descendant cleanup conflict after other effects, a remote-effect-unknown result or a changed budget remains blocked; a custom `RuntimeCanceller` must reserve `kernel.ErrConflict` for effects it can prove did not occur.

**Native cancellation no-effect recovery (2026-10-29):** The root `Cancel`, child `CancelInvocation` and `CancelSubtask` now recognize an exact `kernel.IsControlNoEffect` result **only** from a concrete native `*kernel.Runtime` canceller. The same existing cancellation-intent Item is retired after authoritative verification that the target Run still has a recognized nonterminal status (`running`, `paused_budget`, or `waiting_input`), no shared budget ledger or Interaction mutation exists, and the failure was in the target cancellation rather than descendant cleanup. A native pre-Store revision conflict may retry within the existing bounded loop and keeps its direct proof if all retries exhaust. Conversely a native `Store.ApplyControl` error (including a wrapped `kernel.ErrConflict`, even after a committed-but-unacknowledged CAS) is **never** an automatic no-effect retry or cancellation-intent release. Custom `RuntimeCanceller` implementations retain their preexisting `kernel.ErrConflict` no-external-cleanup contract and `running`-only recovery scope. A failed intent completion write, partial business callback, budget revocation, descendant cleanup or unknown remote result stays fenced. No new SQL schema, version, migration, scheduler or compatibility bridge.

**Native Cancel entry preflight no-effect correction (2026-10-29):** Cancellation can fail *before* `Kernel.ApplyControl`: the Harness `cancelRuntimeRun` reads the target Run, then `Kernel.Cancel` reads and checks its revision. Previously a transient error at either read, or a stale revision rejected at this pre-control stage, left an otherwise unchanged `running`/`paused_budget`/`waiting_input` Run behind a permanently unresolved cancellation intent. Now the concrete native `*kernel.Runtime` canceller returns its existing **direct private** Kernel pre-Store no-effect proof for its own read/revision rejection. Harness also mints an **unexported direct** `cancellationReadNoEffect` proof strictly for its first read failure before invoking the native Canceller. These two proofs may retire the same immutable cancellation intent only after the existing budget-ledger and Interaction no-mutation checks, authoritative nonterminal Run validation, and successful durable completion Item. A genuinely concurrent Kernel revision advance causes a bounded safe retry without reissuing a physical Store CAS. The first Harness read proof is not a Kernel proof; neither proof is issued for a Store CAS response or wrapped remote side effect. Custom cancellers keep their previous no-external-cleanup `kernel.ErrConflict` contract and running-only release rule. A proven root/child/subtask cancellation preflight error can be retried using the existing protocol; an uncertain Store error, partially revoked budget/Interaction, failed completion Item, or descendant cleanup error remains fail-closed. No new DB schema, scheduler, compatibility bridge or version churn.

**Cancellation request-context recovery (2026-10-29):** A caller can disconnect or time out after Harness persists a cancellation intent but before native `Kernel.Cancel` enters a write-capable Store CAS. Although the native Kernel/Harness then provides a direct no-effect proof, using the same cancelled request Context for budget-ledger, Host Interaction, and authoritative Kernel Run reads left the persisted cancellation intent permanently unresolved. `releaseNoEffectCancellation` now uses `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` only **after** the existing exact no-effect proof gate. The single bounded recovery Context covers **all** budget, Interaction, Kernel status and durable intent-completion checks. A cancelled request can therefore return its original `context.Canceled` error after safely closing a proven no-effect intent; a later authorized request may retry without reentering any unknown effect. A recovery timeout (including an expired deadline after a Store returns a late snapshot), rejected/failed verification, or failed receipt append retains the intent. A Store CAS response is still outcome-unknown, even if the client disconnects after a committed CAS; custom RuntimeCanceller proof, partial budget/Interaction cleanup and descendant uncertainty remain unchanged. No SQL schema or state version changes, recovery daemon, or compatibility path were added.

**Custom Canceller pre-dispatch read recovery (2026-10-29):** The Harness's own `cancelRuntimeRun` Kernel `Load` occurs **before any** `RuntimeCanceller.Cancel` callback, regardless of its concrete implementation. Previously that read failure produced the private `cancellationReadNoEffect` proof only when `Cancellation` was exactly `*kernel.Runtime`, and the common intent-release gate likewise excluded custom cancellers, leaving a permanent cancellation hold after a request disconnect despite zero custom calls or control CAS. The Harness now mints its **direct private** pre-dispatch proof for every Canceller, and the shared release gate recognizes this proof independently of the callback type. It still requires bounded, independent verification of the Run's nonterminal status, absence of shared-budget/Interaction mutations, and a durable intent-completion receipt. A custom Canceller's **returned** errors remain governed by its old contract: only `kernel.ErrConflict` certifies no remote cleanup, and safe release after its callback is limited to `running`. Arbitrary `context.Canceled`, wrapped Kernel proof, lost Store ACK, partial Host mutation, or remote-outcome-unknown after the callback cannot release the intent. No SQL schema, version change, bridge or new state machine.

**Cancellation post-intent Host CAS boundary (2026-10-29):** The post-intent `cancelPreflightWithIntent` claim performs a second read-only inspection and may also cancel waiting Host Invocations via `Store.UpdateInvocation` CAS. Previously any claim error on a newly committed cancellation intent automatically wrote a completion Item, even if a Host Invocation cancellation CAS had already committed and only its reply was lost. This permitted other admissions despite a partially cancelled Host Interaction and a still-running Kernel Run. The claim now explicitly marks its **first Host write-capable Store attempt before calling `UpdateInvocation`**, including root, child Invocation, and Subtask cancellation. A failed claim can retire its own fresh intent only when no Host CAS has been attempted. Once a write-capable Host CAS was entered, even `ErrConflict` or an unknown/lost reply retains the cancellation fence for authoritative reconciliation. Post-intent *read-only* failures before any attempted Host CAS still retire the fresh intent, preserving safe retry. Physical Kernel cancellation, shared budget changes and partial descendant cleanup continue to fail closed. No schema/DDL change, compatibility bridge, additional state machine or version churn.

**Bounded cancellation intent settlement (2026-10-29):** Cancellation intent completion was previously appended on an unbounded `context.WithoutCancel(ctx)` in the read-only post-intent claim rejection and in the successful `Cancel`, `CancelInvocation` and `CancelSubtask` deferred completion paths. `CancelSubtask` also recorded a failed Subtask fact using an unbounded detached Context. A client disconnect or SQL Turn-row lock could therefore hold these writes indefinitely, and Host Stores requiring deadline-bound writes could strand cancellation recovery even when no physical effect occurred. The existing **5-second** recovery bound now applies to all such completion and failure-fact writes through one private `cancellationSettlementContext`. The write is detached from the caller cancellation but has its own deadline; the caller receives an error if the Store fails or the deadline expires, even if an adapter returns a late success. A timed-out or lost completion acknowledgement does **not** certify whether its immutable receipt committed; only the durable Item itself resolves admission. A physical cancellation, Host CAS, shared-budget or descendant outcome that was unknown remains fail-closed, and no automatic redispatch, schema migration or compatibility bridge was added.

**Host output and terminal Feed recovery (2026-10-26):** Application completion and cancellation now follow the same `FinalizeHostOutput` protocol as Agent: no terminal Feed event and no terminal assistant-message Item until the host-bound assistant output has been durably acknowledged. Repeated `FinalizeHostOutput` repairs a lost notification after the output Item has already committed. Terminal notification deduplication checks **all retained Feed pages**, serialized across Runners sharing a `TurnFeed`; it is still best-effort across different processes. When the replay cursor has expired, publication locates the earliest readable suffix and scans retained events (if any) before emitting the missing terminal notification. A completely expired prefix cannot prove that no earlier terminal event ever existed, so delivery remains at-least-once, not exactly-once. Clients must tolerate replay or restore from an authoritative terminal Snapshot.

**Persisted records / rollout:** The new fields live in the existing JSON ConfigSnapshot payload; there is no SQL DDL, new schema number, synthetic state machine or backwards-compatible admission bridge. Existing incomplete Turns without `AdmissionHash`, including old child/interaction/delegation paths, cannot prove the new frozen start identity and must not be redispatched automatically. In-flight Turns created by the previous unmerged candidate also lack the newly bound root-typed intent and must be drained or isolated before changing the candidate. Before rolling out a new prerelease, pause admissions, stop old Harness/continuation workers, inventory **all** nonterminal Turns and child Invocations, drain or explicitly isolate old work, and separately reconcile Application Invocations in `running` with an unverified host effect. Never manufacture a new hash, reset Invocation status or rerun an uncertain effect. Completed historical Turns can still be read but a new `Start` of an old Turn cannot be used to reinterpret its admission. Consumer, recovery, SQLite/PostgreSQL and effect-boundary tests remain release gates; published tags must not be replaced.

**Engineering evidence:** [Agent/Harness/Kernel four-stage correction audit](audits/2026-10-25-harness-hardcut-recovery.md).

## 2026-10-24 — OpenAI Responses assistant phases

**Breaking behavior:** Phased assistant responses now use their single `final_answer` for terminal normalized content and retain original `commentary`/`final_answer` message items for stateless replay. Unknown phases, mixed phased/unphased messages, duplicate final answers, commentary-only terminal output, and final-answer-plus-local-Tool output fail explicitly. There is no fallback to the former concatenation or to JSON extraction.

**Host upgrade:** Drain affected in-flight work before upgrading. If a persisted replay envelope's phases no longer project to its committed normalized content, reconcile or quarantine that Run through the existing host process; do not rewrite the frozen request, rehash old evidence, or automatically repeat committed Tools. Earlier messages whose phases were discarded cannot be reconstructed from normalized text alone.

**API and persistence:** No public Go symbols, HTTP fields, SQL migration, or schema-version change. Phased completed text turns can now carry the existing optional `ProviderReplay` envelope. Native streaming deltas remain progress; terminal `Response.Content` is the final-answer contract. Historical benchmark reports remain unchanged and readable.

**Release admission:** Stop old workers and inventory *all* nonterminal Agent Runs in the actual PostgreSQL/SQLite Kernel Store before cutover. Drain or explicitly quarantine affected work and reconcile old Host transcripts separately; lost phases cannot be inferred from normalized text. Read-only queries and the required operator sequence appear in the [phase upgrade note](releases/unreleased-openai-response-phases.md). Do not automatically migrate, rehash or replay effects. Streaming encrypted reasoning is retained from an exact-matching completed `response.output_item.done` item when the terminal response omits it; no partial or invented ciphertext is allowed.

**Reference:** [OpenAI response phase upgrade note](releases/unreleased-openai-response-phases.md). Publish only in a new immutable prerelease.

## 2026-10-24 — MCP/A2A redirects fail closed

**Breaking behavior:** MCP and A2A clients reject all HTTP redirects, including same-origin redirects, regardless of whether the host HTTP client's `CheckRedirect` allows them. The adapters previously reattached endpoint-scoped authentication after a redirect without validating the new endpoint. A host rejection still takes precedence; the supplied HTTP client is copied and remains unchanged.

**Host upgrade:** Configure the final MCP endpoint and A2A discovery/interface URLs directly. Trailing-slash normalization, HTTP-to-HTTPS redirects, and cross-origin gateways must be resolved in trusted host configuration before making a protocol call. There is no opt-out or compatibility redirect path. Hosts continue to own DNS/IP policy, TLS, proxy and egress controls.

**API and persistence:** No exported symbols, Kernel states, database schemas, or persisted records change. Existing in-flight work can fail if its frozen endpoint depends on a redirect; reconcile or restart it with an explicitly authorized final binding instead of silently retargeting that work.

**Reference:** [Protocol redirect safety upgrade note](releases/unreleased-protocol-redirect-safety.md). Publish only in a new immutable prerelease.

## 2026-10-23 — ModelInvocation retry/claim boundary (ADR 0011)

**Breaking:** A directly resumed Agent Run no longer bypasses the Model retry due time when `RunExecution` is unset. Every retryable Model failure commits a Feature-owned `RetryAfter`; any premature `Resume` returns `ErrModelInvocationBusy` without reissuing a Provider call. The native Store-fenced and direct Agent paths now have the **same** retry admission contract. There is no immediate-retry compatibility branch.

**Removed API:** `harness.BudgetMiddleware.ModelWithPrepare` has been deleted; it was a second post-intent model input preparation route. Compose all message/Tool schema projections in `agent.Dependencies.PrepareModelRequest`, then run the standard shared-budget `ModelMiddleware`. The old method has no alias or bridge. Pre-intent failures do not create a Shared Budget reservation.

**Persisted state:** Released Pending `ModelInvocation` records with `ExecutionAttempt > 0`, no active claim and no persisted `RetryAfter` are **invalid**. Earlier Beta Runs in that condition must be drained or explicitly reconciled before upgrading; no silent ReadyAt reconstruction, retry replay or state migration is provided. First-attempt, unclaimed pending invocations remain valid.

**Execution safety:** The final `model.Client` boundary reloads the committed claim **after ModelMiddleware and concurrency waiting**, fencing stale Revision/attempt/lease combinations before network dispatch. A stolen or expired Claim leaves a recoverable Pending Run; a permanently invalid post-intent Middleware request instead fails the Run with `agent.model_dispatch_invalid` rather than retrying indefinitely. This costs one extra Store read per physical Model delivery but adds no Kernel state or database schema. It reduces stale-worker duplicate dispatch; it does **not** prove physical exactly-once after upstream delivery.

**Reference:** [ADR 0011 — ModelInvocation Physical Dispatch Claim and Retry Eligibility](adr/0011-model-invocation-lease-and-retry-governance.md).

## 2026-10-23 — Durable Model dispatch / pre-intent preparation

**Breaking:** Agent no longer allows post-intent Model Middleware to rewrite model-visible messages or Tool schemas, including text-only Runs. A single pre-intent Model Request Preparer freezes the Provider/model identity, transcript, output contract, Tool schema and authorized Catalog evidence into `ModelInvocation`. Budget middleware may only apply bounded dispatch restrictions.

**Deleted entrypoints:** `harness.NewContextWindowMiddleware`, `harness.NewRoleModelMiddleware`, `harness.NewCapabilityInvocationModelMiddleware`. No aliases or compatibility middleware are retained. Use `PrepareModelRequest = harness.MaterializeContextWindowRequest` or `harness.NewManagedContextPreparer`, `harness.NewRoleModelRequestPreparer`, and `harness.NewCapabilityInvocationRequestPreparer`. Combine the required stages with `agent.ChainModelRequestPreparers`.

**Persisted state:** `ModelInvocation.RequestHash` now includes ordered original Catalog fingerprints as well as the model-visible Request. Old in-flight Model Invocations are **not supported** by the new runtime and must be drained, cancelled/quarantined or reconciled before deployment; they are not automatically migrated, rehashed or replayed. Kernel and SQL storage schema versions are unchanged.

**Removed behavior:** Role post-intent child instruction fallback, Context double-injection compatibility path, and post-intent shared-budget system guidance. Host middleware that changes the frozen Request now fails before Provider dispatch.

**Reference:** [ADR 0010 — Pre-stable Model Invocation and Request Preparation Contract](adr/0010-prestable-model-dispatch-contract.md). Run release/consumer gates before publishing the next Beta tag.
