# EA-1 B07 — remaining Workflow entry boundaries

Date 2026-11-12. Frozen source `49237900d5dcf51c070b80233fa4a2e639972ec8`. Tests only; no production changes.

Ten fixed cells PASS: WR-S P0/P1/P5; WC-S P0; WC-R P1/P5; WR-W P2/P5; WC-W P0/P2.

Start P0 uses actual StartRun preflight, duplicate source, and Create-to-Begin races. After admission failure an already-created Run remains, with exact winner state, zero external dispatch and no Grant. Child P0 additionally invokes the actual built-in Registry → NodeSubworkflow → internal StartRun path: original parent Effect/ref/revision/hash, child identity/request/actor/thread/input/nested depth/attempt are checked before a peer child revision wins admission. Direct parented Start is not substituted for subworkflow provenance.

Child Wait P0 uses actual ResolveWait preflight plus original checkpoint-resolution and ancestor-terminal races. Every denied request preserves full original child/ancestor snapshots. A legitimate winning checkpoint resumes once with full-request-hash receipt.

Root/Child Wait P2 start at Wait with zero physical effects, then actual ResolveWait sends one original Effect whose result is Pending. Original resolved Wait/checkpoint/response and durable intent precede send. Fresh owners, true native SQLite close/reopen and independent PG connections retain exact source/attempt/Grant. Repeated ResolveWait is exact WaitPending; other recovery/control is Busy and cannot replay.

Remaining Start/Child Resume P1 and Start/Child Resume/Root Wait P5 use actual public entries at deterministic admission or callback barriers. Valid self/ancestor controls, original Wait identity, complete request source, exact safe-retry attempt-specific relation owner, full-request-hash receipts and per-Run reservation/settlement are checked. Child Resume additionally exercises a real Store-clock-due ancestor deadline; safe retry is a separate non-expired case.

Four directed backend commands PASS with -race at the frozen SHA. PG runs all four top-level suites three times without skips. Three module lint runs report 0 issues. This test-only batch does not repeat full-module race suites, which last ran at B06 source6d08152; they are NOT represented as this SHA's full-suite acceptance. Final common-source full suites remain required.

Independent limited ten-cell source review at 4923790 found no blocking findings, including the actual built-in subworkflow source fixture. Seven final logs, commands, origin and checksums: `docs/audits/evidence/issue107-ea1-b07/`.

Workflow Runner has no Agent ExecutionLease/cross-Run SharedBudget dependency and StartRequest no Root DeadlineAt. These precise sub-vectors are technically inapplicable to these public entries, not tested claims or whole-cell N/A. Agent/Harness obligations remain. Actual authority is the original physical Grant and per-Run budget.

Ledger: 62 PASS / 82 UNRUN. Prior 52 retain their actual tested SHAs and logs. Overall EA-1/WP-RUNTIME and independently WP-GAOGE remain NO-GO. Remote PR-head CI remains unexecuted under no-push/no-PR authorization. No Host edits, push, PR, merge or production data operations.

Disk 9.4 GiB before/after; cleanup 0 bytes. Next B08 fixed WR-Q/WC-Q P0/P1/P2/P5: reuse precise actual Reconcile proof where available and add missing preflight/concurrent retirement cases rather than duplicate fixtures.
