# PR108 — corrected-source complete local revalidation

Status: all scheduled gates completed; the 138-cell candidate awaits independent durable and semantic mapping review. This is not full 144-cell GO and does not authorize Host work or merge.

Tested source: `2d216518cfe48bc9e2ea6b02beb7733c246376a7`.
Evidence: `docs/audits/evidence/issue107-pr108-final-accepted/`.

The single persistent runner began at 2026-11-13 09:44:07 UTC and completed before the 10:46:40 UTC read-only check. It was not restarted. All 36 command groups exited zero: frozen dependency restoration, schema rejection, coverage reducer, actual coverage and block identity comparison, all-module lint, complete check-no-race, all twelve module full-race suites, fixed PG/HostPG/attempt repetition requirements, security, integration, actual OTel, beta contract, managed Store/Host/process recovery repetitions and final clean source check. Available space at completion was about 5.4 GiB.

Coverage is 40,726 / 51,801 statements (78.62%, unchanged 70% minimum). Compared with the previously accepted source, source/block differences are permitted only in the exact four changed Go files. Every unchanged file retains its complete block and statement identities; zero-coverage blocks remain in the denominator. This is not a claim that the changed files have unchanged denominators.

The exact-test replay verifier finds 138 mechanically covered Runtime cells and six independent UNRUN H-FROZEN cells, with no missing or pending requirements. The candidate changes only their test SHA, commands and evidence, plus the same-repository H-AC Host SHA. Existing per-cell notes, backend limitations, expected outcomes and all six H-FROZEN raw lines remain unchanged. The formal ledger has not yet been changed by this evidence proposal.

The original PR-head integration failure and deterministic sub-microsecond reproduction remain in their original archives. The correction seals the managed safe point to the actual PostgreSQL persisted-shape aggregate in the original transaction; it does not relax digests, authority, unknown fencing or receipt checks. The four-Store directed supplement is explicitly historical and excluded from the 36 same-source gate counts.

At this exact tested head, draft PR #108 has all five configured required contexts successful. CI run 43403966671 succeeded. The remote OTel job is skipped by its existing event condition; actual local OTel ran successfully here. The stored GitHub snapshots describe this exact head only, not any later documentation commit. Final publication will require checks for its final PR head again. No merge, auto-merge, production migration, data deletion or Gaoge implementation occurred.

The old local 0a4009a acceptance remains historical evidence, not a substitute for this rerun. All original failures are retained. Full strict 144-cell acceptance remains NO-GO because the six original-host H-FROZEN cells are independent and unexecuted.
