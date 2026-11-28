# ADR 0020: Privacy-minimized retirement of negative Run creation decisions

- Status: Proposed for design review; no implementation or release implied
- Date: 2026-11-28
- Issue: [Gaoge-Agent-Runtime #161](https://github.com/orz-i/Gaoge-Agent-Runtime/issues/161)
- Baseline: `7c7e498d3f6e0c29edec2c0be523ea5ba23d8214`

## Context

`RunCreationDecision` is stored under the same permanent Run identity used by
positive `Create`. Positive identities use an empty payload. Negative decisions
persist a payload containing the original status and reason, ancestry, origin
fingerprint, and optional budget enrollment. That payload is used for exact
decision replay, anti-replay selection checks, and validation of retry lineage.
It can outlive a Run because the rejected Run was never created.

The Kernel identity record does not itself contain a verified Tenant/Actor
owner, and the Runtime has no actor-erasure API. Consequently, a Host cannot
safely infer ownership from a Run ID, parent, request-supplied Actor, or the
presence/absence of a Run row. Directly deleting the current identity record
would also release the permanent no-recreate reservation.

This proposal separates the permanent identity reservation from the
owner-attributable decision payload. It is a storage/lifecycle contract, not a
product retention policy or an authorization grant.

## Proposed decision

### 1. Preserve the identity; make the private payload independently retireable

Keep a permanent, immutable reservation for every Run ID. A negative decision
payload is a separate, removable record keyed by that reservation. It contains
only the decision fields needed while the decision remains live, plus a
verifiable binding to the authoritative owner source. Owner attribution must
come from trusted native creation/admission provenance, atomically checked by
the component that owns that provenance; it is never accepted as an arbitrary
Actor/Tenant value from a cleanup caller.

Positive Create identity behavior is unchanged. A negative identity remains
reserved even after its payload and owner binding are retired. No deletion,
compaction, or migration may make the ID available for a fresh Create.

### 2. Retire only with verified ownership and quiescence

Payload retirement is an operation through a narrow Runtime/Store lifecycle
capability, not raw table access or a general-purpose delete method. It must
verify all of the following against authoritative state:

1. The requested owner scope matches immutable provenance attached when the
   decision was recorded. Missing, ambiguous, or legacy-unattributed provenance
   is not guessed; the record blocks automatic owner-scoped retirement.
2. The owner lifecycle has crossed a durable retirement fence: no in-flight
   Create/Decide operation can commit afterward, and no admitted operation can
   still project or retry this decision. The fence and decision mutation must
   serialize with native creation and retry validation.
3. No live retry lineage still requires the decision payload. A decision with
   no Run is not presumed terminal merely because no Run row exists. If the
   implementation cannot prove all dependencies retired, it fails closed.
4. The operation removes only the payload and its owner-attribution material.
   It does not rewrite the SDK decision, mutate arbitrary Run/ledger rows, or
   remove the permanent identity reservation.

The exact owner-proof protocol is intentionally not defined as a new public
caller-supplied field in this ADR. The design follow-up must identify the
existing native source for root decisions with no Run as well as child
decisions; if there is no authoritative source, those decisions remain
unattributed and are not eligible for owner-scoped retirement.

The current first-party selection is not sufficient proof by itself. Root
selection in `go/agent-runtime-harness/cancel_undispatched_root.go` can carry a
Turn-scoped budget only when shared budgeting is configured. Child selection in
`go/agent-runtime-harness/delegation_execution.go` carries parent and origin
facts, and likewise has optional budget scope; neither shape establishes an
Actor owner in the Kernel. The design must add a trusted provenance binding at
the Harness/Runtime boundary rather than reinterpret those existing fields.

### Cross-store retirement boundary

The Harness owns Turn/Invocation/Delegation admission evidence; the Kernel
Store owns the Run identity and native budget facts. A transaction in either
store alone cannot prove that the other store has stopped admitting work. The
implementation design must therefore provide an idempotent retirement
handshake, not pass a caller-supplied `authorized` flag or assume a distributed
transaction:

1. The authoritative owner source durably fences the exact owner scope against
   new first-party admission and retry selection.
2. The Runtime validates a provenance-bound fence reference and, in the native
   identity transaction, retires eligible payloads while preserving their
   permanent reservations. Decision creation/retry must serialize against the
   corresponding native fence or equivalent durable owner-scope state.
3. The owner source finalizes its own deletion only after Runtime confirms the
   complete eligible set. A crash resumes idempotently from the durable fence;
   it does not reopen admissions or drop the Run ID reservations.

The exact source references and fence storage are implementation-design gates.
If they cannot be anchored in existing authoritative records without trusting
arbitrary Host mutation, the feature remains unavailable for that scope.

### 3. Define behavior after payload retirement

Exact replay of the original `Reason` and complete decision is possible only
while that payload is retained. After retirement:

- `LoadCreationDecision` reports a distinct retired/erased outcome; it does not
  fabricate a decision or return a synthetic Run.
- A stale `Create` or `DecideBeforeCreate` using the reserved ID is rejected
  permanently. Neither a matching nor a changed selection can replace the
  original decision or recreate the Run.
- Retry validation that needs the removed predecessor decision fails closed;
  missing payload is not evidence that no prior decision existed.

This deliberately preserves anti-replay and no-recreate safety, but does not
promise exact negative-decision replay after the product has authorized payload
retirement. The final public error shape and compatibility handling for existing
callers require review before implementation; they are not silently changed by
this design-only PR.

### 4. Keep product authorization and physical deletion claims separate

Runtime defines what data is required for execution safety and how an
authorized retirement preserves identity. The product owner decides whether a
particular actor's data may be retired and when. A logical row deletion is not a
claim of secure physical erasure from database pages, WAL, replicas, exports, or
backups; any such claim needs a separate storage and backup-retention policy.

## Required implementation proof

The implementation PR must add identical contract coverage for Memory,
PostgreSQL, and SQLite (including independent PostgreSQL connections and SQLite
close/reopen):

- Owner-attributed negative decisions both with and without a Run; verify the
  provenance is genuine and scope-matched before retirement.
- Unattributed, ambiguous, foreign-owner, corrupt, or stale-provenance records
  block retirement without mutation.
- Competing Create/Decide/retry and retirement operations serialize correctly;
  commit/ack loss cannot clear or replace the permanent reservation.
- Before retirement, exact replay and existing retry validation still behave
  as specified. After retirement, stale positive Create, negative replay, and
  retry cannot revive the identity or manufacture a Run.
- Cross-owner identity, payload, budget ledger, Run, and event state remains
  unchanged; checks include complete before/after state, not only row counts.
- Migration preserves all existing identity reservations and payloads. It
  does not infer owner scope from Actor fields, parents, or missing Runs, and
  fails closed on unsupported or ambiguous layouts.

Tests cited by downstream product work are useful evidence for that product's
Actor Scope behavior; they do not replace Runtime Store conformance or the
cross-backend lifecycle proof above.

## Consequences and review gates

- Existing negative payloads remain intact until a separately authorized
  lifecycle operation can prove their owner and quiescence. No automatic
  cleanup or data migration is part of this proposal.
- The design does not add a generic Host permission to delete Runtime records.
- Before implementation, reviewers must approve the source of owner provenance,
  the durable retirement-fence protocol, the public post-retirement error
  behavior, and the dependency rule for retry lineage. If any cannot be proven
  with existing authoritative state, narrow the feature rather than weakening
  identity or anti-replay guarantees.
- This ADR alone does not authorize schema changes, release publication,
  deployment, or product data deletion.
