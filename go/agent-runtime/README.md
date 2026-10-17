# Agent Runtime Core

`github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime` is the host-neutral Agent Runtime
Core. The module is split into explicit capability packages such as `kernel`,
`agent`, `tools`, `interaction`, `workflow`, `team`, and `compose`. Hosts construct only the capabilities they need; there is no
compatibility `Engine` facade or automatic Runtime-kind selector.

For an in-process durable-contract implementation, use `memory`:

```go
store := memory.NewStore()
runtime, err := kernel.New(kernel.Dependencies{Store: store})
```

Production hosts normally pair Core with the PostgreSQL Store and either the
Redis or memory generation stream adapter.

## Experimental direct-Agent execution lease (Stage 2B)

**Stage 2 governance:** The [final Stage 2 exit audit](../../docs/audits/2026-10-16-stage2-final-exit-audit.md) closes the previously demonstrated 2A/2B/2C experimental engineering increments, **not** ADR 0008's full production acceptance. A [subsequent hard-gate correction](../../docs/audits/2026-10-16-stage2-hard-gates-followup.md) adds Run Store quarantine and exact internal repair, self-wakeup source binding and partial Direct/Worker concurrency proof. A [subsequent terminal-DLQ handoff test](../../docs/audits/2026-10-16-stage2-terminal-dlq-handoff.md) now verifies protected Queue expiry and PG-quarantine-before-Redis-Nack ordering. Actual product host authorization/Provider identity, migration of old unprotected Jobs and full Direct/Pump/Hosted eligibility are **still not certified**. Do not treat this opt-in API as unattended production multi-worker certification.

An opt-in `agent.Dependencies.RunExecution` configuration lets a Direct
Agent `StartRun` or `Resume` claim the existing Store execution lease
before its Model/Tool loop. It is **not** a workflow scheduler or a new
state machine. For example:

```go
RunExecution: &agent.RunExecution{
    OwnerID: "profile-worker-1",
    LeaseDuration: 5 * time.Minute,
},
```

The Store must implement `kernel.ExecutionStore`; otherwise Runner
construction fails. **Native `StartRun` requires a caller-stable nonempty Run ID**, allocated before any Create attempt: a scope-scoped unique `RequestID` is not guaranteed by the current Store. A failed or lost Create response must be reconciled by that same Run ID, never retried with a freshly minted identity. HTTP hosts using experimental Native mode must therefore supply a stable `ClientRunID` or their own authenticated idempotency admission layer. Legacy single-executor Starts retain their optional auto-ID behavior. Native creation **atomically reserves Epoch 1**;
Legacy Runs at Epoch 0 cannot be upgraded to native execution while
old executors may still be running. The first native owner claims Epoch 2.
The lease must cover the configured Tool retry/backoff window, though
external calls may still ignore Context deadlines. In opt-in Native mode,
a retryable Model error persists `ModelInvocation.RetryAfter` in existing
Agent State alongside the committed Outbox `WakeupAt`. A Direct `Resume`
before the due time returns `ErrModelInvocationBusy` **without claiming a
new Store Epoch or calling the Model**; due recovery reuses the same
invocation identity. The host must provide a trusted consistent Model
scheduling clock. The Legacy explicit-resume path retains its previous
immediate-retry semantics; this does not claim a production Local Pump.

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

## Runtime kinds

Agent Runtime exposes explicit Run kinds over the same durable Kernel primitives:

- Agent Run is the direct model/Tool loop and is provided by `agent.Runner`.
- Agent Team is coordinator-led, open-ended multi-agent collaboration.
- Dynamic Workflow is a versioned, deterministic DSL with structured data,
  bounded concurrency, hard budgets, durable waits, cache policy, and
  compensation.

These kinds do not masquerade as one another through modes or compatibility
facades. Callers select the Runtime kind before Run creation.

Kernel terminal output remains feature-neutral through `kernel.Result`; each
Feature owns the schema placed in that result. Provider-specific protocols and
host business objects stay outside Kernel.
