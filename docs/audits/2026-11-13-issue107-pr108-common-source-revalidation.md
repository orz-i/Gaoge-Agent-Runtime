# PR108 — corrected-source complete local revalidation

Status: independently accepted for the 138 Runtime-owned cells at the corrected source below. This is not full 144-cell GO and does not authorize Host work or merge.

Tested source: `2d216518cfe48bc9e2ea6b02beb7733c246376a7`.
Evidence: `docs/audits/evidence/issue107-pr108-final-accepted/`.

The single persistent runner began at 2026-11-13 09:44:07 UTC and completed before the 10:46:40 UTC read-only check. It was not restarted. All 36 command groups exited zero: frozen dependency restoration, schema rejection, coverage reducer, actual coverage and block identity comparison, all-module lint, complete check-no-race, all twelve module full-race suites, fixed PG/HostPG/attempt repetition requirements, security, integration, actual OTel, beta contract, managed Store/Host/process recovery repetitions and final clean source check. Available space at completion was about 5.4 GiB.

Coverage is 40,726 / 51,801 statements (78.62%, unchanged 70% minimum). Compared with the previously accepted source, source/block differences are permitted only in the exact four changed Go files. Every unchanged file retains its complete block and statement identities; zero-coverage blocks remain in the denominator. This is not a claim that the changed files have unchanged denominators.

The exact-test replay verifier finds 138 mechanically covered Runtime cells and six independent UNRUN H-FROZEN cells, with no missing or pending requirements. The candidate changes only their test SHA, commands and evidence, plus the same-repository H-AC Host SHA. Existing per-cell notes, backend limitations, expected outcomes and all six H-FROZEN raw lines remain unchanged. The formal ledger now exactly equals the independently accepted candidate.

The original PR-head integration failure and deterministic sub-microsecond reproduction remain in their original archives. The correction seals the managed safe point to the actual PostgreSQL persisted-shape aggregate in the original transaction; it does not relax digests, authority, unknown fencing or receipt checks. The four-Store directed supplement is explicitly historical and excluded from the 36 same-source gate counts.

At this exact tested head, draft PR #108 has all five configured required contexts successful. CI run 43403966671 succeeded. The remote OTel job is skipped by its existing event condition; actual local OTel ran successfully here. The stored GitHub snapshots describe this exact head only, not any later documentation commit. Final publication will require checks for its final PR head again. No merge, auto-merge, production migration, data deletion or Gaoge implementation occurred.

The old local 0a4009a acceptance remains historical evidence, not a substitute for this rerun. All original failures are retained. Full strict 144-cell acceptance remains NO-GO because the six original-host H-FROZEN cells are independent and unexecuted.

## Independent fixed-evidence and application record

The independent reviewer accepted fixed commit `f0c6462defb13d688a6d7b16e5186f3fd6106b91`: all 117 checksum entries, 36 exact-source gates, 13,395 actual test mapping requirements, coverage union and unchanged-file block dictionaries, and all six unchanged H-FROZEN rows were verified. No non-document source drift exists. The separately labelled historic supplement is not a same-source gate.

A publication safety concern was investigated before any archive commit was pushed. An initial regex mistook the Python runtime DSN concatenation for a complete credential literal. Independent AST reinspection confirmed it reads the private password file and builds the DSN at execution time; the reviewer explicitly retracted this false positive. A private scan of all 71 new unique blobs found zero matches for the active password, its URL encoding or Base64. No secret was printed or committed, and no unnecessary second rotation occurred.

The applied-ledger inventory/unit/strict check records are retained separately. Strict NO-GO is intentionally preserved for the six independent Host cells. The original 0a4009a report remains a historical record; this report supersedes its current-source assertion, not its archived evidence.

The first applied-ledger checker rejected the archive ORIGIN prose header rather than a test result. That exit-1 record is preserved in `issue107-pr108-ledger-checks-initial`. The header was corrected to the existing checker-required `SOURCE_SHA=` format; no log, test source or candidate row changed.
