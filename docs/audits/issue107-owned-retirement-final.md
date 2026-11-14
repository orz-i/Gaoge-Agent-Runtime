# Owned retirement / continuation race closure

Tested source: `a279dceb1b2b1752f12066d4051550bf3f79e657`. Base: merged main `c79d2eea87952bc09d65cee2c4657215873c1262`.

## Result

One sequential, source-locked runner ran from 2026-11-13 15:12:06 UTC through 15:36:35 UTC. All 33 commands exited 0: formatting, metadata, unrestricted lint across 12 modules, six compile checks, original reproduction and Root/P4/wrapper smoke, actual AR-Q Memory/native SQLite/GORM/real PostgreSQL smoke, three SQL backend directed race suites with count=3, the two actual continuation tests with count=20, all 12 module full-package race suites, and all-module vet. Commands and original logs are in `evidence/issue107-owned-retirement-final/`; SHA256SUMS covers the retained bytes. No result is relabeled from an earlier source.

## Safety and compatibility

Finish atomically preserves proof-bound retirement; ACK atomically validates original source/native identity and releases only the originally owned lease. Borrowed leases are not released. Ordinary missing Finish remains a conflict. Fresh public run-ID recovery selects only the unique currently matching RETIRED receipt, never an old epoch or historical ACK. SQL Finish rechecks original native identity in its transaction; expiration alone does not invent authority. Genuine committed Mark ACK loss can proceed through the original marker-bound Finish, while a truly uncommitted marker cannot retire or release. Managed T4 is closed only by its existing private proof before Finish verifies it.

The owner explicitly approved the breaking custom-Store requirement: implement or transparently forward OwnedRetirementStore; missing capability fails before Root admission/physical dispatch. This is not backward-compatible behavior for old adapters. Built-in Memory, native SQLite and PostgreSQL plus in-repo wrappers are adapted. SQLite schema v6 and migration/strict rejection tests are covered.

## Independent review

Independent AI-assisted read-only review covered the exact state machine and source deltas, not a human audit. Findings R1 historical receipt filtering, R2 SQL native identity, and sustained Mark ACK loss were repaired and reviewed at b819af31. Subsequent fixture/lint deltas through a279dceb were reviewed with no remaining source finding. Final durable evidence review remains pending until its separately recorded signoff; source review alone is not execution acceptance.

## Original failures and AR-Q distinction

The c79 deterministic negative, initial implementation failures, compile/lookup/lint diagnostics and full 00d0 AR-Q diagnosis remain in separate source-tagged evidence directories. The original AR-Q single committed Mark ACK loss now has an explicit successful retirement/ACK positive for both Model and Tool. Its paired reopen case adds a clearly identified second fault: exact original grant Finish held before commit. This pair preserves complete source/receipt/completion/authority and zero-redispatch checks; no production expectation is weakened. The latest 88e78b6a lint failure is separately retained in issue107-owned-retirement-arq-lint.

## Remaining gates

This report closes the bounded local hotfix regression, not all 144 EA-1 cells. The historical 138-cell ledger is not silently migrated; six H-FROZEN cells remain blocked and Browser/Host work is preserved separately. New exact PR-head required CI is still pending publication. No new PR merge or deployment is authorized. Final documentation head differs from the tested source only by explicitly recorded evidence/report changes.

## Final independent durable signoff

An independent AI-assisted read-only reviewer accepted fixed evidence head `049f6b66ec46cf5f2213cd710d4432dc74c88094`, tested source `a279dceb1b2b1752f12066d4051550bf3f79e657`, after directly verifying all 72 final-manifest Git blobs, all 33 result/exit pairs and source tags. Actual target PASS records were checked for four AR-Q backends, each directed SQL backend target three times, each original continuation test twenty times, all twelve full-package race logs, unrestricted lint, compile and vet. SQLite's package selector also visits examples with no matching tests; the intended suites did actually run and pass. No new test run was performed by the reviewer.

The reviewer confirmed no Go/ledger/CSV drift between tested source and evidence head, preservation of all original failures, and accurate custom-Store hard-cut documentation. No remaining finding in this bounded hotfix review. This closes local hotfix regression evidence only: new exact PR-head required CI remains to be checked; six Host cells and the historical 138-cell ledger are not re-signed. No merge or deployment authorization is implied.
