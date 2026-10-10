# P2 Runtime-Owned Adaptive Execution — Implementation and Merge Gates

> Date: 2026-10-10; source [ADR 0006](./adr/0006-runtime-owned-auto-allowance-segments.md), P1 SDK `v0.1.0-beta.14@73197d3`. Current branch: `feat/durable-auto-allowance-p2`. **Do not merge/push/tag or update Gaoge SDK pin automatically.**

## Scope and commits

| Slice | Owner / change | Gate |
| --- | --- | --- |
| P2.0 Docs | ADR, policy fields, hard safety/no-progress definition, negative cases, rollout and ownership | Docs-first commit |
| P2.1 Agent state | Optional frozen auto policy + renewal ledger in Agent Run state; refuse invalid policy; bounded CAS renewal before model/Tool dispatch, durable wakeup, P1 manual fallback | Deterministic multi-segment model/Tool tests and no replay |
| P2.2 Stall + guidance | Fingerprint *settled* logical outcomes; stop on repeated unchanged work; soft request-only progress hint in canonical model request when close to segment exhaustion | Repeat vs changed-result tests, unknown-result/approval/deadline regression |
| P2.3 Harness propagation | Host-composed direct Agent-only auto policy through Harness StartRequest and restore paths; no public HTTP or TypeScript endpoint to edit Runtime policy | Harness Start/resume/context/turn/ledger tests, no UI budget editor |
| P2.4 Consolidation | Memory/SQL durable state, CAS/restart/race, SDK full check, review of OpenAPI/public contract, release preparation plan | Full `make check`, GO/NO-GO audit |
| P2.5 Gaoge adoption (separate gate) | Docs and independent host branch, bounded deployment enablement **after a new published SDK release**; no untagged gitlink or protected-branch bypass | Real released tag, host tests, Git push only after final approval |

## Invariants to prove

- `model_calls` and `tool_calls` auto grants never exceed immutable per-Run hard ceiling; optional policy disabled restores exact P1 behavior.
- P2 transition uses the existing Kernel CAS and Continuation wakeup; every tool/model receipt and its actual count remain charged exactly once.
- No auto-renew on failed, cancelled, waiting_input, unknown external result, provider retry failure, deadline or Context Window hard error.
- Stall hash is internal to Run state and cannot be used to skip runtime permissions or trust model text.
- Context Window rollover is exclusively Harness-owned; no second migration/compatibility bridge.
- Team, GroupChat, Workflow and child usage remain unchanged absent an explicit future policy owner.

## Release decision

P2 SDK must finish merge-blocker audit, then independently publish a coordinated Beta after full CI. Before that, host `master` remains on published beta.14 with P1 explicit Continue, and P2 candidate code stays in worktree. Always disclose whether tests are fake/local vs real Provider/SQL.
