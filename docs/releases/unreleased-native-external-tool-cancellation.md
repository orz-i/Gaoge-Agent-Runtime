# Native external Tool cancellation — Draft checkpoint (#162)

**Status: in-progress / NO-GO.** This checkpoint preserves incremental work for
[issue #160](https://github.com/orz-i/Gaoge-Agent-Runtime/issues/160) and
[Draft PR #162](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/162).
It is not a release, a completed acceptance report or authorization to deploy.

## Original failure

At `7c7e498d`, native cancellation closes the original SharedBudget with the
terminal Run CAS. Unknown dispatched Tool reservations block that CAS without
revoking quota. The external cancellation scope verifier instead required an
already-cancelled ledger. For a Root's own external wait or a targeted Member,
the native busy/no-effect path could also finish the original Harness intent.
Both conditions prevented obtaining the original external owner's receipt.

The existing external scope test selected split-store Harness accounting. Its
passing result did not cover the native composition used by Gaoge PR #97.

## Current candidate contract

1. The Host authenticates its original control and invokes `Cancel` or
   `CancelSubtask`. A verified returned external wait retains the original
   Harness cancellation intent and native physical Grant, without budget
   revocation or an external owner RPC.
2. `VerifyExternalToolControlScope` checks the frozen Actor, Root/Member chain
   and elected subtree. **For native accounting this is no longer read-only:**
   it retains the SDK election and binds the owner's exact `ControlID` on the
   original Agent Run. Another owner control cannot replace it. This operation
   itself does not call the owner or change quota.
3. The Host obtains its original owner's durable no-effect or committed
   disposition, under its own original dispatch/deduplication boundary. The SDK
   never supplies an external cancellation RPC retry policy.
4. `ReconcileExternalToolNoEffectForControl` consumes the trusted read-only
   no-effect receipt. Native zero-usage settlement and Run state commit in the
   same receipt-only mutation. `ReconcileExternalToolForControl` stages a
   verified committed original result under the retained election.
5. Ordinary Agent Resume and unrelated result/recovery entrypoints remain
   parked or rejected across original Grant retirement. The Host finishes with
   `ContinueExternalToolCancellation(ctx, turnID, targetRunID, controlID, reason)`
   using the **same bound owner control**. Ordinary `Cancel`/`CancelSubtask` must
   not finish a bound external cancellation for another caller.

The Agent ports retain the original wait revision for exact receipt recovery
after election/control-binding CAS transitions. They do not authorize a changed
call, owner, Grant, operation or input digest. The original native cancellation
CAS still owns final Run/budget closure. No second Host budget ledger, schema
table, synthetic terminal receipt or released unknown quota is introduced.

Gaoge's native Browser composition currently finishes with its old `cancel()`
closure. It must adopt the explicit same-control continuation port before the
new candidate can pass the unchanged downstream test assertions. No downstream
source or SDK pin has been changed in this checkpoint.

## Local evidence at this checkpoint

PASS on Windows AMD64, Go 1.27.4:

```text
go test -race -count=1 -v ./go/agent-runtime-harness ./go/agent-runtime/agent -run '^(TestNativeExternalCancellationRetainsOriginalElection|TestExternalWaitOnMemberOnlyToolUsesExactElectedSubtree|TestExternalNoEffect.*)$'
```

The new native test covers Root external waits, Member cancellation and parent
cancellation with an active Member. Assertions include one retained election,
held unknown quota, started-owner refusal, foreign owner control refusal,
exact no-effect receipt replay, original native retirement, parked Resume after
retirement, ordinary/foreign final cancellation rejection, and zero Tool usage
after the same-control continuation.

The complete Agent package unit command passed during development. A combined
Agent/Harness full command exceeded its 120-second local command limit after
the Agent package passed; **full Harness validation is not claimed**.
Docker Desktop's Linux engine was unavailable. No real PostgreSQL/Redis,
downstream reproduction, release, deployment or merge is claimed.

## Remaining work before Ready / merge

- Complete native committed-owner coverage, including exact billing and final
  continuation with no ordinary Model/Tool replay.
- Exercise multiple Members, nested scope, untouched sibling holds and original
  delegation charges, rather than relying on the single-Member fixture.
- Add deterministic concurrent Resume/control and before/after-CAS and
  retirement-ACK loss tests, rebuilding from persisted Stores after each cut.
- Review missing/foreign/late/ambiguous receipt and expired wait behavior.
- Run shared native conformance on Memory, SQLite and real PostgreSQL; run the
  complete affected unit, Race, vet/static and consumer gates.
- Independently run the unchanged Gaoge tests
  `TestPostgresBrowserCancellationRespectsMemberScope` and
  `TestPostgresBrowserCancellationRequiresOriginalOwnerReceipt` with disposable
  PostgreSQL/Redis and the candidate SDK plus explicit continuation adoption.
- Finish compatibility/rollback review and update PR evidence before marking
  Ready. Do not count skipped real-engine tests or an unavailable Docker daemon
  as integration success.

## Compatibility boundary

This candidate adds persisted Agent election state and an explicit trusted
continuation API, not a SQL schema version. Stop old writers before introducing
these state fields; old writers do not honor the retained cancellation fence.
Do not downgrade an in-flight elected cancellation to an older writer. Resolve
its original effects first. Historical closed intents and unknown receipts are
not retroactively promoted into cancellation authority.
