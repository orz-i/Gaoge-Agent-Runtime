# Issue #107 — Execution Authorization Contract (EA-1)

Status: **EA-1 v1 FROZEN (requirements and acceptance axes); implementation NOT certified**
Owner: Runtime (Kernel / Agent / Workflow / Harness)
Original scope: [Runtime Issue #107](https://github.com/orz-i/Gaoge-Agent-Runtime/issues/107), a Workflow AgentTask child proof and same-Kernel Harness approval.
Current implementation baseline: Runtime `c17eeb49cc1d95b39f16c796edcd403d9b0a5ec2`; Gaoge `c7759dcb27d9ef193f5079be0473cff61626676a`.
Paired acceptance records: [fixed Runtime matrix](issue107-execution-acceptance-matrix.md), Gaoge Hosted integration work package in the `Gaoge` repository.

## Purpose and change control

This document freezes **what must be true**, not a commitment to a new framework or universal replacement of existing stores. Keep existing Kernel Run, physical Grant, ModelInvocation/ToolDispatch, Agent ExecutionLease, Workflow Effect intent and first-party receipts when they satisfy the contract. Do not introduce backward-compatibility bridges for unpublished beta contracts.

A new finding must be mapped to an existing invariant, entrypoint and timing cell in the fixed matrix. Expand a matrix cell's test vectors if necessary, but do **not** silently add new classes of acceptance work during review. A genuinely new threat boundary requires an explicit EA-1 scope amendment, its motivation, affected cells and approval **before** code remediation. A high-risk violation always blocks acceptance even when a matrix cell is incomplete; freezing the plan is not waiving safety.

An existing regression file or a green `go test` is **evidence available**, not a certified matrix cell. Certification requires the exact frozen commit, test command, backend and observed result.

## Authority model

**Single normative rule:** no Model, Tool, Workflow Effect, compensation, or potentially side-effecting Host middleware may issue a physical request unless a Store-authoritative execution admission for its **current original owner** has succeeded.

The implementation may use specialized mechanisms so long as they prove the **same externally observable invariants**:

| Layer | Authoritative responsibilities | May not substitute |
| --- | --- | --- |
| Kernel Store | Immutable Run identity/ancestry; expected revision; Root/self and ancestor serialization; admission vs terminal mutation; trusted settlement marker | Host status reads, in-process mutex, eventual projection |
| Agent | Original Run/ExecutionLease and ModelInvocation/ToolDispatch identity; Tool approval; one attempted dispatch at a time; exact model/tool receipt | Workflow-only Grant or a middleware's promise of no effects |
| Workflow | Segment/Effect/Wait and Cancel/Compensation admission; frozen Effect/Attempt identity; durable intent/receipt; first-party recovery | Child relation alone or a RunStatus=completed check |
| Harness | Original invocation/approval/cancellation election; immutable relation proof; callback and shared-budget fencing | Grant creation or physical receipt fabrication |
| Gaoge Host | Frozen AgentTask input/Context/Model/Tools/budget; original middleware source; provider route/replay persistence | Runtime Kernel authority or a cross-provider replay approximation |

Root Agent currently retains a distinct ExecutionLease + ModelInvocation/ToolDispatch protocol. **EA-1 does not require replacing it with the Workflow Root Grant.** It DOES require an auditable atomic admission covering every effect-capable Root Agent callback, including Host RunMiddleware, or a documented proof that the existing mechanism covers the identical boundary. Until demonstrated, the relevant acceptance cells remain OPEN.

### Required identity

`ExecutionOwner` is the original Run kind/ID, tenant/actor/thread, immutable parent chain, original invocation/effect origin, and exact admission authority (owner/epoch/expected revision). A child cannot be promoted to Root if relation evidence disappears.

`PhysicalAttempt` adds the concrete Effect ID, **attempt/generation**, selected provider/tool/route where applicable, and exact immutable source payload or fingerprint. A prior attempt's receipt, `PhysicalNoEffect`, approval or ModelInvocation cannot authorize a different attempt.

A valid Grant must be indexed against the **executing Run itself** and **every ancestor**, not just ancestors. Its write permit is a scoped first-party capability bound to the original Grant and current revision. No publicly visible Grant ID alone is a mutation or settlement credential.

## Frozen invariants

| ID | Mandatory rule |
| --- | --- |
| **EA-01** | All effect-capable entries (Root/Child Agent/Workflow, Start, Resume, Approval, ToolWait, Cancel/Compensation and Host middleware) admit atomically **before** any external dispatch; pure preflight never performs external work. |
| **EA-02** | Admission verifies original owner, actor/tenant/thread, parent ancestry, effect/attempt source and expected Run revision **inside one authoritative Store transaction**. A stale or foreign request fails before any physical callback. |
| **EA-03** | At most one active physical owner for one execution attempt. Its Grant fences competing control/writes for **self + full ancestor chain**. Native first-party transitions use an exact scoped write permit, not a global exemption. |
| **EA-04** | Cancel/Fail/Deadline/Compensation coordinate with the active owner in the same Store. A busy rejection does **not** revoke the legitimate lease, shared-budget election, or execution epoch. Cancel does not secretly commit partial state. |
| **EA-05** | Original intent/source identity is durably committed **before** any external request; middleware and asynchronous continuations cannot escape the authorized lifetime. A returned child error never rolls back an already sent request. |
| **EA-06** | After a physical request is issued, absence of an authoritative receipt means **UNKNOWN**, not no-effect. Retain original fence; do not retry, release via TTL, auto-reparent or claim completion solely from RunStatus. |
| **EA-07** | A completed receipt or affirmative no-effect proof binds original Run, Effect/ModelInvocation/ToolDispatch, **attempt/generation**, provider/tool route and original owner; no cross-attempt reuse. A timeout or arbitrary conflict is not no-effect evidence. |
| **EA-08** | Durable settlement marker and release are separate, idempotent steps. A lost ACK after marker commit can be recovered by the first-party owner **without redispatch**. Before the marker exists, a clean state cannot retire an active callback. |
| **EA-09** | Recovery must distinguish pre-dispatch failure, dispatched-unknown, receipt-committed, marker-committed and final retirement. Reopen/retry across independent processes must preserve identical conclusions. |
| **EA-10** | All three Stores (Memory, native SQLite, native PostgreSQL) implement the same observable state-machine/error contracts; PG must use real independent connections/transactions for deterministic stale-read, commit/ACK-loss and terminal-race tests. |
| **EA-11** | Harness approval and cancellation use one original child proof, frozen caller identity and same-Kernel ownership; no direct-approval path, missing-relation fallback or foreign child adoption. |
| **EA-12** | Gaoge may only project a frozen, identity-consistent provider replay and SDK cohort. Host integration failures are separate release blockers, **not** evidence that Kernel safety passed or failed. |

## Minimal state machine

```text
PREPARED (pure validation, no effects)
  -> ADMITTED (original owner + exact revision serialized by Store)
  -> INTENT_COMMITTED (original effect/attempt durable)
  -> DISPATCHING / OUTCOME_UNKNOWN (physical call may have occurred)
       -> RECEIPT_COMMITTED (trusted result for the same attempt)
       -> NO_EFFECT_CONFIRMED (trusted proof tied to that attempt)
       -> QUARANTINED_UNKNOWN (no trusted proof: NO replay, NO release)
  -> SETTLEMENT_MARKED (original physical callback safely returned)
  -> GRANT_RETIRED (recoverable, idempotent release)
```

`CANCEL_REQUESTED` is a competing control election, not an override of `DISPATCHING`. If admission has already won, cancellation receives a typed busy/no-mutation response and must preserve the original owner/lease. Once physical settlement is proven, the original cancellation may resume under the same election. A cancellation that itself dispatches compensation must obtain its **own** physical admission before the undo callback.

**Important limit:** This contract cannot make an arbitrary external provider exactly-once by itself. When an external call has an unknown outcome and the provider offers no idempotency/receipt query, **quarantine is the correct safe result**, not fabricated progress.

## Store transaction obligations

1. Read enough immutable identity to choose lock order, then lock Root -> each ancestor -> executing Run. Revalidate current status, owner, revision and original attempt **after locking**, in the transaction that writes Grant admission. A read made before waiting for a lock is provisional.
2. Persist original Grant ownership for self and ancestors in the same transaction; competing admission/terminal/control must return a typed conflict or busy result without partial mutation or revocation.
3. Validate first-party scoped per-revision write permit on every mutation of an active owner (ordinary Apply, control/approval, terminal transition); prove that denied control did not alter an existing execution epoch.
4. Mark and Finish must validate the **complete same original Grant** (including start revision) and the current trusted settlement proof in their own transactions. Lost ACK must be recoverable with a read-only original identity check.
5. Root Agent may use an already-existing specialized execution store protocol if it satisfies EA-01..EA-09. The acceptance test must exercise actual Root Agent middleware/provider/tool boundaries, not only mock Kernel state.

No production schema migration is part of the current local governance scope. The existing unpublished SQLite v3 and PostgreSQL `start_revision` hard cuts remain documented separately.

## Fixed review and release rule

Treat this as **two dependent work packages**, not an open-ended security-audit series:

**WP-RUNTIME:** finalize EA-1, map all applicable cells and run the same deterministic state machine against Memory, SQLite and real PostgreSQL, including attempts, restart and cancellation. Complete the original AgentTask proof acceptance. Frozen, independently reviewed Runtime commit with zero unresolved critical/high-risk invariants and all required checks is its exit.

**WP-GAOGE:** pin the accepted Runtime cohort, fix original Hosted ProviderReplay and run the full Workflow -> AgentTask -> approval -> MCP Tool -> receipt/continuation scenario. Frozen Host commit, full E2E and exact PR-head checks are its exit.

Both work packages may have separate reviewable PRs in dependency order, but **no unsafe interim commit is merged**. Remaining unknown external effects, unexecuted checks, migration risk and source limitations are explicitly listed in the final acceptance record. The final reviewer checks the **closed matrix and affected chain at the exact head**; a new generic audit is not itself an acceptance criterion.
