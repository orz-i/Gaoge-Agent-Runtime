# ADR 0013 — Uniform durable Tool intent and exact no-effect receipts

- **Status:** Accepted for pre-stable beta.24 candidate
- **Date:** 2026-10-30
- **Base:** main@1cd08e21ce225e637a806da807cea50d5ec02f6f (PR #76)
- **Related:** ADR 0008/0009 (Store-owned authority), ADR 0010/0011 (frozen
  ModelInvocation and retries), ADR 0012 (Host-selected capabilities)

## Problem

Previously Agent had two incompatible Tool crash semantics. A direct
Agent without `Dependencies.RunExecution` executed its external Tool Handler
without first persisting `ToolDispatchID`, did not always verify its exact
execution receipt, and could blindly replay the same side effect after a
lost Tool receipt. The Store-leased mode already persisted the intent.

Independent implementation review also demonstrated that simply checking
whether an error was a `tools.RecoverableCallError` was unsafe: a Handler
could **physically commit** a remote operation and then return the same
model-correctable error type. The Agent incorrectly treated this as a
proof of no effect and advanced its transcript. The shared Tool budget
middleware also permitted a previously **Dispatched + Held** reservation to
re-enter the physical Handler on a later call.

## Decision

1. **One Tool effect intent rule for all Agent executions.** Agent persists
   the original Tool `Call.ID` as `ToolDispatchID` by Kernel revision CAS
   before entering the physical Tool/Middleware boundary, independently
   of whether `RunExecution` is configured. The same frozen identity is
   checked on direct `Resume`, external ready signals and reconciliation.
   A pending Tool receipt never clears the marker. Malformed or mismatched
   post-dispatch receipts are unknown, not ordinary Tool failures.
2. **Trust explicit terminal receipts, not a recoverable error type.**
   A typed model-correctable Tool refusal is permitted *after* entering the
   Handler boundary only if the trusted Handler certifies the exact original
   Call ID with `ReceiptDispositionRejected` and a valid result. This is a
   no-physical-effect attestation; it must never be emitted after a remote
   mutation or an outcome-unknown transport error. `RecoverableNoEffectResult`
   is a small Tool Helper to bind that attestation to the original Call.
   Direct typed errors without this proof remain `ErrToolOutcomeUnknown`,
   retaining the committed intent. Pre-dispatch JSON Schema rejection
   remains model-correctable without physical execution.
**Independent PR #77 hard-cut correction:** The same rule covers
`RetryableExecutionError`, not only model-correctable refusal. A transient
error **alone** cannot grant another physical Tool attempt even when the
execution policy says `MaxAttempts > 1`. Only an exact original-ID terminal
`rejected` no-effect receipt, returned with the typed retryable error, can
authorize a bounded same-Call retry (`tools.RetryableNoEffectResult`).
When a shared budget already holds a dispatched reservation it still cannot
be reentered without existing authoritative settlement; safe retry may
therefore be denied by the Host budget rather than minting a second
effect-grant protocol.

A successful Tool result must use exactly `read`, `committed`, or `pending`.
`rejected` must appear **only with** a trusted typed no-effect error.
Empty, unknown, vendor-defined or error-free `rejected` dispositions
are malformed and never settle the Agent or shared Tool budget. A Provider
or custom Tool needing additional status detail must put it into its
validated Tool `Content`/Host receipt, not invent a new dispatch disposition.

3. **Shared Tool budget follows the same evidence.** An already dispatched,
   unsettled `tool:<RunID>:<CallID>` reservation is not a second grant.
   It stays Held and disallows re-entry; only a trusted exact-ID terminal
   receipt or read-only verifier can settle it. A correctable rejection
   attested as no effect may be settled as one *attempted Tool call*; it is
   not a business effect or a second invocation.
4. **Read-only recovery reuses existing boundaries.** Both lease-backed
   and direct Agents can invoke `ReconcileToolOutcome` with a trusted,
   read-only original-Call verifier. First-party Harness Capability and
   Delegation Tools can verify their original child identity and terminal
   outcome without re-entering a Handler. The existing shared budget can
   wrap the verifier to settle the same reserved attempt exactly once.
   No second effect ledger, scheduler, ToolDriver, external Registry or
   synthetic business receipt is created.
5. **Keep execution leases optional, not execution evidence optional.**
   `RunExecution` still adds Store Epoch/lease ownership for multiworker
   operation. The non-leased path now shares Tool intent/receipt rules,
   but is **not** thereby certified for concurrent multiworker ownership.

## Hard-cut compatibility and rollout

- There is **no legacy direct Tool replay path**. Stop old workers, audit
  outstanding Runs and quarantine/drain any in-flight Tool whose external
  effect may have occurred without a matching `ToolDispatchID`.
- Host Tool Handlers returning a bare `RecoverableCallError` after the
  physical boundary must change: either reject before execution or emit
  an exact original-ID `rejected` no-effect result *with* that error.
  Ordinary remote errors and cancellations must remain unresolved.
- Capability Tool `Receipt.ExecutionID` now binds the parent Model Tool
  `Call.ID`, not the child Harness Invocation ID. Child identity remains
  separately visible in the structured Tool result.
- No Kernel status, SQL schema version, mandatory new Store method, old
  shims or automatic Run mutation/migration is added. Old accepted remote
  beta.23 tags remain immutable.

## Proof and known limits

Deterministic fault tests cover direct Tool effect/receipt crash, persisted
unknown-result denial across restart, exact read-only receipt recovery, a
Handler that writes externally before returning a misleading recoverable
error, no-effect receipt ID/disposition mismatch, and held shared budget
re-entry. Regressions for Team/Workflow/Delegation and child Tool
continuations ensure no loss of intended valid behavior.

As with any remote operation, a Handler or custom middleware may lie
about its no-effect guarantee; the SDK cannot cryptographically establish
whether an untrusted external system has committed. Even an original-ID
`rejected` receipt is **Host-attested evidence**, not an exactly-once
delivery claim. A timeout after the effect remains unknown until an
authoritative read-only provider lookup. Multiworker production eligibility,
older in-flight Run inventory, and published-version certification are
separate gates.
