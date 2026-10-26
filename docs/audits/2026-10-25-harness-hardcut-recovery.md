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
- Real PostgreSQL/Redis/Docker integration remains **not verified** until the Docker daemon is available; do not claim production readiness on offline evidence.
- Host effect idempotency and reconciliation are host contracts, not guaranteed physical exactly-once execution by the SDK.
- Existing in-flight pre-hard-cut Turns cannot be granted an invented `AdmissionHash`; drain or isolate them under the operator-owned release gate in `docs/breaking-changes.md`.
