# Changelog

All notable changes are documented here. This project follows Semantic
Versioning once it reaches `v1.0.0`; prereleases use SemVer prerelease labels.

## [0.1.0-beta.24] — unreleased architecture hard cut

- First real Agent pilot positive evidence (2026-11-07): after the scoped
  Host budget correction, the real OpenAI `gpt-5.6-terra` `code-01` task
  completed its verified Oracle and all seven recovery checks with one draft,
  four Model calls and three Tools; 39/40 cases remain unattempted and the
  source checkout was dirty. The preflight's normal Tool continuation passed
  but returned no reasoning item, so that individual probe does **not** prove
  encrypted replay; earlier independent Provider evidence did. The report
  renderer now distinguishes those coverage facts without loosening admission.
  See [pilot audit](docs/audits/2026-11-07-first-live-agent-pilot.md).

- Real pilot Host-budget correction (2026-11-07): the first official
  `gpt-5.6-terra` `code-01` pilot passed Schema and encrypted Tool/replay
  preflight, then failed `context_budget_exceeded` after 2 real Model and 2
  Tool calls (lookup and Context artifact read), before draft creation.
  Without an exact tokenizer, the example's 8,192-token service cap charges
  a conservative hard upper bound of serialized input bytes including opaque
  reasoning replay; model context capacity alone does not lift that cap.
  A 12 KiB completed-reasoning SSE fixture reproduced the failure before the
  third Model request. Limit **only** the quality task family's Host service
  input to 32,768; keep the two-case baseline at 8,192, retain strict
  Context/Provider replay validation and the same evidence Oracle. The
  quality profile's dataset hash changes deliberately; earlier failed pilot
  results cannot be treated as comparable quality samples or silently fixed.
  No SDK/Kernel estimator or persistence protocol changes. See
  [real pilot audit](docs/audits/2026-11-07-first-live-agent-pilot.md).

- First-live-Agent-quality gate (2026-11-07): add opt-in
  `agent-quality-pilot` to measure exactly the first fixed `evidence-40-v1`
  case (`code-01`) through the actual Agent/Tool/Context/recovery Oracle after
  the existing real Provider preflight. Preserve the 40-case manifest and
  historical baseline hashes. Pilot reports carry explicit `pilotOnly`,
  `pilotCaseID` and `pilot_sample_passed` / `pilot_sample_failed` statuses;
  39 unattempted cases never turn into an inferred complete benchmark score.
  Prevent extra or substituted samples, forged summaries, numerical full-suite
  comparison and accidental escalation via `retry`. All real Provider calls
  remain interactive, credential-private and separately paid/authorized.
  The SDK/Kernel/Provider execution contract and database schemas are unchanged.
  See [first-live Agent task gate](docs/agent-effect-benchmark.md).

- Agent effect benchmark governance (2026-11-07): preserve the historical
  two-case real-model baseline while adding opt-in `evidence-40-v1` with
  40 immutable evidence/Tool/Context/recovery cases across five data-variation
  groups. A scripted offline runner validates all 40 actual Agent + Tool
  oracles without network; interactive quality mode reuses the existing hidden
  credential, Provider preflight and fail-closed sample protocol. Reports
  include group-level success/latency/call/known-token observations and
  compare only matching dataset/profile/suite configurations. Unfinished
  attempts and missing Usage never become successful/zero-cost samples. No
  SDK execution loop, Kernel schema, Provider binding or Host policy changes.
  Real OpenAI tool-probe preflight also uncovered an overstrict SDK ciphertext
  comparator: an authoritative completed reasoning `response.output_item.done`
  and a later `response.completed` can expose different encrypted blobs.
  The Provider now replays only the completed-done encrypted bytes after strict
  non-encrypted reasoning, Tool, status, item owner and response identity
  checks; an already-completed `added` item remains immutable. The bounded,
  content-free `wireEvidence` diagnostic remains available for failed probes.
  An independent follow-up adds a bounded, explicit `agent-quality-probe`
  command that runs only the existing Schema and synthetic Tool/replay
  preflight: it cannot start real tasks even after a pass. Probe-only reports
  carry a distinct unscored status, preserve 0 attempted task samples and
  are barred from `retry` full-task execution or numerical quality comparison.
  No new Agent/Kernel replay mode or compatibility bridge is introduced.
  See [scope and limits](docs/agent-effect-benchmark.md).

- PR #92 follow-up P2: centralize fail-closed Unicode validation of
  JSON-bound identity/configuration before admission hashing and Model-visible
  dispatch. Malformed UTF-8 in Host/Actor refs, Config fields, Context
  instructions/history, model metadata and nested raw JSON (including
  unpaired surrogate escapes) is now rejected instead of being silently
  normalized to U+FFFD and replayed as the same immutable Turn. Valid Unicode
  and opaque binary evidence are preserved. This is a deliberate validation
  hard cut within the unreleased beta.24; no schema or replay compatibility
  bridge. See the PR #92 governance audit for the RED/GREEN matrix.

- Minimal text Session onboarding (2026-11-07): add optional
  `harness.OpenTextAgentSession` and `TranscriptHistoryFunc` over the existing
  Agent/Harness execution path. The former composes the original Context
  projection and recovery middleware; the latter reads immutable prior Host
  transcript entries and appends one stable-ID User entry. No second Agent
  loop, transcript Store, implicit Tool/Feature router, persistent schema,
  execution status or replay compatibility bridge. Advanced integration
  remains explicitly composed; Host retains owner/identity and transcript
  responsibilities. The minimal Session example now uses the facade and
  regression tests cover retries, new composition, cross-actor read denial
  and invalid prior history. Independent review P2 remediation now validates
  every prior message's model contract and restricts standard history to
  pure-text User/Assistant content before durable Turn creation; invalid
  history cannot poison a stable Turn ID, and media/Tool/ProviderReplay/system
  content must use explicitly composed sources.

- Follow-up model capability authorization P2 remediation (2026-11-06):
  replace the negative omitted-effort sampling / mandatory Tool flags with
  **positive Host-certified support**. Missing model metadata never
  authorizes advanced physical parameters on either OpenAI or Anthropic.
  The task-agent Host no longer converts user-selected `reasoningEffort`
  into its own evidence, and accepts a separate operator-provisioned
  `GAOGE_MODEL_PROFILES_FILE` for verified capabilities. Schema, Tool/replay
  and effort admission remain independent; ineligible requests fail
  before paid HTTP. Loopback real-adapter and pinned-ModelInvocation
  negative/positive tests accompany the hard cut. See
  [remediation](docs/audits/2026-11-06-host-owned-model-capabilities-p2-remediation.md).

- Host-owned Model Capability Source hard cut (2026-11-06):
  remove every bundled OpenAI/Anthropic public model capability list and
  replace the two independent adapter override fields with one
  `model.ModelProfileSource` interface. The Host imports a snapshot from
  external files, databases or discovery; the adapter copies and validates
  only the model facts it is actually given. Effort, native strict-schema
  support and omitted-effort sampling restrictions are per exact model.
  Bind the **selected model** profile to the original Provider HMAC so
  related changes fence pending recovery but adding unrelated catalog
  entries does not. Old beta.24 persisted Provider fingerprints are
  intentionally incompatible (fail-closed; no bridge); no new database
  schema, secondary agent loop or remote catalog client. See
  [breaking changes](docs/breaking-changes.md) and
  [verification](docs/audits/2026-11-06-host-owned-model-capabilities.md).

- Harness owner-integrity trust-boundary closure (2026-11-06):
  [ADR 0017](docs/adr/0017-trusted-harness-store-and-frozen-owner.md)
  defines the Host-authenticated Actor and trusted durable Harness Store as
  the owner integrity boundary. Memory/SQL conformance and real PostgreSQL
  cross-connection tests exercise immutable Session owner, exact root
  Invocation Input/Hash, concurrent hostile replay and CAS/ID protection.
  A custom Store able to fabricate both immutable records remains outside
  the supported security model; no extra Kernel query, owner-signer,
  schema change, compatibility bridge or duplicative Agent state was added.
  See [audit closure](docs/audits/2026-11-06-harness-store-tcb-closure.md).

- Follow-up independent Session owner P1 remediation (2026-11-06):
  Host-selected Actor and Kernel Thread are now checked against the original
  frozen root Agent Invocation for all SessionClient read/recovery operations,
  and again against the actual returned Snapshot. A Session-scoped Resume
  passes the expected caller identity to its last native/Kernel dispatch
  fence, preventing an inconsistent Store's first Session response from
  authorizing another actor's pending model call. Adversarial once-only,
  persistent and two-stage Store-return substitutions and same-Actor wrong
  Thread are permanently covered; no new Store schema, replay protocol or
  Kernel state. Anthropic's documented exact `claude-mythos-preview`
  supports `max` but not `xhigh`; Unary and SSE are covered. See
  [second-review remediation](docs/audits/2026-11-06-session-owner-followup-remediation.md).

- Independent P1/P2 effort-governance remediation (2026-11-05):
  preserve pre-effort Provider HMAC identity for old pending Runs; reject
  duplicate or aliased ModelOptions *before* Harness canonicalization;
  reject unsupported OpenAI/Anthropic reasoning-and-sampling combinations
  before Unary/SSE physical dispatch; preserve explicit baseline effort
  without accepting null/empty/case-folded shadow fields. Core and
  Provider now share one strict JSON-member validator; cross-restart
  original-ID recovery and independent red-to-green fault probes covered.
  No new Kernel status, Store schema, alternate recovery protocol or
  compatibility bridge. See [remediation notes](docs/audits/2026-11-05-three-part-agent-independent-remediation.md).

- Three-part Provider/Agent-Harness/quality governance (2026-11-05):
  exact-model reasoning effort support with pre-dispatch rejection and
  Provider binding fingerprint, single Session read/resume identity guard,
  and optional effort-aware real-model task/recovery baseline. Offline
  randomized Agent policy oracle and two performance workloads show that
  an attempted hash-index optimization regressed common-case latency;
  the optimization was deliberately reverted. No Kernel schema or Agent
  loop change. See [Provider protocol](docs/provider-adapters.md),
  [benchmark guide](docs/performance.md) and
  [Session convergence audit](docs/audits/2026-11-05-session-identity-convergence.md).

- Harness top-level unknown-effect outcome hard cut: a custom composite Feature,
  Agent or Retry returning no physical Run receipt can no longer be reported
  as successfully cancelled merely because a same-ID local Kernel Run is
  terminal. Unsettled physical dispatch grants now block ordinary terminal
  Refresh/replay projection; only the original first-attempt Agent's proven
  pre-dispatch cancellation conflict remains eligible for narrow recovery.
  The 24-path Agent/Team/Workflow/GroupChat start/retry fault matrix is
  permanently covered. No new persistence/API/schema/compatibility layer.
  See [PR #89 independent P1 remediation](docs/audits/2026-11-05-pr89-top-level-unknown-effect-hardcut.md).

- Harness protective-stop Invocation authority correction: Root/Child Cancel
  validate the frozen execution identity and both canonical Store indexes
  independently of Host-visible OutputRefs. Malformed display metadata can
  fail final projection without blocking an already-authorized physical
  cancellation; changed Attempt, RunID, InputHash or parent cannot substitute
  another execution. Existing Kernel/RunRelation fences and cancellation
  intent/CAS remain mandatory. No API, Store schema, legacy bridge or new
  persisted authority. See the [protective Invocation proof audit](docs/audits/2026-11-04-harness-protective-invocation-authority.md).

- Harness protective-cancellation authority correction: an already-admitted
  Agent/child Run may be safely stopped when Session or Config presentation
  reads temporarily fail; exact Turn/Invocation and existing Kernel/RunRelation
  ownership fences still apply before any physical cancellation. New execution,
  replay and Host-visible reads retain the complete frozen owner proof. Host
  `hs_`/`ht_` namespaces now require the exact lowercase 24-hex digest,
  preserving valid standalone Store IDs with noncanonical lookalike prefixes.
  No new persistence, schema, compatibility shim or Kernel state. See
  [the independent P1/P2 repair](docs/audits/2026-11-04-harness-protective-cancel-and-id-grammar.md).

- Harness I1 read-side ownership hard cut: centralize all Session and Turn
  Store reads behind exact requested-ID verification. Host-derived identities
  additionally bind the original HostThread, HostTurn, owning Session and
  canonical sealed Config before any replay, cancellation, control or
  Host-visible projection. An inconsistent custom Store can no longer
  substitute a different persisted tenant's Session/Turn or rebind the
  original Turn to a foreign Config. `LoadSnapshot` enforces the same
  Session/Config Turn association. No new public API, persisted schema,
  compatibility alias or Kernel state. See the
  [I1 read-side authority audit](docs/audits/2026-11-04-harness-read-authority-hardcut.md).

- Harness I2/I3 sealed-authority convergence: re-derive canonical Config
  ContentHash and Config ID from the actual persisted payload, and use only
  verified normalized configuration for admission, Context, Budget, Tool,
  role and retry paths. Before any physical operation, validate both the
  CreateInvocation reply and its canonical indexed Store record against the
  Host-frozen Invocation/Run/Attempt identity. All top-level, child and
  Handoff creation paths use the single check; old private direct callers
  are removed without forwarding aliases. Malformed pre-stable Beta records
  fail closed rather than receiving a compatibility fallback. No new Store
  field, public API, schema or Kernel status. See the
  [I2/I3 independent convergence audit](docs/audits/2026-11-04-harness-sealed-authority-convergence.md).

- Top-level Store receipt authority: the shared Harness admission path now
  rejects a Store CreateSession, PutConfigSnapshot or CreateTurn response that
  disagrees with the Host-selected immutable identity. A fresh Turn must also
  retain its original Accepted/Revision-1 lifecycle. The first physical
  Invocation, shared-budget reservation and Feature dispatch cannot proceed
  on a substituted Session, Config or Turn. This fixes a confirmed P1 where
  an inconsistent CreateTurn return could complete an Agent under a foreign
  already-persisted Turn. No additional Store state, schema or compatibility
  bridge. See [independent remediation](docs/audits/2026-11-04-harness-top-level-store-receipt-remediation.md).

- Harness/Agent startup minimalization: remove the duplicate private Agent
  top-level admission envelope and Agent StartRun dispatch construction.
  Agent, Team, GroupChat, Workflow and Application share one top-level Turn
  admission envelope; Agent, Team, GroupChat and Workflow share Kernel-backed
  first-admission result settlement. Application retains its independent Host
  effect/OutputRefs settlement. No old forwarding alias is retained.
  Agent's pre-Run recovery and the **first-attempt-only** native
  cancellation-conflict exception remain preserved; subsequent RetryInvocation
  Attempts do not inherit that extra snapshot-conflict suppression. Certified
  no-effect receipts and unknown-effect
  fencing are unchanged. No public API or persistence migration. See the
  [Harness/Agent hard-cut audit](docs/audits/2026-11-04-harness-agent-start-hardcut.md).

- Context Run owner hard cut: process-local Context Window bindings cannot
  authorize a different Agent Run's Session/Turn or elevate a descendant to
  Context `owner`. Rebuild the active Context from durable Invocation/RunRelation
  ownership before trusting any inherited checkpoint. Unknown Runs lose stale
  inherited Context, explicit `read_only` grants remain narrowed, and Handoff
  Specialists **and their nested Agent descendants** remain excluded from
  Supervisor transcripts. Uncertain owner lookups and inconsistent
  RunRelation child identities now fail closed before Context rehydration.
  A final independent audit closed a TOCTOU gap by merging Handoff isolation
  and Turn selection into a single physical ancestry scan; the Context
  Artifact Tool reuses that proof and refuses to expose a Supervisor archive
  to isolated Specialists or their descendants, even if the Host explicitly
  supplies the Tool and a known artifact ID. Final integrity hardening
  validates the actual cached Checkpoint contents before reusing a matching
  durable reference, rehydrating from Store on mismatch; corrupt frozen
  Invocations no longer supply Context ownership or downgrade a Specialist
  into an ordinary child. A later independent follow-up closed three
  conditional Store-read integrity gaps: full content-hash verification for
  reloaded Checkpoints, consistent frozen Invocation Input/InputHash pairs
  (including absent Handoff child payloads), and complete RunRelation
  writer-contract validation before granting Context ownership. A subsequent
  invariant-driven audit found independent failures in Turn identity, indexed
  Invocation provenance, exact Artifact identity, sticky Context exclusion and
  shared-budget Turn attribution. Context, Artifact and Budget now share one
  Run-ancestry authority proof, which corroborates indexed Invocations against
  their canonical immutable records and verifies returned Turn IDs. Artifact
  payloads are checked against their requested IDs and content hashes, and
  explicit Context exclusion cannot be reopened by process-local rebinding.
  No persisted state or public API change. See
  [Context Run owner audit](docs/audits/2026-11-04-context-run-owner-provenance.md).

- PR #87 completion-authority audit: bind each named Provider invocation to
  one physical or verified settled-replay receipt. Middleware cannot replace
  its exact ID or Usage, promote a rejected/failed call to success, or fabricate
  a completion without a source. Rejected stream IDs are reconciled before
  errors return, and persisted duplicate physical receipts fail recovery.
  Shared-budget crash replay remains supported without another Provider call.
  See [convergence audit and regression matrix](docs/audits/2026-11-04-pr87-completion-authority.md).

- PR #87 independent Agent P1 audit: successful replies from named Provider
  bindings must carry an exact original physical response ID, just as known
  rejected replies do. Missing or whitespace-normalized IDs previously
  bypassed the physical completion deduplication fence and authorized an
  actual local Tool (`effects=1`). Agent now rejects these replies before
  shared-budget settlement and again after downstream middleware; SSE
  terminal identity is not trimmed, and contradictory event/final IDs fail
  closed even if a faulty StreamingClient ignores callback errors. Durable
  completed/consumed named-Provider receipts also require identical original
  and persisted IDs on restart; 20 previously accepted invalid state cases
  now fail closed. Synthetic fixtures now supply stable completion IDs.
  Unnamed local Models remain compatible; no Kernel/Store or replay migration.
  See [independent P1 audit](docs/audits/2026-11-03-pr87-named-provider-success-identity.md).

- PR #87 added-item status audit: OpenAI SSE rejects explicit incomplete or
  malformed added-item states before local Tools or terminal receipts.
  Already-completed added items retain full terminal evidence, preventing
  later argument, text, refusal or encrypted-reasoning substitution while
  allowing matching done/final events. Legacy omitted and in-progress
  states remain supported. See the
  [added-item status audit](docs/audits/2026-11-03-pr87-openai-added-item-status.md).

- PR #87 independent OpenAI SSE lifecycle audit: early
  `response.created`/`response.in_progress` observations must not claim a
  terminal failure, truncation or completion, or carry non-null error/
  incomplete details, before a later successful Tool-bearing completion.
  Reproduced executable ToolCalls and terminal SSE success on the prior
  Head; protocol checks now fail closed. Existing omitted status and
  absent/null details remain valid. See
  [SSE lifecycle audit](docs/audits/2026-11-03-pr87-openai-sse-lifecycle-status.md).

- OpenAI completed or replayed message items must contain an actual content
  array before sibling Tools are accepted. Null or missing content fails
  closed; an explicit empty array remains supported. The durable timeout
  retry test now allows enough dispatch time under CI coverage load.

- PR #87 independent message-payload audit: OpenAI `output_text` now requires
  a physically present string `text`. Previously explicit null or omitted
  text could be silently flattened when followed by valid text and a local
  Tool; unary/SSE then authorized the Tool and SSE success. Both paths now
  return only a verified rejected completion receipt; malformed persisted
  replay is refused before continuation. Unphased legacy `phase: null` and
  genuinely omitted phases remain supported, without protocol migration.

- PR #87 independent replay audit: OpenAI persisted function-call evidence
  now rejects an explicitly non-completed `status` before any continuation
  HTTP dispatch. Previously a stored replay with `in_progress`, `incomplete`,
  `failed`, null, or malformed Tool status could pass transcript identity
  checks and be resent as a completed Tool. Omitted legacy status and
  `completed` remain supported; no replay format or Store migration.

- OpenAI SSE response.created and response.in_progress now establish a
  stable response identity that must match completion. Conflicting or
  malformed outer identity cannot authorize a Tool or paid receipt.

- PR #87 OpenAI SSE P1 follow-up: bind immutable output-item identity from
  `response.output_item.added` to the completed item before authorizing
  local Tools or replay. Added/done/final conflicts, duplicate or late added
  events now fail as protocol errors with no trusted terminal receipt;
  partial arguments and encrypted reasoning remain non-authoritative.
  Original-Head regression cases failed before the correction and pass after.

- PR #87 final audit hardening: duplicate physical response IDs are checked
  before shared-budget settlement, including late results and downstream
  middleware, and rejected receipt payloads are not stored in the shared
  ledger. OpenAI finalized argument, text, refusal and content-part events
  must agree with the final response. Anthropic rejected Thinking blocks
  retain signature sequencing evidence. Regressions cover these boundaries.

- PR #87 independent Agent rejected-completion receipt hard cut: a named
  Provider's typed post-generation rejection cannot establish paid
  completion without an exact nonempty physical ResponseID, even with
  apparently valid Usage. Previously an ID-less rejection was billed.
  The Agent now also persists **only ResponseID and valid Usage** for
  known rejected completions: custom Provider/middleware errors previously
  persisted untrusted rejected text and unauthorized ToolCalls inside
  durable ModelInvocation receipts. Correction/restart and normal billing
  remain unchanged. No Host/Kernel migration or compatibility bridge.
  See [independent P1/P2 audit](docs/audits/2026-11-03-pr87-rejected-receipt-agent-authority.md).

- PR #87 further independent P1: Agent now rejects a completed physical
  Provider response ID that has already been consumed by a different
  ModelInvocation under the same named Provider/binding in the same Run.
  Previously an upstream reply could reuse an old response ID with a fresh
  Tool ID and execute a registered local Tool (OpenAI and Anthropic each
  reproduced `effects=1`), or mint a second rejected paid receipt
  (34/10 rather than 17/5 tokens). A minimal Agent-owned receipt fence
  fails closed before Tool dispatch or billing. Retries of one invocation,
  independent Provider/binding identities and unnamed local models remain
  unaffected; no Kernel/Store schema or compatibility bridge. Synthetic
  fault-test Provider IDs now derive from their stable InvocationID.
  See [independent P1 audit](docs/audits/2026-11-03-pr87-provider-response-id-reuse-guard.md).

- PR #87 further OpenAI Responses P1 hard cut: a physical
  `status=completed` envelope must not simultaneously carry non-null
  `error` (generation failure) or `incomplete_details` (truncation).
  Previously both Unary and SSE authorized local `function_call` output
  and completed billing despite contradictory terminal evidence; public
  Agent→SSE→Kernel tests executed a registered Tool Handler (`effects=1`)
  on the original Head in two cases. The shared decoding boundary now
  refuses these envelopes without Tool, replay, completed Usage/ID or
  terminal SSE success. Real absent/null fields remain valid; no
  Agent/Kernel, Host, persistence or compatibility rewrite. See
  [independent audit](docs/audits/2026-11-03-pr87-openai-terminal-evidence-contradiction.md).

- PR #87 additional Anthropic SSE P1: an earlier semantic content rejection
  must not disable physical `content_block_delta` ownership, Tool input
  consistency or Thinking-signature sequencing. Mismatched delta types and
  populated initial Tool inputs followed by contradictory JSON deltas
  previously minted a completed paid ResponseID/Usage receipt despite
  invalid framing. Known malformed frames now fail with no completed
  receipt, replay or local Tool; well-framed unsupported content retains
  its minimal verified paid rejection. Original-Head read-only mutation
  reproduced double ModelInvocation billing; repaired public Agent/Kernel
  regressions and Provider Race are green. No Agent, Kernel, Host, DB
  migration or compatibility bridge. See
  [independent P1 audit](docs/audits/2026-11-03-pr87-anthropic-sse-rejected-delta-framing.md).

- PR #87 independent Provider P1 hard cut: OpenAI function-call arguments
  (JSON encoded as a string) and Anthropic `tool_use.input` must physically
  decode to a **JSON object**, even if the local Tool has a permissive JSON
  Schema. Previously valid but nonobject JSON (arrays, `null`, booleans)
  authorized actual local Tool Handler execution in both Unary and SSE.
  A shared bounded uniqueness/object-root validator now guards Tool decoding,
  SSE completion evidence, replay validation and outbound Tool history.
  Known completed invalid Tool replies retain only verified ResponseID/Usage
  under typed post-generation rejection, never executable calls, replay or
  terminal SSE success. Contradictory Anthropic start/delta frames remain
  protocol failures without fabricated billing. Direct RED→GREEN tests and
  public Agent→SSE→Kernel side-effect tests cover both adapters. No Host,
  Agent/Kernel, persistence migration or legacy bridge. See
  [independent P1 audit](docs/audits/2026-11-03-pr87-tool-argument-root-hardcut.md).

- OpenAI Responses and Anthropic Messages forced-Tool response hard cut:
  `RequireToolCall` now verifies the final provider result actually contains
  a local Tool call, not just that `tool_choice` was sent. An upstream proxy's
  completed text-only reply is rejected in unary and SSE modes without a
  successful terminal receipt. If the response carries valid cumulative
  Usage, preserve its ResponseID/Usage under the existing typed
  ResponseRejectedError (`model.post_generation_failed`) so Agent consumes
  and bills exactly once; missing or invalid Usage still fails closed with
  no invented receipt. For OpenAI phased output, a commentary-only refusal
  cannot waive the required Tool when `final_answer` is ordinary text; only
  the selected terminal message's explicit refusal qualifies. Global
  `Response.Refused` continues to retain commentary safety evidence.
  The final refusal must come from an `assistant` message with a
  `completed` status when those fields are present. Explicit foreign
  roles, incomplete statuses, null and empty field values cannot claim
  the exception; omitted legacy fields remain supported. Explicit terminal
  refusals, ordinary text requests and valid Tool calls remain supported.
  OpenAI additionally checks each physical `function_call.status` before
  authorizing Tool execution, for both optional and mandatory calls.
  Explicit unfinished/invalid items cannot be dispatched; a complete
  billable response preserves only its ResponseID/Usage rejection receipt,
  even if the unfinished arguments are malformed. The subsequent PR #87
  paid-receipt fix also accounts a completed OpenAI/Anthropic message whose
  Tool arguments or identifiers fail post-completion Tool-item validation:
  only ResponseID/valid cumulative Usage survive as a rejected completion;
  no Tool effects or terminal SSE success are authorized. Unknown final
  Usage remains unreceipted. A further independent P1 fix ensures a
  completed OpenAI response containing a valid local Tool item alongside a
  contradictory refusal/final answer, malformed/unknown output item, or
  unencodable replay retains only its verified ResponseID/Usage as a
  rejected completion. No partial Tool batch or terminal SSE success is
  authorized; missing encrypted reasoning replay keeps its existing error
  classification. Anthropic now also retains the minimal paid rejection
  receipt when a valid physical `tool_use` appears anywhere in a completed
  message beside an unsupported text/Thinking/redacted block, or final SSE
  block reconstruction fails. A malformed sibling cannot silently drop known
  cumulative Usage. The next P1 correction separates Anthropic SSE early
  semantic block rejection from transport/framing failures: unsupported
  citations or content types are never authorized, but a bounded, well-framed
  stream can still supply `message_stop` and final cumulative Usage for a
  Tool-bearing paid rejection. Incomplete, malformed (including
  noncontiguous block indices) or unknown-Usage streams remain
  unreceipted. A further P1/P2 repair consumes valid-Usage completed
  **Tool-free unsupported** replies under `RequireToolCall`, and persists
  only ResponseID/Usage (not text, refusal flags or replay) for all
  rejected required-Tool responses. Optional unsupported text, missing
  Usage, incomplete responses and broken SSE framing remain fail-closed.
  A subsequent independent P1 closes Anthropic SSE phase confusion:
  sequential Tool blocks finish before message deltas, block indices must
  be explicit, final stop reason must be observed, and cumulative output
  token counts cannot regress. Malformed late Tool frames never dispatch
  local Tool handlers; valid multiple final deltas still work. A further
  independently reproduced P1 binds Anthropic unary/SSE messages to
  `type="message", role="assistant"` and OpenAI Responses unary/SSE to
  `object="response"` before any Tool, replay or terminal receipt can be
  trusted. Unknown or missing outer ownership now fails closed, with
  cross-provider Agent side-effect tests and no ownerless wire fallback.
  Another independent P1 refuses nonempty or explicit-null Anthropic SSE
  `message_start.message.content`, which had been silently erased before
  later Tool blocks were accepted. A malformed initial content collection
  cannot be replaced by a different executable Tool or grant terminal billing
  proof. Valid empty/omitted initial content remains supported; original
  Head Agent/Kernel mutation reproduces actual Tool effects, fixed source zero.
  A further Anthropic SSE P1 prevents nonempty/null Tool input already
  present at `content_block_start` from being silently overwritten by later
  `input_json_delta` fragments. Only an empty initial input object may be
  populated by streamed arguments; complete initial Tool input without
  deltas remains valid. Read-only original-Head mutation reproduces real
  Tool Handler execution, while repaired Provider/Agent tests prove zero
  effects and no invented completion receipt.
  A further independent P1 closes the unphased OpenAI output-message gap:
  an explicit foreign role or unfinished `message.status` now rejects the
  entire completed output, including any otherwise-valid sibling Tool call.
  Verified final ID/Usage remain as a sanitized rejected receipt; no local
  Tool effects, replay or terminal SSE success are permitted. Truly omitted
  legacy fields and valid completed assistant messages remain supported.
  Another independently reproduced P1 rejects physical OpenAI
  `function_call.call_id` and Anthropic `tool_use.id` values that would
  change under Agent's whitespace normalization. Empty/whitespace-only and
  padded IDs can no longer execute a local Tool or create synthetic Call IDs;
  verified completed Usage is settled as a minimal rejection receipt, with
  no ProviderReplay or terminal SSE success. No compatibility rewrite.
  Another independent P1 binds the **outer completed Provider response ID**
  to its physical bytes: OpenAI `response.id` and Anthropic `message.id`
  may not be blank or padded. Agent previously trimmed that ID when storing
  its ModelInvocation while the Provider authorized a local Tool under the
  inconsistent original identity. Both Unary and SSE now fail closed before
  Tool dispatch, replay or fabricated Usage; actual public Agent/Kernel Tool
  effects on the original Head are absent after correction. No bridge.
  A further independently reproduced P1 protects OpenAI SSE `output_item.done`
  authority: completed Tool and Message items now reconcile with the final
  `response.completed` output before local Tool execution. Contradictory,
  incomplete, missing-index or duplicated done evidence fails closed; no
  Tool, replay or synthetic completed Usage receipt can be created. Existing
  Reasoning continuation and final-only completion remain supported.
  A further independent P1 fences explicit unfinished, null and malformed
  OpenAI `reasoning.status` even when `encrypted_content` and a valid physical
  Tool item are present. Only absent legacy status or `completed` is allowed;
  a known paid response retains sanitized ID/Usage without dispatching local
  Tools or replaying unfinished encrypted reasoning. No compatibility shim.
  Another independent P1 fences duplicate JSON object keys in OpenAI/Anthropic
  unary/SSE responses, including escaped equivalent names and nested Tool/
  Usage authority. Previously Go's last-key-wins decoding could promote a
  foreign or incomplete physical response to an executable local Tool.
  Ambiguous response framing now fails closed without Tool execution,
  replay or invented completed billing; normal JSON remains supported.
  A further independent P1 now checks **double-encoded Tool input** too:
  OpenAI `function_call.arguments` and Anthropic SSE `partial_json` are
  strings not covered by outer JSON-frame uniqueness. Their reconstructed
  Tool arguments must contain no duplicate keys (including escaped aliases)
  before any local Tool or provider replay is authorized; valid completed
  Usage survives only as a sanitized rejected response receipt. The original
  Head executed one real Tool Handler in either bypass; repaired Race and
  Agent/Kernel tests prove zero effects. No compatibility shim is added.
  Another independently confirmed P1 rejects case-folded JSON member
  aliases (`Object`/`object`, `Role`/`role`, Unicode `ſ`/`s`, `K`/`K`),
  which Go's struct decoding otherwise treats as one authority field even
  without identical keys. The shared bounded uniqueness check now rejects
  colliding names at every JSON object level before authorizing Tool or
  replay, including nested JSON input. Distinct sibling objects remain
  supported; case-distinct keys in the *same* object deliberately fail closed.
  Another independent P1 prevents ambiguous *persisted* ProviderReplay from
  bypassing that inbound-response check. Both OpenAI Responses raw output
  replay and Anthropic signed Thinking/Tool content now prove whole replay
  JSON member uniqueness before any outbound continuation request; forged
  case-folded role/status/phase or duplicate Tool/signature fields cannot
  be resent even if normalized Agent Tool identity still matches. Opaque
  valid replay bytes remain unchanged, without a new replay format or shim.
  No new ModelInvocation state, API shape, adapter fallback,
  schema migration or automatic retry.

- Round 3 Harness output provenance hard cut: child Invocation results
  cannot appear as top-level Turn `Snapshot.Output` during unresolved Host
  callbacks. Every physical root status/Item projection (including failed or
  cancelled Runs without Result), Host-message ACK, and Agent Resume requires
  the original frozen Run ID, Kind, Actor, Thread, Goal and Attempt-bound
  RequestID, not just matching IDs. Root Cancel proves ownership before
  physical control; top-level Feature Start replay proves it before Resume.
  Native child Agent/Team/Workflow/GroupChat Invocations now prove their
  immutable root and ancestor Attempts and original Kind/Actor/Thread/Goal/
  RequestID before physical dispatch, Resume, status CAS or result Artifact.
  Local native RunRelation/Invocation ownership checks now require the
  same frozen root/child proof before any Kernel Cancel or Subtask result
  read, preventing same-ID foreign cancellation and cross-tenant disclosure.
  KindCapability RunRelation now additionally binds ParentRunID, ChildRunID
  and OwnerNodeID to the original frozen child/root Attempt. Cross-Turn
  edges cannot lend read or physical-cancel authority; CancelSubtask proves
  that owner before persisting a cancellation intent. Remote Handoff keeps
  separate ChildRunner/receipt ownership; verified
  no-Run cancellation keeps its existing no-effect projection. Application
  roots never read Kernel Results; their output remains
  Host-owned OutputRefs. Preserve result-free legacy *read-only* inspection,
  explicitly fail closed on unverified mutation, and keep original no-Run
  admission failure recovery. No new output Store, status, automatic retry,
  SQL migration or compatibility shim.

- Round 2 Harness lifecycle authority hard cut: root and child Invocation
  projections cannot rewrite a terminal status within the same Attempt.
  Memory and SQL Store `UpdateInvocation` now atomically reject completed,
  failed or cancelled status rewrites, even with the current Revision; only
  explicit `RetryInvocation` can rotate the Attempt and execution identity.
  Same-terminal metadata repair remains allowed. Replace two legacy tests
  that fabricated retries by rewriting completed Invocations with legitimate
  no-effect failure fixtures. No new Run status, SQL DDL, schema version,
  compatibility bridge or automatic retry.

- First-round single-Agent minimization: omit `harness.Dependencies.Clock` to
  use an explicit internal wall clock for Harness timestamps, retaining
  injected deterministic clocks and Store-owned execution lease authority.
  Add a minimal optional Harness Session example (Host transcript + pre-commit
  Context projection + stable Turn replay) alongside the existing Core Agent
  quickstart; print the quickstart's plain text result instead of a content-part
  representation. The Session example now also checks that an old Turn is read
  with `Load` after Host history advances; changed historical `Prompt` input
  remains rejected without a second model call. No additional Agent loop,
  runtime factory, persisted state, schema version or compatibility bridge.

- Clarify the one Agent model/Tool loop and remove obsolete private `Direct Agent`
  replay/admission helper names. SDK user-intent selection remains wholly in
  Host code; no `IntentRouter`, `PrepareIntent`, `DirectMode` or generic Start
  API. Model request projection and durable Model/Tool/cancellation intents
  retain their existing execution safety authority. New event diagnostic text
  says `Agent loop` (event types unchanged); rekey the unchanged portable
  performance baseline under `BenchmarkHarnessStartAgentTurnMemory`.
  See [ADR 0016](docs/adr/0016-agent-loop-and-intent-boundary.md).

- PR #82 independent Workflow sub-run orphan correction: the built-in
  Subworkflow effect commits its deterministic child RunRelation *before*
  physically creating or advancing the child, using the original persisted
  effect ID/Attempt and fan-out owner semantics. If relation persistence
  fails, no child is dispatched; a committed relation with a lost response
  remains conservatively unverified rather than becoming a fabricated
  successful cancellation. Add exact `EffectRequest.Mapped` propagation and
  Memory/SQL/PostgreSQL crash/recovery regressions. No Store/DDL version or
  compatibility router is introduced.

- PR #82 independent cancellation follow-up: reject Host composite Feature wrappers
  without BOTH exact Kernel ownership and RunRelation recorder proof. Merely
  implementing the public Start/Resume adapter or sharing the registry cannot
  authorize local cancellation. Built-in Team, GroupChat and Workflow runners
  now attest their exact Kernel; Harness rejects a foreign native Runtime at
  composition. Unknown Host routes fail closed before physical cancellation,
  retaining any committed intent for explicit reconciliation. The original
  no-effect/retry receipts remain unchanged. No parallel routing bridge, Store
  change or SQL migration.

- PR #82 cancellation follow-up: a composite Team/GroupChat/Workflow Run
  without a verified original child-relation index cannot claim that an empty
  child lookup proves cleanup. Built-in Features must record to the exact
  RunRelation recorder Harness reads; missing or different registries return
  `ErrRuntimeCancellationUnverified`, retaining any committed cancellation
  intent after physical cancellation. Running/Failed parents and Memory/SQL
  regression tests cover the previously false-positive cancellation receipt.
  No new Store protocol or SQL schema.

- Require Feature-owned fixed local route and persisted task proofs before
  observing, approving or cancelling raw Team/GroupChat/Workflow descendants.
  A same-ID local Run or unrelated Harness Invocation cannot replace this
  proof. Delegation Tool recovery/waits likewise require frozen local ownership;
  built-in subworkflows verify immutable origin before resume or receipt use.
  Handoff parent liveness and no-effect exceptions also require original
  route/edge authority. Physical Subtask approvals must use the exact fixed
  child Agent rather than a substitute root AgentStarter. Remote/unproven
  outcomes retain existing reconciliation fences. See the
  [PR #82 route-proof audit](docs/audits/2026-11-01-pr82-feature-route-proof.md).

- P3 canonical Model Message multi-modal hard cut: replace
  `Message.Content string` with one ordered, closed
  `[]ContentPart` (text, image, PDF, audio remote references).
  OpenAI/Anthropic map ordered user image/PDF blocks to native
  request formats; referenced audio and malformed/non-User
  media fail before HTTP. Media URLs, MIME and part order are
  frozen in durable ModelInvocation/Context state, and managed
  media Context budgets require exact provider-aware counting.
  No parallel attachment field, legacy text-string bridge,
  raw binary storage or new SQL schema.

- Hard-cut Harness Store `GetItem(ctx, id)` as a mandatory indexed,
  immutable global-Item-ID read. Physical child ancestry fencing no
  longer scans whole Turns; Memory Item cursor pages now use binary
  search, retaining existing ownership/cycle/terminal safety guards.
  Update custom Harness Store adapters; no SQL Schema changes, cache,
  optional fallback or new execution state.

- Refuse to charge anonymous Hosted Tool terminal facts as independent
  remote executions. Missing Provider Call IDs, even on completed/failed
  outcomes, keep the dispatched Model/native budget reserved for explicit
  reconciliation; mixed anonymous/named receipts cannot partially settle.
  Historical anonymous charges remain visible as incomplete usage, with no
  fabricated per-key identities or second ledger.

- Independent PR #77 safety correction: require original Call-ID
  `rejected` no-effect receipts before any retryable Tool error may
  re-enter the physical Handler; accept only closed successful receipt
  dispositions (`read`, `committed`, `pending`), keeping unknown or
  error-free `rejected` results uncommitted and the budget Held.

- Hard-cut Agent Tool dispatch intent and exact CallID terminal receipts
  across direct/Store-leased execution. Missing, malformed or unknown Tool
  outcomes no longer auto-replay. After physical Tool dispatch, a
  model-correctable error requires explicit original-ID rejected/no-effect
  evidence. Shared budget cannot redispatch Held effects; first-party
  Capability/Delegation can verify COMMIT read-only without Handler reentry.

- Recheck the full parent ancestry around Host-only Application effect
  claiming and immediately before its physical callback. A retired parent
  cannot dispatch a stale child; a known pre-effect rejection fails the
  claimed Attempt without inventing an external effect receipt.

- Fence child Harness Invocation retries against terminal Kernel parents
  and finished intermediate Invocation ancestry before Retry CAS, preserving
  retry identity and valid Host-only Application children.

- Remove top-level Harness RetryInvocation fallback to mutable Session
  identity; require exact frozen Actor and Kernel Thread before allocating
  another execution attempt, with negative persistence tests.

- Treat a missing Hosted Tool status as unfinished, not proof of execution
  completion. Preserve exact held native reservations for identified calls;
  an unidentified unfinished call now keeps the physically dispatched Model
  reservation held and requires explicit reconciliation instead of automatic
  quota release or fake Provider IDs.

- Freeze a nonsecret Provider binding fingerprint in each named ModelInvocation
  and its request hash; reject missing or changed bindings before another
  Model attempt or physical dispatch. Require explicit Router Binding.Identity
  and custom transport scope. No old request-hash compatibility fallback.

- Require the exact displayed Harness approval checkpoint at Go/HTTP/TypeScript
  entry points; remove Turn-only approval and pre-binding Agent checkpoint
  inference. An incomplete older approval must be drained/quarantined, never
  automatically granted by matching its legacy Tool payload.

- Extract the Provider Router, OpenAI Responses and Anthropic Messages adapters
  into independent `go/agent-runtime-provider`; delete the previous Core
  `provider` package without aliases or forwarding imports.
- Replace the implicit `harness.Runner.Start` default with explicit
  `StartAgentTurn` selection, leaving durable intent and recovery evidence intact.
- Move provider examples and Agent-vendor integration fixtures to the new
  module; retain vendor-neutral `model.Client` and its durable replay contract.
- Expand the release cohort and consumer gates to eleven modules. Previously
  published beta.23 tags are immutable; beta.24 is not published by these
  commits. See [beta.24 upgrade notes](docs/releases/v0.1.0-beta.24.md).

## Unreleased

- Preserve OpenAI Responses stateless encrypted reasoning from the completed SSE `response.output_item.done` when `response.completed` omits that field, but only after an output-index/ID/other-fields consistency check. An optional `status: "completed"` in the done item may be absent from the final response; other mismatches still fail closed. Never use partial added items or synthesize missing reasoning. Document the mandatory drain/quarantine and host TranscriptSource cutover for earlier incompatible phase/replay history, with no schema bump or compatibility shim.

- Document dated, report-scoped real-model endpoint compatibility findings and explicit, low-cost recheck discipline; keep gateway-specific evidence in ignored local artifacts, preserve fail-closed replay, and avoid introducing permanent SDK blacklists or mixing independent governance subjects into one PR.

- Gate OpenAI Responses real-model task baselines on a separate, bounded stateless Tool+reasoning replay probe after the strict JSON Schema probe. Demand a side-effect-free synthetic Tool call, verify encrypted reasoning when reasoning occurs, replay the original output with a synthetic Tool receipt, and validate a strict final answer before any task sample runs. Preserve bounded redacted wire evidence, independent usage/request counts, a distinct preflight profile fingerprint, and prior reports unchanged. No SDK API or provider fallback is added.

- Capture the last actual provider HTTP response when a live baseline task fails, with physical-dispatch count, status, bounded credential-redacted body and explicit capture state in console/JSON/Markdown. Mask provider-owned `reasoning.encrypted_content`; do not treat a previously observed response as proof of a later local failure. Add `retry <report.json> --repetitions 1` for lower-cost diagnosis without changing the existing task oracle, replay fencing or earlier reports.

- Classify missing OpenAI encrypted reasoning replay evidence in the live baseline as `provider_replay_unavailable` before generic `provider_unsupported`, keeping the actual provider fail-closed contract unchanged.

- Align the native strict-schema preflight prompt with its JSON contract and record `native-json-schema-v2` in the measurement profile/dataset fingerprint. Earlier conflicting plain-sentence probe results remain distinct; validation stays strict.

- Show the captured HTTP response body and extracted SDK answer for failed baseline preflights by default, and retain both in JSON/Markdown reports after credential redaction. Mark incomplete and truncated captures, escape terminal controls and Markdown fences, and preserve original validation without another model request or a debug flag.

- Preserve typed provider HTTP transport timeouts as the existing `model.upstream_timeout` classification without exposing raw network errors or changing retry budgets. Baseline diagnostics distinguish a timeout with no observed HTTP response from a native-schema failure and direct connectivity checks before another live sample.

- Diagnose failed OpenAI strict-schema probes at the same HTTP request/response boundary with bounded, content-free wire observations. Keep native-schema pass/fail unchanged, distinguish incomplete evidence from a negative finding, and add `run-agent-baseline.py retry <report.json>` to reuse public settings with a fresh hidden key and new report.

- Select OpenAI Responses `final_answer` separately from `commentary`, preserve original assistant phases during stateless continuation, and use the same projection for replay identity checks. Retain private replay evidence in Agent completion and bounded completion-repair history. Reject ambiguous phase combinations without extracting or repairing JSON. Review persisted transcripts before upgrade; see the [phase upgrade note](docs/releases/unreleased-openai-response-phases.md).
- Explain strict-schema baseline failures with safe JSON shape/type, required-field and continuation diagnostics while retaining the native strict contract and historical reports. The `schemaCheck` summary excludes response content and credentials; failed-response text is available separately through redacted `responseDebug`.
- Revise the example baseline service input ceiling from 6,144 to 8,192 after native phase/replay fixtures exceeded the former allowance; retain actual archival/retrieval and recovery checks. Record the changed policy in the profile/dataset hash, read historical reports under their own budgets, and reject score comparisons across the two profiles.

- Resolve recognizable root and `/v1` base URLs at interactive baseline input, displaying the final protocol endpoint before key entry. Explain preflight failures with safe observed HTTP metadata and actionable hints, preserve historical endpoint values, and distinguish the task output budget from model capacity.

- Add an interactive real-model Agent baseline with hidden key input, explicit endpoint/model budgets, native-schema preflight, repeated fixed evidence/recovery tasks, failure-preserving reports and compatible-report comparison. Keep offline protocol tests separate from real-model observations; Python 3.9+ is required for the new launcher tests in `make check`. See the [baseline note](docs/releases/unreleased-agent-model-baseline.md).

- Add explicit `Runner.ResumeTurn` and `SessionClient.Resume` for an existing direct-Agent Turn, restoring persisted Context while retaining Agent revision, execution-claim, retry, approval, budget and uncertain-Tool guards. Allow transcript-free Session read/recovery handles. See the [single-Agent task recovery note](docs/releases/unreleased-agent-task-recovery.md).
- Make the offline quickstart execute an Agent and Tool, and add a task-oriented Context/recovery example with an output oracle plus real PostgreSQL process-recovery coverage. Pin the standalone integration test toolchain to patched Go 1.27.3. Keep deterministic execution evidence separate from optional live-model quality checks.
- Reject every HTTP redirect in MCP and A2A clients, including same-origin redirects, before another request can forward endpoint-scoped authentication or bypass endpoint validation. Preserve host redirect rejections without mutating the supplied HTTP client. Hosts must configure final protocol URLs directly; see the [breaking upgrade note](docs/releases/unreleased-protocol-redirect-safety.md).

- Freeze optional host-prepared Group Chat selector input before its pending invocation, retaining the exact request across restarts and shared-budget receipt replay. Reject preparer changes to logical authority and strip private prepared input from public views and HTTP diagnostics; selectors without preparation remain unchanged.

- Bound Model, Tool and Hosted Tool concurrency admission by Run deadlines, and release permits on expiry or cancellation before dispatch. Permit waiters can no longer remain blocked beyond their Run deadline or consume another Run's concurrency slot.
- Preserve exact JSON numbers in Queue payloads and public HTTP projections. Queue fingerprints now retain numeric spelling; replaying Jobs written with earlier lossy numeric normalization may return `ErrConflict` and requires host reconciliation instead of silently reusing changed input.
- Retry documented transient OpenAI and Anthropic SSE errors through the existing durable ModelInvocation retry budget, while keeping permanent/unknown errors terminal and upstream messages private.
- Encode Windows drive letters as SQLite URI paths, preserving escaped filename characters. This does not certify Windows ACL or directory-sync support for database publication.
- Enforce one root Harness Invocation per Turn atomically in Memory and PostgreSQL Stores, preventing changed RequestIDs or competing capability starts from dispatching a second root Run. Apply the Harness migration before rollout; historical duplicate roots require host review before the new unique index can be installed.
- Keep command-local JSON Schema references and resource scopes intact when projecting model-facing capability arguments; scoped schemas retain their exact command constraints without invalidating unrelated commands.
- Persist classified permanent model-request preparation failures as terminal Run outcomes before creating a model intent or spending budget, while keeping explicitly retryable preparation failures resumable.
- Preserve child Workflow input waits while the parent continues running, and retain the next Interaction wait after a response resumes its owner. Memory and PostgreSQL admission use the same parent/child boundary.
- Present typed capability arguments at the root of the model-facing command schema and validate the frozen command contract before materialization. Invalid argument types remain recoverable Tool failures without executing a Workflow.
- Allow Harness approval requests to carry the displayed `checkpointID`, rejecting stale owner actions before they can resolve a later checkpoint. The HTTP contract and TypeScript client retain support for existing Turn-only clients.

- Atomically retain unfinished hosted-call reservations alongside final model token receipts. Reconcile native identities once across continuations and restarts, preserve unknown outcomes on Run termination, and mark incomplete usage instead of claiming cancellation. Existing settled receipts are not rewritten; no default test ceilings are installed.

- Persist known rejected model completions with `ModelInvocation.errorCode`, final usage and response identity before consuming their charges. Preserve these receipts through streaming, cancellation and budget replay, including failures projecting an already settled ledger. Local post-generation save failures use `model.post_generation_failed` and never authorize regeneration or Tool execution.
- Reuse terminal completion correction for one tool-free repair within the existing call budget. A durable `completionRepair` flag prevents repeated corrections, native pauses or renewed delegation from extending the repair. Repeated violations fail explicitly for parent takeover. State fields are additive; historical terminal Runs are not rewritten. Drain in-flight Runs before downgrading to readers that do not understand rejected completion receipts.

- Use only explicit native-call ceilings and remaining shared quota when reserving hosted tools; remove the implicit two-call cap. Emit one idempotent final-answer instruction after all local and native tools are unavailable, owned by shared-budget preparation.

- Preserve empty assistant placeholders and provider-owned replay for native continuations across Context checkpoints and restarts. Retain stable replay alignment while rejecting malformed local Tool transcripts and unmatched result identities.

- Settle authoritative usage when a host rejects an already completed model response via `model.NewResponseRejectedError`. Replay the rejection after restart without redispatch; partial streams and missing receipts still retain uncertain reservations.

- Add opt-in `MaxConcurrentDescendants` lifetime admission for all unfinished descendants, including waiting coordinators. Queue capacity waits before creating child Runs, reconcile committed terminal facts after restarts, and preserve uncertain usage reservations. Zero remains unlimited; no default testing limits are installed.

- Derive host-only `Snapshot.ToolUsage` from settled Turn ledger receipts, including descendants and native calls by opaque tool key. Keep reservations out of actual counts and mark incomplete accounting explicitly. Count native execution in tool usage even without a native quota; no new default ceilings or HTTP fields.

- Bound child-terminal reconciliation to 128-ID keyset scans before PostgreSQL status joins; maintain a sorted Memory index, preserve empty-page cursors, and rotate a bounded retry backlog without starving later pages.
- Persist monotonic reconciliation progress in two additive PostgreSQL relation columns. Queue acceptance precedes `MarkEnqueued`; feature-verified consumed children use `MarkObserved`. Parent wait commits reopen recovery eligibility.
- Add opt-in first-party Team/Group Chat/Workflow child-consumption guards, a production durable-queue requirement, memory/PostgreSQL paging and migration tests, and a real PostgreSQL + Redis joint restart integration suite. Preserve the legacy custom Store interface. See docs/audits/2026-10-14-stage2-reconciliation.md.
- Recover abandoned Redis continuation leases during `Claim` with a bounded expiry sweep; no crashed-worker Ack or separately deployed Reaper is required. Preserve retry backoff, dead-letter policy, and stale-lease fencing.
- Have the continuation Worker claim deliveries just in time and renew an active delivery while its handler runs. Cancel cooperative dispatch on renewal failure. The optional renewal capability preserves existing custom Queue interfaces; custom queues without `Renew` must bound execution below their visibility timeout.
- Add deterministic queue/Worker regression tests and real-Redis restart evidence for recovery without an explicit Reap.

## [0.1.0-beta.23] — release candidate (unpublished)

- Coordinate ten Go modules including new experimental SQLite Kernel/RunRelation
  Store against the same new Core API cohort; add independent no-workspace,
  no-replace published Go consumer verification before GitHub prerelease.
- Retain the six-status Kernel, bounded execution fencing and native recovery,
  and fail-closed stale Hosted wakeups; external Provider/Host certification
  remains product-specific.
- Remove an extra SQLite Snapshot State copy after caller-owned SQL Scan; no
  SQLite Schema version increase, historical proof deletion, or GC.
- See [beta.23 release-candidate notes](docs/releases/v0.1.0-beta.23.md).

## 0.1.0-beta.22

- Fix a loss-of-wakeup race in the external-result and child-terminal wait continuation: after an unrelated Run revision changes between dispatch and resume, retry the current generation instead of falsely ACKing an unresolved deadline or result-ready delivery.
- Preserve persisted wait generations and idempotency; after eight CAS retries, NACK to the existing durable queue for bounded redelivery. Do not change scheduled and non-generation continuation behavior.
- Add deterministic race regression coverage for deadlines, ready receipts, conflict exhaustion, stale/terminal generations and real in-memory queue NACK/reclaim/ACK.

Upgrade all nine Go modules and the GitHub TypeScript client archive together. No wire, SQL or persisted-state migration change from beta.21; typed waits can upgrade forward, but older cohorts cannot resume them. See [Beta.22 upgrade notes](docs/releases/v0.1.0-beta.22.md).

## 0.1.0-beta.21

- Restore feature-neutral HTTP core projection by moving Agent wait interpretation to `agenthttp.PendingTool` and the Agent start response. Add optional explicit `Dependencies.PendingToolProjector`, called after object authorization, and a neutral `PendingToolResponse` transport DTO.
- Keep HTTP v1 wait fixtures unchanged and lock the optional projector in the clean Go consumer. Add a core import-boundary regression and authorized/denied real HTTP projection tests.

Adopt this cohort for P1 external waits. Upgrade all nine Go modules and the TypeScript archive together; bind `PendingToolProjector: agenthttp.PendingTool` for the shared Run endpoint. No persisted-state or SQL migration change from beta.20. Published beta.20 tags remain immutable. See [Beta.21 upgrade notes](docs/releases/v0.1.0-beta.21.md).

## 0.1.0-beta.20

- Add durable `external_result` and `child_terminal` Tool waits, typed wait queries and identity-checked, idempotent receipt-ready notifications. Normal waiting no longer resubmits the Tool or advances model/Tool usage.
- Persist a wait generation in the transition outbox and continuation payload. Deadline/readiness wakeups survive unrelated Run revisions and reject obsolete waits; committed waits and signals recover after instance restart.
- Suspend native local Agent delegation and feature capability parents until their child terminates. Keep scheduled execution and custom/remote delegation behavior; avoid unchanged Team revisions and reconcile early child-terminal delivery.
- Add optional bounded `pendingTool` HTTP/TypeScript projection and `toolWaitPhase`. Internal operation IDs, input digests and signal caches remain private. Extend reviewed wire fixtures and clean Go/packed TypeScript consumer contracts.
- Verify durable waits on real PostgreSQL 16 and Redis 8, alongside duplicate notifications, cancellation/deadline races and parent/child recovery.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must persist receipts plus a retryable notification outbox, gate claimability on `QueryPendingTool`, and preserve `ToolWaitResumer` when wrapping the Agent. No SQL migration or server execution sandbox is introduced. Once typed waits exist, older cohorts cannot resume their persisted state safely; use a forward upgrade. See [Beta.20 upgrade notes](docs/releases/v0.1.0-beta.20.md).

## 0.1.0-beta.19

- Archive removed transcript messages across ordered, lossless Context artifacts, each within the existing 4 MiB durable ceiling. Replace `PortableCompaction.Artifact` with `Artifacts`; retained entries keep their source and turn identities for attachment replay.
- Distinguish the preferred recent-turn tail from a frozen `MinimumRecentTurns` floor. Hosts can relieve hard input pressure while preserving the complete current turn and any explicitly larger minimum.
- Add bounded literal `query` lookup and Unicode `totalRunes` to `harness.read_context_artifact`, allowing retrieval of fields deep in large archives without scanning every page.
- Include exact artifact references from the caller's durable Turn checkpoint in correctable missing-ID results. Cross-scope access, missing ownership and checkpoint/store failures remain denied.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must consume `Artifacts`, persist every part before committing the rollover and reassess the compacted request before sampling. No SQL migration or per-artifact ceiling increase is required. See [Beta.19 upgrade notes](docs/releases/v0.1.0-beta.19.md).

## 0.1.0-beta.18

- Return a correctable `context_artifact.not_found` Tool result for a missing artifact ID after durable caller ownership has been verified. Cross-scope reads and ownership/store failures remain denied.
- Expose typed `ArtifactPayloadLimitError` size facts and `ErrArtifactPayloadTooLarge`, retaining `errors.Is(ErrInvalidInput)` compatibility and the 4 MiB archive ceiling.
- Register bounded `context.artifact_payload_too_large` and `context.input_budget_exceeded` terminal codes with static HTTP recovery messages, preserving the P2 diagnostic boundary.
- Fall back to conservative accounting when a native counter returns zero for a complete non-empty sampling request.
- Include the post-Beta.17 Windows release-command security fix and accepted Node toolchain dependency updates from main.

Upgrade all nine Go modules and the GitHub TypeScript archive together. Hosts must classify typed Context errors and enable their existing native tokenizer for pure text as well as attachments. No persistence schema, HTTP shape, automatic retry policy or archive ceiling changes. See [Beta.18 upgrade notes](docs/releases/v0.1.0-beta.18.md).
## 0.1.0-beta.17

- Add opt-in provider-neutral typed model failure codes, preserving bounded durable Agent terminal reasons without changing physical retry, usage accounting or persisted schema.
- Restrict ordinary owner Run/Workbench/Harness/Invocation/Subtask/feed/journal HTTP diagnostic projections to bounded public codes and safe details. Internal durable error details remain unchanged for separately authorized diagnostics.
- Change the reviewed `cancel.json` HTTP fixture from the user-supplied cancellation reason to the invariant safe summary; the response shape and owner authorization are unchanged. This is a prerelease public-message behavior change requiring a coordinated Beta cohort.
- Preserve legitimate business `error`/`failure` content inside user-owned input/results, project Workflow failure status and bounded code without raw details, and rehash Workbench, Sections and Topology to the visible sanitized payload.
- Bound Workflow Trace error codes and nested SDK diagnostic-only request/header fields without mutating original persisted execution facts.

Upgrade note: upgrade all nine Go modules and the GitHub TypeScript archive together. Safe Run/Harness/Workbench/Feed error text is now intentional public behavior; do not parse raw `errorDetail` to recover provider causes. Hosts supply only trusted classified model codes; no new SQL schema or automatic retry is introduced. See [Beta.17 upgrade notes](docs/releases/v0.1.0-beta.17.md).

## 0.1.0-beta.16

- Clarify that an automatic execution segment is a renewable soft allowance, distinct from immutable hard call limits and the model Context Window; only an actual Runtime pause or denial warrants a budget-stop claim.
- Add the stable `workflow.EffectRequest.OwnerNodeID` so hosts can register child ownership before synchronous startup resolves parent Harness policy, including retry and fan-out owners.
- Honor `harness.WithoutContextWindow` during context hydration so isolated child tasks remain isolated across start, approval and recovery.
- Use the Windows null device for Collector readiness probes and document the POSIX shell needed by the release Makefile.

Upgrade note: upgrade all nine Go modules and the TypeScript archive together. Hosts starting Workflow children must register the supplied owner before starting the child. No HTTP contract or SQL migration changes. See [Beta.16 upgrade notes](docs/releases/v0.1.0-beta.16.md).

## 0.1.0-beta.15

- Add optional, frozen-per-direct-Agent Run automatic model/Tool execution segment policy while retaining the immutable hard ceiling and Beta.14 manual continuation.
- Persist bounded automatic renewal with Kernel CAS and durable Continuation wakeup; hosted worker yields so tasks can finish across requests without replaying completed model or Tool receipts.
- Fall back to owner-authorized manual pause when renewals or conservative no-progress thresholds are exhausted; preserve approval, cancellation, Context Window and existing shared-budget ownership.
- Cover worker restart, pending Tool batches, SQL reload, Harness lifecycle, false-stall fingerprint cases and real PostgreSQL/Redis and OTel integration gates.

Upgrade note: upgrade all nine Go modules and the TypeScript archive together. No host automatically enables this Go-only opt-in; no new public HTTP endpoint or OpenAPI shape. Do not run mixed Beta.14/Beta.15 workers against newly persisted P2 Runs. See [Beta.15 upgrade notes](docs/releases/v0.1.0-beta.15.md).

## 0.1.0-beta.14

- Add opt-in, durable, nonterminal `paused_budget` and explicit cumulative model/Tool call allowances for direct Agent Runs; maintain immutable deployment hard ceilings and existing default limits.
- Resume the same Agent Run after a bounded, revision-guarded grant without re-calling a completed model step or replaying already completed Tool calls. Keep paused Handoff/Workflow child work pending and gate Continuation wakeups.
- Expose Turn-owner-authorized `POST /harness/turns/{turnID}/allowance` with public Turn revision and strict payload; update TypeScript client, OpenAPI statuses/stream, and adapter/race/schema regression coverage.
- Align release toolchain to patched Go 1.27.2 and refresh vulnerable x/net HTTP/2 dependencies to v0.60.0.

Upgrade note: update all nine Go modules and the TypeScript client archive together; hosts must explicitly opt into an initial allowance and choose whether to expose an authorized Continue control. Existing terminal failures remain terminal; no mixed-version workers, automatic renewal or unbounded execution is supported. See [beta.14 upgrade notes](docs/releases/v0.1.0-beta.14.md).

## 0.1.0-beta.13

- Add SDK-owned, bounded Tool Search for authorized local/MCP tools: frozen definition fingerprints, deterministic lexical lookup, persisted loaded sets/search receipts and runtime-controlled Tool execution.
- Add explicit initially visible (not mandatory) Tool keys and context-read forwarding for delegated subagents; large Tool collections no longer hide already-granted Harness controls.
- Add versioned Hosted Tool grants scoped to model-pinned child Roles, without granting Hosted access to the parent or siblings.
- Reject stale/disabled or changed local/Hosted Tools before Tool execution, durable model retry and physical Provider dispatch; validate reserved SDK control Tool names and canonical Tool ordering.
- Restore official Go SDK protocol negotiation for MCP Streamable HTTP endpoints, including legacy initialization and connection-scoped sessions. Discovery and Tool Registry preserve the negotiated protocol version; host endpoint validation, authentication, timeouts and cancellation remain in effect.

Upgrade note: upgrade all nine Go modules and the TypeScript client archive together. SDK Tool Search and scoped Hosted grants are opt-in through explicit host composition/Role authorization; no mixed-version persisted Run or Worker compatibility is established. See [beta.13 upgrade notes](docs/releases/v0.1.0-beta.13.md).

## 0.1.0-beta.12

- Add bounded model/Tool/selector execution with deployment-owned timeout, retry/backoff, concurrency, deadline handling, and panic containment.
- Add Koanf host bootstrap assembly for component selection, secret references, Agent limits, and external-call execution policy.
- Make Agent request/Role limits subordinate to Runtime hard ceilings and add Harness-owned default topology/delegation policy for new Turns.
- Preserve structured shared-budget exhaustion dimensions, including stable Agent stop codes and recoverable child-run delegation exhaustion without phantom child topology.

Upgrade note: per-Run limits can no longer widen Runner hard ceilings. Harness hosts may configure `Dependencies.Execution`; zero-config shared Budget middleware freezes 32 descendant Runs, 8 active execution slots, and delegation depth 4 for new Turns while leaving shared model/Tool/token ceilings unset. Existing persisted Runs/Turns are not rewritten. See [beta.12 upgrade notes](docs/releases/v0.1.0-beta.12.md).

## 0.1.0-beta.11

- Add the supported `go/agent-runtime-otel` module for content-safe OpenTelemetry spans and low-cardinality runtime metrics while leaving SDK, exporter, sampling, resource, and shutdown ownership with the host.
- Add repository coverage and vulnerability gates to CI and Beta release verification; refresh TypeScript quality dependencies to patched versions.
- Remove the PlanExecute runtime feature, HTTP routes, Harness capability surface, topology projection, and TypeScript client API; planning is no longer a first-party Runtime kind.
- Add HTTP edge-guard coverage for Agent, Team, and Harness handlers and document the architecture, observability, and production deployment boundaries.

Upgrade note: update all nine Go modules and the TypeScript archive together. The new OpenTelemetry module is optional and does not change Kernel persistence or HTTP v1. Hosts that enable it must supply their own OpenTelemetry providers and exporter lifecycle. See [beta.11 upgrade notes](docs/releases/v0.1.0-beta.11.md).

## 0.1.0-beta.10

- Add durable Group Chat directed, selector, and visible-speaker handoff routing, including frozen A2A speaker bindings and remote wait resumption.
- Unify routed role delegation policy and add manual subtask spawning under the frozen role, depth, and shared-budget boundaries.
- Redact hosted-tool stream feed bodies to lifecycle identity/status metadata.
- Preserve top-level capability startup failure causes in failed Turn/Invocation state across reload; include the tool key when hosted-tool resolution fails.

- Add explicit Go consumer signatures for all eight modules, host Store/model/tool/worker ports, and a standalone Agent/HTTP composition check.
- Share reviewed JSON fixtures between Go HTTP serialization, OpenAPI validation and the packed TypeScript consumer; check cancellation, errors and terminal SSE consumption through the published ESM entrypoint.
- Document the existing Checkpoint and Result wire fields. Correct Run and Workbench kind schemas and TypeScript `RuntimeKind` to accept feature-owned strings, including A2A shadow runs and host extensions.
- Correct PostgreSQL construction examples and document version alignment, migration ownership, CAS/retry handling and contract review requirements.
- Verify current-version PostgreSQL migrations can be reapplied without losing the populated Kernel aggregate/journal/outbox or Harness Context checkpoint ownership.

Upgrade note: update all modules and the TypeScript archive together. See
[beta.10 upgrade notes](docs/releases/v0.1.0-beta.10.md) for Group Chat composition,
new persisted execution data, feed redaction, and error behavior. No new SQL
migration or change to existing Go constructor signatures is introduced.
Consumers that assumed `RuntimeKind` was a closed four-value union must handle
other feature kinds already accepted by Kernel. Checkpoint/Result schemas now
describe their existing emitted fields. Beta version alignment and forward-only
migration policy remain in force; this does not establish cross-release or mixed-worker compatibility.

## 0.1.0-beta.9

- Project nested workflow waits through the owning Harness invocation, preserving the single active interaction contract.
- Resume the exact child wait and frozen execution; replaying a previous response cannot consume a later wait.
- Record each parallel/map effect child separately so cancellation and continuation retain all descendants.
- Preserve historical relation identities and distinguish workflow waits from tool approvals in subtask projections.

## [0.1.0-beta.8] - 2026-09-05

### Added

- Harness-owned per-Turn shared budgets with persistent memory/PostgreSQL CAS ledgers, immutable ancestry, token/call/child/concurrency admission and idempotent settlement.
- Frozen role-based local delegation and authoritative budget/subtask snapshots and Turn Feed Items, including child approval and cancellation APIs in Go, HTTP and TypeScript.

### Fixed

- Planner budget waits resume through continuation. Unknown dispatched requests retain reservations without automatic duplicate dispatch or refund.
- Queued child cancellation fences registration and execution across recovery.

### Upgrade notes

- Migrate Harness PostgreSQL models and compose the shared budget middleware at universal execution boundaries. Existing Turns without shared budgets keep their original accounting.
- Update all Go modules and the TypeScript GitHub Release archive to `v0.1.0-beta.8`. See [release notes](docs/releases/v0.1.0-beta.8.md) for metering and reconciliation requirements.

## [0.1.0-beta.7] - 2026-09-05

### Added

- Hosts can supply `harness.Dependencies.Cancellation` for feature-owned cleanup. The A2A plugin now implements `Cancel` using the shadow Run's frozen remote binding.

### Fixed

- Asynchronous delegations keep the parent Tool call pending until the child reaches a terminal state, avoiding conflicting lifecycle Items and premature completion.
- Pending polls and runner reconstruction reuse the same child identity and policy-prepared input without repeating delegation policy checks or consuming completed Tool-call budget.
- Parent cancellation records the child's actual terminal outcome, including a result that arrived before cancellation.
- Remote cancellation validates the caller's revision, preserves the frozen target configuration, and leaves an unconfirmed or failed cancellation retryable.

### Upgrade notes

- Update all Go modules together to `v0.1.0-beta.7` and use the matching TypeScript GitHub Release archive. No HTTP v1 or database schema migration is required.
- Hosts must connect feature-aware Harness and HTTP cancellation routing to the A2A plugin. The default Harness cancellation remains the local Kernel implementation.
- Hosts using background continuation must provide the Scheduler as the Worker's `Projector` so committed wakeups can resume pending Tool calls. Retry a failed or still-pending remote cancellation by submitting another cancellation request.

## [0.1.0-beta.6] - 2026-09-01

### Added

- Durable continuation projection from committed wakeups, including a generic `run_ready` fallback for feature-owned autonomous progress.
- Durable model and planner invocation receipts with stable invocation identity, execution claims/leases, response IDs, and provider usage accounting.
- Feature-owned budget vocabulary and an observability recorder seam that keep provider and telemetry SDKs outside the Kernel.

### Changed

- Kernel transition outbox storage is owned by the Store; snapshot loading and event-journal reads are separate contracts.
- PlanExecute `Planner.GeneratePlan` now returns `PlannerResponse`, and `PlannerRequest` carries the stable `InvocationID`.
- HTTP shared composition requires explicit Run loading and authorization dependencies for object-level access checks.
- Agent budget state is projected through `View.Budget`; Run snapshots expose `eventHead` while events remain on the journal endpoint.

### Fixed

- Autonomous Agent, Workflow, PlanExecute, and Team progress now has durable recovery sources across crash windows.
- Compose startup rollback uses a detached bounded cleanup context and poisons the application when rollback cannot prove a clean retry state.
- PostgreSQL transition outbox persistence retains wakeup metadata and real PostgreSQL integration evidence covers large event histories.

### Upgrade notes

- This is a Beta hard cut: update all Go modules together to `v0.1.0-beta.6` and use the matching TypeScript GitHub Release archive. No compatibility bridge is provided for the replaced Planner, Shared HTTP, Workbench, transition-outbox, Agent budget, or snapshot/event contracts.
- Hosts should carry Runtime `InvocationID` independently from host request identity and use it only with provider idempotency/retrieval mechanisms whose semantics are known.
- Prereleases are not published to the npm registry. `npm pack` is used only to build the GitHub Release `.tgz` artifact; no npm publication token is required.

## [0.1.0-beta.5] - 2026-08-31

### Fixed

- The TypeScript package guide now uses the supported `agent.start`,
  `runs.feed`, and `snapshot.run.id` APIs instead of retired generic run and
  event-stream examples.

### Verification

- The clean-consumer release gate compiles every fenced TypeScript example in
  the packaged README, preventing stale public examples from passing a future
  release.

### Upgrade notes

- No runtime API, HTTP v1 contract, or persisted schema changed. Update the Go
  modules together to `v0.1.0-beta.5` and use the matching TypeScript archive.

## [0.1.0-beta.4] - 2026-08-27

### Added

- Hosts can project durable Workflow waits into application-owned Harness
  interactions with `WorkflowWaitInteractionProjector`.
- `WithoutContextWindow` lets explicitly prepared tasks omit the active
  conversation checkpoint while preserving cancellation, tracing, and other
  host context values.

### Fixed

- Accepted interaction responses now resolve the exact waiting Workflow run
  and return its durable result instead of leaving the turn waiting for input.
- Recovery can project an interaction after both the turn and invocation have
  already entered the waiting state; memory and PostgreSQL stores enforce the
  same matching-owner state checks.
- Concurrent replays of an accepted Workflow start refresh the durable run
  after an optimistic-concurrency conflict instead of reporting a false failure.

### Upgrade notes

- Beta TypeScript packages are now distributed as GitHub Release archives;
  npm registry publication is reserved for stable releases.
- Source change: custom implementations of `harness.WorkflowFeature` must
  implement `ResolveWait(context.Context, string, uint64, json.RawMessage)`.
  The SDK Workflow runner already implements this method.
- Hosts opting into wait projection implement `WorkflowWaitInteractionProjector`
  on their existing interaction handler; projection must be deterministic for
  the same immutable wait.
- No HTTP v1 contract or persisted-record schema migration is introduced by
  this release. Update the Go modules together to `v0.1.0-beta.4`.

## [0.1.0-beta.3] - 2026-08-25

### Added

- Immutable, scoped Dynamic Workflow Definition revisions with content hashes,
  activation CAS, typed validation, and durable registry adapters.
- Deterministic workflow execution for conditions, parallel branches,
  subworkflows, interactions, retries, budgets, compensation, and effect
  receipts.
- HTTP, Go, and TypeScript contracts for definition lifecycle, exact revision
  execution, cancellation, and trace inspection.

### Changed

- Harness can resolve and execute an exact Definition revision while retaining
  immutable definition identity in checkpoints and traces.
- Workflow effects fail closed through typed routers instead of evaluating
  arbitrary expressions or mutating active definitions.

### Verification

- Go and TypeScript unit, integration, clean-consumer, persistence, and release
  gates cover restart recovery, stale CAS rejection, idempotent retry, budget
  exhaustion, compensation, and trace replay.

## [0.1.0-beta.2] - 2026-08-24

### Added

- Explicit `protocol.a2a` microkernel plugin and finite `a2a:<public-id>`
  handoff routing without adding protocol dependencies to the kernel.
- A2A 1.0 HTTP+JSON client and server surfaces for Agent Card discovery,
  messages, streaming, artifacts, task get/list/cancel, and task subscription.
- Durable shadow runs with frozen discovery revisions, resumable
  input/auth-required waits, cancellation, and recovery after restart.
- Host-neutral authentication, tenant-scoped optimistic task persistence,
  Agent Card cache validators, and a pinned official A2A TCK runner.

### Changed

- A2A moves from experimental to Beta-supported for the documented HTTP+JSON
  surface. JSON-RPC, gRPC, push notifications, and extended cards remain out of
  scope and fail closed.
- Handoff accepts a narrow product-owned child resolver through
  `handoff.NewRouted`; the existing static `handoff.New` path is unchanged.

### Security

- Production A2A hosting now requires HTTPS, explicit authentication, durable
  owner/tenant-scoped storage, and declared Agent Card security requirements.
- Endpoint validation, same-origin redirect policy, reserved-header protection,
  bounded remote data, and generic persisted transport errors are enforced at
  the edge.

### Verification

- Official A2A TCK commit `5996b79f9cefa6fc390980e383e358a66fb9e49e`
  reports 100% compatibility for the selected HTTP+JSON surface (85 passed,
  180 intentional scope skips, zero failures).

## [0.1.0-beta.1] - 2026-08-24

### Added

- Durable Kernel run state machine with optimistic concurrency.
- Agent, tool, approval, continuation, workflow, team, and evaluation features.
- Harness turn orchestration with Context V2 checkpoints, compaction, durable
  artifacts, interactions, delegation, and recovery.
- PostgreSQL, Redis, HTTP v1, MCP, A2A, and TypeScript client adapters.
- Real PostgreSQL and Redis integration gates plus clean-consumer release tests.

### Beta notes

- A2A integration is experimental.
- Persisted schemas are forward-migrated; downgrade migrations are not
  provided during Beta.
- Source compatibility may change between Beta releases and will be recorded
  in this changelog.
