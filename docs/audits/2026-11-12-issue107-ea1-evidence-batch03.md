# Issue #107 EA-1 — WP-RUNTIME Evidence Batch 03

Date: 2026-11-12. Local fixed-matrix progress only; WP-RUNTIME and WP-GAOGE remain NO-GO.

Frozen tested source: `c91a17a1a92c1116b99962992e36a618aad2ecc6`.
Starting authorized workspace HEAD: `a0d540ac98c59cf0e512bcbdb63470726c108a13`.
Changes are tests/conformance fixtures only; no production execution paths changed.
No push, PR creation, merge, Host edit or production database operation.

## Fixed seven-cell outcome

- **EA1-WR-W-P0**: actual Root ResolveWait rejects invalid response, zero/stale/future revision, missing Run and absent pending Wait. A deterministic second Store worker wins the original checkpoint between initial Load and admission; loser receives exact ErrExecutionConflict, no Effect fires and no Grant is stranded. Winning state/revision is unchanged, duplicate resolution is rejected and the original winner resumes exactly once. Native PostgreSQL uses independent connections.
- **EA1-WR-R-P3 / EA1-WC-R-P3**: actual Resume after a yielded segment, with second physical receipt durably stored before Mark. Receipt derives from the original full EffectRequest and is checked against exact Run, Actor, Thread, compiled Definition ID/hash, Effect ID, attempt, kind/class, compensation flag and input. Two effects cannot share a receipt identity. Premature Reconcile/Finish fail and child ancestor control remains fenced; original callback completes once.
- **EA1-WR-C-P3 / EA1-WC-C-P3**: actual Cancel invokes registered undo. Original undo receipt is durable but unmarked Grant remains; first-party Mark is necessary for retirement and parent control cannot mutate while fenced.
- **EA1-WR-C-P4 / EA1-WC-C-P4**: compensation Mark commits and its ACK is lost. Native SQLite closes/reopens; PostgreSQL uses a new independent connection. Original receipt, revision and Grant are recovered without reserve/undo replay. Forged start revision cannot retire the Grant. First Reconcile succeeds; repeated Reconcile returns the existing API's exact ErrNotFound after retirement, preserving state/effect idempotence. Memory/GORM execute the same state machine with fresh Kernel/Workflow owners; they are not described as durable process restart.

P0 interface scope is RunID/revision/response: this API has no caller Actor/parent/route input. These results do not certify Host authorization or frozen-provider-source cells. Their immutable Store identity obligations and H-FROZEN remain separately tracked.

## Evidence and review

All four directed commands passed with `-race` on the frozen SHA. PostgreSQL16 tests ran three times, with no TestEA1 skips; SQLite P4 genuinely closes the original Store. Core, PostgreSQL and SQLite full module lint each reported 0 issues at this SHA.

A targeted independent source review of precisely these seven cells found two coverage gaps (constant receipt identity and absent Memory P4 entry). Both were supplemented; final source review at c91a17a found no further finding. Reviewer did not run tests and did not conduct a generic architecture audit.

Durable logs, sanitized commands, origin SHA and checksums:
`docs/audits/evidence/issue107-ea1-b03/`.

Earlier exploratory runs exposed test-contract mismatches, not production fixes: stale Store admission uses ErrExecutionConflict, repeated retired-Grant reconciliation uses ErrNotFound, and a Run without a pending Wait returns ErrWaitPending. Final tests assert these exact errors rather than accepting arbitrary errors.

The prior complete directed battery and four module race suites were also run successfully at earlier test SHA `d03f510775d74f97f910ded9ae176e2679481d72`; three lint scopes then reported import grouping failures, corrected before the final SHA. Those earlier runs are NOT presented as final-SHA full-suite acceptance. Old 25 PASS ledger entries retain their existing Batch02 SHA/evidence. Full final-SHA suite/consumer gates await matrix source convergence.

## Ledger and limits

Local ledger now has **32 PASS / 112 UNRUN**, zero N/A waivers. Seven new cells refer to c91a17a; prior 25 PASS cells remain at their earlier source SHA. Strict acceptance must remain NO-GO until every required cell and full checks converge on one final source SHA.

WP-GAOGE remains independently blocked. Remote PR-head checks cannot run under the current explicit no-push/no-PR instruction and remain unexecuted, not waived.

Mac free disk: 9.9 GiB before, 8.7 GiB after. No cache cleanup was needed in this batch; reclaimed bytes: 0. Source, ledger and evidence were preserved. No active build was interrupted.

Next fixed Runtime group: remaining Workflow Wait/Resume admission and recovery cells, selected explicitly from the unchanged ledger before implementation; no new matrix axes or unbounded review.
