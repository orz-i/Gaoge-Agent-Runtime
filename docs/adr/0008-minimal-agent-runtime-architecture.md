# ADR 0008: Minimal Agent Runtime Architecture (Embedded-First, Durable by Choice)

- **Status:** **Accepted, amended by ADR 0009 on 2026-10-17**. Stage 1 verified; Stage 2A/2B/2C **bounded experimental Kernel engineering exit GO** under the amended scope. **Full product Host/Provider/mobile/production acceptance remains NO-GO**; no breaking migration or release is authorized.
- **Date:** 2026-10-15
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Scope:** Gaoge-Agent-Runtime Go SDK; desktop/mobile embedded and hosted execution
- **Acceptance decision record:** [Stage 0 SQLite and ReadyWork choices](../audits/2026-10-15-minimal-runtime-stage0-decisions.md)
- **Implementation evidence:** [Stage 1 single-executor SQLite Store audit](../audits/2026-10-15-minimal-runtime-stage1-prototype.md); [Stage 2 Phase 1 execution-fence evidence](../audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md); [Stage 2 final engineering exit audit and unfulfilled normative gates](../audits/2026-10-16-stage2-final-exit-audit.md)
- **Normative test and migration annex:** [Minimal Agent Runtime — implementation and conformance contract](../minimal-agent-runtime-implementation.md)
- **Accepted amendment (2026-10-17):** [ADR 0009 — Minimal Kernel recovery surface](0009-minimal-kernel-complexity-reassessment.md) supersedes **only** mandatory seven-kind ProgressPlan, duplicate ActiveSegment and forced five-state Stage 3 hard cut. The six Run statuses and all execution/receipt/security invariants remain accepted. [Stage 3 admission audit](../audits/2026-10-17-stage3-admission-after-adr0009.md) records the bounded design-entry decision.
- **Related:** [Current architecture](../architecture.md), [ADR 0001](./0001-runtime-durability-governance-v1.md), [ADR 0005](./0005-durable-agent-execution-allowances.md), [ADR 0006](./0006-runtime-owned-auto-allowance-segments.md), [Reliability evidence](../reliability.md)

## Context and decision

Gaoge must be an **embeddable Agent Runtime**, not a small imitation of
Temporal/Cadence. Preserve the reliable boundaries already built around
Agent execution, while refusing infrastructure that is unnecessary for
the local Agent Loop.

The target is **Agent Loop + explicit state commit + safe resumption**.
One feature-neutral Run Kernel serves ephemeral, embedded-durable and
hosted-durable profiles. Storage and activation mechanisms vary;
authorization, receipt and CAS safety invariants do not.

The Kernel is **not** an event-sourced deterministic-replay engine or
a distributed transaction coordinator. A Run Snapshot, not a full
historical replay, is the authoritative recovery state. A physical
external Tool/model call can execute twice after an ambiguous crash;
the SDK guarantees fenced state advances and logical receipt
consumption, not downstream physical exactly-once.

### Verified baseline

As of the stated commit:

- `go/agent-runtime/kernel` imports only the Go standard library but
  shares a module with `agent`, `workflow`, `team`, `runrelation`,
  `continuation` and other Features.
- Kernel supports six statuses (`running`, `waiting_input`,
  `paused_budget`, `completed`, `failed`, `cancelled`) and full-JSON
  state replacement guarded by one Run revision CAS.
- `kernel.Store` embeds event reads and a committed-transition
  Outbox; `Wakeup/WakeupAt/WakeupGeneration` and terminal projections
  are already implemented.
- PostgreSQL atomically commits Run/journal/outbox and has Outbox
  claim leases; Redis and Memory queues already support delivery
  claims and dead letters. `runrelation` remains separate.
- `agent.Runner` already persists stable model/Tool intent identities,
  receipts and bound approvals. `compose` owns explicitly selected
  Worker lifecycle. Memory is not process-restart durable.
- SQLite is currently exercised by tests of the PostgreSQL adapter;
  there is **no independent production embedded SQLite adapter**.
  These existing adapters are not proof of mobile portability.

## Decision 1 — One Kernel, three deployment profiles

| Profile | Store | Activation | Infrastructure |
| --- | --- | --- | --- |
| **Ephemeral** | Memory | Direct in-process Agent Loop | None; no crash durability claim |
| **Embedded Durable** | SQLite | Direct Agent first; optional on-demand Host activation when implemented/verified | No daemon, Redis, PostgreSQL or network listener |
| **Hosted Durable** | PostgreSQL | Continuous/scheduled workers, optional Redis queue/feed | Deploy only what workload needs |

The Kernel exposes Run identity, lifecycle, single-Run CAS, opaque
feature state/checkpoints/results, and **atomic recovery obligations**.
The Agent Engine, Workflow, Team, Harness and model/tool layers
**consume** Kernel; none are inside it. Static Go construction remains
the default. `compose` / `bootstrap` own lifecycle and configuration.

```text
Embedding App / Hosted App
       |              |
       +-- Agent / Workflow / Team / Harness (Feature-owned behavior)
                             |
                       Minimal Run Kernel
                 identity / CAS / lifecycle / commit
                             |
                Memory / SQLite / PostgreSQL
                        |             |
                  local activation   optional hosted delivery / Redis
```

Event feed, Session history/branching, transport adapters and telemetry
are distinct from authoritative Run state. JSONL can be a Session
export format, not the atomic commit source. Embedded applications
must provide stable, installation/profile-scoped Actor/Thread
references without inventing globally trusted device or tenant
credentials. **Identity is not authorization.** Embedded V1 defaults
to a single trusted Profile with its own protected SQLite database;
a shared database across Profiles is permitted only behind a mandatory
scope-authorizing facade for every read, mutation, approval, budget
operation, work claim and Tool capability. Raw Kernel Run-ID APIs
are trusted internals, not safe untrusted-user endpoints. See the
[profile boundary tests](../minimal-agent-runtime-implementation.md).
No new standalone Kernel Go module is justified until API stability
and measured dependency benefits warrant it.

**Embedded V1 promises only durable Direct Agent** (including the
bound approval/wait behavior stored with that Agent). Durable
RunRelation, Workflow definitions/effects, Team/GroupChat topology,
Harness Sessions/Turns/Context/Artifacts and shared budget ledger
each need their **own** SQLite implementation and crash conformance
before they can be advertised as supported. A Memory fallback must
not be misrepresented as durable. See the [feature matrix](../minimal-agent-runtime-implementation.md).

## Decision 2 — Retain six Run states and Feature-owned gates (ADR 0009)

**ADR 0009 amends the former five-state target:** do not replace the current
six statuses (`running`, `waiting_input`, `paused_budget`, `completed`,
`failed`, `cancelled`) with a mandatory `suspended` status. The existing
Kernel transition validation remains authoritative. No mandatory persisted
gate enum is added to reinterpret the approvals and budget pauses already
represented by Feature State and Checkpoint.

**Run status is not permission to execute.** `running` can have a scheduled
model retry, an unresolved external Tool wait, a stable in-flight invocation
or a child-terminal dependency. The owning Feature derives the current
eligibility from persisted Feature State, Checkpoint, stable intent/receipt,
committed Outbox work and optional Run Store execution lease. It validates
exact call/wait/generation/due-time identity and known Provider dispositions
before Model/Tool dispatch. Incorrect or unknown facts **fail closed**.

- **Manual/privileged gates:** `waiting_input` and `paused_budget` keep exact
  Feature approval/allowance identity and may only be released by the
  authenticated host's narrowly validated control CAS; a generic Resume or
  timer does not grant approval or budget.
- **Condition gates:** wait/signal, child, timer and retry become eligible only
  after their Feature-specific evidence is verified. The same physical wait
  may have alternative signal/timeout triggers, never independent permissions.
- **Recovery gates:** stable model/Tool intent and unresolved external COMMIT
  require receipt verification; an expired Store lease is not permission to
  repeat an unknown physical effect.
- **Execution gates:** Native Direct and any configured activation Worker
  contend for the one Store epoch and must check current Feature facts and
  exact Run Revision under their existing protected execution path. A queue
  claim is only a candidate and cannot bypass Run quarantine.

**No automatic five-state migration is authorized.** Stage 3 reassesses the
six-state contract and cost of any proposed change; a later hard cut needs
its own positive evidence, separately approved ADR, migration/rollback plan
and coordinated Host/consumer release gates. ADR 0005/0006 approval and
budget safety requirements remain intact.

## Decision 3 — Recover from existing Feature facts and committed delivery

**One recovery authority, not a second ProgressPlan:** a durable nonterminal
Agent or optional Workflow/Team must leave its next action discoverable from
its current Feature State/Checkpoint, stable invocation/effect intent and
receipt, and the existing committed `TransitionOutbox`/SQLite ReadyWork view.
The Kernel stores these facts atomically with Run Revision CAS and Events;
it does not parse private Feature JSON into a second seven-disposition state
machine. The owning Feature proves the actual recovery condition in negative
Conformance tests. Unknown or malformed state is not implicitly runnable.

The former names `ReadyNow`, `ReadyAt`, `AwaitSignal`, `AwaitChild`,
`ManualGate`, `InFlightRecovery`, `ActiveSegment` and `Terminal` are
**examples of logical obligations, not mandatory persisted types**:

| Existing authoritative fact | Required recovery behavior |
| --- | --- |
| First committed Run wakeup / due Outbox record | Direct and a configured local/Hosted activator compete for the **same** Run work identity; no second execution authority |
| Feature-owned retry `ReadyAt` and wait generation | Early Direct/Queue invocation cannot dispatch; verify the same due fact and generation before work |
| Current Checkpoint, blocked allowance and exact control grant | Privileged decision is validated by the Feature and committed via guarded control CAS; no arbitrary bypass |
| Tool/Model intent and receipt, unknown external outcome | Retain stable physical operation identity and reconcile before any blind retry |
| Run Store epoch, owner, lease and expiry | This **already is** the active execution segment; intermediate CAS must not require a new persisted `ActiveSegment` kind |
| Child relation/terminal fact (optional Feature) | Parent wait consumes only the matching relation/generation; late handoff remains recoverable |
| Terminal Run and its transition fact | Never re-execute or revive; preserve optional parent handoff facts |

Persist a **single minimal missing discriminator** (e.g. retry due time or
wait identity) in the existing Feature/Run record only if a concrete failing
crash/recovery test demands it. Do not add a generic `ReadyWorkSource`,
ControlCommand queue, independent inbox or independent operation ledger by
default. Embedded Direct Agent needs no always-on local daemon or Redis.

A pre-wait external callback may arrive before the Feature has committed the
final Tool wait. Prefer dispatching with a precommitted stable operation ID;
otherwise a **real authenticated host** must retain and redeliver the signal
from its durable ingress. The SDK must return a retriable rejection rather
than falsely ACK an unmatched callback. An actual selected ingress/Provider
requires its own delivery and permission tests, not a generic mandatory
Kernel inbox.

**Uncertain Store commits:** Native Create uses a caller-known stable Run ID;
a host that enables opt-in `ApplyIdentified` may find its original operation
receipt in the existing atomic EventJournal even after a newer revision.
Without an identifiable, retained proof a newer Run Revision is **not**
evidence of that operation's COMMIT or rollback. Never rebase the operation
or mint a new physical effect identity. Current plain `Apply` remains a
legacy CAS and does not automatically obtain this recovery guarantee.
Unsettled and redeliverable effects must retain their proof, and any future
GC/compaction must fail closed rather than delete unknown COMMIT evidence.

A Hosted commit may project its existing outbox to optional Redis; an Embedded
commit can record SQLite ReadyWork in the same transaction. A delivery job,
ACK or live telemetry stream is not another Kernel execution authority.

## Decision 4 — Lightweight execution segments and authoritative fencing

A native synchronous `StartRun` commits the initial Run and its recoverable
wakeup, then claims one **Store execution epoch** before dispatch. If the
Host composes a foreground/local/Hosted activator, it must contend for **the
same** persisted Run work and epoch, never run a second unguarded loop.

The existing Store `(RunID, Epoch, OwnerID, LeaseID, Expiry)` **is** the
active execution segment; no extra persisted `ActiveSegment` type is needed.
An owner may make several CAS commits within one bounded execution lease.
A crash leaves the same persisted Feature intent/receipt and Store lease for
fenced takeover and cautious external-effect reconciliation. Intermediate
wakeup/outbox records are delivery facts, **not** fresh execution permits.
Avoid needless per-step queue fanout where proven safe, but do not delete
existing unacknowledged work without consumer/expiry/receipt proof.

**Authoritative fencing lives in the same Run Store as the CAS.**
A Run-scoped monotonically increasing execution epoch and expiry
(or equivalent persisted authority record) are assigned atomically
when ownership is taken over. Every **execution-owned** `Apply`
checks expected Run Revision **and** active execution epoch/identity/
expiry using that Store's transaction and time authority. Merely
checking Revision, the Redis delivery lease, or a preflight query
is insufficient. Renewals/handovers are fenced. A stale owner may
not commit after its lease expires, even when no other claimant has
advanced the Run Revision. SQLite uses its own transaction; Hosted
uses PostgreSQL as the authoritative Run fence even when Redis
delivered the work.

**Authorized control commands are a separate, narrow mutation path.**
Approval resolution, budget grants, cancellation, deadlines, and
validated external signals may update state without an ordinary
execution claim, but must enforce the exact Feature permission,
checkpoint/operation identity and CAS. Cancellation, terminal
transitions and changes invalidating the active segment must also
revoke/bump its execution epoch atomically; other concurrent
control changes require the owner to reload and revalidate before
continuing. This is **not** a general bypass API for arbitrary
Feature execution.

Claims/renewals use the authoritative Store's current-time policy;
lease loss cooperatively cancels the runner. A remote effect may
still execute after cancellation or expiry, so stable invocation
IDs and reconciliation still gate retries and receipts. The
lightweight delivery states are `Ready -> Claimed ->
Acked / RetryPending / Quarantined`. ACK requires durable consumption
or proof the delivery is stale. **Quarantine must also be an
authoritative Run Store execution-eligibility block** for the exact
current work ID/generation; a queue-only DLQ flag cannot stop direct
`Resume` from bypassing isolation. Embedded writes that block in
SQLite; Hosted first records the block in PostgreSQL before best-effort
Redis dead-letter/ACK, then reconciles interrupted cross-store handoff.
If the authoritative block cannot commit, do not acknowledge and
discard the only recoverable delivery. Every Acquire/Resume rejects
a quarantined current work identity until an owner-authorized repair
command validates current Run and effect safety, clears the block and
atomically republishes eligible work. Quarantine never reports Run
success and must not starve unrelated work.

## Decision 5 — SQLite and OS lifecycle are optional activation hosts

Add a production `go/agent-runtime-sqlite` adapter (subject to driver/
build validation). **Embedded V1 uses one SQLite DB with an indexed
ready-work ledger**, not a Redis-like daemon or mandatory separate
Outbox-plus-Queue implementation.

SQLite transactions must couple Run CAS, receipt/checkpoint and
ready work. **Transactions are short-lived:** a logical execution
segment can span several atomic commits, but never holds an open
SQLite write transaction across model/Tool execution, network waits,
user approval, sleep or lease-renewal delays. Acquire/revalidate,
CAS+receipt+Feature State/Outbox commit, and lease renewal each use bounded short
transactions. Compare `BEGIN IMMEDIATE` for known writers against
`DEFERRED` under contention; bound busy handling and do not promote
a stale read snapshot to a writer. Test cross-process claim fencing,
WAL and single-writer contention, `SQLITE_BUSY` vs
`SQLITE_BUSY_SNAPSHOT`, backups, disk-full, checkpoint starvation and
migration failures. Evaluate
`synchronous=FULL` for critical records; do not infer power-loss
safety from process-kill tests. Driver/CGO choice, encryption,
key rotation, binary size, locking and filesystem behavior require
desktop/mobile-specific evidence.

**Temporarily inaccessible protected storage/key is not corruption.**
If file protection, Keychain/keystore or database access is denied
while locked, do not create a replacement empty DB, delete work,
rewrite schema or terminally fail a Run. Exit the current activation
without mutation and retry once the OS makes the original store
available. Actual corruption is separately diagnosed and fail-closed.

OS foreground return, launch, user Continue, network availability and
eligible Android/iOS background callbacks only *activate* a bounded
due-work scan; none is Kernel status or a permanent Worker guarantee.
A deadline or timer means "no earlier than due according to the
authoritative Store clock; on the next permitted execution
opportunity", **not** guaranteed wakeup while frozen.

Separate absolute user deadlines and `ReadyAt` from execution-lease
TTL/heartbeat scheduling. Live-process renewal intervals may use a
monotonic clock; persisted times require the Store's clock and
explicit skew policy. After restart, monotonic state is gone.
Large or backward wall-clock jumps must not silently authorize
expired-claim side-effect replay or rewrite deadlines. Bias toward
conservative revalidation/quarantine of ambiguous effects and accept
delayed progress where a reliable clock cannot be established.

Activation reopens/validates storage, reclaims eligible expired
claims under the Store's epoch fence, rechecks Feature eligibility,
runs bounded work, and commits receipts/new obligations before ACK.
It must honor CPU/time/battery budget.

## Decision 6 — Hosted and topology are optional, without weaker safety

Hosted retains PostgreSQL Run CAS/Outbox and bounded workers; Redis is
an optional queue/feed adapter, not a dependency of minimal Agent.
PostgreSQL-to-Redis is at-least-once projection, not one cross-store
atomic transaction. Redis claims carry *delivery eligibility* only;
a Hosted worker must obtain/renew a fenced **PostgreSQL Run execution
epoch** before doing protected execution and must verify that epoch
inside the authoritative Run CAS. A stale claimant cannot commit
even if its external effect remains in flight. Redis ACK after the
Run commit remains an idempotent transport step, not the state
machine's success proof.

`RunRelation` stays a **separately composed Feature-neutral topology
service**, not a Kernel aggregate. For a Feature that enables parent/
child work, correct recovery requires all three tests:

1. Child is terminal before relation creation.
2. Child is terminal after relation creation but before parent wait.
3. Child becomes terminal after parent wait, including process restart.

Relation creation and parent wait must check terminal child facts;
a bounded indexed/dirty-relation reconciliation pass repairs missed
handoffs. Watermarks advance only after durable enqueue or
Feature-proven parent consumption. Parent cancellation/join policy
remains Feature-owned; no implicit cross-Run transactions or cascade
semantics enter Kernel. **Embedded V1 does not yet claim this Feature.**

## Decision 7 — Deliberate exclusions

Do **not** introduce deterministic event-history replay, 2PC, a
generic DAG/orchestration engine inside Kernel, model/prompt/token/
Tool/MCP logic, RBAC, platform background APIs, or mandatory
Redis/HTTP/daemon workers. Do not mix Session JSONL with Run
transactional state.

Defer partitioned CAS, generic JSON Patch, a universal distributed
lease engine, multi-device Run merging, and a standalone Kernel
Go module until measured requirements justify a separate ADR.
Set an explicit **complexity budget** at Stage 0: public Kernel
surface, transitive dependencies, Run metadata and per-Agent-step
write amplification. If the common contract starts requiring a
general scheduler, revisit its boundary instead of adding another
mandatory abstraction. The initial single-Run revision CAS remains
the correctness boundary.
A future Hybrid owner transfer requires explicit fencing/handshake,
not silent concurrent merging.

## Rollout (ADR 0009 amendment; release authorization is separate)

| Stage | Outcome and exit rule |
| --- | --- |
| **0 · P0** | Inventory Feature and failure cases; establish baseline API/dependency/SQLite footprint and complexity thresholds. Completed engineering evidence does not certify production OS/device behavior. |
| **1 · P0** | Single-executor SQLite Direct Agent on **existing six states**; prove restart/receipt/approval without mandatory PG/Redis. |
| **2 · P0** | **Bounded experimental Kernel safety integration:** Store epoch and multi-CAS fencing, Feature-validated controls, Native Agent intent/receipt and due work, exact signal/poison and uncertainty failure proofs using existing State/Checkpoint/Outbox. Stage 2 engineering scope is **sealed** at its tested boundary; real Host, optional Profile, Provider and device acceptance remain **deployment/release gates**, not further Kernel sub-stages. |
| **3 · P1** | **Design review completed 2026-10-17 — retain six states.** [Measured compatibility decision](../audits/2026-10-17-stage3-six-state-design-decision.md) found no evidenced improvement from a five-state migration. Do not add a generic ProgressPlan or modify status/schema; any future breaking implementation requires a **separate approved ADR**. |
| **4 · P1** | Optional Hosted parity/Host authorization and Provider confirmation with real PostgreSQL and Redis delivery tests for a **selected deployment**. Publish only explicitly certified features. If a future breaking Kernel change is separately approved, coordinate its Stage 4 Host/consumer release gate. |
| **5 · P1** | Optional SQLite Workflow/Team/Harness and OS/mobile adapters only with per-Feature/device implementation and fault evidence. |
| **6 · P2** | Evidence-led state/journal compaction and CAS performance optimization, behind separate measured proposals. |

**Stage 3 design entry is not production eligibility.** It may follow the
completed Stage 2 experimental Kernel safety evidence while selected Host or
mobile release gates remain NO-GO. Stage 3 itself does not implement a
breaking migration or authorize a production deployment. For any separately
approved future breaking API/DDL change, require an explicit migration,
backup/rollback and coordinated module, Worker and consumer acceptance before
release; no partially published mixed-version SDK.

## Acceptance and negative gates

The [conformance annex](../minimal-agent-runtime-implementation.md)
defines exact crash windows, revision/lease races, ready-work truth
tests, older-record mappings, SQLite/OS failure cases, feature support,
device checks and per-stage evidence. No production support claim may
precede that evidence. Mandatory outcomes include:

1. No mandatory services for ephemeral/local Direct Agent; SQLite
   sequential prototype restores after process exit with no lost
   logical work, but is **not** certified concurrent until Stage 2.
2. Every required recovery path is in the same atomic commit; the
   **Feature eligibility predicate** rejects an unrelated trigger
   even when Run status is `running`.
3. Execution commits verify both Run Revision **and Run Store
   execution epoch/expiry atomically**, including Hosted Redis
   delivery and stale claims that have not changed Run Revision.
   Direct execution spans multiple CAS without a queue job per step.
4. No skipped approvals, excessive budget grants, stale-claim
   receipts, terminal revival or ambiguous Tool replay. Early
   signals and uncertain COMMIT responses are durably reconciled.
5. Parent-child late handoffs, locked keys, SQLite contention,
   wall-clock changes, poison quarantine and external queue faults
   recover safely in profiles where those Features are enabled.
6. Memory/unit tests do **not** stand in for real PostgreSQL/Redis,
   cross-process SQLite or Android/iOS lifecycle evidence. Record
   commit, PASS/FAIL/SKIP, workload-size/WAL baselines, migration/
   rollback and `make check`/relevant integration results.

## Alternatives and consequences

| Alternative | Decision |
| --- | --- |
| Temporal/Cadence deterministic workflow replay | Reject: over-constrains a general embedded Agent Loop |
| Volatile Pi-style Loop alone | Reject: cannot safely restore approval and effect receipts |
| Redis or separate queue service for every local app | Reject: unnecessary process/service dependency |
| JSONL as durable transaction ledger | Reject: no atomic Run CAS + receipt + due work |
| Put parent-child hierarchy into minimal Kernel | Reject: Feature-specific semantics, optional relation repair |
| Extract standalone Kernel module before API is stable | Defer: Go module count is not architecture quality |

Benefits: lighter embedding and clearer lifetime boundaries without
sacrificing previous receipt/CAS security properties. Costs: a real
SQLite adapter, Feature-owned recovery conformance and stronger
platform-specific evidence. A separate migration decision may be required
only if later justified by measured benefit.

**Accepted as an architectural direction on 2026-10-15 after the
final architecture review and user agreement.** The two outstanding
acceptance selections are documented in the
[Stage 0 decision record](../audits/2026-10-15-minimal-runtime-stage0-decisions.md):
`modernc.org/sqlite` is the initial `database/sql` desktop candidate
(pending a pinned security/conformance review), and ReadyWork V1 is
materialized directly alongside Run state in **one SQLite database
transaction**, without an independent queue service.

Stage 0's early desktop probes have been extended by a **Stage 1
single-executor SQLite Store prototype**, whose six-state Kernel
Conformance, atomic rollback, and cross-process receipt-recovery
tests are documented in the [Stage 1 audit](../audits/2026-10-15-minimal-runtime-stage1-prototype.md).
**The target architecture is not fully implemented.** Mobile
runtime/file-lock evidence, production security/license signoff,
quantitative release budgets and selected production Host/Provider/OS
release gates remain pending. The mandatory five-state Stage 3 hard cut
was superseded by accepted ADR 0009. Android/iOS compile/link evidence is **not** a
production support claim. Existing six-state SDK and ADR 0005/0006
behavior remains in force until a coordinated implementation release.
Accepting this ADR does not authorize business-code changes,
consumer upgrades or deployment by itself.

## References

- [Pi Agent overview (Chinese)](https://dg-ai-notes.pages.dev/modules/ch01-overview/) and [Pi source](https://github.com/earendil-works/pi) — minimal Agent Loop, composition and extensibility, not a normative durability model.
- [OpenAI Agents SDK Runner](https://openai.github.io/openai-agents-python/running_agents/) and [RunState](https://openai.github.io/openai-agents-python/ref/run_state/) — recoverable interrupted execution and Session separation.
- [SQLite transactions](https://www.sqlite.org/lang_transaction.html), [WAL](https://www.sqlite.org/wal.html), [`SQLITE_BUSY_SNAPSHOT`](https://www.sqlite.org/rescode.html#busy_snapshot), [PRAGMA synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous) — short write transactions, contention and durability.
- [Android persistent work](https://developer.android.com/develop/background-work/background-tasks/persistent) and [Apple BackgroundTasks](https://developer.apple.com/documentation/backgroundtasks) — OS-controlled execution opportunity.
