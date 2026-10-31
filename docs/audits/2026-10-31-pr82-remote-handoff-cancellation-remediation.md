# PR #82 — Remote Handoff cancellation proof hard cut

- **Date:** 2026-10-31
- **Original independently audited pushed Head:** `da417375dcc6a35964ad225a3631126829f0a76e`
- **Code correction:** `17d4d5a` (missing Run proof); `22c1471` (canonical ModelOptions); `86bef37` (A2A shadow and legacy-proof hard cut)
- **Pull request:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Decision:** [ADR 0015](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md), §§15–16
- **Status:** Code correction and local acceptance PASS; pushed-Head merge admission remains independent.

## Reproduced P1 — local Kernel absence was falsely treated as remote cancellation

The independent audit verified that PR #82 now creates a durable child Invocation for both Role and MemberID-only Handoff. The child can be **running on an independently routed ChildRunner** even while the Harness-local `kernel.Runtime.Load(childRunID)` returns `kernel.ErrNotFound`. `cancelTurnDescendants` called `cancelRelatedRuns`, then `addMissingInvocationCancellations` created a synthetic `cancelled` snapshot for that still-running child. `syncInvocationRuns` persisted `InvocationCancelled` with a new revision; no ChildRunner cancellation had occurred. `CancelInvocation` independently had the same false no-Run cancellation fallback.

Original Red evidence: `/tmp/pr82-independent-remote-cancel-v2-20261031.log` and `/tmp/pr82-independent-member-cancel-v3-20261031.log` on Mac Pi. The second test reproduced an actual persisted Member-only child transitioning to `cancelled` despite no external cancellation callback.

## Correction

1. The **exact routed Handoff child identity** is detected from the existing immutable hashed `Invocation.Input`, not inferred from `ExecutionClass=agent` or optional `RoleID`.
2. After an authoritative **local Kernel `ErrNotFound`**, cancellation consults the child Invocation by its persisted execution reference. A potential Handoff physical effect is established from existing durable grant Items: any admitted dispatch is potentially external **unless every relevant grant has a first-party, matching owner/Run `physical_dispatch_rejected` no-effect receipt**. A returned `handoff_start_returned` receipt confirms dispatch but does **not** certify cancellation. Pre-hard-cut child records missing the frozen `harnessRoot` proof cannot establish a safe no-effect history and fail closed.
3. For a remote Handoff with a potentially executed physical grant, both root Turn cancellation and direct child cancellation report `ErrHandoffCancellationUnverified` rather than invent a terminal Run. **An A2A shadow Run that does exist locally is still remote:** the default `*kernel.Runtime` canceller cannot perform the A2A network cancellation, so it also returns the same fail-closed error for a live `a2a.remote` shadow. A separately composed host `RuntimeCanceller` remains responsible for externally verified cleanup. The child Invocation/Attempt remains unchanged, no remote Start/Cancel is reissued, and the outstanding cancellation intent remains fenced. Root cancellation may already be authoritative in the local Kernel while descendant cleanup is unresolved; retries must not claim that the remote Run has stopped.
4. For **never-dispatched** or definitively pre-effect-rejected Handoff grants, the existing no-Run cancellation path remains available: no external ChildRunner was entered. Ordinary local Agent/Team/Workflow child cancellation still uses its own authoritative Kernel Run and the existing `RuntimeCanceller` contract. The synthetic fallback additionally guards against an omitted Handoff ID in future subtree traversal.
5. This correction adds **no** remote cancellation RPC, HandoffStarter method, Store interface, SQL DDL, new dispatch phase, fallback route or compatibility bridge. Remote cancellation requires a separately designed and verified host-owned route; until then, operators must read-only reconcile and explicitly quarantine unresolved external work rather than assert a cancellation receipt.

## P3 — canonical JSON at the frozen Handoff comparison boundary

`json.Marshal(Delegation)` compacts and escapes `json.RawMessage` values before hashing the child Input. Comparing the decoded persisted canonical ModelOptions against the caller's otherwise identical whitespace-preserving bytes rejected legal internal requests before grant admission. `sameHandoffFrozenExecution` now compares `ModelOptions` using the **same `encoding/json` wire normalization**, failing closed on invalid JSON, while all other frozen fields retain their exact equality requirement. Existing negative mutation cases for Model, instructions, ToolKeys, hosted grants/versions and limits remain rejected. Normal public ConfigSnapshot/Role paths already canonicalized this field; this is a defensive boundary correction, not permission to alter authorized model options.

## Verification record

- Original independent Red: running remote Member Handoff falsely persisted as `InvocationCancelled`, revision incremented, without a ChildRunner cancellation receipt.
- New Memory tests: `TestPR82CancelTurnDoesNotFabricateRemoteHandoffCancellation`, `TestPR82CancelInvocationDoesNotFabricateRemoteHandoffCancellation`, `TestPR82NeverDispatchedHandoffCanBeCancelled`, `TestPR82RejectedHandoffDispatchAllowsNoEffectCancellation`, `TestPR82UnsealedHandoffCannotClaimNoExternalEffect`, `TestPR82DefaultKernelCancellerCannotFalsifyA2AShadowCancellation`, and `TestPR82LocalHandoffChildStillUsesVerifiedKernelCancellation` — PASS (Race, repeated). The independent A2A-shadow test was Red on the earlier fix source (`/tmp/pr82-independent-a2a-shadow-cancel-20261031.log`): native Kernel Cancel returned success without any remote A2A cleanup.
- New Store-backed tests: `TestPR82RemoteHandoffCancellationUsesDurableProofAcrossStores` (Memory + SQL/SQLite, both role modes, both public cancellation entrypoints and both absent Run / present A2A-shadow variants) — PASS (Race). The real PostgreSQL variant `TestRealPostgresPR82RemoteHandoffCancellationRequiresReceipt` — **PASS** within the completed Docker integration cohort (0.94s).
- Canonical JSON positive regressions and existing negative frozen-scope tests — PASS (Race, repeated).

- Complete `make check` — **PASS (exit 0)** after final code commit `86bef37` with no subsequent code changes. Includes Go unit tests, Race, Vet and Lint (0 issues), TS lint/typecheck/tests/build, evaluation and benchmark smoke, OpenTelemetry E2E smoke, A2A product checks, release consumers and beta.24 clean-consumer local cohort. Evidence: `/tmp/gaoge-pr82-remote-cancel-final-20261031.check.log` and `.check.exit` on Mac Pi.
- Real Docker PostgreSQL/Redis `make integration` — **PASS (exit 0)** on the same source. Includes the new PR #82 real-PostgreSQL Handoff cancellation proof test, cross-connection admission/cancellation recovery, Redis, and continuation/restart tests. Test containers and network were removed. Evidence: `/tmp/gaoge-pr82-remote-cancel-final-20261031.integration.log` and `.integration.exit` on Mac Pi.
- `gofmt`, `git diff --check`, focused Harness/Harness-Postgres Race and both module Lints — **PASS**. Two unrelated, original untracked OpenAI Responses probe scripts remain excluded from this change.

Independent verification of the final **pushed** PR Head and new GitHub required checks remains separate. This evidence is **not** an APPROVE, merge or release authorization.
