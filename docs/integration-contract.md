# Integration and upgrade contract

This is the supported integration boundary for the current Beta release. It
records executable expectations for hosts and consumers; it does not declare
the SDK API stable under SemVer v1. HTTP `v1` identifies the protocol namespace.
The [support policy](../SUPPORT.md) still allows documented changes between
prereleases and supports only the latest Beta.

## Choose one release

Pin every consumed Go module to the same exact prerelease tag and the TypeScript
client to that release's GitHub archive. The server's HTTP adapter, Go Runtime,
Harness and persistence modules belong to that same release cohort. Do not
combine client, worker and schema versions based only on their shared HTTP
`v1` path. Mixed-release rolling upgrades are not currently covered by the gate.

Use the root README's tested toolchain for release validation. The TypeScript
package's Node.js >=20 engine declaration is a consumer minimum; the workspace
and release tests use Node.js 24 LTS+. No claim is made that every permitted
Node version has been exercised.

## Go host boundary

| Boundary | Integration requirement |
| --- | --- |
| `kernel.Store` | Commit aggregate replacement, revision CAS, journal append and required transition outbox entries atomically. Keep journal reads bounded and outbox leases fenced. Run the public conformance suite for custom implementations. |
| Model and Tool ports | Inject `model.Client`, `tools.Catalog` and `tools.Executor` explicitly. Preserve Run/invocation identity in provider adapters and apply the host's effect idempotency policy. A durable receipt is not a guarantee of exactly one physical provider request. |
| Features and workers | Compose declared `Requires`/`Provides` capabilities explicitly. The host owns worker startup, shutdown, delivery and reconciliation; installing a module does not start background recovery. |
| Harness Store | Preserve revision checks, immutable configuration/context facts, atomic interaction resolution and Context checkpoint ownership. Clone values across persistence boundaries. |
| PostgreSQL | Supply `*gorm.DB`, run explicit migrations, then construct Kernel/relation/definition and Harness stores. Constructors do not migrate schemas. |
| Redis | Supply the client, queue/feed configuration and clock where applicable. Queue delivery is leased and may repeat; semantic feeds have finite retention. |
| HTTP and protocol edges | The host resolves principals and object authorization and supplies provider credentials, outbound HTTP clients and endpoint policy. MCP/A2A protocol support is adapter-specific. |

The checked external consumers live in [contracts/consumers/go](../contracts/consumers/go).
They pin constructor signatures for all eight Go modules and explicitly spell
out the Kernel Store, Harness Store, model, tool, worker and Run-authorizer
interfaces. Required port methods cannot be added silently behind an embedded
SDK interface. The core consumer also runs a complete Agent with a local model
adapter, and the HTTP consumer mounts and calls the Run route.

## HTTP and TypeScript boundary

The canonical protocol description is
[OpenAPI](../contracts/agent-runtime/v1/openapi.yaml), with capability route
fragments alongside it. The TypeScript client exports through its package root.
Host/provider internals and private persisted feature state are not additional
public client APIs.

| Concern | Consumer behavior |
| --- | --- |
| Starting work | Use the feature-specific start API. Supply a stable `clientRunID` when the caller needs to recover a lost response. Before reissuing an ambiguous start, load that Run and reconcile its state; a repeated POST is not a universal replay-success contract. |
| Revision CAS | Use the authoritative snapshot's `run.revision` for `expectedRevision`. A conflict requires reloading and making a new decision. A feed/journal sequence is not a revision. |
| Snapshot | `run`, opaque `state`, and `eventHead` form the envelope. `checkpoint` and `result` are omitted when absent. Their payload/content values remain feature-owned JSON. |
| Run kind | Treat `kind` as an extensible feature-owned string. Built-ins include `agent`, `plan_execute`, `workflow`, `team`, and `a2a.remote`; generic Run/Workbench reads may include host extensions. |
| Journal | Page `/runs/{runID}/events` with exclusive `afterSeq` and a bounded `limit`. Persist the last processed journal sequence independently of semantic feed cursors. |
| Feed | Persist the last processed feed sequence, tolerate reconnect replay, and stop on `terminal`. On cursor expiry, restore an authoritative snapshot and use the returned feed head. Run and Harness feeds have separate cursors and recovery headers. |
| Cancellation | Supply the current revision; the response contains `run`. Reload to obtain a full snapshot. Use Workflow cancellation for its compensation behavior. Cancellation cannot retract an external effect that already happened. |
| Errors and retry | Branch on HTTP status and `error.code`, retain `requestID` for diagnostics, and treat messages as explanatory text. The client reconnects feeds; ordinary requests do not acquire automatic retry/idempotency guarantees. |
| Authorization | The host controls enabled routes and access. Owner-only Run authorization is the default when no custom authorizer is supplied. A denial may be rendered as not-found. |

The [reviewed JSON fixtures](../contracts/agent-runtime/v1/fixtures/manifest.json)
cover running/waiting/completed and A2A snapshots, checkpoint/result envelopes,
empty/nonempty event pages, cancellation, errors and terminal Run feed events.
Go serializes the public DTOs and compares them with those files, then validates
the result against OpenAPI schemas. The release gate compiles the same JSON
literals against the **packed** TypeScript declarations and exercises actual
client reads, cancellation, error decoding and terminal SSE consumption using
the packed ESM entrypoint. Error envelopes are tested through `RuntimeAPIError`;
they do not introduce a separate public TypeScript DTO.

## Persistence upgrade procedure

1. Record the source and target release, read each intervening upgrade note,
   and pin all target modules/client artifacts. Identify any required data
   conversion; a successful `AutoMigrate` is not proof of semantic compatibility.
2. Rehearse on a restored copy of the deployment's PostgreSQL data and required
   Redis delivery state. Include waiting Runs, active leases, unprojected
   transitions, Harness interactions and Context checkpoints relevant to that
   deployment. Verify the backup can actually be restored.
3. Stop accepting new work and quiesce old workers/projectors before schema
   changes. Preserve durable IDs and unresolved external-call records. Do not
   run old and new workers concurrently without separate compatibility evidence.
4. Run `postgres.Migrate(db)` and, if Harness is deployed,
   `harnesspostgres.Migrate(db)` from one host-controlled migration process.
   Stop on error; do not assume several migration operations form one rollbackable
   transaction. Inspect partial changes before retrying.
5. Construct the target stores, load representative snapshots and context
   ownership, page journals and reconcile outbox/queue delivery. Exercise one
   waiting continuation and the matching HTTP/client before reopening traffic.
6. If rollback is necessary, stop the target workers and restore a consistent
   pre-upgrade backup with the matching binaries. In-place downgrade migrations
   are not supported. Reconcile external effects that occurred after the backup
   before replaying restored work.

The real PostgreSQL tests reapply **the current version's** migrations to
populated Kernel and Harness storage, then reload aggregate/journal/outbox and
Context checkpoint ownership. This proves migration reapplication at this
version, not an upgrade from every previous Beta. There is no schema-version
compatibility negotiation or cross-release migration matrix in this gate.

## Changing a contract

Run `make check` for Go tests, route/OpenAPI/fixture agreement, TypeScript checks,
README examples and external consumers. `node scripts/check-release.mjs` runs
the clean-consumer subset with `GOWORK=off`; Go test dependencies must not include
the Gaoge host. `make integration` adds the real-engine recovery and migration
checks described in the [reliability evidence map](reliability.md).

Review fixture and consumer changes as public API changes. If a signature,
required method, wire field/type, error/status behavior or persisted format
must change, document the before/after behavior and caller/data migration in
`CHANGELOG.md`. Do not automatically regenerate fixtures to make a failing
check pass. Preserve existing examples unless their intentional change has
been reviewed.

These selected checks complement existing behavioral and conformance suites.
They are not an exhaustive snapshot of every exported Go symbol, every feature
payload, every Harness HTTP response, or every historical release. They do not
establish live-provider exactly-once behavior, zero-downtime upgrades, or a
SemVer v1 guarantee.
