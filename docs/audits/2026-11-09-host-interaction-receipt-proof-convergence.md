# 2026-11-09 — Host Interaction receipt-proof convergence

## Problem / scope

A resolved Host business Interaction is **not** proof that its physical
callback completed. The Harness has two distinct read-only callers of this
fact: Refresh/Resume (which must avoid entering another continuation during
an unresolved Host callback), and cancellation preflight (which must not
cancel a Run while that same callback may still be executing).

On `main@a6aa6bb4d9f33cc737a568b374612515d433142e`, these paths
independently implemented the same `InteractionResolved × InvocationRunning ×
exact Host response receipt` loop, including owner/Turn identity checks and
lazy paged Item reads. Future changes could accidentally make cancellation
accept receipts that Refresh rejects, or vice versa.

This is a **read-only proof convergence**, not a cancellation redesign.
`resolvedInteractionClaimPending` and
`inspectRelatedCancellation` now use one internal
`unverifiedResolvedInteraction` implementation. It reads persisted
Interactions, requires the original owner to belong to the exact Turn, and
checks the original running owner's business completion using
`hasVerifiedInteractionResponse`. A missing or forged receipt makes the
matching resolved callback pending, not successful. The caller supplies its
existing scope predicate; no new execution grant is minted.

## Trust boundaries and invariants

| Boundary | Original requirement | What changed |
| --- | --- | --- |
| Host callback → durable receipt | Only a receipt with the exact Interaction, Run, Invocation, Turn, parent Item and `host_response_verified` payload certifies callback completion | Existing `hasVerifiedInteractionResponse` remains sole receipt proof; one shared read-only caller |
| Running Invocation → continuation | A Resolved callback without a verified response blocks `Refresh`, `ResumeTurn`, child synchronization and Application refresh | Existing API and error mappings remain |
| Related descendant → cancellation | An unverified callback for any targeted Invocation blocks cancellation *before* revoking a budget or controlling a Kernel Run | Existing `inspectRelatedCancellation` maps shared proof to `ErrConflict`; no-effect outcome preserved |
| Missing RunRelation → conservative checks | Read-only admission may check all owners; **writes may affect only precisely related targets** | Widening and `collectCancellationScope`/post-intent scoping remain unchanged |
| Waiting Interaction → write-capable claim | A waiting owner's Invocation CAS is the only path to cancel the waiting response; a lost ACK must keep the intent | `fenceInteractionCancellationWithClaim` remains separate, unchanged and write-aware |
| Verified callback → Runtime resume | Business-response receipt does **not** imply a committed runtime continuation or permit a second callback | Existing unique continuation claim and Kernel reconciliation remain unchanged |

No new public API, Agent loop, FeatureDriver, Kernel state, SQL schema,
Profile binding, retry semantics or backward-compatibility bridge.
The scope predicate is local read-only selection, not a Host intent router.
Existing read failures, foreign owner Turns, incomplete Item pages and
misattributed receipts remain fail-closed.

## Independent test matrix and performance interpretation

- The new dedicated read-only fake Store panics if a write is attempted.
  Enumerate **3 Interaction states × 7 Invocation states × 6 receipt cases =
  126 combinations**. Verify both original whole-Turn/single-owner
  continuation and relation-scoped cancellation proof against an
  independently stated boolean oracle.
- Verify the original verified owner is not blocked by an unverified sibling,
  a relation-selected set flags its unverified owner, an unrelated missing
  owner is ignored only when out of scope, and Item evidence is lazily
  loaded once per query rather than once per Interaction.
- Verify foreign owner Turn, missing owner and failed Item read produce
  the unchanged error classes; no Store writes or irreversible effects
  occur in the proof.
- Exercise existing root/child/subtask cancellation tests, in-flight
  business callback vs Cancel/Refresh, omitted relation registry,
  ambiguous response and later legitimate cancellation with Go Race.
- The refactor removes duplicated high-risk proof code but does not make
  latency, allocation or throughput claims. The complete native
  `make check` / smoke benchmarks remain the release-local gates, and
  a repeated fixed-fixture performance experiment would be required
  for a quantitative optimization claim.

## Exit criteria and remaining gates

The implementation is not a release or merger decision. Before submission,
require targeted and full Harness Race tests, complete `make check`,
unchanged scripted Agent 40-case quality profile and original SHA-256
historical evidence. Then carry out an **independent code audit of the
entire cancellation/interaction/dispatch/continuation chain** and finally
run the exact committed PR Head in the five GitHub required checks.

Not claimed by this slice: new external paid Provider compatibility,
unrun real Postgres/Redis services in this local worktree, a paid
40-case Agent benchmark, or production performance improvement.

## Local implementation verification — PASS, before independent audit

- `make check` was run against the completed uncommitted governance
  worktree: **exit 0**, including Go fmt/tidy/vet/unit/race/lint, Python
  baseline protocol, TypeScript lint/typecheck/46 tests/build, Agent
  evaluation and portable benchmarks, OTel smoke/E2E-local fixture, A2A
  product and beta.24 clean-consumer release checks. The ignored receipt
  and log are `coverage/interaction-receipt-governance/check.exit` and
  `coverage/interaction-receipt-governance/check.log`.
- New table-driven, read-only proof test: **126** combinations (3
  Interaction statuses × 7 Invocation statuses × 6 receipt variants)
  against an independently stated oracle. Other fault tests cover
  foreign-Turn owner, missing owner, failed Item read, unrelated sibling,
  relation-selected owners and one evidence read per proof.
- Independent temporary Go Overlay **RED** mutants: disabling the
  original Owner/Turn binding, accepting missing/forged business receipts,
  or ignoring relation-selected sibling isolation all caused their
  regression tests to fail. Original source remained **GREEN**; the
  mutations were never written to the governed worktree.
- `go test -race` targeted the new proof matrix and existing real
  cancellation/Refresh/Interaction tests: **PASS**.
- `make agent-baseline-test`: **131/131 PASS**. Offline
  `make agent-quality-calibrate`: **40/40 scripted PASS**, 160 fake
  Model calls/120 fake Tool calls and unchanged dataset SHA-256.
  Both ignored historical real Provider reports passed read-only
  hash/schema/evidence anchor verification.
- `make benchmark-smoke`: **PASS**. This one-shot result is not
  a comparative latency, allocation or throughput claim.
- The main worktree remains unchanged, including its two user-owned
  untracked OpenAI probe scripts. There is no GitHub PR/CI result
  or separately reviewed final commit yet.

The next stage is a **read-only independent final code audit** of the
entire affected cancellation/interaction/dispatch/continuation chain;
only then consider segmented commits and exact-PR-Head merge admission.
