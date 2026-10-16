# Minimal Agent Runtime — implementation and conformance contract

- **Status:** Normative implementation/conformance gates for [accepted ADR 0008](adr/0008-minimal-agent-runtime-architecture.md); Stage 1 SQLite proof and opt-in Stage 2 Phase 1 execution-epoch Store port implemented; full ProgressPlan/control/Hosted release gates pending
- **Date:** 2026-10-15
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Scope:** Normative gates for staged implementation; driver and ReadyWork V1 choices are in the [Stage 0 decision record](audits/2026-10-15-minimal-runtime-stage0-decisions.md)

This document is a **test and migration contract** for the future
`ProgressPlan`, `ReadyWorkSource`, Run-execution epoch and five-state
lifecycle. A [Stage 1 SQLite Store prototype](audits/2026-10-15-minimal-runtime-stage1-prototype.md)
**now exists**, but implements **only the current six-state Kernel
Store** with existing TransitionOutbox semantics; it is neither the
final target architecture nor a production durability certification.
Current published behavior remains governed by the
[architecture](architecture.md), [ADR 0001](adr/0001-runtime-durability-governance-v1.md),
[ADR 0005](adr/0005-durable-agent-execution-allowances.md) and
[ADR 0006](adr/0006-runtime-owned-auto-allowance-segments.md)
until an accepted, implemented and released change supersedes the
specific older contracts.

## 1. Source of truth and invariants

| Subject | Source of truth | Required guarantee |
| --- | --- | --- |
| Run | Kernel Snapshot + CAS revision | No stale/terminal mutation |
| Approval | Feature pending call + bound decision/checkpoint | An approval is never transferable to the next Tool |
| Budget | Feature allowance and hard ceiling; Harness ledger when present | A grant never expands the frozen authorization or hard ceiling |
| Model/Tool side effect | Stable intent ID + durable receipt/ambiguous-outcome state | Receipt consumed once; no claim of external physical exactly-once |
| Execution ownership | **Run Store** execution epoch/owner/expiry plus stable ready-work ID | Execution-owned CAS checks Revision **and** epoch/expiry inside one Store transaction; Redis lease alone is not authoritative |
| Poison-work quarantine | Run Store guard for exact work ID/generation, separate from transport DLQ | Quarantined work cannot be acquired through direct Resume/Pump/Hosted until authorized repair |
| Privileged/ingress controls | Feature command identity, actor authorization and Run Revision | Exact approval/grant/cancel/signal command can mutate without an execution claim, never through a generic bypass |
| Future progress | Committed progress plan / indexed due-work, active segment or resolvable gate | No missing durable recovery path for an autonomous nonterminal Run |
| External signal | Committed wait identity or bounded durable pre-wait inbox | An early callback cannot be silently discarded or authorize a different wait |
| Commit identity | Stable logical operation ID / durable receipt or marker | An ambiguous COMMIT response is reconciled before any repeated operation |
| Parent/child relation | Durable edge + terminal child fact + consumed-watermark proof | Early terminal and late parent waits are repaired |
| OS lifecycle & time | Platform callback, Store time policy; **not** Kernel Run status | Freezing/time jump may delay execution but must not erase or authorize intent |

No optional Worker/queue is required for a minimal in-process Agent.
No durable deployment may claim safe automatic resume without an
authoritative progress record and a way to activate eligible work.

## 2. Progress obligation conformance

### 2.1 Who generates and verifies it?

**The owning Feature generates one versioned progress plan** whenever
it prepares a durable `Create` or `Apply`. The plan identifies the
current logical phase and **all permitted alternative triggers**
for that phase (e.g. external result or matching timeout); it does
not make each trigger an independent dispatch authorization.
Feature transition builders classify their own state; Kernel cannot
derive semantic needs from opaque JSON. The Kernel/Store reject a
missing/invalid plan, check status/phase/wait ID/generation and
persist the plan with its required ready-work, active-segment expiry
or inbound-signal indexes in the same transaction.

During a claimed execution segment, intermediate CAS commits may
declare `ActiveSegment` rather than create another `ReadyNow` row.
The persisted authority/expiry and stable invocation ID are the
recovery source if the process dies. A controlled yield or a
non-execution command that unblocks a wait publishes the **next**
work obligation atomically.

The Kernel cannot detect a semantically dishonest Feature plan:
each Feature needs transition-by-transition tests proving that
every autonomous commit has a truthful recovery path. Unknown
plan kinds/versions fail closed; no implicit `NoWork` fallback.

### 2.2 Required test cases

| Commit boundary | Progress disposition / proof |
| --- | --- |
| New autonomous Direct Agent | `ReadyNow` with same stable work identity for direct and Pump execution |
| More steps in **same claimed execution segment** | `ActiveSegment` persists Run owner/epoch/expiry and in-flight work identity; **no new queue item per CAS** |
| Segment yields or loses authority | Next `ReadyNow`/`ReadyAt` or expired-segment recovery source; new owner must reconcile ambiguous effects |
| Model retry after bounded backoff | `ReadyAt`, preserved logical invocation ID/attempt and exact due time |
| External Tool pending/leased | `AwaitSignal` or `InFlightRecovery` with stable operation ID/generation; timeout is an **alternative trigger for same wait** |
| Approval required | `ManualGate` with checkpoint ID, pending Tool call ID, decision authority; **no automatic unblocking** |
| Budget exhausted | `ManualGate` with blocked allowance dimension/remaining hard ceiling |
| Workflow explicit signal wait (future scope) | Condition-bound `AwaitSignal` with verified wait ID/generation |
| Parent waiting on child (future scope) | `AwaitChild` plus durable relation and late-child repair |
| Run terminal | `Terminal` and durable terminal fact available to optional parent reconciliation |
| Process stops between Create commit and direct drive | Persisted `ReadyNow` remains eligible on next activation |
| Process stops during claimed multi-CAS segment | Expired `ActiveSegment` is discoverable by indexed scan and authoritative takeover; prior effects first reconciled |
| Signal arrives before wait becomes visible | Precommitted wait identity or authenticated bounded durable inbox; correlate exactly once |
| Caller sees error after successful commit | Same stable commit ID is found on reload even after later revisions; no new external call |
| Existing `running` with no live executor | Still recoverable from committed intent; does not need synthetic `suspended` |

Where a Feature chooses to leave a delayed or in-flight Run in
`running`, a due/reconciliation obligation is still required. A
`suspended` Run must have a gate descriptor and may not be resumed by
the generic `Resume` path while unresolved.

### 2.3 Suspension release matrix

| Candidate / state | Expected result |
| --- | --- |
| Correct approval ID/call ID, authorized decision, current revision | Feature validation + single CAS gate release; new progress obligation in same transaction |
| Old approval response after next Tool begins | Reject/no-op without executing next Tool |
| Budget wakeup without privileged grant | Remain gated; no model/tool dispatch |
| Correct budget grant under ceiling | Exactly one CAS; no grant to other dimension |
| Signal with stale wait generation | No current-gate release; ACK only after proving stale |
| Correct signal arrives while unrelated revision changes | Reload, bounded guarded retry; cannot silently ACK still-needed signal |
| Deadline becomes due while gated | Terminal deadline transition under current revision, without unauthorized work |
| Already terminal Run receives signal/approval | No revival, no new external effect |

A timer firing does not authorize an approval or budget grant.
A delivered child terminal fact does not release an unrelated gate.

### 2.4 Shared eligibility predicate: status is necessary, not sufficient

Every direct `Resume`, on-demand Pump and Hosted dispatcher must
enforce this **same logical predicate**, using freshly loaded
authoritative state; before an external side effect, acquire and
recheck a fenced execution segment:

1. Run is nonterminal and the candidate matches the **current
   progress plan**: ready-work source revision for a new claim,
   exact wait ID/generation for condition triggers, or current
   execution epoch for continuing an active segment. Earlier
   ReadyNow revisions cannot resume an unrelated later phase.
2. Feature validates plan-specific facts (due time, signal, child,
   manual gate, receipt/ambiguous effect policy). Terminal state or
   absent/unknown plan **never defaults to runnable**.
3. Before acquiring execution, the **Run Store** confirms the exact
   current work ID/generation is not quarantined; this block is
   authoritative even if a transport queue has already DLQ'd a job.
   Execution is then acquired under epoch/expiry fencing. A
   privileged signal/grant/approval command is handled through the
   separately validated **control mutation path**, never mistaken
   for an execution job or a generic quarantine bypass.
4. After any CAS conflict or change of plan/gate, reload and recheck;
   ACK a delivery as obsolete only when authoritative evidence proves
   its exact logical work was already consumed or superseded.

| Current plan | Direct/queued candidate | Permitted action |
| --- | --- | --- |
| `ReadyNow` | Matching work ID, eligible revision/generation | Claim Run authority, then execute |
| `ReadyAt` | Correct work ID **and** Store time at/after due | Claim and execute; early delivery must wait |
| `ActiveSegment` | Current owner with valid epoch/expiry | Continue without new queue claim; expired owner must stop; replacement must reconcile |
| `AwaitSignal` | Matching durably recorded signal and generation | Verify resolution; mere `running`/old `ReadyNow` is insufficient |
| `AwaitChild` | Matching terminal child and relation/wait | Feature verifies child and wait before resuming |
| `ManualGate` | Generic Resume/timer/any old event | **Reject/keep blocked**; only authorized exact control may release |
| `InFlightRecovery` | Matching invocation/effect ID and safe recovery classification | Reconcile receipt/unknown outcome before new dispatch |
| `Terminal` | Any | No execution or gate release |

The plan may have **alternative triggers** (signal vs timeout)
for one wait. Consuming one must atomically invalidate other
alternative triggers for that wait; outdated callbacks never reopen
a new generation. This applies even if Run status is `running`.

## 3. Run execution segments, fencing, and commit uncertainty

### 3.1 Authoritative execution ownership

**The stable logical work identity is shared by direct Start/Resume,
foreground Continue, and background/hosted delivery.** A logical work
identity includes Run, source phase/revision, trigger, and relevant
wait/effect generation; no random new ID per poll.

A **separate durable Run execution epoch and active-lease record
resides in the authoritative Run Store** (SQLite or PostgreSQL).
Epoch monotonically increases upon new ownership/takeover. A
delivery-queue claim (including Redis) is merely a candidate: the
worker **must additionally acquire the authoritative Run epoch**
before protected execution. Neither `expectedRevision` alone nor
a Redis lease is a sufficient commit fence.

Conceptual contract — *not* finalized exported Go signatures:

```text
AcquireRunExecution(Run, WorkIdentity)
  -> atomic check of current plan/eligibility
     + epoch increment + active owner/expiry in Run Store

ApplyExecution(Revision, Epoch, Owner, Mutation)
  -> one authoritative transaction:
     WHERE revision = expected
       AND execution_epoch = expected_epoch
       AND owner = expected_owner
       AND lease_valid_under_store_time
     + next Feature state/ProgressPlan/receipt
     + retain ActiveSegment OR publish next ready work

ApplyValidatedControl(Revision, ValidatedCommand)
  -> CAS + Feature authorization + exact checkpoint/wait identity
     + invalidate epoch atomically when command revokes execution
```

The exact SQL predicate, connection isolation and time comparison
are adapter contracts. A standalone **preflight** execution-lease
read does not replace the commit-time fence. Stale claimant A must
fail after B takes over **even if B did not increment Run Revision**.
A claim expires even without takeover; the old worker must not
commit after the authoritative Store expiry time. Renewals cannot
resurrect a lost epoch.

Control commands do not need ordinary execution ownership, but
must be narrowly typed and owner/tenant authorized. Cancellation,
terminal decisions and any command that invalidates the active
phase atomically revoke/bump its epoch. A concurrent control update
that preserves the active phase still forces an execution claimant
to reload/revalidate after CAS conflict; an approval/grant/signal
cannot turn into a general arbitrary-Apply bypass.

### 3.2 Segment lifetime and crash recovery

1. `Create` commits initial `ReadyNow`. Synchronous `StartRun` and
   pump contend for **that same** work; only one obtains authority.
2. Owner performs a bounded segment, possibly multiple `Apply`
   transitions under the **same** Run epoch. Those commits persist
   `ActiveSegment`, stable in-flight effect ID, current lease expiry
   and enough indexed recovery evidence; **no new queue work per
   internal CAS**.
3. A segment may renew its own authoritative lease. Yielding,
   terminal completion or waiting ends the segment and atomically
   publishes the appropriate next plan/work (or ManualGate). Old
   owner is revoked; a new segment requires Acquire.
4. If process dies mid-segment, an indexed expired-segment scan
   discovers the Run; new authority increments the epoch. The
   new executor classifies unsettled external effects before
   considering another physical call.
5. State/receipt commits require Run Revision **and** valid epoch/
   expiry in one Store transaction. After Store commit, transport
   ACK is allowed. Crash before ACK may duplicate delivery,
   **not** consume the same durable receipt twice.
6. Worker lease loss cooperatively cancels execution but cannot
   preempt an uncooperative provider/Tool or promise exactly-once
   remote effects. Durable IDs, downstream idempotency and
   reconciliation are still necessary.

Test an owner that performs **three or more internal CAS steps** in
one segment; no duplicate ReadyWork enqueues and no extra claim
generation per intermediate revision. Then expire the segment
between steps without a Run revision change and verify that old
owner's next commit fails while replacement safely reconciles.

### 3.3 Early signals and ambiguous commit responses

Preferred order for a callback-capable effect:

```text
Commit stable operation/wait ID -> dispatch external operation
    -> receive signal -> CAS exact current wait/generation
```

If the external protocol can callback **before** a final wait
checkpoint is committed, its ingress must durably store a bounded,
authenticated, deduplicated early signal keyed by a preallocated
stable operation identity (same Run Store/inbox when possible).
When the wait becomes visible, reconcile/consume the signal
atomically with the matching Feature state; do not discard it or
accept arbitrary orphan signals. Set retention and quota bounds;
unmatched/cancelled/unauthorized signals fail closed. If the
identity cannot be assigned before dispatch, do not advertise
lossless early-callback semantics for that adapter.

For `Create`/`Apply`, each logical mutation has a stable operation
ID and durable commit receipt/marker discoverable even if a later
Revision has advanced. For `Create`, require a caller-stable Run ID
**or** a Store-enforced, scoped-unique `RequestID` (with lookup):
the current code's optional RequestID field does **not** by itself
prove idempotency. Never mint a different Run ID on an uncertain
initial creation. A connection error after COMMIT is
**indeterminate** until that ID is read and compared:

- Matching committed operation ID/fingerprint: treat as committed,
  reload state and re-evaluate next work; **do not redispatch**.
- Authoritatively proven not committed: retry with **same identity**,
  after normal CAS and Feature safety validation.
- Cannot establish outcome: keep durable uncertainty/reconcile or
  quarantine; never generate a fresh logical operation or physical
  effect merely because the caller saw an error.

A higher Run Revision alone does **not** prove the requested
commit was applied. Avoid adding a general transaction log:
a bounded durable commit index or existing immutable receipt/event
identity may supply the proof, provided it survives the required
recovery horizon.

**Retention/GC rule:** An unsettled, ambiguous, quarantined, or
redeliverable logical operation retains its proof **without a
wall-clock TTL**; an offline app may return after months. A
compact immutable tombstone (operation ID, scope, fingerprint,
final disposition and settlement generation) can replace bulky
receipts when safe, but must continue to reject reuse of a
retired identity. Cleanup of full records requires positive proof
that the operation is settled, all durable deliveries and waits
are acknowledged/retired, any advertised replay horizon has
closed, and no old producer can reissue an indistinguishable
request. A bounded retention policy may forbid further new work
when its quota is exhausted, but **must never delete an
unresolved proof in order to resume**. If proof is missing or
outcome uncertain, fail closed and require reconciliation rather
than issuing a fresh operation ID. Include backup/restore and
historical duplicate delivery in the GC conformance matrix.

### 3.4 Delivery failure handling

**Delivery outcomes:** `Ready -> Claimed -> Acked / RetryPending /
Quarantined`. ACK/Retry/Quarantine are fenced to the current active
delivery claim and cannot erase the authoritative Run progress
plan. **Quarantine is two things:** a transport dead-letter record
for investigation **and** an authoritative Run Store execution-
eligibility guard for the exact current work ID/generation.

A direct `Resume`, background activation or Redis-delivered Hosted
worker must check the same Run Store guard **atomically with**
`AcquireRunExecution`. `ReadyNow` remaining in the Progress Plan
does not override that guard. Stale blocks for a previous work
generation must not quarantine an unrelated later operation.

**Cross-store Hosted ordering** (without pretending PostgreSQL and
Redis have a shared transaction):

1. On terminal poison classification, commit the blocked current
   work ID/generation and bounded diagnosis in **PostgreSQL**
   under a guarded CAS/epoch, then mark the Redis delivery dead.
2. If Redis DLQ/ACK fails after PostgreSQL commits, a duplicate
   delivery is rejected by the Run Store and an idempotent handoff
   reconciler retries transport finalization.
3. If the Run Store block fails, **do not ACK or irreversibly DLQ**
   the only recovery source; leave/redeliver the job and retry the
   protected quarantine transition. Configure terminal-NACK paths
   so queue-side automatic exhaustion cannot bypass this ordering.
4. If a malformed delivery cannot identify its Run/work identity,
   preserve a durable orphan incident and reconcile missing work
   from the Run Store; never treat the orphan DLQ as proof its Run
   progressed or as permission to discard a Run obligation.

In Embedded, blocking current work and recording its diagnosis
happen in one SQLite transaction. A quarantined record retains
stable identity, source revision/generation, bounded failure code,
attempt count and timestamp. **Owner-authorized repair** verifies
current Run ownership/permissions, effect ambiguity and exact
blocked identity, then atomically clears the Run Store block and
publishes a new eligible work record with the **same logical effect
identity where applicable**. This is an explicit control command,
not an arbitrary `Resume` escape hatch. Other jobs continue;
quarantine never turns an unfinished Run into `completed`.

## 4. Embedded feature and storage coverage

| Feature | Embedded V1 promise | Required extension gate |
| --- | --- | --- |
| Direct Agent + Kernel | SQLite, crash-safe progress/receipt, local tool/approval guards | Initial acceptance target |
| Local approval/wait | Checkpoint and exact-call decision persisted with Direct Agent | Duplicate/refusal/next-batch safety |
| RunRelation | Not in V1 | SQLite relation store; early terminal/late parent wait/race recovery |
| Workflow | Not in V1 | SQLite definitions, effects, waits and child/effect repair |
| Team/GroupChat | Not in V1 | Durable child identity, member relations and parent wakeup |
| Harness Session/Turn/Context | Not in V1 | Separate SQLite Harness Store with consistency checks |
| Shared budget ledger | Not in V1 | Dedicated ledger CAS and restart/concurrent settlement |

The embedded Direct Agent example must not secretly instantiate a
Memory-based database, queue, ledger or relation store while claiming
those features are durable. Features excluded from V1 must fail
composition/validation clearly when durable support is requested.

### Local identity and namespace (ergonomics without weaker isolation)

Current `kernel.Create` validates nonempty `ActorRef.TenantID`,
`ActorID`, `ThreadRef.Kind/ID` and a `Goal`. Embedded apps should
receive a composition-level **stable installation/profile namespace
factory** (persisted in protected local settings), not be required
to invent a global server tenant or expose a device hardware ID.
**Stable identity is not authorization.** Embedded V1 supports one
trusted Profile with a separate protected SQLite database and
separate OS-secured secrets by default; applications that expose
untrusted user inputs must still authorize privileged commands.

**Shared multi-Profile SQLite is opt-in**, not automatically safe.
The embedding application must authenticate the active profile
and enforce ownership/scope in a mandatory facade before exposing
any `Load`/snapshot/event/feed/list API, `Resume`/`Cancel`, approval,
allowance grant, signal ingestion, quarantine repair, ReadyWork
claim, Tool/catalog access or secret retrieval. Raw Kernel calls
accepting a Run ID are **trusted-internal** and must not be directly
exposed across profiles. The authorizer must check the persisted
Run owner/namespace, not the untrusted caller's supplied `ActorRef`.
Fail closed for missing/disabled profile or namespace mismatch.
Do **not** loosen Kernel object isolation, trust arbitrary model-
provided IDs, or put platform identity/RBAC policy inside Kernel.
Test two profiles sharing one DB across all above surfaces and
verify no Run, approval, Tool or secret privilege crossover; repeat
after restart. Hybrid export of a Run needs a distinct explicitly
authorized identity mapping.

### SQLite and protected storage

- Write Run, feature state, execution receipt and required ready-work
  within one database transaction. Fault injection at every insert/
  update must roll back all of them.
- **Bound each write transaction to short local SQL work only.** A
  multi-CAS execution segment holds an execution epoch **across
  transactions**, never a SQLite write lock across LLM/Tool/network
  calls, approval, sleep, retries, or filesystem Tool execution.
  Acquire+fence, receipt+plan commit, renewal, and quarantine each
  use a short bounded transaction. Compare `BEGIN IMMEDIATE` when
  a writer lock is known to be required against `DEFERRED`; ensure
  busy waits are bounded and a stale read transaction is restarted
  rather than upgraded blindly. Measure transaction hold time,
  contention latency and WAL growth under competing readers/writers.
- Test two process openers and stale readers. `SQLITE_BUSY` can use
  bounded contention retry; `SQLITE_BUSY_SNAPSHOT` must reload and
  restart a read-write transaction, never blindly retry within a
  stale snapshot.
- Test `SQLITE_FULL`, corrupt record, checksum/schema mismatch,
  backup/restore, WAL checkpoint starvation, long readers, WAL growth,
  disk quota and interrupted startup migration.
- Select and document SQLite driver/CGO choice per target OS/ABI,
  process-lock behavior and binary-size cost. Evaluate WAL +
  `synchronous=FULL` for critical effects; explicitly document
  power-loss durability assumptions of any weaker configuration.
- Test platform-locked file protection/Keychain/keystore unavailable.
  Classify temporary store/key unavailability separately from
  corruption: do **not** create a new empty database, reset schema,
  drop ready work, leak protected data or terminally fail a Run.
- On the next eligible activation, retry opening the original store
  once OS protection allows it. Backup/export and encryption key
  rotation must not silently break pending work.
- **Clock domains:** stored `ReadyAt` and user deadlines are
  wall-clock UTC instants checked against Store time; lease TTL/
  renewal interval during a live process may use monotonic elapsed
  time. PostgreSQL uses its database clock for guarded claims/
  commits; SQLite uses the device clock inside its Store transaction,
  which is **not an external trusted time source**. Monotonic time
  cannot be persisted across reboot.
- Test backward/forward wall-clock jumps and restart. A suspicious
  jump cannot itself license replay of an ambiguous external effect.
  Revalidate the Run, due condition, current epoch and receipt
  state, and conservatively delay/quarantine when the time source
  is too uncertain; record a diagnosable condition. Backward jumps
  may delay progress. Do not shift frozen user deadlines to
  disguise clock change. Stage 0 defines a documented clock-skew
  tolerance/policy; no silent arbitrary drift threshold.
- Activation processes a bounded page/CPU/time budget. Mobile
  background scheduling is opportunistic; deadline/`ReadyAt` means
  no earlier than the due time **under the Store clock**, not
  execution exactly at due time or a guarantee against an
  incorrectly configured device clock.

No mobile production claim before representative real device/simulator
tests exercise both allowed and denied background execution scenarios.

## 5. Parent/child handoff contract (post-V1)

Feature scope remains outside minimal Kernel, but relation-enabled
deployments must pass **all three** races:

1. Child becomes terminal **before relation Ensure**.
2. Child becomes terminal **after relation Ensure but before parent wait commits**.
3. Child becomes terminal **after parent wait commits**; parent may
   restart or update an unrelated revision before receiving handoff.

Relation Ensure and parent-wait commit each check current terminal
child state; a bounded indexed scan/dirty-relation feed repairs missed
handoffs after restart. Relation watermarks advance only after durable
job acceptance or proof of consumption in the parent Feature state.
A stale wakeup cannot satisfy a new wait. A later parent revision
must re-open an early-terminal relation when parent consumption is
still outstanding. Deletion of the independent Continuation Worker
is **not** sufficient replacement evidence.

## 6. Coordinated five-state migration

This is an **implementation-stage proposal**, not permission to
rewrite persisted records in the current version.

| Existing status | Target | Migration prerequisite |
| --- | --- | --- |
| `running` | `running` | Reconstruct/verifiably preserve due/in-flight/wait recovery obligation; if impossible, stop migration for that Run |
| `waiting_input` | `suspended` | Inspect Feature checkpoint; distinguish Tool approval, Workflow user input vs external condition, bind wait ID and generation |
| `paused_budget` | `suspended` | Verify allowance/exhausted dimension and frozen ceiling; persist privileged ManualGate, no automatic resume |
| `completed` | `completed` | Valid result, immutable |
| `failed` | `failed` | Preserve error code and terminal identity |
| `cancelled` | `cancelled` | Preserve cancellation and terminal identity |

Do not infer gate class from status spelling alone. Unknown Feature
kind, missing checkpoint/allowance data, malformed old event or unknown
state version is a **migration blocker** with explicit diagnostics,
never silently mapped to `running`.

Migration applies to **Kernel, Agent, Workflow, Team/GroupChat,
Harness/HTTP/client projections, SQL data, transition outbox and
continuation job payload versions**. Prevent mixed-version producers
and consumers: drain/coordinate workers before the cutover, maintain
replay-safe identity, and test upgraded record reads before release.
If rollback would reinterpret new `suspended` states incorrectly,
rollback must require a version-matched backup or an explicit
forward-fix; **do not** claim automated downgrade.

Once implemented, this status model supersedes **only the specific
Kernel `paused_budget`/`waiting_input` status clauses** of ADR 0005
and corresponding assumptions in ADR 0006. Explicit grant authority,
budget ceilings, checkpoint/approval identity and existing security
invariants remain in force.

## 7. Evidence matrix and stage exits

| Stage | Required evidence | Forbidden inference |
| --- | --- | --- |
| 0 — Contract/baseline | Inventory Feature transition classes and direct/control mutation authority; stable IDs; single-/long-state SQLite metrics, Store-clock/skew policy, Kernel API/dependency/metadata/write budgets, clean-consumer graph | No new SQLite/OS support claim |
| 1 — **Single-executor SQLite proof** on existing statuses | Sequential Direct Agent restart + receipt fault cases, native desktop build, no Redis/PG; do **not** enable concurrent pump/process claim | **Not yet production concurrent Embedded Durable**; no five-state/Workflow parity |
| 2 — Progress/segment/fencing contract | Memory/SQLite/Postgres truth-plan, Store epoch+expiry CAS and validated controls; 3+ CAS under same segment, concurrent direct/Pump, authoritative quarantine, profile isolation, early signal/inbox, retention and uncertain COMMIT tests | Revision-only CAS or Redis lease alone is not a commit fence |
| 3 — Five-state hard cut | Old/new status migration, exact wait/generation, running-but-blocked eligibility, coordinated all-module API/client tests; internal merge permitted | **NOT** independently releasable to production consumers before Hosted Stage 4 parity gate |
| 4 — Hosted parity | Real PostgreSQL outbox/Redis or PG queue handoff, Run Store poison gating, lease/dead letter/recovery, parent/child repair; **joint Stage 3+4 release signoff** | SQLite mock success and passing Stage 3 alone are not hosted production evidence |
| 5 — More embedded features + OS | Dedicated per-feature Store conformance; iOS/Android lifecycle and storage-lock tests | Desktop simulation does not certify mobile |
| 6 — Optimization | A/B benchmark profile, accepted thresholds, regression analysis and ADR | No speculative CAS/queue complexity |

**Coordinated release gate:** Stage 3 may have internal commits and
mergeable migration code, but no production-facing breaking API,
persisted Run encoding, or mixed-status worker/consumer release is
authorized until the **same release candidate** also passes Stage 4
Hosted conformance, real-engine crash windows, worker drain/migration,
clean-consumer tests and explicit rollback policy. A Hosted surface
intentionally kept disabled and experimental is not a supported
production capability and may not be advertised as such. Preserve
existing status contracts for published consumers in the meantime.

### Minimum crash and adversarial windows

- Crash immediately after `Create`/`Apply` commit but before direct
  claim, after claim before external call, after provider/tool response
  before durable receipt, and after receipt/next-work commit before
  ACK. Restart from another process.
- **P0 fencing:** A executes under Run epoch E; lease expires;
  B acquires epoch E+1 **without changing Run Revision**; A's
  unchanged-revision execution `Apply` must fail against SQLite/
  PostgreSQL. Also check A's lease has expired with no B yet:
  A cannot commit. A Redis-delivery claim must not bypass the
  PostgreSQL epoch fence.
- **P0 eligibility:** `running + AwaitSignal` receives unrelated
  `ReadyNow` / direct `Resume`; `running + ReadyAt` receives early
  timer; `running + InFlightRecovery` receives blind resend
  request; all **reject dispatch**. Correct signals/child states
  release only their own exact wait. ManualGate never unblocks
  from generic Resume.
- **P1 segment:** Claim once, make 3+ internal CAS commits with
  `ActiveSegment`, verify no extra queue claims; crash mid-segment,
  scan expired epoch, reconcile pending effect, restart safely.
  Concurrent cancellation revokes epoch atomically, while a
  non-revoking signal update forces revalidation after CAS conflict.
- **P1 signal:** Callback arrives before final wait checkpoint.
  Verify precommitted operation ID or bounded authenticated inbox,
  duplicate ID/cancel/unknown identity handling, concurrent
  matcher, transaction failure and eventual single consumption.
- **P1 uncertain COMMIT:** Inject lost response **after a successful
  DB commit**, then advance Run again before retry. Reconciliation
  locates the original stable operation marker; no repeated
  external dispatch or duplicate receipt. For uncertain `Create`,
  check a scope-unique RequestID or caller-stable Run ID and
  prove no second Run is created. Also test a provably
  rolled-back commit and an outcome that remains unknown.
- Stale approval resolves next Tool or budget grant at wrong dimension;
  signal arrives after unrelated CAS/cancellation/wrong generation.
- Inject DB/queue errors during Run update, journal/receipt/ready-work
  insertion, ACK, retry and quarantine. No partial durable commit.
- **P1 quarantine bypass:** Poison current `ReadyNow` work W at
  generation G and commit the **Run Store** quarantine block. Direct
  `Resume`, pumped activation and Hosted Redis delivery of W/G must
  all refuse `AcquireRunExecution` even while Run remains `running`.
  Redis auto-DLQ before a PostgreSQL quarantine guard is an invalid
  final-NACK path. Crash after PostgreSQL block but before Redis DLQ:
  duplicate delivery stays fenced and reconciliation finishes. Fail
  PostgreSQL blocking first: transport must retain recovery work.
  Stale W/G block cannot disable later W/G+1. Owner-authorized repair
  must revalidate pending effect/Run identity before atomic unblock.
  A malformed unidentifiable delivery retains orphan diagnostics and
  requires authoritative progress reconciliation. Later unrelated
  jobs continue.
- **P1 profile authorization:** Two Profiles share a SQLite DB; an
  untrusted Profile B guesses Profile A's Run ID. Deny Load, events,
  Resume, Cancel, approval, budget grant, signal, quarantine repair,
  ReadyWork claim, Tool catalog/execution and secret read. Test after
  restart. Default single-Profile separate DB path remains usable.
- **P1 proof retention:** Suspend unresolved operations, take an
  extended offline interval past ordinary event retention, then
  deliver historical duplicate work and an ambiguous commit reply.
  Compact retained proof/tombstone must prevent re-execution; GC may
  remove payload only after positive settlement/replay closure.
  Backup/restore and quota exhaustion must never convert missing
  proof into permission to mint a new operation ID.
- **P2 short SQLite transactions:** Hold a model/Tool/network call
  longer than a normal SQL busy timeout while another connection
  writes. Verify no SQLite write transaction spans external work;
  segment still advances under multiple bounded commits. Compare
  `BEGIN IMMEDIATE` and `DEFERRED`, busy/restart behavior and WAL
  growth under readers and writer contention.
- Child terminal before/after relation, late parent wait, durable
  watermark and process restart (only when relation profile enabled).
- Mobile: locked protected DB/key, no background execution window,
  forced termination during write/external effect, foreground
  restoration, offline network, wall-clock shift and due-time scan.

### Measurement gates

Measure the same representative workload and pinned toolchain on an
isolated machine **before implementation** and on each candidate:
minimal binary size, warm/cold start, idle resident memory, no-work
wakeups/polling, median and p95 SQLite transaction latency, **transaction
hold time**, WAL size under long readers, claim contention, and Hosted
throughput/recovery latency. Track the minimal Kernel's **exported
API surface, imported module/dependency graph, persistent Run metadata
bytes/fields, and additional SQL writes/transactions per logical Agent
step**. Treat exceeding reviewed complexity thresholds as an
architecture review trigger, not an invitation to add more mandatory
scheduler machinery. Include Direct Agent snapshots after **1, 10, 100 and 1,000**
representative model/tool transcript steps (with realistic message,
invocation and receipt sizes): snapshot JSON bytes, DB file/WAL bytes,
write amplification, serialization time and restart latency.
Existing `agent.runState.Messages` and `ModelInvocations` grow with
execution history; don't declare JSON Patch/partitioned CAS necessary
until this measurement shows a real bottleneck. Fix reviewed
acceptance **thresholds before** evaluating the candidate; do not
invent absolute numbers without a reference environment. A minimal
embedded idle app must not require a continuous 100 ms polling worker.

All release evidence must include exact commit, command, engine/device
versions, PASS/FAIL/SKIP, data-migration/rollback result and coverage
limits. Required gates remain `make check`, race/conformance,
real-engine integrations where appropriate, and clean consumer
compilation. Stage 3's breaking contract **must not be published**
as production-ready before Stage 4 Hosted signoff. Real provider
idempotency/billing cannot be inferred from fake model tests.

## 8. Source anchors

- Kernel types, transitions and outbox:
  `go/agent-runtime/kernel/{types,runtime,store}.go`
- Agent direct execution, approvals and allowance:
  `go/agent-runtime/agent/{agent,allowance,model_invocation}.go`
- Scheduled recovery and relation reconciliation:
  `go/agent-runtime/continuation/{dispatcher,scheduler,worker}.go`,
  `go/agent-runtime/runrelation/runrelation.go`
- PostgreSQL transaction, SQLite conformance fixture:
  `go/agent-runtime-postgres/{kernel_store,kernel_store_conformance_test}.go`
- Harness, Workflow and budgets:
  `go/agent-runtime-harness/store.go`,
  `go/agent-runtime/workflow/registry.go`,
  `go/agent-runtime/budget/ledger.go`
