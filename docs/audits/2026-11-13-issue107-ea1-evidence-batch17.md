# EA-1 B17: actual Child Agent Resume P0–P5

Frozen source: `606a7f559801e8f64d72dfc5c1f0aca4c90f5e80`. This local test-only batch changes exactly the six AC-R rows to PASS, reaching **116 PASS / 28 UNRUN**. The other 138 ledger rows remain unchanged, with their original evidence SHAs. This is not common-source completion or overall GO. No push, PR, merge, Gaoge edit or production-data migration.

## Distinct setup and measured entry

`RunEA1ChildAgentResumePhases` and `RunEA1ChildAgentResumeSettlement` prepare a legitimate Root Workflow → middle Workflow → Child Agent relation with original Actor/Thread, request and origin. Actual PrepareRun must have zero measured Host/Model/Tool calls and retire its setup Grant. Source is checked at actual Create and the distinct prepared snapshot. Only then is the measured actual Resume armed at that prepared revision. These are not Start tests relabeled as Resume.

Actual Resume ordering is BeginChild → native owner acquisition → Host callback. Unlike Child Start's earlier Host boundary, Resume's Host has acquired native authority. P0 preserves complete winner snapshots for missing/stale/terminal, parent-wins and competing child-revision admission. No mutation or callback is hidden by comparing only source identifiers. P1 protects the child and both ancestors from competing control and ordinary/terminal writes with exact typed errors and original lease/epoch, Grant and full snapshots unchanged.

P2 exercises actual Host/Model/Tool uncertain outcomes and original receipt faults. Full original Generate request/catalog/provider/binding tuple is captured before legal consumed-field pruning and bound to durable hash/attempt/source revision. Actual Model and Tool returns, complete receipt and terminal result are compared, not just receipt IDs. Original two-second native authority genuinely expires by Store time before fresh owner/native reopen attempts; expiry never releases the original unknown scope or permits replay. Full child and both ancestors, original source and budget remain preserved.

P3 distinguishes an original durable receipt from original callback settlement. The actual callback returns but original Mark is faulted before commit; clean receipt cannot substitute for that missing marker. Fresh ChildReconcile must reject Busy plus ExecutionConflict and must not mint Mark. P4 separately commits the actual original Mark then loses its ACK; fresh reconciliation consumes that marker without another provider/Tool send. Both phases cover Model yield and terminal Tool outcome, full original result and ancestor state.

P5 holds two actual peer Resume requests at BeginChild with the same prepared revision. The winner Host remains blocked without advancing that revision until the losing caller is observed with exact Busy and unchanged original snapshot. Only the winner acquires native execution and sends. Parent cancellation and actual grandparent deadline control remain fenced while the scope is live; the original callback then performs its legitimate Mark/Finish/native release once. This is an admission Begin barrier, not a Finish barrier: one copied auxiliary-GORM comment still says Finish, but the executable barrier and this report state the actual cutpoint. No production or behavioral change is implied by that comment typo.

Resume accepts an existing Run and expected revision; it does not accept replacement caller Actor/route/origin fields. Original immutable source is verified rather than claiming invented caller-authentication vectors. Root shared-budget election and Workflow compensation are not exposed by this Child entry and remain separately mapped obligations.

## Exact verification

`evidence/issue107-ea1-b17/` contains eight source-tagged logs with checksums and actual commands: initial Memory smoke, three linters and four directed backend race suites. The first smoke passed all fifteen subcases. Both real PostgreSQL suites actually ran three times each; none skipped. Memory retains its Store with fresh owners, auxiliary GORM SQLite retains shared DB semantics, native SQLite closes both handles before OpenExisting, and native PostgreSQL uses independent pools. Reopen is not falsely described as killing an OS process.

All checks passed at the first frozen source; no B17 failure log was discarded. This test-only batch does not repeat full module race runs. Latest complete four-module race evidence remains B14 source `a774e5c`, not this commit. Final same-source full local gates, remaining Runtime matrix rows and independent H-FROZEN/remote authorization restrictions remain outstanding. Strict matrix checker must remain NO-GO.

Independent bounded source review accepted the six-cell implementation with no concrete blocking finding; durable evidence signoff follows separately. Disk remained about 9.2 GiB available, cache cleanup zero. Next frozen batch is Child ResolveApproval P0–P5, with explicit-native checkpoint continuation and legitimate default-child inline Tool execution kept distinct.

## Independent durable signoff

Independent reviewer verified fixed blobs at `a8d72420c248eedde1bd3ce6b680276ba30027bc` against source `606a7f559801e8f64d72dfc5c1f0aca4c90f5e80`: eight matching log hashes/source/zero exits, no skipped tests, both real PG suites actually three times. Only AC-R's six rows change to PASS; the other 138 remain byte-identical and Go source has no drift. Inventory, strict NO-GO and six checker tests agree with 116 PASS / 28 UNRUN. The actual Begin barrier, initial smoke success, no discarded B17 failures and historical full-race limitations are accurately recorded. No tests were rerun by review; this is only batch signoff.
