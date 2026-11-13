# Limited coverage measurement correction

Status: design awaiting independent confirmation; no implementation or acceptance implied.

The completed common-source run at 08d236bb6766dd54547fbf8802a168a610878f13 passed every scheduled gate except coverage: 31,973 / 51,790 statements, 61.74%, below the unchanged 70% threshold. Original logs and profiles remain in issue107-ea1-final-coverage-initial. No ledger migration has occurred.

## Actual diagnostic

At d9353623a503739c9b4bf0fe34cbe986278830f9, the Core module was executed with its original ./... package set and -coverpkg=./.... Compared with the original profile, both have exactly the same 16,116 unique source blocks and 23,607 statements. Coverage changed from 9,954 to 18,650 covered statements. Conformance helpers changed from 0 to 8,052 / 10,251 despite an unchanged denominator. The instrumented profile contains 425,676 repeated rows from test binaries; summing rows would incorrectly count 647,713 statements. This diagnostic is not the full gate or an acceptance verdict. Raw profile is preserved compressed, with decompressed checksum.

## Closed implementation scope

1. Add -coverpkg=./... only to each existing module coverage command. Keep its original ./... package/test scope; no third-party instrumentation or exclusions.
2. Extract a pure profile reducer. Identify blocks by the entire source path and complete start/end line and column range. Across all profiles, union whether each block executed. A zero-count block remains in the denominator. Repeated blocks count once. Different paths or ranges never collapse.
3. Require the same statement count for identical block identities. Conflicts or malformed profile/count data fail closed rather than silently shrinking the denominator. Preserve the current summary fields, percentage precision, threshold override validation and default minimum 70.
4. Unit regressions cover duplicate zero/positive union, repeated all-zero blocks, distinct paths/ranges, inconsistent statement counts, and malformed counts. Include the test in the existing mandatory metadata gate.
5. No production Go changes, coverage exclusions, threshold reduction, or removal of conformance code.

## Validation and final SHA

Obtain independent design confirmation before implementation. Freeze the limited implementation, independently review its exact delta, then run reducer units and actual make coverage first. Preserve full raw profiles (compressed if needed), summary, commands, source and hashes. Compare unique source/block/statement sets to the old profiles; do not accept a reduced denominator without explicit evidence of a real source change (none intended here).

If coverage succeeds, run every other scheduled final gate again at that same frozen SHA: mandatory check-no-race, twelve full module race runs, fixed matrix repetition requirements, security, integration including the original process crash, OTel, beta contract, and managed repetitions. The earlier 08d236bb green logs remain historical evidence only; they are not relabeled as the new SHA. Final 138-cell mapping and independent durable signoff follow actual successful commands. H-FROZEN six cells, WP-GAOGE and unauthorized remote PR-head checks remain blocked and overall release NO-GO.
