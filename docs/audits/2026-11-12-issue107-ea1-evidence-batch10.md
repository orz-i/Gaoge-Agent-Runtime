# Issue 107 EA-1 batch 10: Child reconciliation boundaries

Source `14c436216137c9a8ff55c0096664686ee4e77aa4`; only EA1-AC-Q-P0/P2 changed from UNRUN to PASS. Ledger is 72 PASS / 3 FAIL / 69 UNRUN. Prior 70 PASS and B09 three Root FAIL records remain unchanged, including their original SHAs. Overall NO-GO.

## Actual assertions
P0 exercises the actual public Child Agent Reconcile entrypoint with nil runner, empty/missing Run, wrong kind, Root instead of child, corrupt Agent state, and clean child without a Grant. Every rejection preserves the original full self and parent snapshots, immutable source and original Grant, with no physical callback. Reconcile accepts only runID: it does not accept caller identity, model route, attempt or expected revision. This batch does not invent caller-authentication coverage.

P2 obtains the actual Child physical Grant before a synthetic Host external send followed by error, and before actual Model/Tool responses whose receipt writes fail. A fresh runner's Reconcile remains typed Busy, retains the exact Grant and full original state, and performs no Host, Model or Tool redispatch. The Model/Tool cases wait for actual native Store lease expiry; the Host callback is after child Grant but before native Acquire and does not claim native TTL coverage. Model physical requests are checked against the original durable invocation, full request, hash and attempt before dispatch; Tool original consumed Model receipt is retained.

Native SQLite closes the original database and opens it again. Native PostgreSQL uses an independent connection pool and repeats both suites three times. Memory and auxiliary GORM create fresh runner ownership on retained Store state, not a claimed persistent-process restart. This proves Reconcile, not Child Resume or Root recovery.

## Evidence
`evidence/issue107-ea1-b10/` contains seven final source-tagged logs, exact commands and SHA256 manifest: four directed backend commands with race checking and no skipped tests, plus three module linters, all exit zero. Inventory checker succeeds. No full-module result is claimed on this source; final unified-source acceptance remains outstanding.

Two prior fixture diagnostics are preserved separately. At source 54985e08, PrepareRun entered the faulting Host middleware while creating unrelated negative-input fixtures. At source cb3d5887, isolated setup was corrected but actual child Host Start lacked its mandatory read-only StartControlPreflight. Neither is a production defect or accepted evidence. Final fixture prepares unrelated inputs without Host middleware and supplies exact frozen request plus parent equality preflight for actual Start; safety assertions are unchanged.

Independent source and temporary evidence review accepted final 14c43621: both fixture corrections were verified, all four backend results and three linters passed, PostgreSQL executed both suites three times. Durable evidence signoff follows separately.

No Root production edits, push, PR, merge, runtime data migration or WP-GAOGE changes. Disk available before batch 9.2 GiB and after 9.0 GiB; no cache deletion. Another Go server uses shared cache, so it was not cleared or interrupted.

Durable independent signoff: c56297a66e583149a8eaf1dc6262762d5eb05b15 was checked read-only. All seven checksums/source labels/zero exits matched; PostgreSQL ran both suites three times without skip/fail; no Go drift occurred. Exactly the two specified rows changed, all prior 73 recorded PASS/FAIL rows were unchanged. This is B10 evidence acceptance, not Root implementation or full-matrix GO.
