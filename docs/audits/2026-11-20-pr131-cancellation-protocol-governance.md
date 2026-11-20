# PR #131 cancellation protocol governance

## Reviewed boundary and root causes

The final audit of `5198b27d` found two related protocol gaps, not two isolated
error cases. A cancellation election paused new admission but supplied no
exclusive Host-cleanup dispatch identity. Agent return, Kernel terminal status
and a Root-only busy/no-effect witness could consequently acknowledge or replay
an independently unknown Host/descendant cleanup. Separately, cancellation
recovery understood an undispatched replacement Root but not every legitimate
pre-dispatch lifecycle cut.

This record supersedes the third-audit claim that current native custody alone
could safely authorize every original cancellation phase. It retains the
original Kernel custody, complete global receipts, Attempt CAS and accounting
obligations from all preceding rounds.

## One election, separate effect authority

Implementation: `7c767046` (`fix(harness)!: separate cancellation election and original effect settlement`).

New intents freeze the exact `effects_v1` payload and native/Host route class.
Recovery first elects the one original unresolved intent and validates that
protocol, before accepting any Kernel snapshot or current Runner composition.
Unresolved older intents are protective unknowns, never automatically upgraded.

Native cancellation continues to use the original Kernel control CAS,
physical-grant barrier and actual callback custody. It does not acquire a second
Store/lease owner. A custom Host callback uses a separate private effect claim:

1. Append an immutable `harness.cancel.effect` claim **before** callback entry.
   It identifies the original intent, target Run/origin and stable Host owner
   where configured. The original Turn lock rechecks the exact Root/target
   Attempt, active intent and complete global effect receipts.
2. At most one unsettled Host claim may exist in that elected cleanup scope.
   A concurrent caller cannot reuse the election as callback dispatch authority.
   A terminal target cannot admit a late Host claim after terminal projection
   wins that same Turn transaction.
3. Give the Host `CancellationEffectFromContext`, whose ID fences/deduplicates
   its one external operation. A matching returned terminal Kernel result is
   necessary but insufficient to acknowledge unknown historical Host work.
4. Record one private, original-claim-bound `settled` or `no_effect` receipt.
   Lost ACKs, timeout, panic and ordinary Host errors do not create a receipt.
5. Global completion requires every original effect receipt under the same
   Turn lock. A Root busy/no-effect proof settles only the Root callback that
   returned it, never the later child cleanup it enabled.

Public AppendItem cannot mint or squat in claim/receipt IDs. Globally indexed
children, including foreign-Turn contradictory siblings, are the receipt source.
Memory and SQL consume the same predicates. SQL reads just the original
cancellation/effect parents and their bounded global children, not unrelated
Timeline payloads on every terminal CAS.

### Terminal projection is also an authority boundary

Checking only the final fence receipt would leave another race: a caller could
observe a terminal Kernel Run before a competing Host claim commits, then write
a terminal child/Turn or confirmed Subtask after that claim begins.

The Store now arbitrates both directions. A claim admitted first prevents the
affected terminal Invocation CAS, whole-Root terminal Turn CAS and cancellation
confirmation append; terminal projection admitted first prevents a late Host
claim for that target. These checks use the original Turn lock and effect
receipts, not another read-side observation. Unrelated child settlement is not
treated as the selected child's Host cleanup.

Normal return is required before the outer Cancel path can append successful
settlement. A panic across a write-capable native Store boundary cannot exploit
an as-yet-nil named error to close the intent.

## Original-owner reconciliation, not another Cancel RPC

Optional `RuntimeCancellationReconciler` supplies a stable
`CancellationOwnerID` and read-only `ReconcileCancellation` for the exact stored
`CancellationEffect`. A different owner is never queried. `Settled` also requires
the matching authoritative local Kernel terminal origin; a foreign same-ID
Snapshot fails. `NoEffect` must certify the original operation can never dispatch,
including through a delayed original caller, not merely that a remote task is
currently absent. The Host owns durable operation-ID fencing and authorization.

An opaque custom canceller still performs its first legitimate callback, but an
in-flight/unknown claim is not called again. Retry returns
`ErrCancellationOutcomeUnknown`, HTTP 409
`harness.cancellation_outcome_unknown`. Automatic read-only recovery requires
that a stable original owner was recorded at claim admission. An opaque claim
cannot acquire a new owner by upgrading the current composition; it requires
explicit out-of-band original-owner/operator reconciliation before rollout.
No read-only port issues another Model, Tool or remote Cancel operation.

Mixed `LocalKernelRunKindCanceller` implementations may pass through direct
Kernel child-busy no-effect only as an explicit assertion that **that callback**
did no Host cleanup. If Host cleanup could have begun, it must return unknown,
not lend the native proof to a broader effect domain.

Diagnostic Timeline/feed projection strips the private original Kernel origin
and owner identity, as well as nested physical Run references. HTTP errors use
safe messages without the Host journal's private failure detail.

## Complete no-dispatch lifecycle meaning

Initial control and recovery consume one frozen-source/no-dispatch predicate:

| Original lifecycle | Required source / retained barrier |
| --- | --- |
| First Accepted Root | Exact native Agent/Kernel/frozen input, no physical Start grant, original admission and budget generation |
| Accepted replacement Root | Same proof plus original atomic Retry generation and complete predecessor child history |
| Waiting-only | Exact typed frozen capacity wait, no Started admission, child Invocation, physical grant or unexpected relation |
| Frozen Started before child Invocation | Exact original queued execution envelope/parent and optional matching delegation relation; no child/physical grant |
| Waiting -> Started before child Invocation | Both immutable observations must agree; the same admission reducer is used by Cancel, Retry, Refresh and terminal CAS, including predecessor history |
| Prepared Handoff child | Frozen current Attempt/ancestor chain and affirmative original no-dispatch grant proof |
| Terminal non-Handoff Host owner | Preserve its original terminal projection only with no possible physical grant or unverified RunRelation; never reopen it |
| Opaque old Started / remote missing Run / pending grant | Remain unknown; missing Kernel Run is never a successful cancellation receipt |

`Store.CancelUndispatchedRoot` is now required, replacing the optional
Retry-only capability. Memory and SQL atomically commit both Invocation and Turn
terminal revisions under their original admission lock, including the first
`TurnAccepted` lifecycle. The initial budget closure does not invent a Run
registration. Replacement closure preserves the existing admitted Root grant.
No synthetic Kernel Snapshot supplies Root execution identity.

Original charged/unknown reservations, old Attempts, Waiting observations and
terminal child receipts remain immutable. A cancellation reply lost after the
atomic terminal CAS recovers the same records without another physical Start.

## Regression and crash matrix

The first-party real PostgreSQL lifecycle selector explicitly registers all new
protocol scenarios; DSN support in an otherwise unselected test is not coverage.

- Root / Invocation / Subtask; same controller and reconstructed controllers;
  custom unknown -> native and same-custom recovery.
- Original callback still inside Cancel; returned unknown with external cleanup
  still pending; genuine natural child completion while Host cleanup is unknown.
- Genuine native Root busy/no-effect followed by unknown child cleanup; no
  repeated child dispatch on Root retry.
- Original stable owner reconciliation, foreign owner refusal and foreign
  same-ID terminal Snapshot refusal; no additional physical Cancel RPC.
- Intent, Host claim, Host receipt and final completion before-write failures
  and committed-but-lost ACKs.
- Waiting request/budget/confirmation/projection/completion cuts; first Root
  with and without shared budget; atomic Root terminal CAS before/after ACK.
- Prepared Handoff and frozen Started-before-Invocation cuts; both relevant
  control scopes recover without inventing Kernel execution.
- Waiting -> Started at that same no-Invocation cut, before and after Root Retry;
  nested old Waiting controls cannot borrow an unchanged parent's physical Run
  ID after the original Root Attempt rotates, even before intent admission.
- Raw claim/receipt/global-completion forgery refusal and legacy intent
  quarantine.
- Deterministic late-claim vs terminal-projection race in **both** election
  orders; no Host callback after terminal projection wins and no terminal ACK
  after unknown Host claim wins.
- Panic at the native write-capable cancellation boundary retains the intent.

The protocol matrix has 195 backend/scenario combinations across Memory, GORM
SQLite and actual native SQLite Kernel/RunRelations. Native SQLite still uses
Memory Harness in this repository; there is no claimed native SQLite Harness
adapter. The same SQL cases run in isolated real PostgreSQL schemas.

The historical async test that reissued unknown external cleanup now verifies
original-owner read-only reconciliation instead. The old opaque staged-child
fixture now explicitly retains its fence; a separate real frozen pre-Invocation
scenario proves safe recovery. Managed process-crash CANCEL fixtures elect an
explicit native v1 intent rather than silently upgrading an untyped old intent.

## Rollout and persistence

- No new SQL DDL or native Kernel schema: new facts use the existing Harness
  Item ledger. Native SQLite remains schema v8, with its previously required
  explicit migrations unchanged.
- This is a protocol/source hard cut, not a compatibility alias. Custom Harness
  Stores must implement `CancelUndispatchedRoot` and the private effect claim,
  receipt, terminal projection and final-completion checks under their Turn lock.
- Stop old writers; retain backups; drain or explicitly reconcile original
  unknown operations before rollout. Do not backfill source/owner from the new
  deployment or reset historical fences to restore admission.
- Product SDK pin/composition and original-owner reconciliation adapters require
  explicit Host adoption. No product rollout or companion Inspect/Control change
  is claimed by this Runtime-only PR.

## Validation gates

- The original final/deep-audit overlays pass three race-enabled iterations
  against Memory, GORM SQLite and actual native SQLite Kernel.
- The expanded real PostgreSQL 16.16 lifecycle suite passes `-race -count=3`:
  195 protocol backend/scenario cases, 585 successful executions, including
  65 distinct real PostgreSQL cases in each iteration. The original lifecycle
  cases also remain registered and pass.
- Core, Harness, Harness PostgreSQL, native Kernel Stores and dependent
  Provider/HTTP/A2A/Redis/MCP/OTel/transport/task-agent recursive race suites pass.
- Full local `make check-no-race` passes: format/tidy, vet, lint, unit/example
  tests, TypeScript gates, integration compilation and clean release consumers.
  The consumer port explicitly records the reviewed required Store method and
  optional original-owner reconciliation interface, rather than hiding the hard
  cut behind an embedded interface or compatibility adapter.
- Full Go workspace coverage gates pass. After the final SQL admission reader
  was aligned with Memory's pending-effect predicate, protocol and damaged
  authority Store tests also pass three race-enabled iterations with a real
  PostgreSQL DSN.
- Full `make integration-test` passes against private PostgreSQL/Redis, including
  actual managed process-crash reservation recovery and continuation E2E. The
  new native cancellation source is explicit in its crash fixture.
- New-HEAD CI, not the audited baseline's green CI, remains the remote acceptance
  gate. Both private PostgreSQL and Redis instances were stopped after validation;
  final remote results are recorded with the pushed PR.
