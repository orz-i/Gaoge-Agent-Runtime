# Stage 2B exit audit and Stage 2C first slice — Native post-wait signal

- **Date:** 2026-10-16
- **Branch baseline:** `governance/kernel-runtime@947f39c`
- **Status:** Stage 2B **experimental engineering exit accepted for entering Stage 2C**; Stage 2C **first narrow implementation slice** verified; no production approval
- **Architecture:** [accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) / [proposed ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Previous audit:** [Stage 2B second code-review remediation](2026-10-16-stage2-phase2b-second-code-review-remediation.md)

## Stage 2B re-audit

The earlier findings were examined again against the unchanged `947f39c` baseline
before expanding the execution surface:

- **Lease/revision fencing:** revalidated stale post-Acquire revision,
  idle lease cleanup and immediate replacement owner eligibility.
- **Native receipt identity:** normal and reconciled Tool receipts require
  exact durable `CallID`; wrong receipt identities preserve the intent.
- **Pending/ambiguous external effects:** a native Pending Handler cannot
  automatically execute again from `Resume` while the dispatch is unresolved.
- **Trusted verification:** `ToolOutcomeVerifier` is an injected
  provider-side read-only lookup, not caller-supplied result data; missing,
  wrong or inconclusive receipts block recovery.
- **Timeout semantics:** known late responses are persisted; an unconfirmed
  physical COMMIT remains unknown, never automatically replayed.

No recurrence of those Stage 2B defects was found in the tested paths.
**Stage 2B may progress to Stage 2C engineering**, but a Store lease and
receipt CAS alone **never guarantee physical exactly-once Tool effects**.
This is a scoped engineering-exit finding, not a public release or
unattended multi-worker certification.

## Stage 2C slice: native signal after a persisted Tool wait

### Reproduced failure

Existing `Runner.NotifyExternalToolResultReady` used unbound `Runtime.Apply`.
A native Run has a sticky execution Epoch; that call rejected a valid
already-persisted external-result readiness signal with
`kernel.ErrExecutionRequired`. This prevented native external waits from
recording their ready-at-generation wakeup.

### Minimal change

- Added one narrow `kernel.ControlExternalSignal` variant to the **existing**
  `ControlStore`. There is **no new queue, table, signal service, outbox
  implementation or authoritative ProgressPlan**.
- The Agent first checks the persisted `ToolWait` identity and generation,
  exact `CallID`, external operation ID and digest, deadline and existing
  `SignalID`. It seals the proposed next Feature State through the existing
  first-party `internal/featurecontrol` mechanism.
- `ControlStore` revalidates the sealed Run/Revision/wait identity,
  authoritative **Store clock** against the frozen wait deadline, and legal
  nonterminal transition **in the same CAS transaction** as the Feature State,
  wakeup event and Execution Epoch revocation.
- The existing `ExternalSignals` in Agent State and `TransitionOutbox` remain
  the only durable signal and delivery facts. Duplicate identical signals
  do not advance the Run; conflicting/old signals are rejected.
- A ready signal is **not** an external COMMIT receipt. Native
  `ResumeToolWait` still refuses to call the potentially non-idempotent
  Tool Handler when an unresolved `ToolDispatchID` is present. The host must
  invoke `ReconcileToolOutcome` using its trusted `ToolOutcomeVerifier`
  before completing the Tool.
- Legacy single-executor signal handling continues to use the existing
  `Runtime.Apply` behavior unchanged.

### Evidence

| Test | Result |
| --- | --- |
| `TestNativeExternalResultSignalIsGuardedAndIdempotent` in Memory Agent | PASS — wrong/duplicate signal, pending intent, no second Tool effect, final verified result |
| `RunKernelControlSuite/native-external-signal-guarded-cas` across Memory/SQLite/PostgreSQL fixture | PASS — missing/incorrect proof, wrong wait generation, expired Store-time deadline, atomic Epoch revocation, stale replay |
| `TestNativeExternalReadySignalAcrossProcesses` against real SQLite file | PASS — three fresh OS processes: create Pending wait → store readiness → provider-verified completion; one Tool call |
| `TestRealPostgresNativeExternalSignalAndVerifiedReceipt` | PASS — independent real PostgreSQL 16 connections, trusted final receipt |
| Actual PostgreSQL 16 Execution/Control/native Agent suites | PASS under `go test -race -count=2` on disposable PostgreSQL 16; container removed |
| Entire monorepo ordinary test + Race, Lint, Vet, format, tidy | **PASS**, full Go gate group returned exit 0, 0 lint issues |
| TypeScript, scenario, Benchmark Smoke, OTEL, A2A, release compatibility | **PASS**, full non-Go gate group returned exit 0 |

## Deliberate remaining Stage 2C gates

1. **Early signals before their ToolWait exists** remain a visible
   `ErrToolSignalConflict`, not silently accepted or lost. A host must
   retain and retry a proven early delivery; this slice does **not**
   add a speculative durable inbox.
2. **Physical provider idempotency/unknown COMMIT** remains host-specific;
   verifier implementations must actually check external effects.
3. **ReadyWork ACK/retention/poison cases** and Direct/Pump/Hosted eligibility
   require a separate failure test before changing delivery semantics.
4. **Tenant/actor authorization** belongs at the trusted host boundary;
   an internal first-party proof does not authenticate an HTTP caller.
5. **No Stage 3 five-state migration** and no mobile/production
   certification. The SQLite Schema stays at the **unreleased v1**.

Next Stage 2C increment should be selected by an actual failing early
signal, queue-wakeup, unknown-COMMIT or multi-tenant test rather than
pre-implementing a general workflow engine.
