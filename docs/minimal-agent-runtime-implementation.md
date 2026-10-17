# Minimal Agent Runtime — implementation and conformance contract

- **Status:** Normative implementation/conformance gates for [accepted ADR 0008](adr/0008-minimal-agent-runtime-architecture.md); Stage 1 and the implemented opt-in Stage 2A/2B/2C Kernel engineering slices have regression evidence, but full Stage 2 **production/host eligibility is not certified**. Do not expand internal Stage 2 phases without a concrete failing safety test.
- **Date:** 2026-10-15
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Scope:** Normative gates for staged implementation; driver and ReadyWork V1 choices are in the [Stage 0 decision record](audits/2026-10-15-minimal-runtime-stage0-decisions.md)
- **Accepted simplification:** [ADR 0009](adr/0009-minimal-kernel-complexity-reassessment.md) accepted 2026-10-17. Feature-owned recovery obligations reuse existing State/Checkpoint/Outbox/Execution Epoch; mandatory ProgressPlan and Stage 3 five-state hard cut are **superseded** while all Store safety invariants remain in force.
- **Engineering scope freeze and Stage 3 entry:** [Stage 2 engineering exit](audits/2026-10-16-stage2-final-exit-audit.md) plus [Stage 3 admission audit](audits/2026-10-17-stage3-admission-after-adr0009.md). Native Agent reuses Feature State for model ReadyAt and keeps Legacy immediate retry compatibility. Product Host, actual Local Pump, Provider authorization, shared-Profile opt-in, GC and device tests are deployment/release conditions, **not new Stage 2 Kernel sub-stages**. Stage 3 is an architecture reassessment, not migration approval.
- **Phase 2A implementation evidence:** [Exact guarded approval/budget/terminal CAS](audits/2026-10-16-stage2-phase2a-guarded-control.md) and [code-review remediation](audits/2026-10-16-stage2-phase2a-code-review-remediation.md), not full Agent concurrency or Hosted release authorization
- **Phase 2B implementation evidence:** [Opt-in native Direct Agent execution leasing](audits/2026-10-16-stage2-phase2b-native-run-execution.md), [first code-review fixes](audits/2026-10-16-stage2-phase2b-code-review-remediation.md) and [second review fixes](audits/2026-10-16-stage2-phase2b-second-code-review-remediation.md); requires trusted Tool-outcome verification and explicit Pending handling, **not** a generic Pump, automatic unknown-COMMIT resolution or external exactly-once acceptance
- **Stage 2B exit/Stage 2C narrow signal evidence:** [Native post-wait signal guarded CAS](audits/2026-10-16-stage2b-exit-stage2c-postwait-signal.md), [early-signal/ReadyWork recovery](audits/2026-10-16-stage2c-early-signal-readywork-recovery.md), and [Run-isolation/dispatch remediation](audits/2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md). Native early signals are retryable; unresolved provider effects NACK rather than falsely ACK. No Kernel signal inbox, autonomous Pump or production multi-worker effect fencing is authorized.

This document is the **conformance, architecture-review and conditional
migration contract** for accepted ADR 0008 **as amended by ADR 0009**.
Its seven old progress dispositions remain useful **test-case names**, not
mandatory persisted `ProgressPlan` variants. Existing six-state Kernel
Store plus Feature-owned state/checkpoints/receipts and committed-transition
Outbox are the execution truth. Stage 3 may review alternative encodings,
but there is **no approved five-state migration** or new SQLite Schema version.
Stage 1/2 in-repository evidence does not certify Hosted/mobile production.
Current published behavior remains governed by
[architecture](architecture.md), [ADR 0001](adr/0001-runtime-durability-governance-v1.md),
[ADR 0005](adr/0005-durable-agent-execution-allowances.md) and
[ADR 0006](adr/0006-runtime-owned-auto-allowance-segments.md)
until a separately approved, implemented and released change supersedes it.

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
| Future progress | **Existing** Feature State/Checkpoint/intent/receipt + committed Outbox/SQLite ReadyWork + optional Store execution lease | No missing durable recovery path for any advertised autonomous nonterminal Run; no extra mandatory plan |
| External signal | Committed Feature wait/operation identity and **Host-owned durable ingress when needed** | An early callback cannot be silently ACKed or authorize a different wait; no mandatory Kernel inbox |
| Commit identity | Stable logical operation ID / durable receipt or marker | An ambiguous COMMIT response is reconciled before any repeated operation |
| Parent/child relation | Durable edge + terminal child fact + consumed-watermark proof | Early terminal and late parent waits are repaired |
| OS lifecycle & time | Platform callback, Store time policy; **not** Kernel Run status | Freezing/time jump may delay execution but must not erase or authorize intent |

No optional Worker/queue is required for a minimal in-process Agent.
No durable deployment may claim safe automatic resume without persisted
Feature recovery facts and a **selected** Host activation mechanism.

## 2. Feature-owned recovery obligation conformance

### 2.1 Who owns and validates recovery?

**The owning Feature** derives each recovery condition from its existing
opaque State, pending Checkpoint, stable Model/Tool intent/receipt and optional
committed work projection. It validates call/wait identity, generation,
due time, manual permission and external result before acting. **The Kernel
does not own a parallel versioned progress plan.**

At a durable `Create` or `Apply`, the same Run Store transaction must commit
all actual State/Checkpoint/receipt/journal/outbox facts required for recovery.
Intermediate CAS under an active Store epoch can retain that same epoch and
Feature intent without publishing a second Queue permit per step. An
unrecognized Feature state or unresolved Provider outcome fails closed.

If a concrete failing fault test proves a missing discriminator, persist the
**smallest** due/wait/work identity in the existing Feature/Run record and
re-run Conformance. There is no mandatory `ProgressPlan` kind, ActiveSegment
row, general Inbox or `ReadyWorkSource` port.

### 2.2 Required test cases

| Commit boundary | Feature-owned existing proof (former plan names are illustrative) |
| --- | --- |
| New autonomous Direct Agent | Initial committed wakeup with same stable work identity for Direct and **a configured** local activator |
| More steps in **same claimed execution segment** | Existing Store epoch/owner/expiry plus Feature intent are retained; no **new authoritative** execution permit per CAS |
| Segment yields or loses authority | Existing due Outbox/Feature State or expired Store lease; new owner must reconcile ambiguous effects |
| Model retry after bounded backoff | Feature-owned `RetryAfter` plus existing Outbox `WakeupAt`, preserved invocation ID/attempt |
| External Tool pending/leased | Existing ToolWait/dispatch ID, pending effect identity/generation; timeout is an **alternative trigger for same wait** |
| Approval required | Current `waiting_input` Checkpoint/pending Tool call/decision authority; **no automatic unblocking** |
| Budget exhausted | Current `paused_budget` allowance dimension/remaining hard ceiling |
| Workflow explicit signal wait (future scope) | Existing Feature wait State with verified identity/generation |
| Parent waiting on child (future scope) | Existing Feature wait plus durable relation and late-child repair |
| Run terminal | Terminal status and durable terminal fact available to optional parent reconciliation |
| Process stops between Create commit and direct drive | Initial committed wakeup/Run State remains available at next permitted activation |
| Process stops during claimed multi-CAS segment | Expired Store execution lease plus persisted intent support a **selected Host** recovery scan; prior effects first reconciled |
| Signal arrives before wait becomes visible | Precommitted stable identity or authenticated Host durable redelivery; correlate exact wait |
| Caller sees error after successful commit | Same stable commit ID is found on reload even after later revisions; no new external call |
| Existing `running` with no live executor | Still recoverable from committed intent; does not need synthetic `suspended` |

Where a Feature leaves delayed or in-flight work `running`, the due or
reconciliation obligation must still be recorded in its current State or
Outbox. Existing `waiting_input`/`paused_budget` remain precise manual gates;
no generic `suspended` status is introduced.

### 2.3 Feature gate release matrix (six-state contract)

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
   Feature-owned recovery facts**: committed work source revision for a new claim,
   exact wait ID/generation for condition triggers, or current
   execution epoch for continuing an active segment. Earlier
   ReadyNow revisions cannot resume an unrelated later phase.
2. Feature validates obligation-specific facts (due time, signal, child,
   manual gate, receipt/ambiguous effect policy). Terminal state or
   absent/unknown recovery proof **never defaults to runnable**.
3. Before acquiring execution, the **Run Store** confirms the exact
   current work ID/generation is not quarantined; this block is
   authoritative even if a transport queue has already DLQ'd a job.
   Execution is then acquired under epoch/expiry fencing. A
   privileged signal/grant/approval command is handled through the
   separately validated **control mutation path**, never mistaken
   for an execution job or a generic quarantine bypass.
4. After any CAS conflict or change of Feature wait/gate, reload and recheck;
   ACK a delivery as obsolete only when authoritative evidence proves
   its exact logical work was already consumed or superseded.

| Conceptual eligibility (not a persisted plan type) | Direct/queued candidate | Permitted action |
| --- | --- | --- |
| Ready now (committed wakeup) | Matching work ID, eligible revision/generation | Claim Run authority, then execute |
| Ready at due time (Feature-owned State + Outbox) | Correct work ID **and** supported authoritative time at/after due | Claim and execute; early delivery must wait |
| Existing active Store lease | Current owner with valid epoch/expiry | Continue without a second execution authority; expired owner must stop, replacement reconciles |
| `AwaitSignal` | Matching durably recorded signal and generation | Verify resolution; mere `running`/old `ReadyNow` is insufficient |
| `AwaitChild` | Matching terminal child and relation/wait | Feature verifies child and wait before resuming |
| `ManualGate` | Generic Resume/timer/any old event | **Reject/keep blocked**; only authorized exact control may release |
| `InFlightRecovery` | Matching invocation/effect ID and safe recovery classification | Reconcile receipt/unknown outcome before new dispatch |
| `Terminal` | Any | No execution or gate release |

A Feature wait may have **alternative triggers** (signal vs timeout)
for one stable identity. Consuming one must atomically invalidate other
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
  -> Feature validates persisted work/wait/due/receipt eligibility
     + Run Store checks status/quarantine and atomically grants epoch
     + active owner/expiry in the same Run Store

ApplyExecution(Revision, Epoch, Owner, Mutation)
  -> one authoritative transaction:
     WHERE revision = expected
       AND execution_epoch = expected_epoch
       AND owner = expected_owner
       AND lease_valid_under_store_time
     + next Feature State/Checkpoint/receipt
     + existing Outbox wakeup only when that transition needs delivery

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

1. `Create` commits its initial recoverable wakeup. Synchronous `StartRun`
   and an **optional selected** activator contend for the same Run epoch.
2. Owner performs a bounded segment, possibly multiple `Apply`
   transitions under the **same** Run epoch. Those commits retain the
   existing Store epoch/expiry and stable in-flight Feature intent;
   no new **execution authority** is created by intermediate CAS.
3. A segment may renew its own authoritative lease. Yielding,
   terminal completion or waiting ends the segment and atomically
   commits its appropriate Feature gate/Outbox fact. Old
   owner is revoked; a new segment requires Acquire.
4. If process dies mid-segment, a selected Host must rediscover expired
   Store authority and preserved Feature intent; the new owner increments
   the epoch. The new executor classifies unsettled external effects before
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
one segment; no duplicate **execution permission or epoch claim** per
intermediate revision. Existing per-step Outbox facts may be emitted but
must remain idempotent and are retained until proven consumed. Then expire the segment
between steps without a Run revision change and verify that old
owner's next commit fails while replacement safely reconciles.

### 3.3 Early signals and ambiguous commit responses

Preferred order for a callback-capable effect:

```text
Commit stable operation/wait ID -> dispatch external operation
    -> receive signal -> CAS exact current wait/generation
```

If the external protocol can callback **before** a final wait
checkpoint is committed, a **selected Host ingress** must durably retain and
redeliver bounded, authenticated, deduplicated early signals keyed by a
preallocated stable operation identity; no generic Kernel inbox is required.
When the wait becomes visible, reconcile/consume the signal
atomically with the matching Feature state; do not discard it or
accept arbitrary orphan signals. Set retention and quota bounds;
unmatched/cancelled/unauthorized signals fail closed. If the
identity cannot be assigned before dispatch, do not advertise
lossless early-callback semantics for that adapter.

For **opt-in Native or selected production Host** `Create`/`Apply`,
a known stable Run ID or identified Apply proof must be retained and
queryable even if a later Revision has advanced. Existing plain Apply is
not automatically identified. For `Create`, require a caller-stable Run ID
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
delivery claim and cannot erase the authoritative Run recovery
facts. **Quarantine is two things:** a transport dead-letter record
for investigation **and** an authoritative Run Store execution-
eligibility guard for the exact current work ID/generation.

A direct `Resume`, background activation or Redis-delivered Hosted
worker must check the same Run Store guard **atomically with**
`AcquireRunExecution`. A queued wakeup or `running` Run status
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
  Acquire+fence, receipt+Feature State/Outbox commit, renewal, and quarantine each
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

## 6. Stage 3 reassessment — keep six states unless a separate ADR approves a cut

Accepted [ADR 0009](adr/0009-minimal-kernel-complexity-reassessment.md)
**cancels the mandatory five-state status migration** previously proposed
for Stage 3. The implementation contract remains the current six statuses;
`waiting_input` and `paused_budget` encode distinct privileged gates and
MUST NOT be silently reinterpreted as a new generic `suspended` state.
No Schema bump, SQL/Queue/wire conversion, status alias or compatibility
fallback is authorized by entry to Stage 3.

**Stage 3 design review completed on 2026-10-17: [retain six statuses](audits/2026-10-17-stage3-six-state-design-decision.md).** No measured migration benefit justified a five-state cut, and none was implemented.

Stage 3 is a **bounded architecture/compatibility comparison**, not another
Kernel implementation phase. Its entry requires the already tested Stage 2
Kernel safety baseline and explicit acceptance of ADR 0009. Its deliverable
is a short design decision backed by **measured evidence**, comparing:

| Option | Concrete review obligation |
| --- | --- |
| **Retain six states (default)** | Review actual API/wire enums, use of `waiting_input` and `paused_budget`, Feature State/Checkpoint, existing Store DDL, minimal per-step writes and failure tests; document remaining complexity |
| **Propose five states (not authorized)** | Demonstrate quantified benefit over six states and account for new gate encoding, status migration, downgrade/rollback, SQLite/PostgreSQL, Go/TS/HTTP/Queue/consumer compatibility and operational cost |

If the second option is measurably superior, draft a **separate ADR** with
legacy-record mapping, full negative conformance, disabled/unknown Feature
fail-closed behavior, rollout/rollback and explicit approval. **Do not
implement that cut as an automatic Stage 3 action.** A design-only Stage 3
review is permitted even while actual product Host/Provider/mobile release
gates remain NO-GO. Any later approved breaking changes must pass selected
Host parity and coordinated consumer release gates before publication.

## 7. Evidence matrix and stage exits (ADR 0009 amended)

| Stage | Required evidence | Forbidden inference |
| --- | --- | --- |
| 0 — Baseline | Feature transition/ownership inventory, stable IDs, six-state API/dependency and SQLite size/time metrics | Measurements do not certify mobile/Hosted production |
| 1 — Single-executor SQLite proof | Direct Agent restart, receipt/approval and six-state Store CAS without mandatory PG/Redis | Not concurrent durable mobile certification |
| **2 — Bounded experimental Kernel integration** | Memory/SQLite/PG epoch/expiry CAS; exact Feature controls, Native intent/receipt, 3+ CAS segment, current Run quarantine, due/wait source checks, early signal refusal, uncertainty recovery and actual selected Worker/Queue fault tests | **Engineering GO is not production GO**; don't demand generic ProgressPlan, general Inbox or production local Pump as new Kernel sub-stages |
| **3 — Architecture reassessment** | ADR 0009 accepted, Stage 2 experimental safety evidence; compare six-state semantics vs any proposed five-state benefit, public API/wire/DDL consumers, negative tests, migration/rollback and ongoing operator cost. **Default: keep six states.** | No automatic five-state migration, public breaking contract or production release |
| 4 — Selected Hosted integration | [PG16/Redis8 SDK/adapter fault contract](audits/2026-10-17-stage4-hosted-orphan-delivery.md) verified for test Host, including configured AOF process SIGKILL recovery; real product Host/Provider/tenant authorization remains conditional release evidence | **Experimental engineering GO ≠ production Hosted GO**; no new Kernel state or automatic Stage 5 |
| 5 — Optional embedded Features/OS | Per-Feature SQLite conformance, device filesystem/lifecycle failure evidence | Desktop evidence does not certify mobile |
| 6 — Evidence-led optimization | Baseline/after A/B metrics, proof retention, accepted thresholds and explicit approval | No speculative GC/JSON Patch or extra scheduler state |

**Release gate independent of Stage 3 entry:** Stage 3 may review designs
without a new implementation or release. If a **separate ADR** later
approves breaking Run status, persisted-state or public API changes,
coordinate their SQLite/PostgreSQL/Queue migrations, Go/TS/HTTP consumers,
worker drain and tested rollback with the selected Stage 4 Host deployment
before publishing. Do not advertise Hosted or mobile production support
from experimental Kernel/Memory fixtures alone.

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
- **P0 Feature eligibility (conceptual names, not a plan enum):** a
  `running` Run awaiting an external signal receives an unrelated
  self-wakeup / Direct Resume; a Feature-owned retry `RetryAfter` is
  not yet due; an unknown in-flight Tool result receives blind resend;
  all **reject external dispatch**. Correct signals/child states
  release only their own exact wait. ManualGate never unblocks
  from generic Resume.
- **P1 segment:** Claim once, make 3+ internal CAS commits under the same Store epoch, verify no extra **execution** claims; crash mid-segment,
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
compilation. Any **separately approved future breaking contract** must pass
its selected Host/consumer acceptance before publication; Stage 3
reassessment alone makes no breaking change. Real provider
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
