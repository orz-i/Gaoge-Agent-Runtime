# Stage 2 Phase 2B — Native Direct Agent execution segment

- **Status:** **Native StartRun/Resume implementation slice verified**, **NOT complete Stage 2B, production multi-worker recovery or external exactly-once effects**
- **Date:** 2026-10-16
- **Branch:** `governance/kernel-runtime`
- **Architecture:** [Accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) and [proposed simplification ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Predecessor:** [Phase 2A code-review remediation](2026-10-16-stage2-phase2a-code-review-remediation.md)

## Implemented: one existing execution lease, no new scheduler

`agent.Dependencies.RunExecution` opts a Direct Agent Runner into
native Store-authoritative execution without test Store wrappers.
The configuration is only `OwnerID` + `LeaseDuration`; it requires
a Store that actually implements the existing optional
`kernel.ExecutionStore`. A nil configuration preserves legacy
six-state/single-executor behavior.

- `StartRun` creates the Run, then obtains the Store's execution
  lease before publishing the started event or entering the
  Model/Tool loop. A failed claim returns the **created Run
  Snapshot** and error, permitting recovery by Run ID without
  replaying side effects.
- `Resume` loads and validates the exact Revision, acquires a
  new execution claim **before** its Run middleware and Agent
  drive, binds a scoped `kernel.Runtime.WithExecution` and
  delegates to the **existing** Agent state machine.
- One lease spans multiple internal CAS operations. Between
  logical steps, the Runner renews only if the caller's
  conservative `Clock` hint says less than one maximum bounded
  provider-call window remains; the Store remains authoritative
  for expiry and commit rights. This avoids writing an extra
  SQLite/PostgreSQL lease renewal on every tiny CAS.
- Clean completion, a confirmed approval/budget pause or another
  clean yield releases the lease best-effort. A failed, cancelled
  or ambiguous execution **retains the lease until its Store TTL**
  so a replacement owner cannot immediately replay unknown work.
  The Epoch remains sticky after release.
- `RequireExecution` is still checked in the drive loop and
  immediately before SDK-owned Model/Tool dispatch. An unbound
  legacy Runner cannot execute a previously fenced Run merely by
  calling `Resume`.
- `RunExecution` enforces a valid nonempty owner and a TTL longer
  than each bounded single Model/Tool invocation timeout, with
  the Store's existing 24-hour maximum. This is a configuration
  guard, **not** a physical exactly-once guarantee.

**No additional persistent table, Queue, ActiveSegment enum,
ProgressPlan record or five-state migration** was added. The
unreleased SQLite schema is **still v1**.

### Usage (unreleased governance branch only)

```go
executor, err := agent.NewRunner(agent.Dependencies{
    Runtime: rt, Model: modelClient,
    Catalog: toolCatalog, Executor: toolExecutor,
    RunExecution: &agent.RunExecution{
        OwnerID: "profile-worker-1",
        LeaseDuration: 5 * time.Minute,
    },
})
```

Once `StartRun` or `Resume` returns a `waiting_input` or
`paused_budget` Run, the existing trusted
`ResolveApproval` / `GrantCallAllowance` must validate and
commit its Control CAS. Call `Resume` **again** after the
control transition to acquire a new owner epoch. Do not call an
unbound, legacy Runner to skip this step.

## Negative and recovery evidence

| Case | Verification |
| --- | --- |
| Native SQLite `StartRun` → approval/denial → `Resume` across fresh OS processes | PASS: first approved Tool executes once, first rejected Tool zero times, second Tool independently requires approval |
| Native SQLite concurrent owner during `Resume` | PASS: competing valid lease yields `ErrExecutionConflict` before any Tool effect |
| Native SQLite budget pause → grant → resume across OS processes | PASS: wrong budget dimension/hard cap rejected, exactly two Model calls and one Tool call |
| Native StartRun claim conflict immediately after Create | PASS: returns persisted Run Snapshot, zero Model effects; after competing lease Release, original Run resumes and completes |
| Opt-in on an opaque legacy Store, invalid owner/TTL | PASS: rejected during Runner construction; no new orphaned Run |
| Long logical turn with injected Store clock advancing beyond two lease TTLs | PASS: step-boundary renewal permits the same segment to finish without losing the Store fence |
| Real PostgreSQL 16 native approval/resume across independent connections | PASS under `go test -race -count=2`, alongside prior real-store Control/Execution conformance; disposable DB removed |
| All `make check` target gates | **PASS**: `make metadata fmt-check tidy-check go-vet go-test go-race go-lint` and `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` |

The first single `make check` invocation was interrupted by the remote
session near its final release check, with no logged code failures.
All its declared targets were subsequently rerun in the **two
complete successful groups** above; this is not a claim that the
interrupted single invocation exited 0.

## Explicit remaining problems — not a Stage 2 exit

1. **TOCTOU at the physical external effect boundary.**
   A Store lease may expire after `RequireExecution` but
   while the Model/Tool provider is already processing the
   operation. A stale CAS rejection does not undo provider
   side effects. Keep stable invocation IDs, receipts, provider
   idempotency and ambiguous-COMMIT reconciliation. A late
   external effect is a Stage 2C/production gate.
2. **No autonomous local Pump or ReadyWork acknowledgement
   strategy.** An app/host must explicitly invoke `Resume`
   after an eligible transition. A healthy finished Run can
   still retain unconsumed historical ReadyWork; do not
   delete these facts merely because the Run advanced.
3. **StartRun Run-middleware compatibility boundary.**
   Existing host Run middleware executes before `StartRun`
   creates/claims the Run; the opt-in fence protects native
   Model/Tool dispatch, but a malicious/side-effecting host
   Run middleware is outside that Store execution guarantee.
   Host middleware must remain trusted and idempotent.
4. **No automatic process-wide heartbeat during one blocked
   external invocation.** Each bounded invocation must fit
   its configured Run lease interval; a stuck provider can
   still expire and require identity-based reconciliation.
   On an error, do not blindly Release/steal and replay.
5. **Go module release boundary.** The evolving Core API
   is still unpublished relative to the pinned
   `agent-runtime v0.1.0-beta.22` dependency in the
   experimental SQLite and PostgreSQL modules. A clean
   independent `GOWORK=off` consumer build of these
   unreleased adapters is not yet a valid public release gate.

**Next step:** independent code audit of native execution
ownership and actual external effect timeouts, followed by
precise stale-effect/unknown-COMMIT reconciliation tests.
Keep ADR 0009 **Proposed** pending explicit architecture
acceptance; do not claim the entire Stage 2 or Stage 3 complete.
