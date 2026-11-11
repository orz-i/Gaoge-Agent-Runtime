# Hosted storage queries

The PostgreSQL adapters expose bounded, tenant-scoped Run and event metadata queries and Turn-owned frozen configuration projections. These APIs replace Host knowledge of private Runtime table names. They do not grant authorization: the Host authenticates and authorizes the tenant/actor scope. Event metadata never includes raw messages, event payloads, feature state or prompts. Use `agent.ViewState` for Agent-specific projections from canonical snapshots.

`ListRuns` uses ascending Run ID keyset pagination. Event pages have deterministic timestamp/Run/sequence ordering; total and page are observational reads, not one frozen execution snapshot. `CreatedTo` is exclusive; `CreatedThrough` is inclusive. At most one upper bound is accepted. Run ID substring search treats `%` and `_` literally.

## Maintenance remains a hard-cut gate

This change does not expose actor erasure or event pruning. Lifecycle terminal status, an empty outbox and settled budget reservations alone do not prove physical quiescence: loaded resumes, dispatch grants, cancellation fences and unknown effects may remain. A safe actor-erasure API must enforce a verified retirement/reconciliation boundary rather than merely document a caller obligation. That work is deferred; no production data was erased.

The event journal is execution/recovery authority, not a disposable log. Existing Kernel Apply proof and descendant/retry validation may require historical events even for terminal Runs. There is intentionally no replacement for unrestricted `DELETE events WHERE created_at < cutoff`, and deleting whole Runs instead would change product retention semantics. Hosted adoption must define and verify a recovery-retirement/archive policy that preserves the product retention commitment before enabling event pruning.

No schema migration, version tag, release or desktop/mobile implementation is included. Hosted phase one is not complete merely because these read-only query seams are available.
