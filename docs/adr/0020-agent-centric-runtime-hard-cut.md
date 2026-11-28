# ADR 0020: Agent-centric durable execution hard cut

- Status: Accepted for implementation; candidate not release-qualified
- Initiative: GAR-I-1 (GAR-14–25)
- Source baseline: `7c7e498d3f6e0c29edec2c0be523ea5ba23d8214`

## Decision

Host selects and authorizes; Runtime executes and recovers. One Agent definition
(model, instructions, tools, skills, limits and policies), one Agent loop, one
Run per execution identity, and one durable operation/effect protocol. Root and
named local children use the same loop; parentage constrains authorization and
usage. A Session is the Host's durable conversation/command/projection boundary,
not an independent execution authority. A remote Agent is an external effect;
its remote authorization and receipt remain independent.

Prepare → freeze intent → admit under the current execution fence → execute →
record original outcome → consume and advance the Run. Context, summary, inputs,
approval and usage are fixed lifecycle stages/policies, not selectable alternate
execution engines. Summary sampling and ordinary sampling use the same effect
contract. Record stable operation ID, immutable input/authorization, original
outcome or pending/unknown status and consumption/usage in the existing
transactional store; keep only necessary current execution state hot. Immutable
conversation history and committed events are separate read surfaces. Reuse the
existing reducer and transaction boundary; no generic workflow interpreter,
event-sourced reconstruction requirement, second ledger or compatibility facade.

Store implementations share one internal transactional contract and invariants:
identity/parent ownership, single valid execution fence, atomic Run/effect/usage
commit, exact outcome binding and idempotent consumption. CAS, epoch, lease and
receipts are implementation details supporting these invariants, not separate
business protocols. Post-commit projections and worker queue ACKs cannot grant
execution. Pure calculations may be repeated. Replay-safe operations may retry;
externally idempotent operations reuse the original ID under their actual
contract. Unknown non-replayable effects (including hosted effects/usage) need
original authoritative evidence; lease expiry alone cannot authorize redispatch,
success, cancellation settlement or quota release. The rejected-hosted-receipt
fix in #159 is an explicit invariant. Parent cancellation must not retire an
unsettled child or unknown effect. Approval decisions consume the original
checkpoint once. No weakening of scope, immutable authorization or receipt
identity to achieve deletion targets.

## Hard-cut inventory and implementation ownership

The old names below identify removal targets and migration sites at the frozen
baseline, not guarantees that every matching file is deleted wholesale. Keep
necessary behavior in the destination owner. Check public symbols, production
callers, schema and adapters together; no equivalent replacement switch.

| Card / owner | Delete or consolidate (baseline anchors) | Destination / retained behavior |
| --- | --- | --- |
| AC-01 / Agent extension surface | `plugin.RunMiddleware`, `ModelMiddleware`, `ToolMiddleware`, `RunNext`, `ModelNext`, `ToolNext`, `RunChain`, `ModelChain`, `ToolChain`, `nextLifetime`; `agent.Dependencies` middleware fields, `first_party_stages.go`, Harness `context_run_middleware.go`, `timeline.go`, adapter chain registration and `ManagedRecovery`/`SummaryNativeRecovery` selection | Fixed Agent stages, pure `ModelRequestPreparer`, approval policies/handler, observers, model and tool adapters. Migrate callers and tests in the same change. AC-02 owns the underlying persistence contract. |
| AC-02 / Kernel and Memory | `ExecutionStore`, `OwnedPhysicalExecutionStore`, `OwnedRetirementStore`, `ChildExecutionStore`, `ManagedRecoveryStore`, `ManagedRecoveryHostStore`, `ControlStore` as separately selected authority ports; `OwnedPhysicalAdmission`, `ChildExecutionGrant`/`Settlement`, `OwnedRetirementReceipt`, `ManagedTransfer`, `ManagedRecoveryAuthorization`, `ManagedClosureProof` and their overlapping guards | One internal Run/effect transaction with stable ID, pending/recorded/consumed facts, usage and fencing; shared reducer, Memory reference implementation. Preserve original parent/child relations, exact receipts, creation/cancel race and unknown-ACK recovery. |
| AC-03 / SQL | `agent-runtime-{sqlite,postgres}` old `managed_recovery`, `owned_physical`, `child_execution`, retirement and grant tables/validators; necessary Harness backend contract changes coordinated with AC-06 | Same AC-02 transaction/reducer, native DB lock/clock and unique IDs. New profile/schema; explicitly reject unsupported old in-flight records, do not reinterpret/delete real data. |
| AC-04 / Agent loop, context, model and usage | `agent.go`, `model_invocation.go`, `sampling_plan.go`, `model_candidate.go`, `context_summary*`, `bounded_context*`, `run_inputs.go`, `first_party_stages.go`: duplicate preparation/managed branches, summary-specific physical admission, full-history hot writes | One preparation/generation/consumption loop, immutable request & original receipt, stable input cursor, fixed compression/limits, usage and #159 quota semantics. Preserve archival/source identity. |
| AC-05 / Tool, Agent-as-Tool and controls | `tool_admission`, `tool_outcome_receipt`, `tool_reconciliation`, `external_wait`, `child_batch`, `remote_execution`, `handoff/`, control/allowance branches | Common operation result/wait/approval/cancel/resume contract with explicit parent ownership and remote adapter; preserve no-effect proof and externally unknown outcome. |
| AC-06 / Session and edge | Harness `agent_session`, `runner`, `store`, `session_client`, `agent_invocation`, `execution_dispatch`, `cancellation*`, `managed_recovery*`, `terminal_*`, `interaction*`; `SettleUncreatedRoot`, `RetryInvocation`, authority-only `AppendItem`/`ListItemChildren` use; Harness SQL, HTTP/TS, MCP/A2A/ACP consumers | Session Prompt/Steer/Resume/Cancel/Subscribe and product IDs/read projections; Runtime committed facts own execution and recovery. Retain Host authentication, original selections, transcript and remote-specific semantics. |
| AC-07 / Infrastructure | `continuation/`, `queue/`, `runfeed/`, `runrelation/`, Redis, necessary OTel: duplicate phase jobs/eligibility/ACK proof | Wake a resumable Run, project committed events, durable relation constraints, bounded delivery retry; no execution authority in scheduling. |
| AC-08 / integration | All residual old callers, schema, tests asserting retired mechanisms, obsolete public interfaces and stale docs | Actual hard cut; preserve behavior coverage, integrate consumers; no code relocation to satisfy budget. |

AC-00 precedes AC-01 then AC-02. AC-03 (backend) and AC-04 (loop) may proceed
in parallel only after AC-02 freezes the store contract; AC-05 integrates both.
AC-06 and AC-07 may proceed in parallel after AC-05; they must coordinate shared
event/relationship contract changes, and AC-07 must not edit Harness-owned
files. AC-08 integrates, AC-09 qualifies the fixed candidate, AC-10 independently
reviews, AC-11 closes with authorized merge and verified Initiative acceptance.
Qual is the dependency authority. No parallel execution path while migrating.

Actual consumer: Gaoge-Agent-Server `512d3bf3b71c18bcd83052257dcd1da7d6dcc8b8`,
`internal/runtimehost/host.go:48–102` (Kernel/Postgres store + Harness store,
`OpenTextAgentSession`, `RunExecution`, transcript and limits), `:117–135`
(Prompt/Load and Snapshot projection), and `host_test.go`,
`http_integration_test.go`. AC-06 must migrate this boundary if it changes;
AC-09 pins the candidate and validates it in an isolated consumer worktree.
Do not require a downstream facade just to preserve retired SDK internals.

## Local acceptance (2026-11-28 user decision)

GAR-I-1 uses local verification and Qual strict acceptance instead of waiting
for GitHub Actions. The repository's ordinary PR/main CI, CodeQL, dependency
review and PR-description workflows are manually paused; release/tag workflows
remain active. The existing main branch protection is unchanged; resolving its
remote-check requirement is a separate explicit merge-admission decision.

- AC-01–08: use existing targeted tests, changed-module compilation/static checks
  and actual Memory/SQLite/PostgreSQL contracts when affected; record exact
  candidate SHA, commands, results, failures and net code delta in Qual. Do not
  rerun the full suite at every checkpoint or wait for remote checks.
- AC-09: on one fixed candidate, reuse `make check-static`, `make ci-consumers`,
  `make ci-race`, `make ci-coverage`, `make security`, `make integration` and
  `make integration-otel` locally, plus the existing downstream and Agent-effect
  acceptance. Use isolated real PostgreSQL/Redis and SQLite reopen tests. Record
  platform/tool limitations (including Windows and CodeQL) as unverified, not
  equivalent passes. AC-10/11 independently verify the same SHA and evidence.
- Preserve the 30% reduction target, behavior/safety assertions, strict
  per-criterion evidence, distinct Reviewer, and explicit merge authorization.
  Update still-open Qual acceptance criteria referring to remote CI before
  execution/acceptance; rebind AC-11 to the current Initiative scope and contract.

## Measurable gates

Count tracked `go/**/*.go` production files excluding `*_test.go`, `examples/`
and `conformance/` at both immutable Git trees (including added/deleted files).
Baseline Runtime 172 files / 36,774 lines; Harness 87 / 22,456; total 59,230;
all Go production 455 / 102,581, including independent provider transport.
Required Runtime+Harness ≤41,461 lines (30% reduction; stretch 35,538), with
whole-Go production net reduction too. Record actual source/test/docs delta
per card, exported ports/constructors, persisted state kinds and deleted call
sites. Do not cut safety/behavior to hit a number or shift code to another
module; only explicit user-approved quantified exception can change the gate.

Baseline source inventory (`rg -l` tracked Go excluding tests, examples and
conformance): middleware/Next 20 production files (Runtime 17, Harness 3);
managed/summary recovery 28 (Runtime 14, Harness 8, Harness PG 1, Harness SQLite
1, PG 2, SQLite 2); old execution/physical/retirement/child Store interfaces 23
(Runtime 14, Harness 2, PG 4, SQLite 3); `PhysicalDispatch|ItemDiagnostic` 30
(Runtime 6, Harness 19, Harness PG 4, HTTP 1). These are search baselines, not
independent complexity metrics or disjoint file sets.

Baseline on darwin/arm64 Go 1.27.4: `go test ./go/agent-runtime/plugin
./go/agent-runtime/kernel ./go/agent-runtime/agent ./go/agent-runtime/memory
./go/agent-runtime-harness -run 'Test.*(Middleware|EA1RootAgent|EA1ChildAgent|ManagedRecovery|NativeAuthorityCutover|OwnedPhysical|OwnedRetirement|HostedReceipt)' -count=1`
reported Plugin, Kernel, Agent pass; tool timed out at 60s before Memory/Harness
results. Separate exact focused Memory
`-run '^TestOwnedPhysicalMemory(AdmissionBindingAndTTL|ReceiptOnlyRecovery)$'`
and Harness `-run '^TestAgentSessionManagedRecoveryRequires(ExactHostStore|ExplicitMatchingStores)$'`
both passed with `-count=1 -timeout=10s`. No failure conclusion from the broad
timeout; SQLite reopen, actual PostgreSQL, downstream, full local acceptance, long-history
benchmark and task effectiveness are later AC-03/09 gates. Reuse existing
behavior and failure fixtures; do not multiply test matrices per card.

## Relationship to historical ADRs

This is the new implementation target for GAR-I-1; it supersedes conflicting
mechanisms, not already released behavior or safety constraints. ADR 0001
keeps transactional journal/outbox, exact intent, Host auth and observer seam;
its independent feature-owned state/accounting boundaries consolidate into one
Run/effect/usage protocol. 0002 retains hard limits and parent scope but removes
role/allowance-specific accounting paths. 0008/0009 retain embedded-first and
fencing, replacing separate feature-owned recovery gates and recovery modes.
0011 and 0013 retain immutable model/tool dispatch identity and authoritative
outcome/no-effect evidence, removing their parallel admission/retirement
machinery. 0015 retains indexed parent ownership but replaces distinct root and
child execution protocols. 0017 retains trusted Store and frozen authorization,
removing Harness as a first-party execution authority. Other decisions remain
unless explicitly superseded; independent provider transport (0019) is outside
this hard cut. Existing historical ADRs remain intact. AC-08 will reconcile
current public architecture/API documentation with the actual implemented tree.
