# PR #82 — Native middleware cancellation and local Handoff Run ownership

- **Date:** 2026-10-31
- **PR:** [#82](https://github.com/orz-i/Gaoge-Agent-Runtime/pull/82)
- **Independently audited original Head:** `92cb8ee3778044f56aef48a8ded335dd26e57780`
- **Scope:** Two independently reproduced P1 cancellation-authority defects; pre-stable hard cut, no compatibility route.
- **Fixed code commit:** `09d6b4b6440f9dd6076f98302e5e501504dda3bf`
- **Local acceptance:** `make check` and `make integration` PASS (exit 0); final pushed-Head independent merge admission remains separate.

## Reproduced blockers

1. **Native Agent middleware could issue a Host physical effect after a successful Cancel.** An unsettled native Agent Start was exempted from Harness cancellation arbitration whenever the local Kernel Run was found and the Canceller was the same Kernel. A configured RunMiddleware was paused after the Agent entered a Model call but before its own next Host effect. `Cancel` successfully terminalized the Turn, then the middleware still performed the effect. The isolated red test reproduced the timing deterministically; repeating both tests three times under `-race` on the original Head reproduced both failures 3/3.
2. **Routed Handoff could cancel an unrelated local Run with the same RunID.** An actual remote Member-only Handoff child with a valid settled physical grant was overlaid by a local foreign-tenant Agent Run with its ID. `CancelInvocation` cancelled the unrelated local Run and falsely projected the remote child as Cancelled. The existing absent-Run fence did not cover the present-but-wrong-source case. The original red test reproduced consistently.

## Hard-cut repairs

- The concrete first-party Agent exposes its **immutable composition-derived** native Start cancellation proof. The exception is available only when the exact Agent and Kernel Runtime match, the Canceller is that same Kernel Runtime, and no Host Run/Model/Tool middleware or Model request preparer can execute outside native Kernel CAS/Epoch fences. All other in-flight Starts retain their physical grant and reject concurrent cancellation until settlement. Custom and composite agents never inherit the exception.
- For every locally present routed Handoff child Run, Harness validates the immutable parent Delegation Item and its exact parent Invocation/Run, the child's hashed frozen Input and root Attempt, and the **same** `handoff.VerifiedChildRun` predicate already used by Coordinator Load/Start/Refresh (Run ID, kind, Tenant, Actor, Thread, Goal, and parent-ID-derived RequestID). Invalid or missing proof returns `ErrHandoffCancellationUnverified` **before** Kernel/Host cancellation and before accepting a local terminal state. A custom Canceller's returned Snapshot is independently checked before terminal projection.
- Regression fixtures now persist the authentic Role or Member started Item and the parent-bound RequestID. No synthetic identity mismatch is grandfathered in.

## Proof and coverage

- New deterministic native RunMiddleware race reproducer: `TestPR82NativeRunMiddlewareCannotHandoffUnsettledStartToKernelCancel`; guards unresolved Grant, nonterminal Turn, rejection, then legitimate completion and settlement.
- New `TestPR82HandoffCancellationRejectsForeignLocalRunCollision`: Member-only and Role selected routes × incorrect tenant/actor/thread kind/thread ID/Goal/RequestID/Run kind. Verifies no foreign Kernel mutation, no false Invocation cancellation, and retained cancellation intent.
- Existing local native Handoff cancellation and A2A shadow rejection tests remain supported, using genuine StartRequest provenance.
- Focused red/green verification, plus the two repaired P1 regressions and original legitimate local cancellation, **PASS under `-race -count=3`**. Evidence: `/tmp/gaoge-pr82-two-p1-regressions-race-triple.log` on Mac Pi.
- Full `make check`: **PASS, exit 0** (Go tests and Race including Harness/SQLite/PostgreSQL, Vet, zero Go lint issues, TypeScript checks, deterministic evaluation/A2A, release and beta.24 clean-consumer cohort).
- Full Docker PostgreSQL/Redis `make integration`: **PASS, exit 0**, including real cross-connection cancellation/recovery and continuation E2E; test containers/network were cleaned up. Evidence for both complete gates: `/tmp/gaoge-pr82-two-p1-final-all-gates.log` and `.status` on Mac Pi.
- This is local quality acceptance, **not** final pushed-Head independent GitHub merge admission; do not auto-merge or publish a tag.

## Compatibility and ownership

This narrows **observable cancellation behavior** for composed native Agents with arbitrary middleware: where cancellation could previously appear successful while a Start was still effectful, the same call now returns `ErrConflict`. Foreign local RunID collisions never authorize a delegated cancellation. No Store API change, SQL DDL, schema version, Run state, grant receipt, migration, synthetic cleanup, dual router or backward compatibility adapter. The original in-flight effect stays fenced until it completes or is explicitly reconciled.
