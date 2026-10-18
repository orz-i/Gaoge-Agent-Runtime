# Stage 2 Phase 2A — Code-review remediation

- **Status:** Five confirmed audit findings remediated and verified on the local governance branch; **NOT** approval for production distributed Agent execution
- **Date:** 2026-10-16
- **Base revision:** `governance/kernel-runtime@87ef1f3`
- **Design:** [Proposed minimal ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md); [Phase 2A guarded-control implementation](2026-10-16-stage2-phase2a-guarded-control.md)
- **Follow-up:** [Stage 2B opt-in native Agent execution](2026-10-16-stage2-phase2b-native-run-execution.md) has now implemented owner acquisition for `StartRun` and `Resume`. This Phase 2A audit remains a historical checkpoint.
- **Scope:** Kernel + first-party Agent + Memory/SQLite/PostgreSQL; no new queue, progress schema, state machine or database format version

## Findings fixed

| Audit finding | Remediation | Negative verification |
| --- | --- | --- |
| **P1: unbound `Runner.Resume` invokes Tool before receipt CAS is rejected** | Added Store-owned `ExecutionStore.CheckExecution` and `Runtime.RequireExecution`; refuse unbound/stale/expired fenced Runs **before** Agent drive/middleware, recheck within the drive loop and at actual local Tool and Model dispatch. Existing Store transaction CAS remains independently fenced. | `TestUnboundFencedResumeDoesNotExecuteToolBeforeCAS` (before fix: 1 physical Tool execution and receipt CAS failure; after: 0 Tool executions, `ErrExecutionRequired`), shared `RunKernelExecutionSuite/pre-dispatch-authority-before-and-after-takeover` |
| **P1: direct Kernel/Store controls bypass Feature validation** | A tiny `go/agent-runtime/internal/featurecontrol` package mints **first-party-only** mutation-bound approval/allowance proofs **after** the Agent validator approves. Each proof binds Run ID, expected revision, exact gate, intended JSON state and resolved checkpoint. External modules cannot import the internal issuer. `ControlGuard` stores an unexported proof; all three Stores independently require it during the authoritative CAS. An approval must persist a valid resolved decision; a budget grant must change Feature state. | Shared `RunKernelControlSuite` rejects unsealed Runtime and direct Store calls, mismatched gates and replay. `internal/featurecontrol/proof_test.go` checks Run, Revision, gate, State and Checkpoint binding; external Go-module import is rejected by the compiler. Real Agent tests still pass approval/rejection and valid budget grant. |
| **P2: approval/allowance may unlock after frozen deadline** | The **Store-owned clock** now rejects approval and allowance transitions that resume at/after `DeadlineAt`, regardless of a caller-provided time. | Shared `Store-deadline-gates-approval-and-allowance`; Memory, SQLite, real PostgreSQL 16 |
| **P2: bound Runtime cannot call Cancel** | Kernel `Cancel`/`Expire` use a temporary unbound Runtime reference **only** for their already-authorized terminal-control CAS. The Store atomically revokes the previous Epoch. General approval/allowance still cannot be called with a bound Runtime. | `RunKernelControlSuite/bound-owner-cancel` |
| **P2: `Expire` reports `expired=true` on a rejected transaction** | Return `expired=true` only after a successfully committed Store transition. A fast caller clock cannot claim persisted expiration when Store time says not due. | `fast-caller-clock-does-not-report-false-expiry`, including standalone SQLite reproduction |

## Correctness details

**Model/Tool dispatch safety:** the new authorization query is a
pre-dispatch **read** of the Run's authoritative epoch, owner,
lease nonce and Store-time expiry. A nil lease is permitted only
when the Store has never claimed that Run (`epoch==0`). An
outdated bound lease fails. `RequireExecution` is checked
at resume entry, on each drive iteration and just before the
SDK-owned Model/Tool provider callback, after limiter and
middleware delays.

This prevents the demonstrated **unbound Resume → physical Tool
effect → rejected receipt** failure. It does **not** make an
in-flight provider/Tool effect physically exactly-once: a lease
can expire between the pre-dispatch check and the external
system's commit, and custom middleware may have separate
side effects. Explicit stable invocation identity,
idempotent effect handling, provider reconciliation and
owner-integrated `StartRun`/`Resume` remain Stage 2B/2C gates.

**Feature-proof boundary:** an external consumer cannot import
`internal/featurecontrol` and cannot set the private
`ControlGuard.verified` field. The Store checks the proof
in its authority-protected transaction, so bypassing
`Runtime.ApplyControl` and calling the adapter directly
does not work. This is **not** cryptographic authentication
against hostile code already running *inside* the trusted Core
module, nor host/user authorization; the HTTP/Harness caller
still must check actor/tenant access before calling a trusted
Feature.

**Rejection Checkpoint:** the Agent now persists the same
resolved Checkpoint for a rejected first Tool, rather than
committing a nil checkpoint as an unverified gate reset.
A later distinct Tool still requires an independently bound
approval. The checkpoint is consumed on later Tool completion.

**No automatic five-state migration:** the six published Run
statuses remain unchanged. SQLite developer Schema remains
**`PRAGMA user_version=1`**, and no table, index or additional
delivery service is introduced.

## Validation

- **PASS** `make check`: entire monorepo (format/tidy/vet/test/race/lint,
  TypeScript, evaluation, benchmark smoke, OTEL, A2A,
  clean consumer/release compatibility).
- **PASS** race-count repeated for the Core proof package,
  Kernel, Memory, Agent and SQLite/PostgreSQL control suites.
- **PASS** isolated real **PostgreSQL 16**, both Execution and Control
  Conformance, `go test -race -count=2` using a disposable local
  container (stopped/removed).
- **PASS** SQLite multi-process approval/rejection and budget,
  exact second-Tool approval, no physical Tool effect on
  unauthorized Resume, and rollback-after-failed-control tests.
- **PASS** rejected external import of the first-party
  `internal/featurecontrol` issuer.
- **PASS** `git diff --check` and documentation link validation.

## Remaining limits and next gate

The `ExecutionStore` and `ControlStore` interfaces remain
experimental, requiring a coordinated Core/adapter Go module
release. Custom Store wrappers that hide the optional
`ExecutionStore` capability cannot be automatically inspected:
trusted host composition must forward execution authority
correctly; it must not treat an opaque wrapper as proof of
an unclaimed Run.

**Next:** independent re-audit of this patch followed by
Stage 2B's native direct Agent lifecycle integration and
stable external effect reconciliation tests. This record
does not approve a production concurrent executor, public
release, PR merge, or physical exactly-once guarantee.
