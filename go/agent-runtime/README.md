# Agent Runtime Core

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime` is the host-neutral Agent Runtime
Core. The module is split into explicit capability packages such as `kernel`,
`agent`, `handoff`, `tools`, `interaction`, `runrelation`, and `continuation`.
The only built-in orchestration is Agent/SubAgent delegation, not a separate
Team/GroupChat/Workflow engine. There is no compatibility facade or automatic
Runtime-kind selector.

For an in-process durable-contract implementation, use `memory`:

```go
store := memory.NewStore()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
```

Production hosts normally pair Core with the PostgreSQL Store and either the
Redis or memory generation stream adapter.

**Agent state wire hard cut:** original state evidence v3 removes the duplicate
`ModelInvocation.Usage` field (use `Response.Usage`) and omits empty consumed
requests. The original response and transcript are retained. Existing v2 state
is not rehashed or resumed by the v3 decoder; drain it using the previous
writer. SQL schema migration does not convert Agent state.

## Optional Agent Run execution lease (original Stage 2B)

**Tool retry/disposition contract:** Even an explicit
`tools.NewRetryableExecutionError` requires a trusted, original-Call-ID
`rejected` no-effect receipt before another physical Tool call.
Use `tools.RetryableNoEffectResult(callID, err)` only when the Handler
proves it did not send a remote effect. A normal Tool result must have
`read`, `committed` or `pending` disposition; an error-free `rejected`
is invalid. A previously dispatched shared budget reservation may
still decline a same-ID retry. Unknown physical outcomes require
authoritative read-only reconciliation rather than a second send.

**Tool durability in every execution mode:** An external Tool effect now
commits its exact original Call ID as an Agent dispatch intent **before**
the Handler runs, even when `RunExecution` is not configured. Missing or
unverified receipts block automatic replay and require a trusted read-only
`ToolOutcomeVerifier`. Correctable errors from a Handler after dispatch
are not automatically no-effect proof; they require an exact-ID rejected
no-effect receipt via `tools.RecoverableNoEffectResult`. Shared Tool budgets
also fence already-dispatched Held reservations. This does **not**
certify concurrent multiworker use of the unleased path. See
[ADR 0013](../../docs/adr/0013-uniform-tool-effect-intent-and-receipts.md)
and the beta.24 breaking-change ledger.

**Stage 2 governance:** The [final Stage 2 exit audit](../../docs/audits/2026-10-16-stage2-final-exit-audit.md) closes the previously demonstrated 2A/2B/2C experimental engineering increments, **not** ADR 0008's full production acceptance. A [subsequent hard-gate correction](../../docs/audits/2026-10-16-stage2-hard-gates-followup.md) adds Run Store quarantine and exact internal repair, self-wakeup source binding and partial Direct/Worker concurrency proof. A [subsequent terminal-DLQ handoff test](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md) now verifies protected Queue expiry and PG-quarantine-before-Redis-Nack ordering. Actual product host authorization/Provider identity, migration of old unprotected Jobs and full Direct/Pump/Hosted eligibility are **still not certified**. Do not treat this opt-in API as unattended production multi-worker certification.

`agent.Dependencies.RunExecution` optionally configures the Root execution
owner and lease duration. When unset, the Runner creates a unique internal
owner and a checked lease duration covering the configured call window.
Both configurations require Execution, ChildExecution and OwnedPhysicalExecution
Store capabilities. Root middleware and the full synchronous Next lifetime are
covered by one durable physical scope bound to the original execution lease.
A caller-bound `Runtime.WithExecution` lease is reused without acquiring,
renewing or releasing the caller's lease.

Explicit configuration still requires a caller-stable nonempty Start Run ID.
Default configuration retains auto-generated Run IDs. A failed or lost Create
response must be reconciled by the original Run ID, not retried with a new one.
Root creation reserves execution authority atomically; old epoch-zero Runs
cannot be upgraded while unleased executors may still be running.

Before each Model send, the original invocation and attempt receive a durable
dispatch-intent marker. A dispatched invocation without an authoritative receipt
returns `ErrModelOutcomeUnknown`; neither RetryAfter, lease expiry nor a generic
retryable error permits resending. A pre-dispatch denial remains distinct from
an uncertain physical outcome. No background recovery worker is started.
A completed receipt may be consumed without reissuing the original invocation.
Root Host errors retain the original scope; TTL cannot manufacture completion.
`ReconcilePhysicalExecution` can retire a durable completion marker or an
original grant-bound callback-returned completion record with all physical intents settled.
A clean snapshot alone is insufficient. A Run status alone never proves callback completion; the original opaque
completion record or durable settlement marker is required. This is not production multi-worker certification.

Tool consumption is independently qualified against the actual executor or
native settled ledger replay. Middleware must preserve the original result,
Call identity, and original error, and first-party persistence failures fence
consumption before the next Model call. A returned physical Grant may bind
multiple outstanding Tool Calls, each to its own original ledger and request
(and receipt digest when settled). Recover held and settled Calls independently;
only the original whole callback and all resolved obligations qualify Grant
retirement. Result classification is not a new Grant control custody.

The original Call return also binds the actual completed approval policy or
approved checkpoint and (when available) the exact independent owner result.
A recovered Tool consumes that proof without a new approval callback. The
read-only owner check includes staged recovered results as well as consumed
receipts; neither zero local usage nor a successor callback upgrades original
custody. Only the native Tool Budget adapter settles the original ledger
reservation. Host outcome verifiers read evidence and must not write Budget
settlements. Newly bound Calls reject unsealed legacy receipts.

Pending approval evidence is captured at the actual qualified Tool return,
independently of its ChildTerminal, ExternalResult, Scheduled or descriptor-less
wait representation. Independent Child calls may still be launched in one
batch. A later restrictive approval decision is parked against its exact
unsent Call while the original physical frontier drains; only a subsequent
owner may create and publish its approval Checkpoint. Local Tool allowance
counts deferred physical obligations before new Call admission. An exhausted
allowance cannot pause a batch with unresolved children.

The native Tool admission state separates authorization evaluation, committed
restriction, committed authorization, capacity waiting and unsent closure. The policy is
invoked only after evaluation intent is durable; uncommitted decisions keep
the original physical Grant fenced. Capacity is reserved against the original
ledger before entering the fixed Tool stage or writing a dispatch intent. An
unsent wait is not a provider Pending receipt, and a later owner can retry
admission only with the same Call, committed authorization and bound ledger.
A local Tool permit is acquired before shared capacity, and the physical
intent is written immediately before the fixed Tool stage entry. The no-entry
witness survives the intent acknowledgement and the final local and original
ledger admission checks. Before entry, a native no-send disposition distinguishes terminal admission denial
from resumable interruption. The disposition commits before ledger release
or park; both operations verify the original undispatched, zero-usage
reservation at its exact ledger revision. The original returned Root Grant
can also seal no-entry proof for an uncertain dispatch-intent ACK. That
proof reconciles the original marker without a provider lookup, and never
qualifies a Tool result. Tool stage entry alone cannot prove no-send, but
the actual native Budget Dispatch CAS can definitively refuse the exact
original reservation under elected ledger cancellation. If that frame has
not entered the Executor and the fixed stage returns its trusted receipt
and the exact original denial, Runtime consumes the same native no-send
disposition. A speculative dispatched candidate or arbitrary error does not qualify. Pending physical effects and their settlement
continue to use the existing Call-return proof.

Native Run admission uses the same exact-return qualification for all
Start/Prepare/Resume/Approval and managed continuation invocations. The
actual bounded Budget Run frame can record an acknowledged original
cancellation denial before entering Next. Only its invocation-bound return
and completed callback lifetime, together with the unchanged original
state, qualify retirement. The elected Kernel control retains authority
over business cancellation. Host projection failure, opaque error
classification, and unresolved original effects are not native returns.
The actual Tool denial uses the same return identity, with a per-attempt
callback qualification before no-send consumption and the existing whole
Grant return qualification before retirement. The original elected Harness
cancellation can reconcile that sealed return with earlier physical child
obligations without entering a new Agent invocation or querying a result
verifier for the unsent Call.

Receipt consumption and Grant retirement are separate durable transitions
but one original recovery scope. Once all original physical obligations
have drained, the same original callback completion and exact current
source drive atomic Finish (including full-Snapshot settlement validation,
managed closure, receipt and owned Lease release), even if a receipt CAS
response was lost. Parent-bound borrowed Grants retain their original Mark
return witness; Owned Grant retirement does not write an intermediate Mark. The current
final receipt is selected only by the exact source, epoch and Lease
disposition; historical receipts never authorize a successor. `RootDeferredCleanupEligibility`
includes the retirement-only phase, and its observed scope may be passed
to `ReconcileRootDeferredChildren` to prevent reselecting a different
Grant. Elected Harness cancellation resumes this same scope without a
business Resume or repeated result lookup; an already committed Failed
or Completed status is preserved while its exact retirement finishes.

Every dispatched Model invocation durably binds the exact physical SDK request
through its `SamplingPlan`. Context and SharedBudget admission freeze that plan
before the invocation is committed; otherwise the final dispatch boundary freezes
it. A budget reservation retains its logical admission identity and separately
binds the same `model.PhysicalRequestHash` as the sampling plan. Once frozen,
late middleware cannot alter the physical request. The versioned digest binds typed SDK request evidence, including exact original
Tool/Provider JSON bytes and the distinction between missing RawMessage values
and explicit JSON null; it is not the provider-native wire payload or a token-count
serialization.

The Runtime's native budget coordinator atomically commits observed usage, hosted
Tool receipts and the Model response's execution qualification. A rejected
completion is charged but retains only billing identity and its rejection code,
never executable assistant content. Accepted receipt bytes are losslessly sealed.
Native replay and read-only recovery interpret the same sealed qualification.

An explicitly configured `ModelOutcomeVerifier` receives the reconstructed
physical request, not the logical intent. For a frozen SharedBudget reservation,
its settled native receipt is authoritative; an external Provider receipt cannot
override a rejection. While the original reservation is still held and the Host
return is unknown, external evidence may be retained without granting consumption.
Recovery never invokes generation or replans settlement. A
previously dispatched record without the required physical identity remains
unknown; recovery never guesses a hash, replans or falls back to logical lookup. If
the original Host callback completed, receipt recovery can retire its scope.
If that Host callback remains unknown, complete authoritative response evidence
may be saved without consuming it: the original scope, errors, messages, Tool
state and SDK budget usage stay unchanged. Billing-only evidence cannot promote
a pending invocation to completed. No wakeup or further execution is authorized
by this evidence-only write.

A native Tool writes a durable dispatch intent containing the stable
Call ID **before** executing. The returned native receipt must match
that ID exactly. Pending or unknown outcomes retain this marker,
so `Resume` cannot blindly re-execute the Handler.

For a confirmed external COMMIT, configure a trusted provider-side
read-only lookup through `agent.Dependencies.ToolOutcomeVerifier` and
invoke `runner.ReconcileToolOutcome(ctx, runID, revision, callID)`.
The Runner invokes this verifier with the immutable Tool request and
persists the matching receipt without calling `Tool.Execute`.
Without a verifier the Run remains blocked; callers cannot pass an
arbitrary `ExecutionResult` as evidence of a COMMIT. Host-side caller
authorization and actual external provider verification remain required.
Late known Model/Tool success is persisted and reported via
`ErrModelLateResult` / `ErrToolLateResult`.

A clean pause releases the claim; after a trusted approval/budget
control, invoke `Resume` to acquire a new Epoch. For an **already
persisted external Tool wait**, `NotifyExternalToolResultReady` now
uses the same guarded Control CAS and Store deadline as other native
controls. This stores readiness without re-running the Tool; a trusted
provider receipt must still be reconciled to finish it. When a Native
Provider signals during `Tool.Execute` **before** the Pending wait is
committed, `NotifyExternalToolResultReady` returns
`ErrToolSignalNotReady`. This is a **negative acknowledgment**: the host
must retain the signal in its own durable delivery source and retry
after the ToolWait exists. The Kernel does not buffer early signals.
ReadyWork projector leases and ACKs are not permission to execute a Tool.
An exact native ready-wakeup now attempts the existing trusted Provider
lookup; until an external COMMIT is verified, Dispatcher returns an error
for queue NACK instead of falsely ACKing an unresolved Tool. A native
ToolWait that is overdue but still uncertain reports
`ErrToolOutcomeOverdue` while retaining its durable Call ID.
Transport ACKs may arrive after lease expiry only while that exact
Worker/Lease is still current; a replaced worker cannot ACK.
See the [Stage 2C Run-isolation and dispatch remediation](../../docs/audits/2026-10-16-stage2c-run-isolation-and-dispatch-remediation.md).
None of this guarantees physical exactly-once effects.

### Experimental Run-level poison fence

`kernel.Runtime.QuarantineExecution` writes a durable, exact Run/Revision/Generation diagnosis to the **existing Run authority** and invalidates the active execution epoch. Memory, SQLite and PostgreSQL now refuse Acquire, pre-dispatch Check and fenced Apply until a trusted host invokes exact `RepairExecutionQuarantine` or the Run settles terminally. An unrelated nonterminal Feature control cannot silently clear the block. An execution-lease-bound Runtime cannot set or repair it. These methods are **trusted-internal control capabilities**, not authenticated public endpoints: the embedding host must verify the active Run owner, poisoned Feature work and any ambiguous Provider outcome *before* using either method. For newly projected Continuation work, Scheduler opts into `queue.Policy.RequireTerminalFence`: Queue `Nack` and automatic lease-expiry recovery keep the Job retryable even after MaxAttempts unless an explicit `AllowDeadLetter` follows a verified authoritative block. The host provides `continuation.WorkerOptions.TerminalQuarantine`, which MUST validate persisted Run/Actor, immutable Work/Generation and uncertain Provider outcome, commit `QuarantineExecution`, and return nil only after durable success. The Worker retains Queue lease renewal throughout that commit, then permits final Redis NACK. When no trusted callback exists or it fails, the Job remains retryable and the Worker reports `ErrTerminalFenceRequired`; malformed orphan deliveries retain diagnostics rather than inventing a Run proof. This is **not** an authenticated operator repair endpoint or proof of physical exactly-once. **Existing Redis Jobs without the new protected Policy remain legacy/unprotected:** coordinate drain/reprojection or explicit recovery on rollout; stable Job fingerprint changes and automatic upgrade is intentionally not claimed. See the [Stage 2 hard-gate audit](../../docs/audits/2026-10-16-stage2-hard-gates-followup.md) and [terminal-DLQ evidence](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md).

### Experimental identified Apply outcome recovery (S2-G6)

For an external host/Feature that must reconcile an ambiguous Run CAS, opt into
`kernel.Runtime.ApplyIdentified(ctx, runID, expectedRevision, operationID, mutation)`
and `VerifyIdentifiedApply`. The caller MUST choose and durably remember its
stable operation ID, exact expected Run revision and identical intended Mutation
*before* attempting the write. The identified Apply appends one reserved
`kernel.apply.evidence` event **in the same transaction** as Run State,
journal and existing Outbox. Verification can return a committed revision even
when unrelated later revisions or a terminal state have replaced the original
snapshot. When no proof exists and the Run remains at that exact revision,
retry **only the original revision-bound CAS**; if the journal is missing or
Run has advanced without a matching proof, fail closed with
`kernel.ErrApplyOutcomeUnknown`. The same operation ID with different content
or revision fails with `kernel.ErrConflict`, and a known duplicate returns
`kernel.ErrApplyAlreadyCommitted`.

This is an **explicit recovery API**, not a replacement for ordinary `Apply`,
not an authenticated Run access facade and not an external Provider effect
receipt. It scans the retained EventJournal in bounded pages on the cold
recovery path; host retention/compaction must preserve this proof. No new Store
port, table or SQLite schema version is introduced. See the
[Stage 2 identified Apply evidence audit](../../docs/audits/2026-10-16-stage2-g6-identified-apply-evidence.md).

### Stage 4 Hosted Continuation: no ACK without authoritative Run proof

An immutable Redis/Memory Continuation Job is **delivery evidence**, not
proof of Run completion. A `continuation.Dispatcher` now NACKs when the
**authoritative** Run Store returns `kernel.ErrNotFound` during its initial
load, CAS-conflict retry, or terminal-result verification. A disappeared
Run cannot be treated as an obsolete/consumed Job. Protected Jobs remain
retryable until the trusted Host verifies and persists the exact current
Run quarantine; persistent orphan failures require Host monitoring and
reconciliation. Do not expose raw Redis Nack/terminal controls to untrusted
callers. The [real PostgreSQL 16 / Redis 8 fault test](../../docs/audits/2026-10-17-stage4-hosted-orphan-delivery.md)
proves NACK, client/Worker reconstruction and later fenced Run completion.
This is **not** a production Hosted authorization, Provider or physical
exactly-once certification.

**Stage 4 revision-bound self-wakeup correction:** a newer nonterminal Run
Revision alone is **not** proof that an older `RunReady`/`ModelReady`/
`SegmentYielded` Job was consumed. Dispatcher now returns `kernel.ErrConflict`
for these unverified old candidates and for an interleaved CAS conflict,
causing the Worker to NACK rather than incorrectly ACK or blindly replay
external Model/Tool effects against a newer phase. Actual terminal Store
state still permits ACK, and stable child/ToolWait-generation routing is
unchanged. Old Jobs can remain retryable until a trusted Feature/Host uses its
existing current-Revision recovery path or proves consumption; persistent
conflicts require diagnostics and bounded Host intervention. See the
[Stage 4 P1 remediation audit](../../docs/audits/2026-10-17-stage4-p1-revision-wakeup-ack-remediation.md).
This fail-closed safety fix does not certify autonomous recovery or production
Hosted readiness.

## Run kinds and subagents

The built-in model/Tool execution loop is `agent.Runner`. It supports delegated
SubAgents through `handoff.Coordinator`, scoped child Runs, the Harness
Delegation Tool, and authoritative `runrelation.KindDelegation` ownership facts.
The Host chooses the role, model, tool policy, and delegation scope before
dispatch; the Kernel remains neutral about product intent.

Removed orchestration kinds and their APIs are intentionally not loadable or
recoverable as first-party executions. See
[ADR 0018](../../docs/adr/0018-subagent-only-orchestration-hardcut.md) for the
pre-stable breaking change.

Kernel terminal output remains feature-neutral through `kernel.Result`; each
Feature owns the schema placed in that result. Provider-specific protocols and
host business objects stay outside Kernel.
