# EA-1 B08 — Workflow recovery boundary completion

Date 2026-11-12. Frozen tested source `179acd4bf61d5680ef23c4f3df1bc63d1ae4ddf8`. No production edits.

Eight fixed cells PASS: WR-Q/WC-Q P0/P1/P2/P5. Previously certified P3/P4 are not silently assigned a new SHA.

P0 invokes actual Reconcile with nil/empty/missing/wrong-kind/corrupt-state/valid-no-Grant origins. Exact typed failures preserve selected state and a separate active original owner's full state/Grant. This API accepts runID only: origin isolation is not caller authentication.

P1 reuses the actual Reconcile call in the original ResolveWait admission barrier, adding immediate full self/ancestor snapshot and exact Grant invariance before any other competitor. Recovery cannot use a clean pre-dispatch snapshot to retire an unmarked original Grant. Original owner later executes once.

P2 reuses actual fresh-owner Reconcile in WaitUnknown. Original resolved Wait/response/physical intent, exact source and attempt remain unknown with no receipt/no-effect witness. Every denied operation keeps full snapshots/Grant; native SQLite really reopens and PG uses independent connection. Recovery does not issue a physical request itself.

P5 is new true concurrency, not sequential repetition: two actual Reconcile owners each capture the same marked Grant and meet at a barrier before Store Finish. Exactly one returns nil, the other Busy+ExecutionConflict; a subsequent call returns NotFound. Original receipt/full-request hash/source/attempt and complete state remain unchanged, no physical replay. Ancestor controls are fenced until retirement and can proceed afterward. Native SQLite closes the first Store then opens two connections; PG opens two independent pools.

Initial 97539e4 passed Memory/native SQLite/real PG, but auxiliary GORM-SQLite's two DEFERRED transactions competed for a write lock and returned ErrConflict. Independent limited review accepted a test-factory-only MaxOpenConns(1) configuration for this auxiliary dialect. The two actual runners still both reach the pre-Finish barrier with the original Grant. GORM SQL transactions are explicitly serialized, NOT independent-connection competition evidence. Native SQLite/real PG retain true independent connections. No production behavior or expected error contract was changed; the initial diagnostic logs remain source-tagged.

Final four backend -race commands PASS, PG each three suites runs three times without skips. Three module lint runs return 0 issues. This test-only batch does not repeat full-module race suites; final unified-source full checks remain pending. Seven logs, commands, origin and checksums: evidence/issue107-ea1-b08.

Independent limited source review found no blocking finding for these eight cells and approved the explicit auxiliary fixture configuration. Reconcile proves recovery of selected original Run/source, never external caller authorization.

Ledger is 70 PASS / 74 UNRUN. All 60 Workflow cells now have local evidence, but at multiple honest source SHAs; this is NOT final common-SHA acceptance. Older 62 rows retain their actual prior source/evidence. Agent/Harness/middleware/Host obligations remain; overall EA-1/WP-RUNTIME and WP-GAOGE remain NO-GO. Remote PR-head checks remain unexecuted under no-push/no-PR authorization.

Disk 9.4 GiB before/after, cleanup 0 bytes. No push, PR, merge, Host modifications or production data operations. Next B09 fixed AR-S P0/P1/P2, using actual Root Agent authority rather than substituting Workflow semantics.
