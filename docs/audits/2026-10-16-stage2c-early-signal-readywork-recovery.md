# Stage 2C — Early external signal and ReadyWork recovery gate

- **Status:** Minimal early-signal retry contract and projector-lease regression implemented and locally verified; **not** full Stage 2C acceptance or an autonomous Pump
- **Date:** 2026-10-16
- **Baseline:** `governance/kernel-runtime@dd7afbb`
- **Architecture:** [accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) and [proposed minimality ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Predecessor:** [Stage 2B experimental exit and Stage 2C post-wait signal](2026-10-16-stage2b-exit-stage2c-postwait-signal.md)

## Failure window (reproduced)

The Native Agent persists an exact `ToolDispatchID` **before** calling
`Tool.Execute`. An external Provider can synchronously announce a ready
receipt during that call, **before** the Agent can commit the returned
`PendingExecution` descriptor/`ToolWait`. Before this patch,
`NotifyExternalToolResultReady` returned only a generic
`ErrToolSignalConflict`. A host that treated this as permanently invalid
could acknowledge/drop a legitimate delivery; no future ToolWait has
proof that the signal ever existed.

A provider operation ID/digest cannot be verified against an unfinished
wait yet. **Do not** accept this event into Run State or invent a
universal new inbox to mask the missing match.

## Minimal change

1. Introduced one **typed, retryable**
   `agent.ErrToolSignalNotReady`. It is returned *only* while the
   authoritative Native Run is `running`, its `ToolWait == nil` and its
   already-committed `ToolDispatchID == signal.CallID`.
2. The return is a **negative acknowledgment**, not a success:
   **no Run revision, signal or ReadyWork is committed**. The host
   must retain the original signal in its **own durable source**,
   not ACK that source, and retry the same Run/Call/operation/digest/
   SignalID after the Pending wait has been committed.
3. Wrong CallID, unknown/uncommitted work, changed operation identity,
   stale generations and conflicting reused SignalIDs retain the
   existing `ErrToolSignalConflict`/terminal behavior. A retry cannot
   authorize an unrelated Tool.
4. The existing Stage 2C **post-wait guarded Control CAS** still
   validates the exact persisted wait and Store-owned deadline and
   atomically writes one `ExternalSignals` entry, one
   `EventExternalToolResultReady` outbox fact and revokes the old
   native Execution Epoch. Duplicate delivery does not advance Revision.
5. **ReadyWork is delivery evidence, not execution permission.**
   Native `ResumeToolWait` refuses to re-run the ambiguous Tool while
   its `ToolDispatchID` is unresolved. A consumer may ACK a successfully
   projected wakeup only with its own exact Transition Lease. Stale
   projector ACKs cannot consume a newer claim.

**No new Runtime/Store interface, DB table, Schema version, ProgressPlan
kind, polling service, general ControlCommand queue or worker was
introduced.** The new exported error is the only product API surface
change; the SQLite unreleased `Schema v1` is untouched.

## Executed adversarial evidence

| Case | Source/test | Verified result |
| --- | --- | --- |
| Signal emitted inside `Tool.Execute` before `ToolWait` exists | `TestNativeExternalSignalBeforeToolWaitIsRetryable` | `ErrToolSignalNotReady`, unchanged Run revision, later exact replay accepted, wrong call rejected |
| Signal survives process death on host-owned persistent delivery | `TestNativeEarlyExternalSignalRedeliveredAcrossProcesses` | Five independent SQLite OS processes: Create/Pending, ready delivery, projector crash, projector reclaim, verified result; host file retained until Run CAS succeeds |
| Projector crashes after Claim and before ACK | Same SQLite test + `TestNativeExternalReadySignalAcrossProcesses` | One immutable `WakeupGeneration`, second lease attempts=2; old ACK `ErrConflict`; new ACK succeeds |
| Wakeup projected while Tool effect unresolved | Same tests | `ResumeToolWait` returns `ErrToolOutcomeUnknown`; external Tool physically called once |
| Real PostgreSQL 16 early signal | `TestRealPostgresNativeExternalSignalAndVerifiedReceipt` | Native first-party handler receives retryable early refusal; new connection resends after wait, verifies receipt, completes |
| Real PostgreSQL 16 Control/Execution regression | `TestRealPostgresKernelControlStoreConformance` / `TestRealPostgresKernelExecutionStoreConformance` | `go test -race -count=2` PASS on disposable PostgreSQL 16 |
| Go and release gates | All `make check` targets | PASS — Go tests/Race/Vet/Tidy/format, all-module Lint and non-Go/release group |

The first compound Go check lost its remote session while finishing
Lint. Its completed test/Race results are present in the log; running
`make go-lint` separately returned exit 0 with **zero issues** across
all Go modules. The non-Go group `make ts-lint ts-typecheck ts-test
ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check
release-check` also returned exit 0. **No single-command `make check`
exit code is claimed.**

The host-owned test file represents an external durable delivery
obligation; it is **not** an inbox implemented in Kernel or proof
that a generic HTTP service already persists incoming signals.

## Deliberate remaining Stage 2C work

- A host that sends signals **without** first durably recording them
  can still lose an event when the host itself crashes. To certify
  hosted/mobile early-signal recovery, prove the actual host delivery
  source persists until the negative/positive acknowledgment contract
  is honored. No silent acceptance of unverified operation IDs.
- Late or genuinely unknown external Provider results still require
  an independently implemented, tenant-authorized
  `ToolOutcomeVerifier`. A successful Tool dispatch can have a
  physical side effect after its Run lease expires.
- Existing historical ReadyWork must remain until correctly projected
  and ACKed; no safe automatic compaction or native direct/Pump
  consumption equivalence is claimed by the projector-lease tests.
  The earlier observed 1/10/100 Tool baseline (10/55/505 unACKed
  rows) is an input to future write-amplification review, not
  permission to delete recovery facts.
- Run-level semantic poison handling, exact authorized Profile scope,
  multi-worker Hosted eligibility and cold/mobile lifecycle evidence
  require separate concrete negative tests before Stage 2 exit.
- No public Core/adapter release, ADR 0008 replacement or mandatory
  five-state migration is approved by this increment.

**Decision:** this early-signal and ReadyWork delivery-contract slice
can be reviewed and advanced locally within Stage 2C. It does
**not** change full Stage 2/3 production readiness.
