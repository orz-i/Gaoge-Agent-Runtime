# PR 155 — native admission return and Grant retirement

Base: `6baff2ee`. Local governance after independent audit; not merge admission.

## Contract

- A committed Tool intent is not callback admission. Keep the original
  no-entry witness through intent acknowledgement and the final original
  reservation, Context, deadline and native execution checks. Drop that
  witness only at actual ToolChain entry. An interruption before entry uses
  the existing `unsent` closure or original returned no-entry proof; after
  entry keep the original physical-outcome fence.
- All native RunChain entrypoints use one invocation-bound return frame
  (Start, Prepare, Resume, ResolveApproval and managed continuation). The
  selected concrete Budget Run frame records a denial only when its native
  registered ledger proves elected cancellation, its projection returned
  successfully and Next has not been invoked. The unique original error
  identity and exact empty downstream return are checked by that same frame;
  caller-provided error types and unrelated/nested invocations are not
  authority. Context replacement by middleware preserves this original frame.
- A qualified bounded Run denial returns the unchanged original Run
  snapshot, using the existing native return qualification and callbackscope
  receipt. Root and parented Grant retirement still require the original
  whole callback and valid physical-state predicate. Do not manufacture
  terminal business state or resolve an approval; the elected Kernel control
  may finish cancellation after retirement. Opaque errors or failed/altered
  returns retain their original fences.
- No new SQL schema, physical Grant type, cancellation owner, custody
  classification, synthetic Tool receipt or settlement path.

## Local evidence

- Both independent P1 reproductions pass. Four ACK-cut cases cover metered
  and unmetered caller cancellation, final Store owner check rejection, and
  original Ledger cancellation. The exact no-entry Call never enters the
  Tool timeline, never calls its Handler, does not fabricate a receipt or
  require provider verification; resumable cases later execute once.
- Seven native return cases: Tool-only, Run+Tool, native parented Run+Tool,
  projection failure, opaque denial before Next, opaque augmented error
  after Next, and opaque masked denial. Unknown/altered cases retain the
  original Grant. Known Run denials permit the elected Kernel Cancel.
- Actual Harness StartAgentTurn with the original Run/Model/Tool Budget
  adapter and elected Harness.Cancel: inject a native Resume after the
  Ledger cancellation CAS commits but before its ACK returns. Resume
  observes the exact bounded denial, retires its native Grant, and the
  original Harness cancellation finishes. Neither Model nor Tool is sent
  again, original reservation is released, cancellation fence completes.
- Start/Prepare/ResolveApproval native denial tests preserve original
  snapshots (including the pending approval checkpoint) and retire Grants;
  no generic pre-Create no-effect marker is minted.
- Shared receipt/approval/unsent/native admission matrix passes Memory,
  native SQLite, GORM, independently reopened encrypted SQLite Kernel/Ledger,
  and actual PostgreSQL Kernel/Ledger (61 leaf cases). Core/Harness package
  tests, directed Race, fmt/tidy and Core/Harness lint pass.

## Boundaries

This is an original native admission result, not general external callback
no-effect authority. A cancellation may race after the final check and
actual ToolChain entry; external/opaque callbacks or unproved effects after
entry remain fenced. The independent Kernel and Ledger checks are not a
distributed atomic transaction. Production process-death/lease-expiration
and complete continuation-worker mixed-batch delivery matrices are not
newly certified; final Store owner-check failure was injected, not an
actual expired lease. The real Harness cancellation interleaving above was
tested with Memory stores, not real PostgreSQL Harness concurrency.
Unacknowledged Host projection or unregistered/ambiguous budget binding
is not a qualified original denial. No commit, push, merge or deployment
performed in this governance batch.
