# EA-1 B06 — fixed compensation matrix batch

Date 2026-11-12. Frozen tested source `6d08152d30647d8c24939bd5e78cdfcfc943ba43`.

Eight cells PASS: WR-C/WC-C P0/P1/P2/P5. Actual public Cancel runs registered Undo after real reserve-and-Wait setup. Already-PASS P3/P4 were not re-certified at a new SHA without their own evidence.

P0 proves public preflight and Store admission races, including valid peer revision and terminal self/ancestor winner, zero losing Undo and complete unchanged self/ancestor snapshots. Valid winner compensates once.

P1 holds successful admission before failure intent or Undo dispatch. Competing self/ancestor writes, cancellation, resume and actual approval resolution cannot bypass the original Grant. Full original state and Grant remain unchanged.

P2 observes actual dispatched Undo Pending with no receipt/no-effect proof. Intent and full EffectRequest identity precede callback; fresh owners, true SQLite close/reopen and independent PG connections retain original Grant and refuse replay/forged completion. Every denied control preserves full self/ancestor snapshots.

P5 holds Undo callback against competing control and same-run budget election. A separate confirmed-no-effect attempt legitimately schedules attempt 2, retaining EffectID and exact source; attempt-specific OwnerNodeID is asserted according to the existing public relation-owner rule, not incorrectly required to be unchanged. Full-request hash receipt, compensation identity and budget settle exactly once. Child deadline is a real Store-clock-due ancestor Expire, not a fake peer clock; it must return Busy with no mutation. Retry is a separate non-expired case.

## Failures, bounded fixes, evidence

Initial source 3a4e031 exposed two real defects independently confirmed by limited source review:
1. Dispatched unknown Undo returned SegmentYielded, and settlement treated its pending Effect as if not dispatched, incorrectly retiring authority (EA-06).
2. A legitimate Resume that completed the original cancellation intent returned the private cancellation signal; settlement rejected that signal despite complete receipts, stranding the Grant (EA-08).

Minimal production fix preserves SegmentYielded compatibility while additionally identifying EffectPending. Pending now takes precedence and requires the exact existing preceding no-effect attempt witness for any safe retry. Completed cancellation requires the private signal, exact cancelled Run/code and persisted original FailureIntent, then still passes every original Effect receipt check. No status-only settlement or public-grant bypass was introduced.

Initial deadline fixture used inconsistent peer/Store clocks and two lint findings; corrected without changing production. At b418f5f production scenarios passed but an overstrict fixture compared attempt-specific OwnerNodeID as immutable; 6d08152 checks both exact owner formulas then compares all other fields, preserving the full source assertion. Both earlier diagnostic runs remain in separate immutable source-tagged evidence directories, not presented as acceptance.

Four backend directed -race commands PASS, real PG each suite count=3 with no skips. Three module lint commands report 0 issues. Four entire module race suites (core, PG, SQLite, Harness) PASS at 6d08152. Eleven final logs, commands, origin and checksums are in evidence/issue107-ea1-b06.

Independent limited review of the production delta and exact eight-cell fixture source found no remaining blocking findings. The coordinating reviewer accepted the technical applicability analysis below before ledger certification.

## Exact applicability, not waivers

Workflow Runner does not create or accept Agent ExecutionLease or cross-Run SharedBudget dependencies; Workflow StartRequest has no DeadlineAt. Therefore those precise sub-vectors are not applicable to this public Workflow compensation entrypoint. The actual authority is its original physical Grant and this Run's frozen budget. Tests do NOT claim Agent lease, cross-Run shared-budget or Root deadline coverage, and do not borrow Agent S-CTL tests as dynamic proof. Child ancestor deadline is actually tested. These statements do not waive Agent/Harness cells, introduce whole-cell N/A, or change the frozen matrix.

Ledger: 52 PASS / 92 UNRUN. Older 44 PASS retain their real prior SHAs. Final common-SHA replay and remaining matrix/gates are pending; overall WP-RUNTIME/WP-GAOGE remain NO-GO. Remote PR-head CI remains unexecuted under no-push/no-PR authorization.

No push, PR, merge, Host edits or production data operations. Disk 9.5 GiB before, 9.4 GiB after; cleanup 0 bytes, no builds interrupted.

Next fixed B07: WR-S P0/P1/P5, WC-S P0, WC-R P1/P5, WR-W P2/P5, WC-W P0/P2 (ten remaining Workflow entry cells).
