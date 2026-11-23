# Harness SQLite Store

The desktop adapter implements `harness.Store`, the Turn budget ledger and the
managed recovery Host Store. It uses modernc SQLite with a private on-disk file
and runs with `CGO_ENABLED=0`. Timeline, Handoff and cancellation facts use the
same ownership and receipt checks as the relational PostgreSQL adapter.

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

The Host supplies its configured Kernel and Agent to this constructor. It may
also pass the Store to the Harness budget middleware. The Kernel file owns the
physical Run and executor authority. The Harness file owns the Session, Turn,
Invocation, original dispatch, response receipts and budget reservations.

## Persistence and recovery

Schema v1 uses application ID `GAHS`. Creation initializes and checkpoints a
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
For a consistent backup of a desktop profile, stop its executors, close both
Stores, and retain both databases together.
