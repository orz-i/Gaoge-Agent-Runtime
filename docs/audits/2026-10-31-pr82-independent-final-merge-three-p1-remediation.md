# PR #82 — Independent final-Head 3×P1 cancellation and read ownership remediation

- Date: 2026-10-31
- PR: [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- Independently audited input Head: `01a867e26f3ea1939e0158d8cc27f29801ddf18e`
- Decision: [ADR 0015 §§29–31](../adr/0015-indexed-child-ownership-and-root-entry-boundary.md)
- Status: pre-stable hard cut; final pushed-Head CI and independent merge admission remain separate.

## Three independently reproduced P1 blockers

1. **Foreign native Kernel Canceller on root Agent.** Two unrelated Kernel Stores held Runs whose ID, kind, Actor, Thread, Goal and RequestID were identical. A Harness root using the foreign `*kernel.Runtime` as its `RuntimeCanceller` cancelled only the foreign Run, left its actual local Run Running, yet projected `TurnCancelled, err=nil`. This was not a Handoff, so the previous route-specific check did not apply.
2. **Root Refresh bypassed unresolved remote cancellation.** A root cancellation had already committed its Kernel Run to Cancelled, but an external Handoff child remained Running and the cancellation Intent was durably open. `Runner.Refresh` projected the root Kernel fact directly to `TurnCancelled, err=nil` without completing descendant cleanup.
3. **Foreign Run projected through child RefreshInvocation.** An already admitted remote Handoff child had a coincidental Harness-local Run with the same ChildRunID but foreign Tenant, Run kind, Thread, Goal and RequestID. Its local terminal snapshot was accepted by `Runner.RefreshInvocation`, which changed the remote child Invocation from Accepted to Cancelled with no route or frozen origin proof.

Original independent repros on the input Head, unchanged test logic: `/tmp/gaoge-pr82-audit-three-p1-repro_test.go`, `/tmp/gaoge-pr82-audit-three-p1-repro-overlay.json`, `/tmp/gaoge-pr82-audit-three-p1-race-repro.log`. Each P1 failed **3/3** under `go test -race -count=3`.

## Corrections

- Native `*kernel.Runtime` cancellation composition is accepted only when it is the exact Harness `Dependencies.Runtime` pointer. `NewRunner` rejects wrong native Kernel composition immediately. The physical cancellation entry also checks ownership (including internally reconfigured instances), before callbacks or local terminal projection. Root failures return a Harness-minted direct no-effect `ErrConflict`; Handoff keeps its stronger route-owned `ErrHandoffCancellationUnverified` path and its legitimate never-dispatched no-effect case.
- Generic root terminal reconciliation checks the durable cancellation ledger. When an intent remains active, `Refresh`, ordinary continuations, and callback projections return `ErrConflict` instead of terminalizing the Turn from the parent Kernel status. Only the original `Cancel` path, after its descendant cancellation and delegation reconciliation succeeded, has the internal authority to synchronize terminal root state. Its durable intent is settled after successful projection as before. Unknown remote outcomes are never treated as cleanup receipts.
- Read-only `RefreshInvocation` detects the original frozen Handoff child, refusing arbitrary local RunID lookups. Only a fixed `handoff.New` Coordinator whose `LocalKernelChildRunner` explicitly owns the same Harness Kernel may use local observation, and even then it must pass the frozen Delegation Item/parent origin and full `handoff.VerifiedChildRun` task identity before projection. Remote/dynamic/custom ChildRunners fail closed with `ErrHandoffCancellationUnverified`; they require their original ChildRunner-owned observation and explicit parent authority. Legitimate fixed local Handoff Refresh stays supported.
- An existing asynchronous delegation test no longer expects `Refresh` to succeed while external cancellation is unresolved; it now requires `ErrConflict` until the resumed Cancel verifies cleanup.

## Regression and quality evidence

- Original three independent Red tests reused unchanged in Go overlay on corrected code: **Green 3/3 each** with `go test -race -count=3`. Evidence: `/tmp/gaoge-pr82-three-p1-original-red-to-green-race.log`.
- Permanent regression tests: `go/agent-runtime-harness/pr82_final_merge_admission_test.go` (root foreign Cancel; unresolved root Refresh; foreign Handoff child Refresh; valid same-Kernel Handoff Refresh).
- The existing original Handoff ownership tests continue to enforce `ErrHandoffCancellationUnverified` for foreign Cancel, including exact metadata collisions.
- Full local `make check` **PASS, exit 0** on the final corrected Go code: all Go test/Race/Vet/Lint, TypeScript lint/typecheck/build/tests, smoke/evaluation/A2A, and beta.24 clean-consumer local cohort. Evidence: `/tmp/gaoge-pr82-three-p1-make-check-20261031.log` and `.exit`.
- Docker-backed real PostgreSQL/Redis `make integration` **PASS, exit 0**, including PR #82 remote Handoff cancellation ownership regressions and cleanup of the test containers/network. Evidence: `/tmp/gaoge-pr82-three-p1-make-integration-20261031.log` and `.exit`.
- The existing legitimate Role child approval/cancellation flow and positive fixed-local Handoff Refresh regression both pass; no generalized denial of Handoff approval was shipped.
- Pushed exact remote SHA/PR CI gates and a **new independent merge review** remain separate. A green test or this document does not authorize merge/tag.

## Migration and explicit exclusions

Native Kernel Canceller instances are not interchangeable across Harness runtimes even if they address identically named Runs; fix composition rather than retry against a foreign Kernel. Root `Refresh` now reports conflict during unresolved cancellation instead of claiming terminal success. Child `RefreshInvocation` for an externally routed Handoff does not fabricate a new remote-read adapter and cannot promote a matching local Kernel Run to remote authority. Preserve original external work for reconciliation or quarantine. No SQL DDL, schema version, mutable replay authority, synthesized cancellation receipt, routing fallback or compatibility shim is introduced. Published beta.23 remains immutable; beta.24 is unreleased.
