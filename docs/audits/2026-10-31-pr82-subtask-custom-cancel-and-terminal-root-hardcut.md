# PR #82 — Subtask source, custom Canceller receipt and terminal root recovery hard cut

- Date: 2026-10-31
- PR: [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- Independent pre-fix Head: `ff4b5a32781806c8cae17b0c6ff879c1beb19397`
- Decision: [ADR 0015 §§32–34](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- Scope: Three independently reproduced P1 families, no SQL DDL, no compatibility branch, no parallel Host router.

## Independent Red evidence

1. **Subtask identity pollution / physical CancelSubtask bypass.** A remote Handoff remained Running while a distinct Harness-local Kernel contained a foreign cancelled Run with the same ChildRunID and different Tenant, Kind, Goal, Thread and RequestID. `Runner.Load` projected the foreign Goal/status as if the remote Subtask were Cancelled; public `CancelSubtask` then returned `nil` without any cancellation intent or remote cleanup. The remote Run continued Running.
2. **Custom RuntimeCanceller result laundering.** On an ordinary native root, a custom Cancel returned `nil` with an unchanged Running Snapshot. Harness returned success and closed the cancellation intent. Another custom Cancel returned a different foreign Kernel's same-ID Cancelled Run; Harness terminalized its own Turn though its actual Kernel root remained Running.
3. **Self-blocking cancellation intent when root already terminal.** The root Kernel could be Failed while the Harness Turn was still Running. `Cancel` persisted an intent, then used generic root terminal projection, which rejected that active intent. Both `Cancel` and `Refresh` then returned `ErrConflict` with a perpetually Running Turn and open intent, including a control case without a single child or external effect.

All five independent reproduction cases failed **3/3 each under `go test -race -count=3`** against `ff4b5a3`. Source/log: `/tmp/gaoge-pr82-finalhead-independent-red-p1-suite_test.go` and `/tmp/gaoge-pr82-finalhead-independent-red-p1-suite-race.log`. Additional child-free root Red: `/tmp/gaoge-pr82-root-failed-no-descendant-repro.log`. Permanent first-party regressions are in `go/agent-runtime-harness/pr82_final_reaudit_remediation_test.go`, kept Red before the corrections and Green after.

## Corrections

- **One Subtask ownership source.** For a persisted Delegation, `materializeSubtask` now treats the immutable Handoff Item as the default observation. A local Run may override it only if an indexed, correctly frozen child Invocation exists, the Handoff Coordinator has a fixed same-Kernel ChildRunner, and the original Delegation Item/parent Run/child Snapshot all pass the strict existing provenance check. Unknown remote/dynamic sources no longer inherit a colliding local Run's status, Goal, Result or approval; `CancelSubtask` consequently cannot skip effectful remote cancellation based on an unrelated local terminal Run. A first-party middleware-bearing Agent may attest to **read-only** fixed Kernel identity, but this does *not* grant Kernel-fenced physical cancellation (which keeps its previous stricter proof).
- **Custom cancellation is not an untrusted projection.** A successful custom `RuntimeCanceller` result needs the exact frozen original Run identity and terminal status, confirmed by a fresh local Kernel Load that reports that same terminal Run/status. Otherwise return `ErrRuntimeCancellationUnverified` (or existing Handoff-specific `ErrHandoffCancellationUnverified`) and retain the original intent for explicit reconciliation. An error after the custom callback never becomes an inferred no-effect `ErrConflict`.
- **Terminal-root cleanup and liveness.** The root may already be Failed or Completed before a new cancellation intent. That root's Cancel still performs the full descendant/delegation cleanup and only then projects the real Kernel terminal status through its exclusive owner path and settles the intent. Pending unknown remote effects leave the intent open. A repeat Cancel can resume that **same** intent for a Failed/Completed root as well as a Cancelled root, preserving the original execution identity and fencing generic Refresh. No fabricated `TurnCancelled`.

## Verification and deployment boundary

- The six original scenarios plus positive local custom Cancel and existing legitimate Handoff/Role approval paths are permanent tests, not only overlay probes.
- Targeted `go test -race -count=3` exit 0: `/tmp/gaoge-pr82-subtask-cancel-root-hardcut-20261031.race.log`.
- Cross-module Go tests (Harness, Harness-Postgres, Handoff, Agent) exit 0: `/tmp/gaoge-pr82-reaudit-fix-go-suite-20261031.log`.
- **Final exact candidate `make check`: PASS, exit 0**. All Go unit/Race/Vet/Lint (zero issues), TypeScript lint/typecheck/build/tests, A2A product, scenario, benchmark smoke, OpenTelemetry smoke and beta.24 external clean-consumer local-cohort gates completed. Evidence: `/tmp/gaoge-pr82-subtask-cancel-root-hardcut-final-20261031.check.log` and `.check.exit`.
- **Final exact candidate `make integration`: PASS, exit 0** against Docker PostgreSQL and Redis, including cross-connection cancellation/retry/recovery and cleanup of test containers/network. Evidence: `/tmp/gaoge-pr82-subtask-cancel-root-hardcut-final-20261031.integration.log` and `.integration.exit`.
- **Final focused Race `-count=3`: PASS, exit 0**, including all six prior Red P1 scenarios, valid same-Kernel custom Cancel and failed-root remote cleanup retry. Evidence: `/tmp/gaoge-pr82-subtask-cancel-root-hardcut-final-20261031.race.log` and `.race.exit`.
- `git diff --check` and full standalone multi-module Go Lint clean. Two unrelated untracked OpenAI Responses probe scripts remain untouched and out of scope.
- Pushed Head requires fresh required GitHub CI and **independent** final merge admission; this document is not merge or Tag approval.

**Host migration:** Stop relying on arbitrary local child Run IDs to reflect remotely dispatched Handoff state. A remote/unproven Subtask may remain conservatively `queued` until authoritative Handoff observation or reconciliation. Hosts implementing custom cancellation must commit an identical local Kernel terminal fact before reporting success; mere external confirmation or `nil` is insufficient. Incomplete prior candidate records must be drained or explicitly quarantined; never rewrite frozen input, invent terminal receipts, rotate Attempts on unknown work, or retain the old permissive fallback. Published beta.23 remains immutable; beta.24 is unreleased.
