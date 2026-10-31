# PR #82 — Handoff route-owned cancellation hard cut

- **Date:** 2026-10-31
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Independently audited input Head:** `4029565d4d94b1c695042f8ccc3b7baa1706c905`
- **Status:** Code correction and focused red/green verified; pushed-Head merge admission is a separate decision.
- **Scope:** Local Handoff cancellation authority, not a new remote cancellation protocol.
- **Fixed code commit:** `d9a13fe968709c60747b13e1b92103e59bc756b8`.
- **Full local gates:** `make check` and `make integration` **PASS, exit 0**; another complete `make check` started **after code commit `d9a13fe`** also **PASS, exit 0**, with no intervening code edits.

## Independently reproduced P1

The previous Handoff fix checked Run ID, Kind, Tenant/Actor/Thread, Goal and RequestID. Two separate Kernel Stores can nonetheless contain distinct Agent Runs with **all those fields identical**. With a remote Handoff physically active, `CancelInvocation` and root `Cancel` cancelled the matching but unrelated local Run, projected the child Invocation as Cancelled, and left the actual remote Run Running. Both entrypoints failed **three times each under `go test -race -count=3`** on source Head `4029565`. The isolated read-only audit used Go test overlay before promoting the red cases into repository tests. Evidence: `/tmp/gaoge-pr82-independent-exact-origin-local-cancel-two-paths.log`.

A follow-up red reproducer found another source problem: even a correctly bound local ChildRunner could be paired with a different **RuntimeCanceller** backed by a foreign Kernel with the same Run. Cancel silently changed that foreign Run and projected the local child as Cancelled. Evidence: `/tmp/gaoge-pr82-route-owner-foreign-canceller-red.log`.

## Corrected ownership contract

- A Handoff's immutable physical Start grant is checked **before** loading/cancelling any coincidentally named local Run. If no Handoff physical effect could have entered the ChildRunner, return the existing no-effect result, *without* calling the local Canceller.
- For a potentially effectful Handoff, exact Run metadata alone is not permission. Only a concrete `*handoff.Coordinator` constructed with `handoff.New(fixedChildRunner)`, whose immutable ChildRunner explicitly implements `handoff.LocalKernelChildRunner`, may certify that its executor owns the **same `*kernel.Runtime` instance** as Harness. Dynamic `NewRouted`, custom HandoffStarter, absent proof, or different ChildRunner runtime fail closed. `*agent.Runner` can certify this only without Host Run/Model/Tool middleware or Model request preparer; those callbacks cannot be fenced merely by Kernel CAS.
- The Canceller must independently be the **exact same native Kernel Runtime** or a trusted Host `harness.LocalKernelCanceller` whose `OwnsKernelRuntime` contract identifies that exact Runtime. A Canceller pointed at another Kernel is not valid, even when its response describes the identical Run. A custom Canceller is responsible for truthful ownership and its no-effect/error contract.
- The existing hashed child Input, original Delegation parent Item and Kernel parent Run, and shared `handoff.VerifiedChildRun` still verify the exact task envelope before any physical Cancel or accepting a terminal Run. A custom Canceller's successful response must be a verified **terminal** outcome; a matching but still-Running response leaves `ErrHandoffCancellationUnverified` and the original cancellation intent intact.
- The Host must preserve its fixed ChildRunner-to-Kernel composition for admitted active children, including during recovery. No alternate dynamic route, guessed origin from metadata, synthetic remote receipt, Host-cancel fallback, additional Store interface, SQL DDL or schema/version migration is introduced.

## Red/green coverage

- Real independent Kernel Stores with identical child Run fields: direct child and root Turn cancellation, with the actual remote Run remaining Running and the unrelated local Run unchanged.
- Fixed local ChildRunner: legitimate Kernel Cancel still works. Fixed remote and dynamic local ChildRunner: refused without route ownership proof. Missing physical dispatch: synthetic no-effect child cancellation cannot touch a coincidentally present local Run.
- Wrong local Kernel Canceller: denied before any physical cleanup. A custom Canceller returning a valid identity but nonterminal Run: does not settle cancellation.
- Regression persisted on Memory, SQL and real PostgreSQL Harness Store, covering Member-only / Role and child / root cancellation; existing A2A shadow, async local, and ambiguous remote Handoff tests remain in scope.
- Full `make check`: **PASS, exit 0**, including Go test/Race (Harness/SQLite/PostgreSQL), Vet, zero lint issues, TypeScript, deterministic evaluation/A2A, release checks and beta.24 clean-consumer cohort.
- Real Docker PostgreSQL/Redis `make integration`: **PASS, exit 0**; both old Handoff recovery and new matching-local-Run collision assertions passed in real PostgreSQL. Test containers and network were cleaned up. Evidence: `/tmp/gaoge-pr82-route-owner-final-v2.log` and `.status` on Mac Pi.
- Independent post-commit `make check`: **PASS, exit 0**, started after code commit `d9a13fe`; includes the same complete Go, race, vet, lint, TypeScript, evaluation, A2A and clean-consumer gates. Evidence: `/tmp/gaoge-pr82-route-owner-post-code-check.log` and `.status` on Mac Pi.
- New pushed Head still requires independent GitHub CI and merge admission; do not auto-merge or tag.

## Migration

This is a **pre-stable behavioral hard cut**. Hosts using dynamically resolved routes or an unproven custom HandoffStarter/Canceller must not call Harness local cancellation and expect successful terminalization for potentially executed Handoffs; they get `ErrHandoffCancellationUnverified` and must use separately owned external cleanup/read-only reconciliation or explicit quarantine. Hosts using a fixed local ChildRunner may expose `OwnsKernelRuntime` only when every effect is fenced by that exact Kernel. No compatibility shim, auto-routing, inferred Host intent, protocol backfill or reclassification of existing uncertain remote effects.
