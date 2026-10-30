# Hosted Tool anonymous terminal accounting — independent local hard-cut audit

- **Date:** 2026-10-30
- **Protected base:** `main@a149942e776e4d47e3d9883cf68558b1ce247383` (merged PR #77)
- **Governance branch:** `governance/hosted-anonymous-receipt-identity-hardcut-20261030`
- **Implementation/architecture Head:** `165c7525b95515696df8b0744e5330d1a52e991b`
- **Scope:** Harness shared-budget Hosted Tool receipt identity and read-only historical usage attribution. No Core Provider protocol, Kernel/SQL schema, execution class or new ledger.

## Reproduced P2 accounting defect

The initial `hostedReceiptTransfers` branch for `call.ID == ""`
rejected unfinished native events but **counted anonymous terminal
events as independent physical calls**. Two identical anonymous
`completed` or `failed` observations each added a Hosted Tool charge.
They had neither a stable Provider Call ID nor a cross-response
deduplication identity, so the extra charge was not verifiable.

The old read-only `ToolUsage` projection independently substituted
`(ModelReservationID, array index)` for an absent Call ID, then
assigned the manufactured observation to `HostedCallsByKey`. It could
present historic anonymous charges as if each mapped to a unique
remote execution.

Deterministic new tests were first run against the original `main`:
- `TestHostedReceiptWithoutIdentityRejectsTerminalCounts` failed for
  `completed`, `failed`, `error`, `cancelled` and `canceled`: two
  indistinguishable anonymous terminal facts were counted as two calls.
- `TestAnonymousHostedTerminalDoesNotSettleModelOrReleaseNativeQuota`
  failed for duplicated completion, failed completion, mixed
  anonymous+identified facts and whitespace-only ID: Provider sent
  once, but Harness returned success instead of
  `budget.ErrInvalidUsage` and prematurely charged the anonymous
  native work.
- `TestHistoricalAnonymousHostedReceiptUsageIsIncompleteWithoutSyntheticPerKeyCounts`
  failed: a historic settled Model receipt with one identified and
  two anonymous terminal observations was projected as three
  verified per-key calls with `Incomplete == false`, although two
  had no remote execution identity.

The pre-fix test log (local, untracked evidence):
`/tmp/gaoge-hosted-anonymous-prehardcut-red-20261030.log`.

## Minimal correction

1. `hostedReceiptTransfers` now rejects **all** missing/whitespace-only
   Provider Call IDs, terminal or unfinished, before `SettleWithReceipts`.
   The original Model reservation remains `Dispatched + Held` with its
   full native allowance. The same frozen Model request cannot be
   physically resent after restart. Mixed named/anonymous receipts
   fail atomically; already parsed named facts are not partially
   committed or silently charged.
2. Existing genuine Provider Call ID handling is unchanged:
   same-response interim/terminal collapsing, cross-response
   unfinished-to-terminal transition, settled-reservation
   deduplication and quota consumption remain authoritative.
   An independent later **final-only text Model** request is still
   possible with zero fresh Hosted Tool allowance while the old
   unknown native work remains Held.
3. Historical settled anonymous totals are not mutated or refunded.
   `ToolUsage.HostedCalls` retains the existing ledger numeric
   count, but the read-only `HostedCallsByKey` omits anonymous
   observations and sets `Incomplete == true`. An array index,
   Model response ID, ToolKey or output hash is **not** a Provider
   execution identity.

No new public Runtime protocol, synthetic Provider ID,
`FeatureDriver`, schema number, SQL DDL, budget store or
compatibility path is introduced.

## Corrected red/green proof

- The original failing memory regression matrix now passes, including
  duplicate named/anonymous mixing and whitespace ID.
- Existing identified Hosted Tool terminal de-duplication and
  restart/continuation/remaining allowance tests pass.
- Repeated `go test -race -count=12` of new negative and existing
  positive cases passed.
- `TestRealPostgresAnonymousHostedTerminalStaysHeldAfterRestart`
  passed on actual PostgreSQL using two separate connection handles
  and the same isolated SQL schema. It observes a held/dispatched
  original Model reservation, no individual synthesized hosted
  receipt, unchanged quota after restart, and no second Provider
  execution. The real PostgreSQL/Redis integration also cleaned all
  temporary Docker containers/network.

## Residual boundaries and upgrade policy

The SDK cannot inspect or deduplicate an unnamed physical effect in
an external Provider. A terminal `Status` alone is not an identity,
so neither an accounting zero charge nor an automatic refund is
claimed; the original dispatched budget remains unresolved.
External Provider billing and operator accounting are Host-owned.
Historical anonymous settled numeric totals remain durable and may
not be independently verifiable from SDK data: `Incomplete` is a
truthful diagnostic, not an automatic correction of past invoices.

This rule applies when Harness shared budget accounting is configured.
A direct Model `Response` without a shared Harness ledger remains
Provider-owned data; this governance does not add a new Core
`model.Client` wire validation protocol. Before activating a
pre-stable beta.24 build, the Host must inventory outstanding
anonymous native effects and drain/reconcile/quarantine those it
cannot identify. Never mint fake Call IDs, reexecute the original
Model invocation or treat a terminal string as a reconciliation
receipt.

The next Beta release and any published root+11-module tag cohort
require independent authorization and published consumer verification.
Local acceptance does not authorize merge, tags or publication.

## Final implementation-Head acceptance

All five full local gates were re-run on exact **source-and-doc Head**
`165c7525b95515696df8b0744e5330d1a52e991b`.
A later audit-only commit changes no executable code.

| Gate | Result | Exact-source evidence |
| --- | --- | --- |
| `make check` | **PASS (exit 0)** | All Go modules' source tests/Race/Lint, formatting/tidy/vet, TypeScript test/lint/typecheck/build, evaluation and benchmark smoke, and clean external Go/TS consumer tests |
| `make coverage` | **PASS (exit 0)** | Go statements **73.66%** (minimum 70.00%). TS statements **88.08%**, branches 76.47%, functions 92.85%, lines 92.13% |
| `make security` | **PASS (exit 0)** | Zero detected reachable Go code vulnerabilities, npm high-severity audit clean. **Three required Go dependency modules still have vulnerability notices**, with no detected callable vulnerable paths |
| `make integration` | **PASS (exit 0)** | Actual PostgreSQL 16 and Redis 8, real cross-process recovery, cancellation and continuation E2E; new anonymous terminal two-connection test **PASS**; test containers/network removed |
| `make integration-otel` | **PASS (exit 0)** | Actual pinned OTel Collector 0.149.0 trace/metric E2E and container/network cleanup |
| Focused negative/positive Race tests | **PASS** | 12 repeated race-detector executions for anonymous terminal and named Hosted Tool recovery, without duplicate native charges |

Logs retained locally on the development machine:
- `/tmp/gaoge-hosted-anonymous-final-20261030.log`
- `/tmp/gaoge-hosted-anonymous-final-20261030-check.log`
- `/tmp/gaoge-hosted-anonymous-final-20261030-coverage.log`
- `/tmp/gaoge-hosted-anonymous-final-20261030-security.log`
- `/tmp/gaoge-hosted-anonymous-final-20261030-integration.log`
- `/tmp/gaoge-hosted-anonymous-final-20261030-integration-otel.log`
- `/tmp/gaoge-hosted-anonymous-prehardcut-red-20261030.log`

**Local result: GO for pushing this branch and seeking independent
PR final-Head merge admission review**. This is **not** a merge
authorization or beta.24 release approval. GitHub protected required
checks, target freshness, a no-P0/P1 code audit and explicit user
authorization remain separate requirements.

Two original user-owned, untracked OpenAI probe scripts were preserved
unchanged; no beta.24 tags were created, and no branch was pushed by
this local implementation.
