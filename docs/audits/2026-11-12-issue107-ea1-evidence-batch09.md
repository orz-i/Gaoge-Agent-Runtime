# Issue 107 EA-1 batch 09: Root admission and unknown outcomes

Local-only negative evidence, not acceptance or release GO.

Source: `2545aa7b6eda95d193ecd549f29f1aecc387d108`.
Scope: exactly EA1-AR-S-P0/P1/P2. Ledger: 70 PASS, 3 FAIL, 71 UNRUN. Prior 70 PASS records retain their original SHAs; no final-SHA certification is claimed.

## Actual findings
- P0 FAIL: after Create, an independent owner wins the native claim. Original Start correctly loses admission but has already invoked arbitrary Host RunMiddleware once. Both Model and Host callbacks must remain zero before admission. The final assertion explicitly checks Host count zero; earlier narrow green P0 logs are diagnostics only.
- P1 FAIL: Host middleware is entered before authoritative ownership. Independent Cancel succeeds and changes state. A second real cutpoint after native Acquire but before Model also allows Cancel to revoke ownership. These violate frozen EA03/G01, not newly added requirements.
- P2 FAIL: Host synthetic external send followed by error is replayed by fresh Resume (1 to 2). Model physical response with receipt-write failure is replayed after real native lease expiry (1 to 2), without authoritative no-effect proof. Tool unknown recovery correctly returns ErrToolOutcomeUnknown without redispatch in all four backends. Tool success does not make the entire P2 cell pass.

## Evidence and limitations
Final logs, commands, source and checksums are in `evidence/issue107-ea1-b09/`. Twelve directed phase/backend commands exit 1 with the expected invariant failures. Native PostgreSQL executes each test three times with independent connections; no skip is treated as coverage. Native SQLite P2 actually closes/reopens. Memory and auxiliary GORM use fresh runner ownership, not persistent-process restart proof. Three module linters and six checker regression tests exit 0. Inventory succeeds; strict validation intentionally fails.

All fixtures here use native RunExecution. Legal nonnative configuration is not certified. No Root production fix was made. No full-module green run is claimed on this negative-test source. Last full four-module race result remains B06 source, not this SHA.

The initial source `2bf48748548dcd34092e87aecc26c9ec616f556c` and intermediate `d42ba54043801a9b410b13546bf156535cd4a30b` logs are preserved in separate diagnostic directories. Initial Tool precondition failure was a fixture defect: actual prior Model receipt is Consumed, not Completed. It is not evidence of Tool replay. Intermediate source corrected that and faithfully forwarded Store capabilities, but still lacked the P0 zero-Host assertion. Final source corrects that omitted same-cell assertion rather than weakening the contract.

## Evidence checker correction
FAIL may reference genuine failed or race logs while retaining source SHA and checksum requirements. PASS, N_A_APPROVED and UNRUN cannot borrow those logs. Regression coverage includes FAIL allowed, PASS/N_A/UNRUN rejected, damaged checksum rejected, invalid/wrong SHA rejected, race failure handling, and strict FAIL rejection. This records failures truthfully and does not relax acceptance.

## Review and next step
Independent reviewer identified the initial Tool fixture issue and P0 missing zero-Host assertion; both were corrected before final evidence. Root admission plus settlement requires one bounded unified design review before production changes. Preserve ExecutionLease, ModelInvocation and ToolDispatch contracts; do not substitute Workflow semantics or permit UNKNOWN replay by TTL.

Disk after batch: 9.1 GiB available, versus initial 9.9 GiB. No cache deletion performed. No push, PR, merge or WP-GAOGE changes. Remote PR-head gate remains outside current authorization. Overall WP-RUNTIME and release remain NO-GO.
