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
They pin constructor signatures for all nine Go modules in the current Beta.11 release cohort and explicitly spell
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
| Run kind | Treat `kind` as an extensible feature-owned string. Built-ins include `agent`, `workflow`, `team`, and `a2a.remote`; generic Run/Workbench reads may include host extensions. |
| Journal | Page `/runs/{runID}/events` with exclusive `afterSeq` and a bounded `limit`. Persist the last processed journal sequence independently of semantic feed cursors. |
| Feed | Persist the last processed feed sequence, tolerate reconnect replay, and stop on `terminal`. On cursor expiry, restore an authoritative snapshot and use the returned feed head. Run and Harness feeds have separate cursors and recovery headers. Terminal TurnFeed delivery is observational and may be republished after a committed recovery; deduplicate by Turn and terminal status. |
| Harness reads | Use `GET /harness/turns/{turnID}/state` (`harness.turns.state`) for a bounded persisted Turn status, and `GET /harness/turns/{turnID}/items` (`harness.turns.items`) for 1–500 paged, filtered Timeline Items with `nextAfterSeq`. The former reports committed status and does **not** prove Host assistant-output ACK; use the full `get` snapshot when that proof matters. Nonterminal feed subscription no longer materializes full Timeline. |
| Cancellation | Supply the current revision; the response contains `run`. Reload to obtain a full snapshot. Use Workflow cancellation for its compensation behavior. Cancellation cannot retract an external effect that already happened. A resolved Harness Interaction may hold a durable `running` Invocation callback claim regardless of whether its parent Turn is `waiting_input` or `running`. Root, child Invocation and Subtask cancellation all fail closed while the Host business effect is unverified. A deterministic persisted diagnostic receipt separates verified business success from the subsequent Kernel continuation; cancellation may proceed once the Host callback is verified, through ordinary Runtime CAS. Response handlers **must** be idempotent by `Interaction.ID` and response for host reconciliation; an unknown callback outcome is **not** automatically redispatched on replay (409 `harness.interaction_outcome_unknown`). Only a host error explicitly wrapping `ErrInteractionNoEffect` certifies safe retry because no external effect was issued. A host-proven successful callback whose Harness commit was lost is recovered through the Go host's explicitly authorized `Runner.ReconcileInteraction` plus optional read-only `InteractionResponseReconciler`, **not** by redispatching the handler. `CancelSubtask` also fences an active business callback claim before changing budget or Run state. |
| Interaction continuation | A Host-verified business response and the Runtime continuation are separate commit boundaries. The first caller atomically claims the continuation with a unique immutable Harness Item before entering Agent/Team/Workflow; subsequent `ReconcileInteraction` requests return `ErrInteractionOutcomeUnknown` (HTTP 409) when that claim has already been consumed and the Kernel is still nonterminal. Resolve the Kernel's actual outcome before performing new work; do not interpret the verified Host response Item as permission for another physical continuation. Recursive Subtask/Invocation cancellation preflights the related Run subtree and Item-parent-owned Application children before any budget revocation or Kernel cancellation. |
| Cancellation admission and interruption | During a cancellation attempt, Harness durably records a Turn-wide admission intent, and Store transactions reject fresh Invocations, Interaction responses or effect dispatches until the intent settles. Existing unrelated cleanup can still complete; fresh sibling admissions are intentionally paused. A definite pre-effect conflict releases a new intent; unknown physical cleanup retains the intent until its outcome is reconciled. Retrying a confirmed cancelled parent may continue descendant cleanup; never reset an unknown intent or redispatch unverified business effects. |
| Physical execution authority | All new Harness-initiated Agent/Team/GroupChat/Workflow Start/Resume and matching Workflow Wait continuation calls require an atomic persisted physical-dispatch grant before their physical call. Pending grants compete with cancellation intents under the Turn Store lock. A pending Start without a Kernel Run or any pending Resume cannot be cancelled as though the effect stopped; once a Start's Kernel Run exists, Kernel cancellation fences its Run. An already-cancelled parent Invocation/Run cannot spawn a late child. Concurrent Start replay while the original Start is in flight returns conflict rather than authorizing a duplicate Resume. Ambiguous dispatch results retain the grant for operator recovery; only an authoritative matching Kernel Run snapshot or an explicit pre-dispatch parent rejection permits release. A custom Feature returning `kernel.ErrConflict` without a Kernel snapshot does not prove no external side effects. Exception: after a crashed native Agent Start, if the original Start grant remains open but the same Kernel Run has a sticky execution Epoch, the native Agent may recover under the Kernel's exclusive execution lease (live lease blocks; expired lease permits a single fenced takeover). This does not authorize custom Agent/Feature replay, and concurrent cancellation remains under Kernel CAS. Host Store adapters must enforce this updated AppendItem contract. |
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
