# Second-round complexity governance — C1 storage boundaries / C2 Application dispatch hard cut

- Baseline: `main@00cca6224dea680a629d88ee352554ae32382ad3`
- Working branch: `governance/complexity-c1-c2-20261116`
- Status: implementation candidate; no remote delivery, production qualification or paid Model run is implied
- Scope: **C1** read-only inventory and decision matrix; **C2** one provably single-variant Host Application Tool edge
- Non-scope: Kernel Run states, Store transactions, lease/epoch/CAS authority, Session/Turn/Invocation persistence schema, A2A, Tool/Model receipt identity, Context compaction, SQL migrations, cross-Store recovery protocols

## C1 — Persistence ports are not interchangeable feature layers

The package names separate a **feature concern** from a **physical backend**.
Both dimensions must be preserved.

| Data / authority | Port / owning module | Memory | PostgreSQL | SQLite | Ordinary file |
| --- | --- | --- | --- | --- | --- |
| Run State/Checkpoint/Result, event journal, atomic transition Outbox, Store-owned execution/grant/Control | `kernel.Store` and optional Kernel Store ports, in `agent-runtime/kernel` | `agent-runtime/memory.Store` | `agent-runtime-postgres.KernelStore` | `agent-runtime-sqlite.Store` (experimental embedded profile) | **No supported general FileStore** |
| Parent/Child ownership index and relation identities | `runrelation.Store` | `agent-runtime/memory.RunRelationStore` | `agent-runtime-postgres.RunRelationStore` | `agent-runtime-sqlite.RunRelationStore` | Not supported |
| Session, Turn, Invocation, Interaction, Config, Timeline, Context Checkpoint/Artifact | `harness.Store`, in `agent-runtime-harness` | `harness.NewMemoryStore` | `agent-runtime-harness-postgres.New` | **No equivalent full durable Harness Store** | Not supported |
| Host business effects | Host's `ApplicationCapabilityExecutor`, called by optional Harness | Host-supplied | Host-supplied | Host-supplied | Not a Store backend |
| Queue/Run Feed delivery | optional queue/feed contracts | memory | PostgreSQL committed Outbox is the authoritative delivery source; Redis is an optional queue | SQLite committed transition Outbox; not a Redis substitute | Not a separate execution authority |

Source evidence: `go/agent-runtime/kernel/store.go`, `go/agent-runtime-harness/store.go`, `go/agent-runtime/runrelation/runrelation.go`, `go/agent-runtime-postgres/kernel_store.go`, `go/agent-runtime-harness-postgres/store.go`, `go/agent-runtime-sqlite/sqlite.go` and `runrelation_store.go`.

**Important:** two Go Store adapters targeting the same PostgreSQL instance do **not** imply that unrelated Kernel and Harness API calls are one SQL transaction. Each owns its own CAS/record transition, and cross-Store physical/recovery boundaries rely on existing admitted grants, receipts, and specialized managed-recovery proof/selection. Do not replace this with a generic merged Store or assume a single connection magically makes a multi-step SDK call atomic.

SQLite is a *file-backed transactional SQL database*, not an interchangeable plain JSON/JSONL file persistence implementation. Its file ownership, initial publication, WAL, privacy checks, engine-time/lock semantics and embedded restrictions differ from PostgreSQL row-lock/clock fencing. Writing an additional FileStore would create another independently conformance-tested transactional backend without evidence of demand; it does not reduce duplicate code.

The existing `agent-runtime-sqlite` adapter does **not** make full Harness Session/Turn/Context restart durability available on embedded SQLite. Use standalone Agent+Kernel for the certified subset; a full Harness-on-SQLite product decision requires its own feature and crash-test acceptance, not a silent Memory fallback.

**C1 decision:** retain the separate Host/Harness, Kernel and RunRelation domain contracts and both SQL backend modules. Keep their schema/Store invariants unchanged. Do not add `UnifiedStore`, `StorageBackend` switch, a FileStore or SQLite Harness support in this work package.

## C2 — Remove the one-branch capability execution interpreter

Concrete source finding: `go/agent-runtime-harness/capability_tool.go` still defined a public
`CapabilityInvocationSpec` sum type containing **only**
`Application *ApplicationCapabilitySpec`. The old class classifier built a
one-element slice, and `startMaterializedCapability` switched over this
one branch. The Tool-result projection also retained non-Application Kernel
Run result and PendingChildTerminal paths no longer admitted by the frozen
Command catalog (the remaining valid Source is `application`).

The candidate hard-cuts that obsolete branching:

1. The Host materializer now returns `*harness.ApplicationCapabilitySpec`
   directly; `nil` remains a rejected materialization and the Host does
   not choose the execution class.
2. The Tool calls the existing `StartApplicationInvocation` directly,
   with the **same** frozen command key/version, parent Item ID, request/Call
   ID, original input and Tool receipt.
3. Result projection explicitly rejects a foreign `ExecutionClass` and
   keeps only original Application statuses / output references. No
   alternative Kernel completion or delayed child-run Tool receipt is
   generated by this Application-only command.
4. No persisted `Invocation`, `CommandDescriptor`, `ConfigSnapshot`,
   Kernel Run, Turn or Tool-result JSON contract changes; no SQL migration.
   Approval, cancellation, unknown-effect reconciliation and original
   Host outcome handling remain in their existing components.

The one-file production implementation changes by **23 added / 89 removed**
lines (net **66 fewer lines**); no generic replacement interface or
second execution dispatcher is created. This measures only the local
implementation delta, not system-wide task complexity or performance.

This is intentionally **source-breaking for any custom Host materializer**:
replace the prior
`MaterializeCapability(...)(harness.CapabilityInvocationSpec, error)` with
`MaterializeCapability(...)(*harness.ApplicationCapabilitySpec, error)`.
Return `nil` for an unsupported materialization, and only return the
Application `Input` payload. No compatibility alias, new generic dispatch
interface or dual path is added.

The deleted code is not the Kernel's valid `ExecutionAgent` vs
`ExecutionApplication` durable discriminator. Root/child Agent and Host
Application Invocations remain separate legitimate categories.

## C2 tests and negative boundaries

The new `capability_single_application_test.go` verifies:

- An authorized frozen command, exact Host arguments/identity and materialized
  Application input are preserved.
- Nil, Host error, foreign execution class and invalid Tool-schema arguments
  fail **before** Application Start; an invalid nested input does not call
  the Host materializer.
- Completed Application emits original `outputRefs` only, Pending status
  preserves the original Call ID/disposition without manufacturing a Child
  Kernel Run, and Failed status does not become success.
- Forged Agent class in the same child Invocation identity is rejected,
  not converted into a successful Application receipt.

### Completed local checks on the candidate worktree

- Full `go test ./go/agent-runtime-harness -count=1` and
  `go test ./go/agent-runtime-harness-postgres -count=1`: **PASS**.
- Full `go test -race ./go/agent-runtime-harness -count=1` and
  `go test -race ./go/agent-runtime-harness-postgres -count=1`:
  **PASS**, including the new materializer and Application receipt tests.
- `go vet` on Harness and Harness PostgreSQL: **PASS**.
- All-module `make fmt-check`, `make tidy-check` and `make go-lint`:
  **PASS** (lint: 0 issues in each module).
- `make integration-compile` and `make a2a-product-check`: **PASS**.
- `make metadata ts-lint ts-typecheck ts-test ts-build release-check`:
  **PASS**, including 46 TypeScript tests and the clean-consumer local cohort.
- `git diff --check`: **PASS**.

The actual PostgreSQL server integration suite, the whole-repository Go test
and whole-repository Race suite, remote PR-head CI and any external
Host-specific materializer rebuild are **not** certified by these checks. An external Host
materializer needs a source update before beta.24 ships.

## C3 — measured boundary, not a speculative code rewrite

Twenty-plus current Harness call sites load full immutable Turn Items via
`listAllItems` for delegation, cancellation, interaction and authorization
proof. These are plausible cost centers, **not proof of a redundant scan or
duplicated authority**. Candidate reductions must first establish the same
complete sibling/ancestor/receipt facts from a bounded indexed query, including
duplicate/foreign facts and missing-page errors across Memory and PostgreSQL.

No extra generic Grant, Executor registry, “unified persistence” layer or
incremental read cache is justified by this inventory. Defer C3 changes until
a real work profile and fault tests demonstrate a removable duplicate,
with a fixed identity × path × exception matrix.

## Release and review boundary

The code slice is limited to the Harness Application Tool edge plus regression
tests; documentation records the source-level pre-stable breaking signature.
Other SDK contracts and both physical backends remain unchanged.

This candidate does not assert simplified production recovery costs, real
performance improvements, or an additional supported persistence backend.
The first-run and final-head test evidence must be reported separately
before any PR merge.
