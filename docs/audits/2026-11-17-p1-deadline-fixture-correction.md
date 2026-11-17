# P1 Provider Transport prerequisite: deadline fixture correction

Date: 2026-11-17. Candidate correction; not a merge or production qualification.
Original PR head: `57c21e2a91f846e5c823bea7bcc3830c815359db` (PR #125).

## Observed CI failure

GitHub CI run `44053083806`, PostgreSQL/Redis job `133935469175`, failed in
`TestRealPostgresKernelControlStoreConformance/deadline-checks-store-time` at
`control_store.go:249` with `kernel run deadline exceeded`. The deadline was
calculated before the Store factory initialized/migrated PostgreSQL. The test's
0.75-second Run budget therefore included unrelated schema setup.

A new Memory regression deliberately spends 0.9 seconds inside that same Store
factory. Before this correction it fails at the original Kernel.Create boundary,
reproducing the reported failure without weakening a Store deadline check.

## Correction and scope

The conformance fixture now constructs the Store first and then calculates its
Run deadline. The early/late expiry, original revision, execution fencing and
post-expiry approval/allowance assertions are unchanged. The same correction is
applied to the adjacent approval/allowance deadline cases. No Kernel, Agent,
Harness, PostgreSQL Store or Provider Transport production implementation changes.

## Verification

- RED: `TestKernelControlDeadlineAfterSlowStoreSetup/deadline-checks-store-time`
  fails before the patch with the same Kernel deadline error.
- GREEN: Memory original `TestControlStoreConformance` plus the full slow-factory
  regression pass under `-race`.
- Native SQLite control conformance passes.
- Core module configured golangci-lint reports zero issues; focused vet passes.
- The 103 moved Transport Go files and its module implementation remain unchanged
  from the already tested extraction head.

The actual PostgreSQL rerun on the new candidate and remote PR-head CI remain
unverified. Local GitHub TLS/SSH connections are unavailable in this session;
a local correction must not be described as already pushed or CI-green.
