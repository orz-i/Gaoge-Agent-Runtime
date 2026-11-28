# Harness SQLite Store

The desktop adapter implements `harness.Store` and the managed recovery Host
Store for product projections and independently fenced Host effects. It uses
modernc SQLite with a private on-disk file and `CGO_ENABLED=0`. Timeline,
Handoff and cancellation share the relational PostgreSQL receipt checks.

## Create and reopen a profile

For encrypted storage, supply the same `encrypted.WithKey(&key)` connection to `harnesssqlite.CreateNewWith` and `harnesssqlite.OpenExistingWith`. The host keeps its Harness key separate from the Kernel key. [Encrypted desktop profiles](../agent-runtime-sqlite/README.md#encrypted-desktop-profiles)

Provision a private profile directory using the [Kernel SQLite profile
instructions](../agent-runtime-sqlite/README.md#minimal-desktop-example).
On first enrollment call `harnesssqlite.CreateNew(ctx, harnessPath)`. Keep the
Kernel database at a separate path in the same directory. For subsequent starts
use the existing database entry point.

```go
host, err := harnesssqlite.OpenExisting(ctx, filepath.Join(dir, "harness.sqlite"))
if err != nil {
    return err
}
defer host.Close()

runner, err := harness.NewRunner(harness.Dependencies{
    Runtime: runtime, Agent: agentRunner, Store: host,
})
```

For SharedBudget, pass the exact Kernel Runtime to `BudgetAuthority` and select
its native `Adapter()` once. The Kernel file owns first-party execution and
budget reservations; their Run/Model/Tool changes commit together. The Harness
file owns Session/Turn/Invocation and budget projections plus dispatch/return
evidence for independently fenced Host effects.

## Persistence and recovery

Schema v4 uses application ID `GAHS`. The new pre-stable Harness Store
Checkpoint payload is `checkpoint-opaque-v1`, preserving original Tool-call
arguments and Provider Replay bytes across close/reopen and duplicate writes.
It cannot interpret older Checkpoint payloads; start with a fresh profile and
do not mix writers from different beta.24 source revisions. Schema v4 removes the retired Host reservation table and its indexes. Creation initializes and checkpoints a
temporary database before atomically publishing it without overwrite. Reopen
checks the application ID, version, WAL mode, exact table and index definitions,
and the original managed recovery authority. A missing or damaged profile
returns an error. For a reported publication-sync failure, reopen the named
file to inspect its durable state.

Each connection uses full synchronous writes. Writer transactions acquire the
SQLite writer lock before checking revisions or admitting work. Contending
writers can receive a transient database error after the bounded lock wait.
Retry the original operation with its original identity and expected revision.

Resume the original Turn after a process exits. A committed dispatch return
can finish projection. An outstanding external call stays pending until the
Runtime receives its original evidence. The two databases commit independently.
Schema v3 deleted obsolete `execution_class`,
`capability_key` and `definition_version` columns from Agent Invocations.
Schema v4 also removes the retired Host reservation table. Older Harness SQLite v1–v3 files are rejected on reopen;
there is no implicit migration or fallback to the legacy execution interpreter.
For a consistent backup of a desktop profile, stop its executors, close both
Stores, and retain both databases together.
