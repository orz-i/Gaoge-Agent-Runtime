# ADR 0018 — SubAgent-only multi-agent orchestration hard cut

- Status: Accepted for the unreleased beta.24 candidate
- Date: 2026-11-14
- Compatibility: **BREAKING CHANGE**, no adapters or legacy replay

## Decision

The Runtime contains **one** first-party model/Tool execution loop (`Agent`)
and exactly one multi-agent composition mechanism: explicit delegated
SubAgents, using `handoff.Coordinator`, a fixed `ChildRunner`, durable
Delegation receipts, optional Join, RunRelation and Harness Subtask controls.
Host-selected Application callbacks are external business effects, not a new
runtime-native orchestration language.

Delete all Team, GroupChat and Dynamic Workflow implementations: Go packages,
Harness dependencies, execution classes, root/child starts, retries, resumers,
interaction projectors, visible speaker handoff Tools, composite cancellation
routing, topology providers, continuation registrations, HTTP handlers,
TypeScript clients and DTOs, Workflow Definition Store and public contracts.
There is **no** alias to SubAgent, disabled feature flag, compatibility shim,
legacy deserializer or automatic migration of persisted composite Runs.

Remove `team_member`, `group_chat_speaker` and `workflow_effect` from valid
RunRelation kinds. First-party physical child grants and settlement proofs now
admit only Agent execution (including authenticated Agent child Runs). The
Kernel's generic custom Run type is **not** a way to execute the deleted
interpreters.

## Non-negotiable preserved invariants

1. Root/Child immutable identity and parent Attempt are checked before any
   physical callback, receipt or result projection.
2. Cancellation, retry and recovery serialize against physical Start/Resume
   grants and must not bypass remaining SubAgent/Delegation ownership fences.
3. A remote ChildRunner is trusted only through its original route, not through
   a coincidentally matching local Run ID. Unknown external effects remain
   quarantined until read-only reconciliation.
4. Agent model/Tool persistence, Context checkpoints, approvals, A2A remote
   shadows and optional Host Application callbacks are not silently rewritten
   or erased by this orchestrator deletion.
5. All public clients and current documentation describe only supported
   operations. Historical audit/ADR material is evidence of past behavior,
   not support for the removed implementations.

## Upgrade and data retirement

- Do not deploy the candidate while older Team, GroupChat or Workflow workers
  remain active. Identify all persisted in-flight composite Runs, queues,
  external-effect receipts and original child relations first.
- There is no guaranteed conversion of a composite Run to SubAgent: explicitly
  cancel/drain or abandon the old work with original executors, reconcile any
  external effects, and begin *new* Agent/SubAgent Runs with new stable IDs.
- PostgreSQL `agent_workflow_definition_heads` and
  `agent_workflow_definition_revisions` cease to be mapped or migrated. After
  backup and operator approval, execute
  `docs/migrations/retire-obsolete-orchestration.sql`. The normal SDK migration
  intentionally does not destroy user data.
- Old external client binaries must be upgraded together with the Go modules
  and HTTP deployment. An older persisted composite execution cannot be
  resumed by the new SDK. Rollback means restoring matching code and a
  consistent backed-up storage/queue state, not installing a compatibility
  bridge.

## Verification

The merge gate requires Go module tests, Harness delegation/RunRelation/
approval/cancellation tests, the three Store implementations' Agent execution
grant tests, HTTP/OpenAPI contract tests, TypeScript build/typecheck/tests,
external consumer builds, `git diff --check`, and checks that removed APIs are
absent. Real Postgres integration requires a separately available database;
the absence of its result cannot be represented as a pass.
