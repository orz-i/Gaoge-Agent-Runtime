# PR #131 third-audit governance

## Findings and segmented fixes

The audit of `4b1090f3` confirmed one P1 cancellation recovery gap and one P2
Retry evidence mismatch. This follow-up preserves the immutable original
intent, physical custody, child ancestry and complete accounting history.

### P1 — Native Root cancellation phase recovery (`342825b2`)

The original cancellation could commit an intent, budget cancellation or budget
projection, then lose its response before the first Kernel Cancel. Its recovery
path required a later Kernel busy witness that could not yet exist.

Recovery now re-proves the exact current first-party Root and its actual
persisted callback custody instead of drawing authority from the returned error:

- A genuinely settled Root uses its existing complete retirement/ACK proof.
- A returned callback whose grant is retained for child-terminal receipts uses
  `ReturnedRootSettlementStore`. Memory, PostgreSQL and native SQLite verify
  the exact current snapshot, original physical grant/epoch/owner, actual Host
  return, scoped custody and complete safe historical ACKs under the original
  Run lock. TTL, a public same-ID snapshot or a newer safe callback is not proof.
- Agent-owned typed deferred-child recovery eligibility must also hold. This
  authorizes re-entering the SAME elected native cancellation, not finishing
  the grant, declaring the children terminal, or releasing the intent.
- Native Cancel still arbitrates its own physical-grant/revision barrier and
  each child retains its separate route, custody and receipt obligations.
- Exactly one valid unresolved original intent may be resumed. Malformed,
  foreign or competing intents, live/unknown/opaque callbacks, borrowed owners
  and custom remote cleanup cannot borrow this recovery authority.

The durable-phase matrix covers original and replacement Roots, all three
native backends and a reconstructed Harness/controller over the original
Store/route/relations. It injects interruption before and after each of intent,
budget CAS, projection and completion. A reply lost after final durable
completion may start a harmless new observation, but every original intent
retains exactly one completion and all financial/effect history remains intact.

### P2 — Independent terminal child evidence for Retry (`c0fc6163`)

A delegated ChildRunner may legitimately leave its Harness Invocation Accepted
after the physical Run and complete original Handoff receipts are terminal.
Normal parent terminal CAS consumed that proof, but Retry only inspected the
stale child status and existence of dispatch.

`RetryChildEffectFenceWithBudget` now consumes the same frozen Handoff source,
exact current/predecessor Attempt, ancestor owners and complete terminal
Delegation/physical-return receipts as normal terminal projection and historical
cleanup. Its SQL loader uses the same globally indexed complete evidence under
the owning Turn lock, including Waiting facts and immutable Attempt transitions.

No child projection is force-updated. Live Host claims (Running, WaitingInput,
PausedBudget), unknown/missing/contradictory receipts, stale/orphaned ancestry
and pre-hard-cut malformed Handoffs remain fenced. Historical budget grants,
shared usage and all original receipts are preserved. The end-to-end regression
proves a known post-child provider refusal can Retry without first calling
RefreshInvocation or replaying the child Model.

## Contract and compatibility

- No SQL schema change: the returned-callback proof reads existing v8 native
  SQLite and current PostgreSQL grant/return/retirement facts. The previously
  documented explicit SQLite migrations through v8 remain mandatory.
- The new optional Kernel Store capability is a read-only original-callback
  custody query, not a generic Cancel, terminal vote or dispatch/Finish grant.
  Unsupported custom Stores retain fail-closed behavior and the existing
  source-restricted native busy-witness recovery path.
- The original Attempt-bound Harness intent admission and final descendant
  cleanup/terminal CAS remain mandatory; this change does not relax them.
- Custom RuntimeCanceller errors are not physical no-effect evidence and do
  not authorize this native phase-replay path.

## Regression coverage

- Intent before-write failure and committed-intent ACK loss.
- Budget CAS before-write failure and committed-budget ACK loss.
- Budget projection before-write failure and committed-projection ACK loss.
- Cancellation completion before-write failure and committed-completion ACK loss.
- Original/replacement Roots on Memory, real PostgreSQL and actual native SQLite;
  unchanged original Item payload/sequence and no extra Model effects.
- Returned callback negative matrix: unfinished/opaque/borrowed callback,
  foreign owner/completion, missing grant, stale snapshot/epoch, old opaque or
  unacknowledged history and duplicate historical epochs.
- A custom cleanup controller cannot consume the native callback proof.
- Accepted terminal child Retry and post-Retry cleanup without rewriting the
  child's independent projection; live claims, missing/foreign/duplicate terminal
  receipts, missing physical return and orphaned ancestors reject Retry.

The phase and independent-child cases are explicitly registered in
`TestRealPostgresRootGenerationLifecycle`, which is selected by the real-service
integration runner. DSN support in an otherwise unselected unit test is not
treated as real PostgreSQL coverage.

## Verification

- Both original audit findings pass their temporary regression overlays with
  race detection and three iterations against Memory/PostgreSQL/native SQLite.
- The expanded real PostgreSQL 16.16 lifecycle matrix, including all durable
  cancellation phases, passes three race-enabled iterations.
- Core, Harness, Harness PostgreSQL, Kernel PostgreSQL and native SQLite
  recursive race suites pass, as do dependent Provider/HTTP/A2A/Redis/MCP/OTel/
  transport and task-agent race tests.
- Full local `make check-no-race` passes, including formatting, isolated-module
  tidy, vet, golangci-lint, unit/example tests and clean release consumers.
- `make integration-test` passes against private PostgreSQL 16.16 and Redis
  8.10.3, including the complete durable-phase matrix and continuation E2E.
  Both temporary service instances were shut down after validation.
- New-HEAD CI, not the reviewed baseline's green checks, is the remote
  acceptance gate.
- Product SDK pin/composition and product Inspect/Control follow-up remain
  outside this Runtime-only change. The user's local probe scripts are untouched.
