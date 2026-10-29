# 2026-10-25 — Agent / Harness / Kernel four-stage recovery correction

- **Baseline:** `main` at `6c88517d8a9b8b7a9616aade6a2c523bcec5932e`
- **Scope:** Harness root admission, direct Agent, typed top-level Features, Application capabilities, terminal projection, and status/Timeline reads
- **Policy:** pre-stable hard cut, no historical compatibility replay or replacement Kernel state machine
- **Execution:** local `governance/harness-recovery-hardcut` branch; no credentialed provider traffic

## Boundary assessment

Kernel remains authoritative for feature-neutral Run CAS, lease/epoch, journal, and transition outbox. Agent owns durable Model/Tool intent and receipts. Harness owns Session/Turn/Invocation/Item projection and explicit application-capability dispatch. A Harness Turn/Invocation lifecycle status must never by itself authorize a second physical Model, Tool, or host effect. Projection repair must be idempotent and derived from already committed evidence.

## Confirmed faults and corrections

| Finding | Reproduction | Corrective invariant |
| --- | --- | --- |
| Missing user/assistant-start Items after admitted Turn and failed Item append | `TestAuditStartReplayRepairsHostMessageItems` | Replay reads frozen HostRefs from ConfigSnapshot and restores stable Item IDs without relying on a mutable Host transcript |
| Changed CallAllowance accepted after Invocation admission, before Agent Run creation | `TestAuditRootReplayRefusesChangedExecutionAllowance` | Turn's frozen Config ID covers the complete admission digest |
| Committed terminal Turn skipped a missing terminal Agent Run Item on Refresh | `TestAuditRefreshRepairsTerminalProjectionCrash` | Terminal status cannot short-circuit harmless Item reconciliation |
| Application Invocation terminal while Turn remained running after projection failure | `TestAuditApplicationTerminalInvocationReconcilesTurnOnRefresh` | Completed Application Invocation is the terminal status authority for a root Application Turn |
| Application executor repeated after external result was lost before Invocation completion CAS | `TestAuditApplicationEffectDoesNotRedispatchUnknownOutcome` | CAS `accepted` → `running` is the only dispatch grant; ambiguous outcome stays blocked until read-only reconciliation |
| Changed ContextSeed accepted after Turn/Invocation admission, before Context checkpoint | `TestAuditAcceptedTurnRejectsChangedContextSeed` | Context identity/content is part of the immutable admission digest, never reconstructed from mutable caller input |

The original fault-injection cases were run with a Go test overlay outside the repository. They are now retained as regular tests with post-correction assertions. An independent follow-up audit on local commit `7189c1b` exposed seven additional failures. Their reproductions are retained in `independent_recovery_regression_test.go` and are required to pass, not merely acknowledged as theoretical risks:

- `Refresh` dispatched a never-started Application effect;
- a legacy Turn without admission proof started a new Team child or handled a resolved business Interaction;
- cancellation could commit while an Application was pre-dispatch and the physical Executor still ran afterward;
- a crash before root Invocation creation permitted altered Team Members/Goal or a switched Team → Application root;
- terminal Turn/Item repair did not restore a missing TurnFeed completion event.

The corrective boundaries are: only explicit Application start/retry may claim `accepted` work, the executing CAS is after all preflight reads, `running` Application cancellation refuses an unproven physical cancellation, the frozen root capability and typed input hash are committed with Turn admission, old Turns are gated at child/delegation/interaction/control dispatch entrypoints, and terminal feed recovery checks retained events for an existing matching terminal signal.

## Second independent merge-audit correction (local `2938dec`)

The next independent audit found and deterministically reproduced four additional failures:

1. Application completion published `turn.completed` before the Host assistant-output acknowledgement;
2. Application cancellation published `turn.cancelled` before that acknowledgement;
3. `ensureTerminalTurnFeed` examined only the first replay page; a 140-event Feed accumulated four `turn.completed` events after repeated `Refresh`;
4. An Interaction response handler ran its external business callback after a successful Application cancellation.

These are retained as `TestReaudit*` in `reaudit_recovery_test.go`; success asserts absence of premature terminal events and safe cancellation arbitration. Repair uses the existing Host Output Item/ACK protocol, a complete cursor-paged retained Feed scan guarded by a shared local mutex, and the existing Invocation revision CAS to claim response handler dispatch.

The same cancellation/handler race was additionally reproduced for a direct Agent root. `TestGenericInteractionCancellationDoesNotFollowCommittedResponse` and `TestInteractionDispatchClaimSurvivesConcurrentRefresh` lock down its resolution: a waiting Interaction's cancellation must win the same Invocation CAS or fail, and an outstanding resolved handler claim must not be overwritten by a concurrent Kernel Refresh or continuation. A follow-up adversarial test also reproduced two concurrent physical business callbacks for the same resolved Interaction when the first caller held a `running` dispatch claim. The fix disallows all duplicate callbacks while the claim remains outstanding: an uncertain Host error blocks with `ErrInteractionOutcomeUnknown`, whereas only a host-certified `ErrInteractionNoEffect` releases it through CAS. The successful callback's in-process execution witness permits its own Runtime projection to finish without confusing another consumer's read-only `Refresh` for a receipt. Host business reconciliation still keys on `Interaction.ID`; the SDK cannot guarantee exactly-once physical execution across arbitrary failures.

Additional tests cover concurrent `Refresh` terminal deduplication, lost terminal Feed publication after committed Host ACK, and Memory/GORM SQL Invocation CAS exclusivity (both winning orderings). No new Kernel state, job queue, parallel effect ledger or SQL schema version was added.

## Third independent merge-audit correction (local `5bfb4d3`)

The third review confirmed three new, independently fault-injected gaps:

1. After `HandleInteractionResponse` performed one external business effect, a failed Invocation completion CAS left `Turn=waiting_input` / `Invocation=running`. The safe no-redispatch policy also offered no public path for the host to assert its independently verified business result.
2. A Turn running longer than semantic Feed retention could reach a durable terminal status after the prior progress events expired, but `publishTerminalOnce` silently abandoned terminal publication on `CursorExpiredError`.
3. `CancelSubtask` bypassed the existing Interaction response callback claim for a child Team Invocation and succeeded while the physical business callback was running.

The executable reproductions were moved into `independent_post_claim_recovery_test.go`: they now require successful read-only `Runner.ReconcileInteraction` after Host evidence, a terminal event after prefix expiry without subsequent duplicate notifications, and refusal to cancel a Subtask once its business handler holds the dispatch grant. The public optional `InteractionResponseReconciler` consumes exactly the persisted Interaction ID and response; `known=false` retains `ErrInteractionOutcomeUnknown`. The verified result reuses the existing Invocation/Turn projection rather than creating another persisted execution object. `CancelSubtask` now competes through the same Invocation status CAS before making cancellation/budget side effects. The Feed recovers its earliest readable cursor through a logarithmic search and checks retained pages before publication. The expired prefix is not proof of physical exactly-once delivery: remote consumers must still deduplicate.

The real PostgreSQL/Redis multi-process failover gate remains unverified while Docker is unavailable. A passing SQLite-backed GORM or memory test is not equivalent to a real shared persistent Store restart.

## Fourth independent merge-audit correction (local `5130842`)

A fourth review deterministically reproduced four variants of two new P1 issues: a child Team Interaction's business callback remained physically in-flight while the parent Turn was `running`, yet `CancelSubtask`, `CancelInvocation`, and root `Cancel` each reported success and cancelled the Kernel Run; independently, read-only `ReconcileInteraction` returned a successful snapshot for a `running` Agent Turn while never invoking its required Runtime continuation. A related fault-injection case also showed `Refresh` could prematurely terminalize the parent while the child's business callback remained unverified.

The correction removes Turn-status shortcuts from the Invocation-based cancellation and execution gates and preserves the replay-versus-verified-continuation distinction. A deterministic, append-only `ItemDiagnostic` fact is recorded after the Host callback returns successfully or a positive read-only reconciliation verifies success, **before** the Runtime continuation. The fact proves business callback completion, not Runtime continuation completion. Cancellation on any Turn status refuses a resolved `running` Invocation that has no such verified fact; once verified, legitimate cancellation is no longer permanently blocked. `ReconcileInteraction` carries a private in-process verified witness so it cannot silently skip the unfinished Agent/Team continuation solely because Turn/Invocation both read `running`. Read-only projection and replay gates inspect unverified child callback grants independently of the parent Turn status. The cases are retained in `independent_turn_running_audit_test.go`, including the post-verification successful cancellation and the premature terminal-Refresh guard.

This uses existing immutable Timeline Items, Store revision CAS and Kernel continuation contracts. There is no new SQL DDL, second dispatcher, old-protocol bridge, or promise of exactly-once physical side effects.

## Fifth independent merge-audit correction (local `c21bbf4`)

The next independent fault-injection audit demonstrated that both sequential and concurrent `ReconcileInteraction` calls could enter `Agent.Resume` twice after one verified physical Host business callback. The callback-success Item had no one-time claim for the subsequent Runtime continuation. A second P1 demonstrated that `CancelSubtask` and `CancelInvocation` could cancel a descendant Team Run whose Interaction response handler was physically in-flight because only the immediate parent Invocation was checked.

The regression file `independent_receipt_continuation_audit_test.go` retains these exact repros and a Running Application child case without a RunRelation. The fix uses the **existing** Store's atomic, unique Item insert to elect one Runtime continuation caller per frozen Interaction ID, separate from the verified Host receipt. A committed continuation claim prevents re-dispatch under concurrent or ambiguous recovery even if the Host reconcilier repeatedly reports success; the owning Kernel CAS / execution Epoch remains authoritative for physical feature progress. Product replay of already-advanced Workflow waits remains read-only and cannot consume the next wait. A resolved `running` Invocation without a verified outcome is not accepted as replay success.

Recursive cancellation now discovers the known RunRelation subtree before physical cancellation/budget changes, extends ancestry by existing immutable Item parent references for Application children without RunRelation, checks all affected callback grants without mutation, and then uses existing Invocation CAS to fence waiting owners. Missing ownership data or an uncertain Application executor fails closed. Tests verify that an unrelated sibling can still be cancelled while another sibling's Host callback is in flight, when RunRelation proves the branches disjoint. Additional fault injection commits the continuation-claim Item but loses its write ACK: the next host reconciliation must not launch the Agent again.

Remaining proof limits: there is no exactly-once physical guarantee across external Host effects or across independently committed Harness/Kernel transactions. After a continuation claim commits but the Runtime execution outcome is unknown, the host must inspect Kernel/feature-owned facts before proceeding; repeating the Host read-only reconciliation must not launch the same continuation. Full live PostgreSQL/Redis process-failure recovery still requires infrastructure-dependent testing before a production release.

## Sixth independent merge-audit correction (local `b70ad35`)

The sixth audit reproduced two additional P1 boundaries and one P2: after a read-only subtree cancellation inspection, a late child could be created and physically dispatch its business callback before recursive cleanup, causing all three public cancellation routes to report success while cancelling an effect still running; `ResumeTurn` and `ResumeInvocation` bypassed the single-consumer continuation claim; and replay of the same resolved Interaction could return success when the continuation claim was committed but its Agent Resume had never started.

The correction anchors cancellation in the existing Store's append-only Item ledger. A Turn-wide cancellation-intent Item is serialized under the same Memory lock / GORM Turn-row transaction as new Invocation creation, Interaction acceptance/resolution and dispatch-grant updates. Read-only inspection happens before the intent; the post-intent CAS fencing and second inspection happen before physical cancellation and budget effects. Only a completed or definitely aborted intent releases new admissions; unknown external cleanup remains fail-closed, with an existing cancelled parent Kernel Run permitting a same-scope cleanup retry. The intent blocks new admissions across the Turn, not merely the inspected subtree, to avoid unseen late RunRelation edges; this conservative constraint is documented as an intentional breaking behavioral change. The normal Store conformance case without a Turn envelope has no possible cancellation intent and retains direct root-uniqueness insertion.

The continuation completion fact is distinct from the already committed claim and the Host success receipt. Root/child and typed Feature resume entrypoints now consult a common outstanding-claim guard; only the single elected callback stack may enter that continuation before the completion fact. Ordinary same-response replay requires a completed fact, newer durable wait or terminal owner state before confirming success. The exact adversarial cases live in `independent_cancellation_intent_audit_test.go` and `independent_continuation_gate_audit_test.go`. `cancellation_intent_test.go` exercises SQL transaction parity, and `TestRealPostgresCancellationFenceAcrossConnections` verifies admission rejection from a second real PostgreSQL connection.

Existing Kernel Store, Redis/PG integration and older advertised tests remain separate release gates. An interrupted cancellation intent or continuation claim with no provable physical outcome is not automatically replayable; operator reconciliation is required. No new persisted schema/table, compatibility shim, or physical exactly-once claim is introduced.

## Seventh independent merge-audit correction (local `59ff94e`)

The seventh audit reproduced **two P1 and one P2** despite all previously existing tests and live PostgreSQL/Redis checks passing. Once a cancellation-intent Item was durable but before actual Kernel cancellation, `ResumeTurn` and `ResumeInvocation` could physically enter Agent/Team resume without changing their Harness Invocation status. An already-accepted child could pass admission, then physically `StartRun` after its root was cancelled. The same flaw was found for a nested child whose parent Team was cancelled while the overall Turn remained active. A separate no-effect Kernel CAS conflict exhausted root cancellation retries and left an intent that prevented safe later cancellation.

The correction adds a **single physical dispatch grant boundary** for Harness-initiated Agent/Team/GroupChat/Workflow Start, Resume, retry and Workflow Wait resolution, using immutable `ItemDiagnostic` grants in the existing Timeline. The grant append and cancellation intent append are serialized on the same Turn lock in Memory and SQL adapters. A new grant checks terminal Turn/Invocation and existing grants; a first-party child also rechecks its persisted Item ancestry and parent Kernel Run status after receiving authority, closing its grant without executing on a cancelled/terminal parent. Any pending Resume grant or Start grant whose Kernel Run has not yet been created blocks cancellation. Once a Start has created a durable Kernel Run, Kernel cancellation CAS/epoch control can take over while its Feature executes. An ambiguous Start/Resume reply leaves the grant outstanding for explicit reconciliation; a returned authoritative Run snapshot or an explicit pre-dispatch parent rejection can close it. Even a custom Feature error wrapping `kernel.ErrConflict` without a Kernel snapshot is ambiguous and cannot discharge its grant; an added adversarial test `TestReauditAmbiguousFeatureConflictKeepsPhysicalDispatchFence` covers this case. The Kernel remains the execution-state authority; no parallel scheduler, Store schema, SQL DDL, or compatibility bridge was added.

**Intent recovery:** A returned `kernel.ErrConflict` from a custom `RuntimeCanceller` must *certify zero external cleanup*. After an exhausted string of such conflicts, root cancellation may close its own intent only when the Kernel root is still running and no shared budget or Interaction mutation was involved. Any uncertain external cleanup, or an earlier physical partial cancellation, remains fail-closed. This hard cut changes previously permitted concurrent Start/Resume replay while Start is still executing: a second Resume now returns conflict, rather than manufacturing progress. Cancellation of an accepted Start before its Kernel Run exists also returns conflict instead of pretending an in-flight Start was stopped.

Executable evidence is retained in `independent_physical_dispatch_audit_test.go`, `cancellation_intent_test.go` and `TestRealPostgresCancellationFenceAcrossConnections`. Diagnostic dispatch facts are included in the durable Timeline; tests verify the product lifecycle independently.

The first real PostgreSQL/Redis cross-process run exposed a recovery regression: the original Agent Start could crash after committing a Kernel Model intent and native execution lease, leaving the Harness Start grant open; blindly rejecting all subsequent Harness Resume attempts prevented Kernel Epoch takeover. The correction allows **only the concrete first-party Agent Runner on a Kernel Store with a previously acquired, sticky Execution Epoch** to recover under Kernel's native `AcquireExecution` lease authority. The original live lease rejects the second process before Model/Tool effects; after PostgreSQL Store-time expiry the new execution Epoch takes over the same persisted Model invocation, then records the original Start grant's completion. Custom runners, unleased Runs, pending cancellation intents, a concurrent Resume grant, or unknown grant phases cannot use the path. There is no second executor or generic bridge. The full live PostgreSQL/Redis cross-process suite then passed, including `TestRealPostgresManagedAgentSessionRecoveryAcrossProcessCrash` and the cross-connection Harness physical-grant test. This is strong native Store evidence, not a claim of exactly-once externally hosted side effects.

## Eighth independent merge-audit correction (local `4573c16`)

The eighth audit reproduced two first-party **P1** control-authority failures and a **P2** recovery omission: a committed cancellation intent did not prevent `ResolveApproval` from resuming a real Agent's next Model/Tool call, nor `GrantCallAllowance` from committing a `paused_budget` → `running` Kernel wakeup even when its later Harness projection returned conflict. Separately, the bounded no-effect cancellation-intent release existed only in the top-level Cancel entrypoint: `CancelInvocation` and `CancelSubtask` were unable to retry after eight proven no-effect Kernel CAS conflicts. An adversarial custom Team Feature was also able to issue a remote effect after creating and cancelling a Kernel Run; that case is explicitly a custom Feature contract concern, not proof of an internal Kernel CAS bypass.

The correction routes top-level approval, delegated Subtask approval and Agent call-allowance controls through the already existing immutable `ExecutionDispatchParentItemID` grant / completion protocol. `ResolveSubtaskApproval` now binds a child Agent Run to its root Harness Invocation when no child Harness identity exists; `physicalRunID` in the grant payload lets a child Subtask cancellation inspect a physically running delegated approval rather than relying on root Invocation ownership alone. The same Turn-scoped Store transaction/Memory mutex that arbitrates cancellation and physical Start/Resume also arbitrates these control grants, with no separate state machine or SQL DDL. If the grant fails, the controller cannot enter the Agent's Model/Tool loop or commit the Kernel allowance wakeup. All three Cancel entrypoints now use the same root proof rule for exhausted no-effect **target** Kernel CAS conflicts; a Subtask does not release its intent for a later descendant-cleanup conflict after earlier effects.

The original adversarial tests have been promoted and strengthened in `independent_control_arbitration_test.go`, including evidence that a settled cancellation intent permits a later legitimate control. `TestRoleDelegationFreezesExecutionAndProjectsChildApproval` tests the delegated Run without a standalone Harness Invocation and child-scoped cancellation. SQL adapter conformance explicitly checks `resolve-approval`, `resolve-subtask-approval` and `grant-call-allowance` grants against committed cancellation intents. Real PostgreSQL/Redis, race, and independent consumer/release checks remain separate mandatory gates. The custom Feature contract continues to require Kernel terminal/Epoch enforcement before external effects; an outcome-unknown callback is never treated as no-effect merely because it returned a generic conflict.

## Four stages

1. **External effect safety.** `ApplicationCapabilityExecutor` cannot be re-entered on `running` Invocation. The durable CAS is recorded before execution; ambiguous error or commit loss returns `ErrApplicationOutcomeUnknown`. Optional `ApplicationCapabilityReconciler` observes the same frozen Invocation and does not dispatch side effects. Explicit `ErrApplicationNoEffect` is allowed only when the host proves no effect occurred.
2. **Frozen admission.** Compute the canonical complete StartRequest plus root Feature class/capability/version/typed-input digest before the first Turn write. Store it in existing JSON ConfigSnapshot and bind it into immutable Config ID. Clone request-owned Context, allowance policies and HostRefs before hashing/dispatch. Changed replay cannot choose a new budget, transcript, message identity, or instructions.
3. **Single recovery explanation.** `Refresh` and root/terminal replays re-project deterministic Items from committed Config, Invocation and Kernel Run. Application terminal Invocation can repair a lagging Turn without re-entering the executor. No independent authoritative recovery object, queue or additional status was introduced.
4. **Bounded reads.** `LoadState` returns Session/Turn/config without full Timeline or Kernel execution; `ListTimeline` validates an ordered bounded Item page. HTTP exposes owner-authorized `/state` and `/items` endpoints and TypeScript convenience methods, with public Item payload redaction. Active SSE subscribers authorize using the bounded state; terminal synthesis loads complete history only when needed to check product Host-output acknowledgment. Full `Load` remains available for explicit full-history use.

## Bounded in-memory performance evidence

Apple M4, Go `-benchtime=50x`, a terminal Turn with synthetic historical Items; these are microbenchmarks, **not production latency/SLO**.

| Extra Items | Full `Load` | `LoadState` |
| ---: | ---: | ---: |
| 0 | ~1.96 µs; ~6 KB/op | ~0.25 µs; 0 B/op |
| 100 | ~20.0 µs; ~105 KB/op | ~0.26 µs; 0 B/op |
| 1,000 | ~266 µs; ~1.24 MB/op | ~0.27 µs; 0 B/op |
| 5,000 | ~1.14 ms; ~6.71 MB/op | ~0.26 µs; 0 B/op |

The benchmark proves removal of complete-Item traversal from the new status path, not that database-backed stores have constant latency.

## Local verification and exclusions

- **PASS:** full ten-module Go tests, Go race suites, gofmt, tidy-check, vet and golangci-lint; HTTP/Provider/Kernel and Harness regression suites.
- **PASS:** Go clean-consumer local-cohort release check; TypeScript lint, typecheck, test and build; Beta metadata/release policy checks; deterministic evaluation/A2A/MCP scenario suites and benchmark smoke.
- **PASS:** GORM SQLite-backed Harness PostgreSQL Store round-trip of the admission hash and two frozen host message bindings; different admission digest conflicts against the original Turn.
- **PASS:** a pre-hard-cut running Agent Turn with missing admission proof is rejected before direct Resume dispatch; old records can still be inspected and reconciled without creating effects.
- **Still required for final production admission:** execute new unknown-Application-effect and projection crash tests using real PostgreSQL and cross-process restart (the local SQL codec test used SQLite), plus real Host effect-reconciliation acceptance.
- **2026-10-29 update:** Docker availability has been restored and the standard real PostgreSQL/Redis/cross-process recovery integration suite passed during the sixth audit; the seventh physical-dispatch correction must be retested before merge admission. Live Host-specific external effect reconciliation and exactly-once claims remain outside these provider/Store integration checks.
- Host effect idempotency and reconciliation are host contracts, not guaranteed physical exactly-once execution by the SDK.
- Existing in-flight pre-hard-cut Turns cannot be granted an invented `AdmissionHash`; drain or isolate them under the operator-owned release gate in `docs/breaking-changes.md`.
