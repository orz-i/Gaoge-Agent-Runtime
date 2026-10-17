# Stage 2B — Second code re-audit remediation (lease, receipt, Pending)

- **Status:** Implementation and negative regression tests completed locally; independent final code re-audit still required
- **Date:** 2026-10-16
- **Base:** `governance/kernel-runtime@51dbd34`
- **Design:** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md), [proposed minimality ADR 0009](../adr/0009-minimal-kernel-complexity-reassessment.md)
- **Related:** [Previous Phase 2B remediation](2026-10-16-stage2-phase2b-code-review-remediation.md)
- **Storage:** existing Run CAS, execution Epoch, opaque Agent State and TransitionOutbox; **no new table, queue or schema version**

## Fixed findings

| Priority | Finding | Minimal remediation | Regression |
| --- | --- | --- | --- |
| **P1** | A successful `AcquireExecution` followed by a failed pre-dispatch revision/status check holds a lease until TTL expiry. | `withRunOwner` releases its newly acquired lease for all pre-dispatch errors (including a cancelled context via a bounded `context.WithoutCancel` cleanup). It retains the lease after actual or ambiguous provider dispatch. | `TestNativeResumeReloadsSnapshotAfterAcquireBeforeTool` on real SQLite: stale revision remains rejected with zero Tool effects; a different owner can immediately claim and release. |
| **P1** | `ReconcileToolOutcome` trusted a caller-supplied `ExecutionResult{Disposition:"committed"}` without proving the external commit. | The signature is now `ReconcileToolOutcome(ctx, runID, expectedRevision, callID)`. A trusted `agent.ToolOutcomeVerifier` must be injected into `agent.Dependencies`. The verifier makes a provider-owned **read-only** lookup by exact `RunID` and stable `CallID` and returns the committed receipt; no verifier means `ErrToolVerificationRequired`. Errors, wrong ID, invalid result, Pending or noncommitted disposition fail closed. | `TestNativeReconciliationCannotInventMissingCommit`: external effect count 0 and missing provider proof cannot complete Run. `TestNativePendingToolNeedsVerifiedResultBeforeReentry`: missing or mismatched lookup refuses commit; a later correct verifier result completes with one Handler call. Three-process SQLite test reads the test provider's effect journal and reconciles without re-executing. |
| **P1** | A normal native Tool executor could return a different `Receipt.ExecutionID` from the pending Tool Call ID and still complete. | After validating result shape, native `executePending` checks exact `Receipt.ExecutionID == call.ID` **before** receipt CAS. Invalid or mismatched native receipts keep `ToolDispatchID` and return `ErrToolOutcomeUnknown` + `ErrToolReceiptMismatch`, rather than marking the Run failed/completed. Legacy behavior stays compatible. | `TestNativeWrongToolReceiptCannotConsumePendingCall`: wrong receipt never consumes the durable Tool intent, is not auto-retried, and cannot be reconciled without an injected verifier. |
| **P2** | Native Pending Tool receipts clear the dispatch marker, permitting a second non-idempotent Handler invocation on `Resume`. | Native `pending` receipts retain `ToolDispatchID` and continue to forbid reentry. The only supported resolution is a trusted final provider receipt or equivalent authorized external status protocol. Existing Legacy Pending behavior is unchanged. | `TestNativePendingToolNeedsVerifiedResultBeforeReentry`: first Pending call causes one effect; Resume returns `ErrToolOutcomeUnknown`; verified COMMIT consumes the call and completes without re-invoking the Handler. |

## Guardrails and tradeoffs

- **Trust is explicit, not magically created by an interface.** The host
  authenticates the operator and injects an implementation that actually
  checks external provider state. Code already trusted by the host can
  supply a dishonest verifier. The Kernel cannot cryptographically prove
  a third-party COMMIT in a generic Tool protocol. Do not expose
  `ToolOutcomeVerifierFunc` as a pass-through for user-supplied JSON.
- A verifier is **not** `tools.Executor` and must **never** create,
  retry or acknowledge a physical effect as a side effect of lookup.
  Verification failure retains the exact durable Tool intent. Because
  verification is read-only, its failed attempt also releases the
  newly claimed Run lease, allowing immediate re-verification.
- Native Pending execution requires external provider lookup before
  completion; this conservatively disables blind repeated calls even
  for formerly idempotent Pending handlers. Hosts that need automatic
  safe Pending polling should supply a separately justified
  idempotency/verification contract; the prototype doesn't introduce
  a generic polling framework.
- Native `Receipt.ExecutionID` must match the durable Call ID. Legacy
  receipts may use independent executor-issued identifiers under
  the existing published contract. This increment changes the
  **unreleased native execution mode**, not all legacy receipts.
- The `ReconcileToolOutcome` Go API change is internal to the
  **unreleased governance branch**. It is not a compatible public
  adapter release. The Core and SQLite/PostgreSQL modules require
  coordinated versioning at the release gate.
- A provider may still physically execute after lease expiry.
  The durable intent prevents **automatic** replay but cannot prove
  physical exactly-once effects. Unknown outcomes without a provider
  receipt remain blocked.
- A terminal/native completed Run, a wrong revision, a wrong Call ID or
  an unverified provider response cannot pass the guarded receipt CAS.

## Test evidence

- **Go gates:** `make metadata fmt-check tidy-check go-vet go-test go-race go-lint` — PASS, 0 lint issues.
- **Non-Go gates:** `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check` — PASS.
- **Real PostgreSQL 16:** isolated ephemeral Docker DB, real-engine
  Execution/Control Conformance and native approval/resume, `-race
  -count=2` — PASS on rerun. The original 150–180 ms
  wall-clock-sensitive deadlines in existing Control Conformance
  flaked under the first PostgreSQL Race run; tests were given a
  more realistic scheduling margin and rerun successfully.
- **SQLite:** stale pre-dispatch Revision with immediate new-owner
  claim; three-process provider-result reconciliation; existing
  native approval/allowance and competing executor tests — PASS.
- **Agent/Memory:** invalid/missing/verifier-error/incorrect-CallID
  proofs; Pending no-auto-replay; trusted later COMMIT; no physical
  Tool invocation during verification — PASS.

## Next review gate

An independent audit is needed before closing Stage 2B or enabling
unattended multi-worker execution. Stage 2C still owns real provider
idempotency and physical effect fencing, early-signal generation,
Run-level poison/quarantine as supported by failure tests, and
Direct/Pump/Hosted eligibility parity. No progress-state
taxonomy, universal command queue or new persistence schema was
added, and proposed ADR 0009 remains **Proposed**.
