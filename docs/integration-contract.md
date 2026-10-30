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
| Harness Store | Preserve revision checks, immutable configuration/context facts, atomic interaction resolution and Context checkpoint ownership. Clone values across persistence boundaries. **Required beta.24 hard cut:** implement `GetItem(ctx, id)` by globally unique immutable Item ID (`ErrNotFound` if absent, defensive payload copy) in addition to `AppendItem` and bounded `ListItems`. Parent-effect liveness fencing must not use an unbounded Turn scan or treat a foreign/missing Item as valid execution authority. Memory/SQL conformance and real PostgreSQL lookup tests are release gates. No compatibility shim or SQL migration. |
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
| Hosted Tool Call identity | With a Harness shared budget, a native Provider Tool's terminal status (including `completed` or `failed`) does not by itself certify a distinct charge. Every accounted `model.HostedToolCall` needs the actual stable Provider-owned `ID`. Missing/whitespace-only IDs leave the original dispatched Model reservation and native quota Held with `budget.ErrInvalidUsage`, even in mixed named/anonymous responses; the identical invocation may not be resent. Separate final-only text responses have zero fresh native allowance. Older anonymous settled totals remain incomplete in read-only `ToolUsage` and require explicit Host reconciliation, never synthetic IDs. |
| Harness reads | Use `GET /harness/turns/{turnID}/state` (`harness.turns.state`) for a bounded persisted Turn status, and `GET /harness/turns/{turnID}/items` (`harness.turns.items`) for 1–500 paged, filtered Timeline Items with `nextAfterSeq`. The former reports committed status and does **not** prove Host assistant-output ACK; use the full `get` snapshot when that proof matters. Nonterminal feed subscription no longer materializes full Timeline. |
| Cancellation | Supply the current revision; the response contains `run`. Reload to obtain a full snapshot. Use Workflow cancellation for its compensation behavior. Cancellation cannot retract an external effect that already happened. A resolved Harness Interaction may hold a durable `running` Invocation callback claim regardless of whether its parent Turn is `waiting_input` or `running`. Root, child Invocation and Subtask cancellation all fail closed while the Host business effect is unverified. A deterministic persisted diagnostic receipt separates verified business success from the subsequent Kernel continuation; cancellation may proceed once the Host callback is verified, through ordinary Runtime CAS. Response handlers **must** be idempotent by `Interaction.ID` and response for host reconciliation; an unknown callback outcome is **not** automatically redispatched on replay (409 `harness.interaction_outcome_unknown`). Only a host error explicitly wrapping `ErrInteractionNoEffect` certifies safe retry because no external effect was issued. A host-proven successful callback whose Harness commit was lost is recovered through the Go host's explicitly authorized `Runner.ReconcileInteraction` plus optional read-only `InteractionResponseReconciler`, **not** by redispatching the handler. `CancelSubtask` also fences an active business callback claim before changing budget or Run state. |
| Interaction continuation | A Host-verified business response and the Runtime continuation are separate commit boundaries. The first caller atomically claims the continuation with a unique immutable Harness Item before entering Agent/Team/Workflow; subsequent `ReconcileInteraction` requests return `ErrInteractionOutcomeUnknown` (HTTP 409) when that claim has already been consumed and the Kernel is still nonterminal. Resolve the Kernel's actual outcome before performing new work; do not interpret the verified Host response Item as permission for another physical continuation. Recursive Subtask/Invocation cancellation preflights the related Run subtree and Item-parent-owned Application children before any budget revocation or Kernel cancellation. |
| Cancellation admission and interruption | During a cancellation attempt, Harness durably records a Turn-wide admission intent, and Store transactions reject fresh Invocations, Interaction responses or effect dispatches until the intent settles. Existing unrelated cleanup can still complete; fresh sibling admissions are intentionally paused. A definite pre-effect conflict releases a new intent; unknown physical cleanup retains the intent until its outcome is reconciled. Retrying a confirmed cancelled parent may continue descendant cleanup; never reset an unknown intent or redispatch unverified business effects. |
| Context replay vs physical Start admission | A Context Store read/build failure after a Turn/Invocation is frozen, but **before physical dispatch authority is obtained**, is request-local. Neither the original Start nor a concurrent replay may mutate the shared Turn to `failed` solely on this error. A later identical frozen Start can safely rehydrate context; changed admission remains rejected. A native Agent `StartRun` performs its read-only capability/catalog and frozen start-state validation before running any Run Middleware or entering Kernel Create. Only the *direct private* `agent.IsNativeStartNoEffect` result returned before those boundaries may close that Agent's physical Start grant after a durable completion receipt. Middleware errors, Store Create errors/lost ACKs, custom adapters and wrapped no-effect markers remain unknown; the public `harness.FeatureStartNoEffect` contract applies to custom Features/Agents, not to a native Agent's effectful Run Middleware. Catalog/capability preflight ports must not dispatch business effects. |
| Feature Start no-effect proof and retry | A no-snapshot `StartRun` error is outcome-unknown, not a safe reason to mark the Turn/Invocation failed. `harness.ErrExecutionOutcomeUnknown` is HTTP 409 `harness.execution_outcome_unknown`; only a host-certified `harness.FeatureStartNoEffect(cause)` after zero Kernel/remote effects and a durable grant-completion receipt permits a failed/retryable Invocation. Invalid or stale callbacks cannot be assumed no-effect from `kernel.ErrConflict` alone. `RetryInvocation` cannot rotate `ExecutionRefID` while any corresponding physical-dispatch grant is unresolved; root retries also respect descendant grants. A failed-state projection competes with a newly acquired grant in one Store transaction. Physical-dispatch receipt writes have an independent five-second deadline; timeout or lost ACK does not prove the receipt failed to commit. Public diagnostic Timeline/Feed payloads exclude internal Kernel Run identifiers even though private Store evidence retains them. |
| Physical execution authority | All new Harness-initiated Agent/Team/GroupChat/Workflow Start/Resume and matching Workflow Wait continuation calls require an atomic persisted physical-dispatch grant before their physical call. Pending grants compete with cancellation intents under the Turn Store lock. A pending Start without a Kernel Run or any pending Resume cannot be cancelled as though the effect stopped; once a Start's Kernel Run exists, Kernel cancellation fences its Run. An already-cancelled parent Invocation/Run cannot spawn a late child. Concurrent Start replay while the original Start is in flight returns conflict rather than authorizing a duplicate Resume. Ambiguous dispatch results retain the grant for operator recovery; only an authoritative matching Kernel Run snapshot or an explicit pre-dispatch parent rejection permits release. A custom Feature returning `kernel.ErrConflict` without a Kernel snapshot does not prove no external side effects. Exception: after a crashed native Agent Start, if the original Start grant remains open but the same Kernel Run has a sticky execution Epoch, the native Agent may recover under the Kernel's exclusive execution lease (live lease blocks; expired lease permits a single fenced takeover). This does not authorize custom Agent/Feature replay, and concurrent cancellation remains under Kernel CAS. Host Store adapters must enforce this updated AppendItem contract. |
| Harness control arbitration | `ResolveApproval`, delegated `ResolveSubtaskApproval`, and `GrantCallAllowance` also require the persisted physical-dispatch grant. Delegated approval binds the exact physical child Run in its grant payload while preserving the root Harness Invocation owner; Subtask cancellation checks the same child Run. An active cancellation intent rejects all three controls without model/tool execution or budget wakeup. A resolved native feature result normally requires an authoritative matching Kernel snapshot before the grant is released. For the three native Agent controls only, the first-party private direct-result proof (also detectable with `errors.Is(err, agent.ErrControlNoEffect)`) certifies this attempt stopped before Run middleware, approval effects, or Kernel ApplyControl; the exact `*agent.Runner` may close its same durable grant without a Run snapshot only when `agent.IsNativeControlNoEffect(err)` confirms provenance. An exported sentinel returned by custom Host middleware or adapters cannot supply that release proof. Errors after physical dispatch, uncertain control CAS acknowledgments, and crashes before a grant completion receipt remain fail-closed; completed receipts survive new connections/processes. Child Invocation/Subtask cancellation can retry after proven target-only no-effect Kernel CAS conflicts with no budget or Interaction mutations; partial descendant cleanup and unknown remote outcomes stay fail-closed. |
| Post-intent Host cancellation claim boundary | A cancellation intent serializes new admission before the second Host inspection. Its `claim` can read RunRelation/Invocation/Interaction state and may also call `Store.UpdateInvocation` to cancel a waiting Interaction owner. A post-intent claim error releases only this attempt's fresh intent when **no Host write-capable CAS has yet been attempted**. The Harness marks that first write boundary before the Store call and retains the intent for every subsequent error, including Host `ErrConflict`, committed-but-lost ACK or a later read failure after a previous Host mutation. No inference from a failed Store response can prove absence of a write. Pre-write read failures remain safely retryable, while partial Host/Kernel/budget or descendant outcomes require reconciliation. |
| Native cancellation no-effect recovery | `Cancel`, `CancelInvocation`, and `CancelSubtask` share the existing immutable cancellation-intent completion mechanism. When the actual canceller is the concrete `*kernel.Runtime`, a **direct** `kernel.IsControlNoEffect` result certifies that the target cancellation did not enter a Store control CAS; after confirming a still-nonterminal target Run (`running`, `paused_budget`, or `waiting_input`) and **no** budget-ledger or Host Interaction mutation, the Harness can close that attempt's intent and permit a later retry. Only target-side proof qualifies; descendants with partial cleanup remain fenced. For a custom `RuntimeCanceller`, `kernel.ErrConflict` still asserts no external cleanup but the safe release scope stays limited to `running`. Native Store `ErrConflict` and every outcome-unknown Store response remain fail-closed even when `errors.Is(err, kernel.ErrConflict)`; no blind retry. An incomplete intent receipt cannot grant new authority. |
| Native cancellation entry preflight | A concrete native `*kernel.Runtime` cannot issue a cancellation Store CAS before both the Harness's initial Run read and `Kernel.Cancel`'s own Run read/revision validation complete. A failure at the latter Kernel boundary returns direct `kernel.IsControlNoEffect` proof. A failure at the former Harness-only read is marked by the private direct `cancellationReadNoEffect` result **before any Canceller is invoked**, whether native or custom. These proven pre-dispatch failures may close only their own intent after authoritative nonterminal Run verification, no shared budget/Interaction mutation, and successful completion-receipt persistence. A pre-Control native revision conflict is retryable within the existing bounded loop; a Store CAS conflict is not a no-effect signal. Custom cancellers receive **no new authority after their callbacks begin**: ordinary `context.Canceled`/unknown errors stay fenced; their contractually no-effect `kernel.ErrConflict` remains limited to `running`. Incomplete receipt paths remain fail-closed. |
| Cancellation request Context and durable recovery | Request cancellation after the Harness's durable cancellation-intent append does **not** prove the native Kernel performed a Store CAS. Only after the existing target-only **no-effect** gate succeeds (direct native Kernel/Harness proof, or a custom Canceller's explicit no-external-cleanup `ErrConflict` contract), `releaseNoEffectCancellation` performs the existing budget-ledger, Host Interaction and authoritative Run checks using a **separate, single 5-second bounded Context** that ignores the original request cancellation but retains its values. The same deadline also bounds the intent-completion receipt append. All checks and the receipt must succeed to release admission; a read error, timeout, lost ACK, prior budget/Interaction mutation, failed receipt write, or result-unknown Store CAS remains fail-closed. The original failed request still returns its error; a later independently authorized request may retry only after the completion Item commits. |
| Bounded cancellation settlement receipts | The cancellation-intent completion Item on both success and proven pre-effect failure is appended under a **fresh, detached but deadline-bound 5-second Context** in root `Cancel`, `CancelInvocation`, and `CancelSubtask`, including read-only post-intent claim rejection. A Subtask's failed-cancellation Timeline fact uses the same bound. A caller disconnect or original deadline does not abort these critical writes; a Host Store row lock cannot wait unboundedly. A write timeout or lost ACK is outcome-unknown, not proof the completion Item was absent or present. Re-read the durable Item before granting new admission, and never infer no-effect from a failed Host/Kernel CAS or from an unacknowledged completion. If no completion Item is committed, the active fence remains for explicit reconciliation. |
| Native Kernel control proof boundary | `kernel.IsControlNoEffect(err)` accepts only the direct private error from `Kernel.ApplyControl` before calling a write-capable Store method: initial Kernel read, revision/pure validation or an unsupported Store guard. The first-party Agent allowance controller may propagate that proof as its private `agent.ErrControlNoEffect` result; Harness still requires an actual durable completion receipt to unlock subsequent controls or cancellation. Any `Store.ApplyControl` or `Store.Apply` error is wrapped as an uncertain Store-phase result, preserving `errors.Is` but never conveying direct no-effect proof, even if a Store forwards a different Kernel's proof. A Store CAS failure or lost response must be reconciled explicitly; a new process can safely retry only after an authoritative no-effect receipt has committed. |
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
