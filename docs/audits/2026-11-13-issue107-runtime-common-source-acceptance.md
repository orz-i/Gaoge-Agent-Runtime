# Issue107 EA-1 — common-source Runtime local acceptance

Status: independently accepted for the 138 Runtime-owned cells at the exact tested source below. The fixed inventory has 144 cells. This report concerns 138 Runtime-owned cells only; six H-FROZEN cells and WP-GAOGE remain a separate work package. It is not a release/remote-CI GO.

## Exact source and evidence

- Tested source: `0a4009a8171f1726edaea895798d551051753400`.
- Evidence/mapping proposal: `b5fbb8db90929ab12f1e73ae04736ec43fb14737`, `docs/audits/evidence/issue107-ea1-final-accepted/`.
- A single persistent runner started at 2026-11-13 07:41:54 UTC and completed before the 08:43:13 UTC read-only check. No interruption/restart or source modification occurred during this run.
- All 35 command groups exited zero. `COMMANDS.txt`, per-command source headers/exit records, `MANIFEST.json` and `SHA256SUMS` describe the exact run. Log bytes, including emitted CRLF, are preserved without Git text conversion in this evidence directory.
- Every original failure and intervening diagnostic remains preserved. No historical PASS was merely relabeled with the final SHA.

## Actual completed gates

The run includes reducer regressions, schema-rejection smoke, dependency presence, all-module lint, complete `make check-no-race`, all twelve modules' full race suites, fixed native-PG/Host-PG/attempt repetition obligations, actual `make coverage`, security, integration including the original process-crash point, OTel, beta contract, and managed recovery repetitions across Memory, SQLite, PostgreSQL, Host stores and process recovery.

Go coverage is 40,706 / 51,790 statements (78.60%, unchanged 70% minimum); TypeScript statement coverage is 88.04%. The measurement repair instruments the original module scope with `-coverpkg=./...` and unions duplicate complete source/block identities. All twelve unique source/block/statement-count maps exactly match the old profiles. No package, zero-coverage block or denominator was removed. The prior 61.74% failure remains in `issue107-ea1-final-coverage-initial`; original instrumented profiles are compressed with raw-content hashes. Conflict/invalid data fail closed and the eleven reducer tests are part of metadata.

## Matrix reconciliation

The exact-test verifier reports 138 mechanically replayed cells and six independent UNRUN cells, with no missing or pending requirements. It checks actual RUN/PASS and required repeat counts from completed same-source logs, not merely top-level suite names. Per-cell semantic limitations and earlier bounded independent source reviews remain necessary; the verifier does not invent callback authority or infer one phase from another.

The independently reviewed reconciliation updates source/evidence for the original 132 PASS cells and closes the six MW-NEXT cells, including the retained historical P2 FAIL. It preserves all anchors, entrypoints, inventory classifications and historical notes except the explicitly reviewed six MW notes and 54 stale SQLite-v4 backend labels corrected to current v5. H-AC's Host SHA refers to first-party Runtime Harness in this repository, not Gaoge. All six H-FROZEN CSV rows remain byte-for-byte unchanged.

## Verified behavioral boundaries

- Root physical admission, durable callback outcome and receipt settlement stay source/owner/attempt-bound. Unknown external effects are not replayed merely because a lease expired or a provider labels an error retryable.
- Child whole-callback journals are an additional necessary retirement condition; clean receipts alone cannot erase swallowed inner callback failures. Start, approval and resume use their actual bound context. Existing receipt/Mark/Finish requirements remain.
- Managed continuation is limited to an actual first-party durable safe point. Kernel PARK precedes immutable Host RESUME/CANCEL selection and ACK-gated enable/cancel. Fixed configured Host authority, exact source/mode proof, no callback before leaving the safe point, and ordinary opaque unknown restrictions remain enforced. It is not general middleware self-certification or arbitrary unknown recovery.
- Provider tests distinguish a real pre-dispatch rejection (zero HTTP sends, exact binding and verified continuation) from requests that may have been sent. The example's v2 known-receipt continuation semantics are explicit; its original receipt-ACK loss point, fourth model step and oracle remain, alongside opaque-unknown negative coverage.
- Memory is not persistent restart. Auxiliary GORM cases are not native PostgreSQL concurrency evidence. Native SQLite reopen and PostgreSQL independent-pool details remain cell-specific; process-kill recovery is claimed only by the actual process integration tests.
- Public interfaces only establish identities they actually accept. There is no invented caller/route authentication, no borrowing of Agent lease/shared-budget tests for Workflow entrypoints where those objects do not exist, and no claim of Gaoge original-host frozen-cohort acceptance.

## Remaining work and operational boundary

Six H-FROZEN cells need the separately resumed WP-GAOGE work package and exact Host source evidence. Required remote PR-head/platform gates remain unexecuted under the current local-only authorization. No push, PR, merge, production migration or real-data deletion was performed. The full strict 144-cell checker must continue to report NO-GO until those independent requirements are satisfied.

At completion the Mac had approximately 5.8 GiB available and no validation process remained. The only cleanup during this final recovery was 36,820 KiB of this task's verified-unused regenerable incomplete dependency backup; shared caches, source and all evidence were retained.

## Independent final acceptance record

The independent reviewer accepted fixed evidence/mapping proposal b5fbb8db90929ab12f1e73ae04736ec43fb14737 for the limited 138 Runtime-owned cells. The review independently verified 100 checksum items, all 35 command logs and exits/source identities, 13,395 actual mapping requirements, all twelve decompressed profile hashes and unchanged full block/statement sets, 54 v5 label corrections, six MW notes, and byte-identical H-FROZEN rows. Two observed SKIP entries are subprocess helper setup only and were not counted toward required coverage. No source drift was found.

Commit accfda61ab2a89d452aaa27ab1a1f5588559244d additionally supplies the checker-required ORIGIN.txt and SHA256SUMS.txt names without altering any test log or candidate row; all 102 fixed-blob hashes were checked. The candidate passed the unchanged inventory checker before application. Subsequent real-ledger inventory/unit/strict checks are recorded separately, and strict NO-GO is expected for the six independent Host cells.
