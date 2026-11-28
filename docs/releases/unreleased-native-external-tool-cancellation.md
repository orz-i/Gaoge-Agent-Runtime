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

## Local evidence at this checkpoint (2026-11-28)

PASS on Windows AMD64, Go 1.27.4:

```text
go test -race -count=1 ./go/agent-runtime-harness
go test -race -count=1 ./go/agent-runtime/agent
go test -race -count=1 ./go/agent-runtime-harness -run '^TestNativeExternalCancellation(Memory|RecoveryMemory|ConcurrentMemory)$'
go test -race -count=1 ./go/agent-runtime-harness-sqlite -run '^TestNativeExternalCancellation(SQLite|RecoverySQLite|ConcurrentSQLite)$'
go vet ./go/agent-runtime/agent ./go/agent-runtime-harness ./go/agent-runtime-harness-sqlite ./go/agent-runtime-harness-postgres
```

The focused real PostgreSQL 16 conformance also passed under `-race`:

```text
$env:TEST_POSTGRES_DSN='postgres://agent_runtime:agent_runtime@127.0.0.1:55432/agent_runtime?sslmode=disable'
go test -race -count=1 -v ./go/agent-runtime-harness-postgres -run '^(TestRealPostgresNativeExternalCancellation|TestRealPostgresNativeExternalCancellationRecovery|TestRealPostgresNativeExternalCancellationConcurrent)$' -timeout 5m
```

Coverage now exercises direct Root, targeted Member, parent and nested
cancellation; committed and no-effect owner receipts; sibling holds; restart
reconstruction; concurrent Resume/notification/foreign-control attempts; and
before/after recovery, retirement and cancellation-CAS acknowledgement cuts.
The original Tool and Model are asserted not to replay, and committed usage is
settled exactly once. An expired elected wait rejects both committed and
no-effect receipt reads without contacting the owner.

The full PostgreSQL harness integration command was attempted with disposable
PostgreSQL/Redis services. The new external-cancellation real-Postgres tests
passed in that run, but the package later hit its 10-minute test timeout in the
existing `TestRealPostgresRootGenerationLifecycle/terminal-independent-child-retry/sql`
test. The whole real-engine harness suite is therefore **not** claimed as PASS.

The real PostgreSQL/Redis continuation integration command
`node scripts/run-integration.mjs --suite=continuation` also ran. The
PostgreSQL/Redis cancellation and external-signal recovery tests passed,
including `TestRealPostgresNativeNoEffectCancelAcrossConnections` and
`TestRealPostgresRedisNativeEarlySignalProviderRecovery`. The overall package
still failed on this Windows host because the unrelated SQLite A2A fixture
rejected the temporary directory's ACLs in
`TestRealPostgresAndSQLiteA2AControlAdmissionAcrossConnections/sqlite`; the
complete integration profile is **not** claimed as PASS. Both test runs used
disposable services and removed their test containers/volumes afterward. No
Gaoge Browser downstream run, release, deployment, merge or full CI consumer
gate is claimed.

## Remaining work before Ready / merge

- Verify the real Browser owner's committed-receipt schema and billing mapping
  in the downstream composition; SDK fixtures cover the receipt contract and
  exact Tool reservation settlement, but are not a substitute for that host.
- Keep the passing multiple-Member, nested-scope, sibling-hold and original
  delegation-charge tests enabled on every backend.
- Keep deterministic concurrent Resume/control and before/after-CAS and
  retirement-ACK loss tests enabled, rebuilding from persisted Stores after each
  cut.
- Review late/ambiguous owner-receipt behavior and its interaction with expired
  waits; missing, foreign, started, denied and expired receipts are covered by
  the current fixtures.
- Run the complete real-engine harness suite to completion and execute the full
  static/consumer gates; focused PostgreSQL tests, affected unit/Race packages,
  focused SQLite tests and `go vet` have passed, but do not replace those gates.
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
