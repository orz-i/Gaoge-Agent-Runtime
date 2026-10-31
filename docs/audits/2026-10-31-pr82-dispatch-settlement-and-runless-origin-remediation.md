# PR #82 — Physical Dispatch settlement and runless owned Item provenance

- **Date:** 2026-10-31
- **Independently audited and reproduced Head:** `8c62bf461259dd1e5affd158d561a3dd558387ca`
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Scope:** Two independent P1 classes, three direct Red reproductions.
- **Decision:** [ADR 0015 §§23–24](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md).

## Red: physical Grant could be settled by unrelated or contradictory facts

Independent detached-worktree tests against the reviewed Head reproduced:

- `/tmp/pr82-reaudit-cross-owner-receipt-20261031.log`: another Invocation/Run's Diagnostic Completion, with only a matching ParentItemID, made a still-unsettled Agent physical grant appear closed. Memory and SQL both admitted Failed projection and Retry to Attempt 2.
- `/tmp/pr82-reaudit-contradictory-receipts-20261031.log`: an effectful Team grant with both `physical_dispatch_returned` and `physical_dispatch_rejected` completions (distinct IDs) was accepted by both Stores; the later no-effect proof authorized unsafe composite Retry.

**Correction:** `VerifiedExecutionDispatchReceipt` is the sole shared settlement predicate: exactly one recognized Diagnostic Completion, matching TurnID, InvocationID, RunID and Grant ParentItemID, with a strictly later persisted Item sequence. Missing, foreign, duplicate, contradictory, malformed or unsupported facts are not settlement evidence. `PendingExecutionDispatch`, SQL `pendingExecutionDispatchTx`, composite Retry, Accepted child Retry, remote Handoff no-effect cancellation and interrupted native recovery all use the same predicate.

The existing Store Turn mutex / SQL Turn row lock now arbitrates the **first** Completion with matching identity and recognized boundary; any conflicting or second Completion referencing an existing dispatch grant is rejected at AppendItem, including cross-Turn claims. Existing malformed historic facts also remain Fail-Closed on read. No new Store interface, SQL schema, wire protocol or inferred cleanup receipt.

## Red: old runless Tool Item could authorize a new Attempt

Independent detached-worktree regression `/tmp/pr82-reaudit-stale-runless-tool-20261031.log` reused an immutable Tool Item with `InvocationID` from Root Attempt 1 but no physical `RunID`. After Root Retry to Attempt 2, `StartTeamInvocation` returned nil and executed the Team Feature once. The original parent provenance guard checked only nonempty Run IDs.

**Correction:** `requireItemOwnerAttempt` fails closed when an Item claims `InvocationID` ownership but has no `RunID` to prove its originating Attempt. Items carrying neither InvocationID nor RunID remain unambiguously Host-owned. Delegation Items continue using frozen `ParentRunID` in their payload. Tests that previously manufactured owner-stamped, Run-less Items now retain their original Run identities; no invented provenance or compatibility backfill.

## Test coverage and delivery

- `TestPR82RunlessOwnedToolCannotAdmitNewChildAfterRootRetry` executes the full public Team child admission path with the original Red Tool Item.
- `TestPR82PhysicalDispatchSettlementRequiresUniqueOwnerBoundReceipt` covers good no-effect/effectful receipts plus foreign owner/run/Turn, malformed, pre-grant, duplicated and contradictory outcomes.
- `TestPR82DispatchGrantRejectsForeignAndContradictorySettlementsAcrossStores` checks the actual Store append, pending grant, Failed CAS and Retry fences in Memory, SQL/SQLite and configured real PostgreSQL.
- Previous Handoff, native/remote dispatch and RunRelation descendant hard-cut tests continue to run. Full local `make check`, Docker `make integration`, pushed Head GitHub CI and independent final merge audit are separate gates.
- Existing two unrelated untracked OpenAI Responses probe scripts are not modified or added to commits.

**Behavioral hard cut:** host code providing incomplete physical receipt identities or trying to use an Invocation-owned Runless Item as parent authority must produce correct first-party provenance, or drain/quarantine the incomplete record. Unknown effects are never retried or declared cancelled. No merge or tag is authorized by this remediation document.
