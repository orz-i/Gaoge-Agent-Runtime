# Stage 2B — Native execution safety audit remediation

- **Status:** Addressed the **three P1 and one P2** findings from the native execution re-audit; full Stage 2 production readiness remains **NO-GO** pending external-effect/idempotency certification and coordination
- **Date:** 2026-10-16
- **Baseline:** `governance/kernel-runtime@2c8cbd3`
- **Predecessor:** [Stage 2B native Run integration](2026-10-16-stage2-phase2b-native-run-execution.md)
- **Architecture:** [accepted ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md) and [proposed minimality ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Scope:** single Run Store, existing six statuses, existing epoch column and Feature state; no new tables, queue or Workflow interpreter

## Fixes and evidence

| Finding | Local implementation | Negative/failure test |
| --- | --- | --- |
| **P1: Resume got a valid Lease but used a stale pre-claim Run Snapshot** | `withRunOwner` re-Loads from the authoritative Store **after AcquireExecution** and checks exact expected Revision, status and live execution authority before invoking middleware, Model or Tool. | `TestNativeResumeReloadsSnapshotAfterAcquireBeforeTool` (SQLite): another valid CAS commits between Load and Acquire; **0 Tool effects**, `ErrConflict` |
| **P1: Legacy epoch-zero and native executors ran concurrently** | `kernel.CreateRequest.ExecutionRequired` reserves **Epoch 1 atomically within Create** for native Runs; first ownership acquisition increments to Epoch 2. Legacy Runs remain Epoch 0 and **cannot silently upgrade** to leased execution. Plain legacy Apply cannot drive the reserved Run even in the gap between Create and first Acquire. Existing Epoch is the only persistent mode discriminator; the new creation flag is transient. | Shared `RunKernelExecutionSuite/execution-mode-frozen-at-create` against Memory, SQLite, PostgreSQL 16; `TestLegacyRunCannotJoinNativeExecutionDuringTool` keeps exactly **one** physical legacy Tool effect |
| **P1: Tool retry/lease expiry led to external COMMIT without receipt then physical replay** | For native Tools, a CAS first persists **`ToolDispatchID`** (stable Tool Call ID) and its dispatch-intent event. The same CAS that persists the successful or pending receipt clears the marker. A new `Resume` refuses any unresolved dispatch with `ErrToolOutcomeUnknown` **without claiming** an executor or reissuing the Tool. `ReconcileToolOutcome` permits a **trusted host that independently verified a committed provider result** to persist the exact Run/Revision/CallID receipt using the existing Store CAS, **without invoking Tool.Execute**. Native configuration also checks the configured Tool retry/timeout/backoff envelope, though physical calls can exceed Context timeout. | `TestNativeToolUnknownOutcomeBlocksReplayAfterExpiry`: 1 physical effect before lost receipt; automatic retry blocked; verified result allows completion with still 1 effect. `TestNativeUnknownToolReconcilesAcrossProcessesWithoutReplay`: isolated three-process SQLite receipt recovery, 2 Model calls, 1 Tool effect |
| **P2: a Tool ignored Context and a late success was reported as ordinary completion** | A valid result returned after `callCtx` deadline is retained as a **known late result**, committed with its receipt, then returned with `ErrToolLateResult`; **no blind retry**. The same policy now records late Model responses under their existing durable invocation receipt and returns `ErrModelLateResult`. A known committed late result releases the lease so explicit Resume can consume it. | `TestNativeLateKnownToolReceiptIsPersistedNotRepeated` and `TestNativeLateKnownModelResponseIsReceiptedNotRepeated`: delayed successful provider response, explicit warning, completion without another invocation |

## Correctness boundary

**Dispatch-intent before Tool.Execute** prevents *automatic* duplicate physical
effects if the Agent dies after a provider COMMIT but before its receipt is
durably recorded. The earlier source of truth remains the existing Feature
State JSON and Run Revision CAS. No duplicate "progress authority" was added.
One additional CAS per external Tool invocation is deliberate because a
multi-attempt execution needs an unambiguous durable operation ID.

The behavior is **fail closed**, not physically exactly-once:

- A Run with unresolved `ToolDispatchID` is **not automatically runnable**,
  even if it is safe in reality. A host must use the stable Run/Call ID to
  query the provider or an idempotent effect journal, confirm the final
  disposition and supply a verified committed receipt to
  `Runner.ReconcileToolOutcome`. Incorrect ID, wrong revision, missing
  intent and fabricated status are rejected where structurally detectable.
  Host authentication and **actual provider verification remain essential**;
  the SDK cannot prove a string-valued receipt against an external system.
- If a provider has **no result lookup, idempotency key or trustworthy
  receipt**, an uncertain physical COMMIT cannot be recovered automatically.
  Leave the Run blocked for authorized manual reconciliation; do not clear
  the marker or retry under a fresh Tool identity.
- The pre-dispatch lease check, revision CAS, retry-envelope validation and
  prepared Tool intent **do not revoke** physical effects already underway
  in a stale process. Host/provider integration must support idempotent
  effect identities for a production multi-worker guarantee.
- Existing Legacy six-state Runs are intentionally **not upgraded in place**
  by attempting AcquireExecution. Coordinating a migration would require
  proof that all legacy executors are quiescent. Nothing silently migrates
  or deletes old Run data.

## Storage and compatibility

`CreateRequest.ExecutionRequired` and transient `Record.ExecutionRequired`
are **creation-time flags**, not new persisted statuses. Memory, SQLite and
PostgreSQL use the **existing Epoch column** as the sole persistence mode:
`0 = legacy`, `1 = native reserved`, `2+ = claimed / later epochs`.
There is no SQLite Schema version bump: **development v1 remains v1**.

An old experimental Run created before this change with Epoch 0 remains
Legacy. It is deliberately ineligible for native AcquireExecution; this
is a safer break in *unreleased* experimental execution behavior, not
a shipped migration.

External adapters implementing `ExecutionStore` must honor the creation
reservation contract and pass its Conformance. Existing published SDK
Core/adapter versions are **not independently releasable** from this
governance branch until the coordinated Go module release.

## Verification

- **PASS:** Shared Memory/SQLite/PostgreSQL execution-mode and epoch fencing
  Conformance, SQLite stale-post-Acquire and mixed-mode race negatives.
- **PASS:** ambiguous Tool receipt no-auto-replay and trusted reconciliation
  with exact identity, including a three-process SQLite test.
- **PASS:** late known Model/Tool success yields a warning with persisted
  receipts, then successful resume without repeated provider effects.
- **PASS:** real PostgreSQL 16, combined Execution/Control and native
  approval/resume conformance, `go test -race -count=2` against an isolated
  disposable test database (removed).
- **PASS:** On the final code, both complete gate groups exited **0**:
  `make metadata fmt-check tidy-check go-vet go-test go-race go-lint`,
  and `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke
  otel-e2e-smoke a2a-product-check release-check`. Each Go module's
  lint returned **0 issues**. An earlier Go group exposed three test-style
  warnings; these were fixed before the complete passing rerun. These
  two successful groups cover every `make check` target, without claiming
  a separately observed single-command `make check` exit code.

## Open release gates

- **Stage 2C:** provider-specific idempotent Tool effect protocol and
  reconciliation for stale physical effects, early signals, unknown model
  COMMIT, multi-tenant authorization, and local/Hosted Pump eligibility.
- Verify wakeup acknowledgments and long-turn SQLite write amplification;
  never delete unconsumed outbox work just because a Run reached a later
  status. No mobile VFS, power-loss or production multi-worker certification
  is claimed.
- [ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md) remains
  **Proposed**, not automatically accepted by this patch.
