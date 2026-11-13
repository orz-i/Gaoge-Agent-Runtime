# EA-1 B11 initial unified-source checkpoint

Source df8dad572c82d1a569dc89d28ff32bd14cad3cc5. This is recorded failure evidence, not a completed batch or acceptance. Ledger remains unchanged at 72 PASS / 3 FAIL / 69 UNRUN, with old source SHAs preserved.

Fifteen source-tagged logs and checksums in evidence/issue107-ea1-b11-initial contain four successful module compile passes, four directed backend failures, three lint results (PostgreSQL passes; core and SQLite have narrow lint findings), and four complete module race regressions that fail. The dedicated PostgreSQL directed run repeats tests three times. No skip substitutes for execution. No production database or user cache was modified.

## Concentrated initial findings
- Independent source review identified a dangerous outer admission-error fallback that could adopt another active grant sharing the same external lease. Only the Kernel exact newly generated Grant ID ACK-loss check is valid. A deterministic two-caller/same-lease regression is required.
- Start's failure return conflates admission denial before middleware with a callback that ran without next, dropping the already-created recoverable Run identity.
- An unnecessary callback-metadata CAS changes revision on already-settled paths, invalidating existing wakeup ExpectedRevision and strict no-mutation expectations. Do not relax dispatcher revision fencing.
- Legal pending Tool receipts without a Pending descriptor and deferred child progression are over-blocked. Recovery must persist each original authoritative receipt and perform only constrained non-dispatch progression, not discard deferred work or retire an unresolved grant.
- A callback returning after native lease expiry needs a constrained original-scope completion fact rather than an expired ordinary Apply. Mere clean-looking state or a receipt alone cannot certify post-next Host completion.
- Explicit-native approval must retain its original Running/zero-Tool return followed by separate Resume, rather than silently drive to Completed.
- Some fixtures omit new Store capabilities or inject revision changes through now-invalid unbound ordinary writes. Correct faithful capability forwarding and actual recovery-CAS fault boundaries, not production fences.
- Old tests that authorize cancellation, takeover or retry after an unproved external send must be separated from legitimate no-effect/known-receipt paths. Preserve typed causes, diagnostics, billing and original source assertions.
- Two narrow fixture issues: default automatic IDs are valid, so the explicit-native missing-ID negative cannot be copied unchanged; the low-level Memory approval checkpoint lacks required CreatedAt/ResolvedAt timestamps.
- Core lint reports an unused old helper and import grouping; SQLite reports a De Morgan simplification.

Root P1 controls, default/explicit external-owner reuse, and Host/Model/Tool unknown no-redispatch cases pass in the directed logs, but do not erase the failed cells or full regressions. PostgreSQL snapshot differences still require exact field diagnosis; they are not assumed to be Store corruption and no full-state assertion will be dropped.

All edits paused for one grouped diagnosis and coherent correction plan. Independent source review is still in progress at this checkpoint. No push, PR, merge, runtime-data migration or WP-GAOGE work. Disk after tests: 8.9 GiB available; shared cache not cleared.
