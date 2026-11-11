# Hosted storage queries and actor erasure

The PostgreSQL adapters now expose bounded, tenant-scoped Run and event metadata queries, Turn-owned frozen configuration projections, and explicit actor erasure. These APIs replace Host knowledge of private Runtime table names. They do not grant authorization: the Host authenticates and authorizes the tenant/actor scope. Event metadata never includes raw messages, event payloads, feature state or prompts. Use `agent.ViewState` for Agent-specific projections from canonical snapshots.

`ListRuns` uses ascending Run ID keyset pagination. Event pages have deterministic timestamp/Run/sequence ordering; total and page are observational reads, not one frozen execution snapshot. `CreatedTo` is exclusive; `CreatedThrough` is inclusive. At most one upper bound is accepted. Run ID substring search treats `%` and `_` literally.

## Account erasure

Stop new account admissions, cancel or reconcile existing work, and drain transition delivery before erasure. Compose Kernel and Harness `EraseActorData` with the product account deletion in one database transaction by passing that transaction to both adapter constructors. Both methods hold PostgreSQL table locks until transaction end; use this only for explicit maintenance, not a request hot path. Keep admission disabled after commit. They do not cancel executions, reconcile unknown effects, delete a pending outbox, or operate on Redis queues. Stale queue delivery must fail closed against missing durable records.

Kernel erasure rejects nonterminal Runs (including paused budget), unacknowledged or quarantined outbox entries and relations crossing the actor boundary. Harness erasure rejects nonterminal Turns/Invocations, waiting Interactions, foreign child receipt edges and active/unsettled budget reservations. `ErrActorActive` means the account must remain intact until work is safely drained. Kernel erasure covers Runs, events and owned relations; Harness erasure covers Sessions, Turns, Invocations, Interactions, configuration, context checkpoints/heads/artifacts, Items and budget ledgers. No real account data was erased during development.

## Event retention remains a hard-cut gate

The event journal is execution/recovery authority, not a disposable log. Existing Kernel Apply proof and descendant/retry validation may require historical events even for terminal Runs. There is intentionally no replacement for unrestricted `DELETE events WHERE created_at < cutoff`, and deleting whole Runs instead would change product retention semantics. Hosted adoption must define and verify a recovery-retirement/archive policy that preserves the product retention commitment before enabling event pruning. The actor-erasure API is not a substitute for event retention.

Context ownership follows persisted Session IDs. The standard Harness creates the Session before Context persistence; Invocation IDs do not establish Context ownership and may collide with foreign Session IDs. Hosts that directly persist arbitrary Context scopes must separately maintain authenticated ownership and erasure of those records. The adapter cannot attribute orphan scopes to an actor by guessing.

No schema migration, version tag, release or desktop/mobile implementation is included.
