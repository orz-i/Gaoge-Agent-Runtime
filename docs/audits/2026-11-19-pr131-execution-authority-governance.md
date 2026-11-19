# PR #131 execution authority governance

## Scope and outcome

Follow-up to the independent audit of `f97c1b9f` in PR #131. The five findings
are addressed through shared authority contracts, not entry-point exceptions.
The lifecycle matrix also exposed and fixes delegation-ID reuse across Root
Attempts when a provider reuses the same Tool Call ID.

## Segmented changes

1. **Actual callback provenance** (`107f7c90`). The actual executing callback
   seals prepared/scoped custody into its immutable return evidence. All
   retirement/ACK history must have safe custody; a rebuilt or same-Kernel
   observer cannot upgrade opaque callbacks, and a later safe callback cannot
   erase an earlier opaque effect. Cancellation rechecks custody after CAS.
2. **One Root-generation cancellation predicate** (`aff79325`). Budget
   admission, prospective child ancestry, cleanup, no-effect recovery and public
   views use `Ledger.RunCancelled`. The original global closure is immutable;
   replacement Roots are independently revoked. New child admission still
   checks its own cancellation and all ancestors before any accounting write.
3. **Immutable Attempt history across the entire lifecycle** (`7ed26059`).
   The original Harness Store writes a reserved transition fact in the same
   transaction as Retry/budget admission. Budgeted and unbudgeted executions
   share predecessor proof for terminal CAS, cancellation and no-Run recovery.
   Historical Handoff receipts and ancestor bindings remain mandatory. Existing
   historical children are not redispatched for cleanup under a new Root.
   Delegation identities retain original first-Attempt IDs while later Attempts
   are scoped to their immutable execution reference.
4. **Lease ownership is not physical admission**. Memory and PostgreSQL persist
   the last actual physical-admission epoch under the original Run lock, for
   both ordinary and managed successor admissions. Settlement requires that
   exact latest receipt and complete safe history, not merely a lease epoch
   comparison. Released/expired undispatched leases can no longer strand
   prepared cancellation. Live callbacks, missing latest receipts, quarantine,
   borrowed ownership and opaque history still fail closed. A transient
   delegated cancellation resumes the same elected intent only after exact
   route/native custody becomes provable again.

## Persistent data and migration

- PostgreSQL `Migrate` adds callback-custody and physical-admission watermark
  columns with unknown/zero defaults. No historical custody is backfilled from
  a new Runner's configuration.
- Native SQLite schema is now v7. Existing v6 databases require explicit
  `MigrateV6ToV7` with old writers stopped and a retained backup. Older schemas
  complete the existing explicit migrations first. All original marks,
  callback hashes, retirement payloads and managed authority are preserved.
- Empty legacy callback custody remains observable/recoverable under its old
  receipt contract, but is not newly certified as safe deferred-control custody.
- Root transitions use the existing append-only Harness Item ledger. Public
  Item append cannot mint the reserved transition namespace; only the original
  Store Retry transaction can create that ancestry authority.

## Regression coverage

| Contract | Positive coverage | Negative coverage |
| --- | --- | --- |
| Callback custody | Native/bounded preparation, selected Model-owned policy | Foreign same-Kernel Runner, reconstructed opaque Prepare, detached policy/completion effects, older opaque history followed by a safe callback |
| Budget generations | Failed and cancelled original Roots, nested prospective children, second/third Attempt, cancelled current-scope projection | Old ancestors, pre-revoked child IDs, stale cancellation, exhausted and unknown usage |
| Historical effects | Budgeted/unbudgeted Retry completion, Refresh/Cancel, repeated cancellation across three generations, unchanged original receipts | Missing transition links, substituted predecessor/input, live old children, missing terminal receipts, orphaned ancestors and public transition forgery |
| Physical epochs | Released and expired undispatched leases, cancellation attempted while a lease is live and recovered after release | Missing/latest admission watermark, active grants/owners, duplicate receipts, stale sources and opaque previous callbacks |

The Harness lifecycle matrix uses Memory and SQL Stores, including durable SQL
RunRelations. When `TEST_POSTGRES_DSN` is configured, those same SQL scenarios
run against isolated real PostgreSQL schemas; otherwise they use the existing
SQLite-backed GORM test fixture. This prevents a separate positive-only
PostgreSQL matrix from overlooking the negative lifecycle cases.

### CI gate follow-up

The first follow-up CI run exposed a missing direct test dependency on Kernel
PostgreSQL in `go/agent-runtime-harness-postgres/go.mod`; workspace compilation
and race tests alone do not validate isolated module metadata. The dependency
is now declared at the coordinated cohort version and the workspace
`tidy-check` passes without changing module files. No tidy gate is relaxed.

The real-service runner selects only `TestRealPostgres*`. A dedicated
`TestRealPostgresRootGenerationLifecycle` entry now runs the same custody,
Retry/cancellation, history, repeated-generation and lease matrix under that
selector. Merely supporting a DSN in an otherwise unselected unit test was not
sufficient real-service coverage. Earlier integration success must not be
mistaken for evidence that this new matrix was selected.

## Verification and remaining boundary

- All original independent audit overlay regressions pass with
  `go test -race ... -run '^TestAudit131' -count=3`.
- Expanded custody/generation/history/lease matrix passes with race detection
  over three iterations.
- Recursive race tests pass for Core, Harness, Harness PostgreSQL, Kernel
  PostgreSQL, native SQLite and the dependent Provider/HTTP/A2A/Redis/MCP/OTel/
  transport modules; the task-agent example tests also pass.
- Workspace formatting, vet and golangci-lint are checked before push.
- The initial governance pass could not use Docker. During the CI follow-up,
  private native PostgreSQL 16.16 and Redis 8.10.3 instances were created in an
  isolated temporary directory, without changing existing services. The newly
  selected `TestRealPostgresRootGenerationLifecycle` matrix passes against real
  PostgreSQL with `-race -count=3`; the PR integration job remains the gate for
  each pushed commit rather than inheriting historical green results.
- Agent Server composition/pin and product Inspect/Control during an accepted
  but unstarted Retry remain the separately documented Host follow-up. No
  product repository or compatibility fallback is introduced here.
