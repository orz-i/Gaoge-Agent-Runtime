# PR 155 — Tool Call dispatch admission and authorization commit

Base: `0b35ca5b`. Local governance after independent audit; not merge admission.

## Protocol

- Replace prerelease `requiredApproval` with one exact-Call `toolAdmission`
  state: evaluating, required, authorized, awaiting_capacity. A configured
  approval policy is entered only after evaluation intent CAS; a positive
  decision or restriction is committed before subsequent actions. The original
  approval source is durable, not reconstructed from a successor policy or an
  in-process allowed map. Uncommitted authorization makes the original whole
  return incomplete and independently blocks physical settlement.
- Scope binding is persisted before native shared-budget reservation. Reserve
  capacity before writing physical dispatch intent or entering Tool middleware.
  The actual Tool adapter receives the same in-process native reservation ticket;
  it does not resolve/reserve another scope. Direct adapter use shares the
  reservation primitive and returns the real capacity error instead of synthetic
  Pending. Native budget denial is distinct from Host projection failure.
- A waiting reservation is unsent, uncharged, without a Tool receipt. A
  reconstructed owner can retry the exact original Call and committed decision
  only under the bound ledger. Removed or foreign adapter, missing same-ID
  ledger, invalid reservation and receipt-only reconciliation of an unsent
  Call are rejected. Known permanent denial releases its unsent reservation;
  earlier dispatched children drain before the later denial/wait is consumed.
- Use the same original native transition for authorization, capacity binding,
  waiting and dispatch intent. Exact source/successor evidence handles the
  existing bounded pure-CAS retry. No increased retry count or additional
  Tool settlement owner.

## Verification

- Reproduce the preceding independent audit's persistent two-write approval
  frontier fault: evaluation intent remains durable, HostCompleted is false,
  child cleanup, original Grant retirement and replacement Resume are refused;
  the original required Call is never dispatched.
- Five capacity cases: no policy, bounded policy, approved checkpoint, mixed
  quota, mixed concurrent slot. Confirm no physical marker or Tool timeline
  during waiting; original policy once, no result-verifier lookup on unsent
  Call, wrong/missing/same-ID empty ledger refused, repeated wait safe,
  released capacity executes original Call once, and mixed permanent denial
  drains original children and releases the unsent reservation.
- Actual Harness BudgetAuthority and Tool Timeline ports pass both middleware
  orderings: no Tool Item before capacity, exactly one Started and Completed
  after capacity becomes available.
- Shared receipt/approval suite passes Memory, native SQLite, GORM,
  independently reopened encrypted SQLite Kernel and Harness ledger and
  independently reopened real PostgreSQL Kernel and Harness ledger (39 leaves).
  Core/Harness package tests, targeted Race, workspace fmt/tidy and Core/Harness
  lint pass. The final Harness lint-only test switch enumerates all ItemStatus
  values; targeted test rerun verifies it.

## Boundaries

No new SQL schema or public settlement owner. This is a private state hard cut
within the unreleased PR: legacy `requiredApproval` states are not silently
upgraded. Repeated failure before durable authorization decision commit is
intentionally fenced, not automatically recovered by re-evaluating the policy;
actual callback evidence must be recovered by an authorized operator/protocol.
Capacity/projection faults that do not provide a qualified original ledger
fact also remain conservative. Complete continuation-worker mixed-batch
delivery, production multi-worker/process-death combinations and downstream
Gaoge integration are not newly certified. No commit, push, merge or deployment
performed by this governance batch.
