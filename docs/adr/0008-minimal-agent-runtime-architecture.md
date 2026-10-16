# ADR 0008: Minimal Agent Runtime Architecture (Embedded-First, Durable by Choice)

- **Status:** **Accepted architectural direction**; Stage 1 SQLite prototype verified and Stage 2 Phase 1 optional Store execution fencing underway; **not fully implemented or released**
- **Date:** 2026-10-15
- **Baseline:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`
- **Scope:** Gaoge-Agent-Runtime Go SDK; desktop/mobile embedded and hosted execution
- **Acceptance decision record:** [Stage 0 SQLite and ReadyWork choices](../audits/2026-10-15-minimal-runtime-stage0-decisions.md)
- **Implementation evidence:** [Stage 1 single-executor SQLite Store audit](../audits/2026-10-15-minimal-runtime-stage1-prototype.md); [Stage 2 Phase 1 execution-fence evidence](../audits/2026-10-16-minimal-runtime-stage2-phase1-execution-fencing.md)
- **Normative test and migration annex:** [Minimal Agent Runtime — implementation and conformance contract](../minimal-agent-runtime-implementation.md)
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
| **Embedded Durable** | SQLite | On-demand local activation pump | No daemon, Redis, PostgreSQL or network listener |
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

## Decision 2 — Five states, distinct recovery gates

The **target** Run lifecycle (introduced only after SQLite V1 proves
the existing six-state flow):

| Status | Meaning |
| --- | --- |
| `running` | Nonterminal and logically eligible; not evidence that a process is active |
| `suspended` | Nonterminal, blocked by a persisted and verifiable gate |
| `completed` | Immutable success, valid Result required |
| `failed` | Immutable failure |
| `cancelled` | Immutable cancellation |

Allowed transitions: `running -> running/suspended/terminal` and
`suspended -> running/terminal`; terminals never revive.

**Process availability, an execution claim, and Run status are
different dimensions.** A terminated or frozen app does not make
the Run `suspended`. Model retry delays, child waits and in-flight
Tool work need not become `suspended` automatically: they may remain
`running` when they have a durable recovery obligation.

A `suspended` Run carries a versioned Feature-owned gate descriptor
(gate class, exact wait ID, generation and referenced pending effect/
decision). The Kernel validates shape and CAS; the Feature validates
the specific condition and authority:

| Gate | Examples | Valid release |
| --- | --- | --- |
| **Manual/privileged** | Tool approval, budget grant, user input | Authorized Feature command binds exact checkpoint/Tool call/decision or exhausted budget dimension and hard ceiling |
| **Condition-verified** | Tool result, child terminal, wait signal, timer | Feature verifies exact current wait ID/generation and evidence |
| **Execution-recovery** | Expired model/effect claim | Reconcile stable effect outcome first; lease expiry is not permission for unsafe replay |

A queue delivery/timer is a *candidate for verification*, not an
authorization. **Status alone never authorizes execution, even while
`running`**. Every direct `Resume`, background pump and Hosted dispatcher
must evaluate the same eligibility predicate: nonterminal status,
current progress plan, matching trigger/work identity and generation,
required Feature condition (including due time), plus an effective
execution claim before side-effect dispatch. `ReadyAt` must be due;
`ActiveSegment` is exclusive to the current fenced owner (or eligible
for takeover after an expired lease and safe recovery checks);
`AwaitSignal` needs the matching committed signal; `AwaitChild` needs
verified child completion; `InFlightRecovery` permits reconciliation,
not blind redispatch. `ManualGate` cannot be bypassed by any general
resume entry point. An unrelated `ReadyNow` delivery is never authority
to advance a different pending wait.

Gate release uses Feature validation and CAS, committing the next
progress obligation atomically. A still-valid condition whose CAS
conflicts must be retried, **not silently acknowledged**. A stale
event can be acknowledged only after proving it no longer applies.
Approvals cannot authorize a later Tool and a budget timer cannot
grant model calls. The predicate and negative matrix are specified
in the [conformance annex](../minimal-agent-runtime-implementation.md).

**ADR precedence:** After acceptance **and coordinated release**, this
five-state model supersedes only the Kernel status enum/transitions
specified by [ADR 0005](./0005-durable-agent-execution-allowances.md)
and corresponding status assumptions in
[ADR 0006](./0006-runtime-owned-auto-allowance-segments.md).
All earlier grant authority, immutable hard ceilings, exact approval
binding and receipt guarantees remain mandatory. Until then the
current six-state contract is operative.

Hard-cut Go APIs are allowed, but persisted records are not
disposable by implication. Version the new schema; migrate old
`waiting_input` only after determining from the checkpoint whether
it was an approval, manual input or condition wait. Map
`paused_budget` only with validated allowance dimension/ceiling.
Unknown/malformed records block migration with diagnostics rather
than silently advancing. SQL, queues, client projections, workers
and release/rollback policy migrate together. See the
[migration matrix](../minimal-agent-runtime-implementation.md).

## Decision 3 — Every durable transition declares how progress resumes

**The Feature, not the Kernel, declares a typed progress plan** on
every durable nonterminal `Create` and `Apply`. A plan has one
authoritative logical progress phase and may expose **multiple
alternative wake conditions** (for example an external signal **or**
its timeout) tied to the same wait identity. An individual wake
record is only a trigger for that plan, not another independent
execution permission. A Store transaction atomically commits:

1. Snapshot and expected-revision CAS, Feature checkpoint/intent/
   receipt/result state.
2. Any journal facts promised by the profile.
3. The required next-work record or durable wait/reconciliation
   reference, with stable identity and eligibility condition.

Conceptual dispositions:

| Kind | Durably recorded obligation |
| --- | --- |
| `ReadyNow` / `ReadyAt` | Indexed work with stable Run/revision/trigger/generation and optional due time |
| `ActiveSegment` | Persisted Run execution owner/epoch/lease and expiration index; no new queue row for each internal CAS |
| `AwaitSignal` | Validatable external signal ID, generation and reconciliation source |
| `AwaitChild` | Stable child/owner identity plus late-child repair source |
| `ManualGate` | Exact manual authority/checkpoint/decision binding; no automatic execution |
| `InFlightRecovery` | Stable effect ID, claim/reconciliation eligibility, ambiguous result policy |
| `Terminal` | No more self-work; retain terminal child fact for optional relation recovery |

The Kernel validates **presence and structural consistency**, status
compatibility, matching wait/generation, and atomicity. It cannot
infer business needs from opaque JSON; Feature conformance tests
must prove a truthful progress plan at **every** autonomous
boundary, including alternative triggers. Missing or unknown plans
fail closed, not as implicit "no work". `Create` has the same
requirement as `Apply`: a crash after Run creation must not lose
the first opportunity to run.

For external callbacks that may race ahead of waiting-state
publication, **prefer committing a stable operation/wait identity
before external dispatch**. If a protocol cannot enforce that order,
the embedding application's durable ingress must atomically record
an authenticated, bounded, deduplicated early signal keyed by that
identity, then correlate and consume it with the eventual wait
via CAS. Uncorrelatable signals fail closed; a transient in-memory
callback or unbounded orphan inbox cannot be the recovery source.

A Store commit can succeed although its caller receives an error
(e.g. transport failure after COMMIT). Each state-changing request
must have a stable logical operation/commit identity discoverable
after later revisions; on uncertain outcome, **read and reconcile
the committed identity/receipt before any retry**. For `Create`,
the caller must provide a stable Run ID or a scope-unique,
queryable idempotency key: generating an unobservable new Run ID
after an uncertain Create is forbidden. Existing `RequestID` does
not automatically imply uniqueness without a Store contract.
A newer Run revision alone is not proof that the particular
operation committed. **Unsettled operations and any identity that
might still be redelivered cannot lose their deduplication proof
through age-based journal cleanup.** Retain a compact durable receipt
or tombstone until the operation is settled, all relevant retries and
deliveries have a proven terminal disposition, and the supported
replay horizon has closed. Indefinitely offline devices cannot be
assumed to have passed that horizon. Do not recycle a retired logical
operation ID or issue another external effect merely because its
original COMMIT response or historical record is unavailable.

An embedded SQLite commit may write `ready_work` directly in the same
transaction. A Hosted commit may use the existing Outbox and
idempotently project into an external queue. **The durable progress
record is necessary; the separate queue service is optional.**
Event journal and live telemetry are not execution authority, and
their existing public semantics must not be removed without separate
conformance. Keep the public Run API narrow (`Create`, `Load`,
`Apply`, optional `Cancel`/`Expire`); expose ready-work claiming and
event reads as separately composed ports, not a universal scheduler.

## Decision 4 — Lightweight execution segments and authoritative fencing

A synchronous `StartRun` first commits its initial `ReadyNow`, then
acquires the **same logical work identity** available to foreground
Continue or the activation pump. These are competing activators,
never separate unguarded Agent loops.

**Do not claim a new queue job on every internal CAS.** A bounded
**execution segment** holds one Run-scoped authority across several
model/tool/state commits while the owner is alive and remains
eligible. Its `ActiveSegment` obligation is persisted and indexed by
lease expiry so a killed process cannot strand a Run. Intermediate
mutations preserve that recovery evidence without publishing a
second `ReadyNow` job. The segment ends at terminal,
manual/condition wait, deliberate yield, cancellation, or lost
authority. Yield atomically publishes the *next* stable work identity
and relinquishes the previous segment; resuming requires a new
claim. No work item is "claimed" twice just because Revision
increased within one execution segment.

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
or proof the delivery is stale. **Quarantine must also become an
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
CAS+receipt+plan commit, and lease renewal each use bounded short
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

## Rollout (separate implementation authorization required)

| Stage | Outcome and exit rule |
| --- | --- |
| **0 · P0** | Inventory Feature transitions, execution-vs-control mutations and crash cases; measure binary/startup/RSS, public Kernel API/dependency footprint, per-step writes/metadata and short/long-state SQLite bytes/WAL/latency; fix reviewed complexity/performance thresholds and migration plan |
| **1 · P0** | **Single-executor SQLite Direct Agent prototype using current six statuses**; prove sequential restart/receipt/approval safety, no PG/Redis; **not yet concurrency-safe Embedded Durable** |
| **2 · P0** | Implement authoritative Store execution epoch, execution segments, shared eligibility predicate, signal ordering/inbox and ambiguous-commit reconciliation; pass concurrent direct/Pump and Memory/SQLite/PostgreSQL contract tests **before** claiming production durability |
| **3 · P1** | **Coordinated five-state hard cut** and gate-aware Feature/Dispatcher, migrations and API/wire/client tests. Code may be merged internally, but **no independently published breaking-contract SDK** before Hosted Stage 4 is accepted (unless Hosted is disabled and explicitly experimental) |
| **4 · P1** | Hosted equivalence including PostgreSQL Run fencing with optional Redis delivery; safe outbox/queue/reconciliation retained while Continuation is simplified. **Joint Stage 3+4 release gate** for any production-facing breaking SDK contract |
| **5 · P1** | Optional SQLite Workflow/Team/Harness/ledger and OS adapters **only with per-Feature/device evidence** |
| **6 · P2** | Evidence-led snapshot/state compaction, CAS optimization or further split behind separate ADR |

**Hard-cut API freedom does not waive persisted-data safety.**
Any release must choose explicit migrate/reject/backup-and-rollback
semantics and coordinate all Go modules and consumers. **Completing
Stage 3 is a development milestone, not a release authorization:**
production-facing breaking APIs must also pass Stage 4's Hosted
parity gate. A deliberately disabled Hosted experimental surface
must not be advertised as supported. Do not publish an intermediate
SDK that misinterprets old approvals or budget pauses.

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
SQLite adapter, new progress/gate contracts, coordinated status
migration and stronger platform-specific evidence.

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
quantitative release budgets, Stage 2 execution ownership and the
Stage 3/4 five-state/Hosted release gates remain pending. Android/iOS compile/link evidence is **not** a
production support claim. Existing six-state SDK and ADR 0005/0006
behavior remains in force until a coordinated implementation release.
Accepting this ADR does not authorize business-code changes,
consumer upgrades or deployment by itself.

## References

- [Pi Agent overview (Chinese)](https://dg-ai-notes.pages.dev/modules/ch01-overview/) and [Pi source](https://github.com/earendil-works/pi) — minimal Agent Loop, composition and extensibility, not a normative durability model.
- [OpenAI Agents SDK Runner](https://openai.github.io/openai-agents-python/running_agents/) and [RunState](https://openai.github.io/openai-agents-python/ref/run_state/) — recoverable interrupted execution and Session separation.
- [SQLite transactions](https://www.sqlite.org/lang_transaction.html), [WAL](https://www.sqlite.org/wal.html), [`SQLITE_BUSY_SNAPSHOT`](https://www.sqlite.org/rescode.html#busy_snapshot), [PRAGMA synchronous](https://www.sqlite.org/pragma.html#pragma_synchronous) — short write transactions, contention and durability.
- [Android persistent work](https://developer.android.com/develop/background-work/background-tasks/persistent) and [Apple BackgroundTasks](https://developer.apple.com/documentation/backgroundtasks) — OS-controlled execution opportunity.
