# Issue #107 EA-1 — WP-RUNTIME Evidence Batch 01

**Date:** 2026-11-12
**Status:** local baseline evidence, **NOT WP-RUNTIME GO**
**Tested Runtime source SHA:** `a6532c705cbe2d2403d3c2522391f1b3b6bd7541`
**Spec:** `docs/issue107-execution-authorization-contract.md`
**Fixed matrix:** `docs/issue107-execution-acceptance-matrix.md`
**Per-cell verdicts:** `docs/issue107-execution-acceptance-ledger.csv`
**Original Source code:** unchanged since `c17eeb49cc1d95b39f16c796edcd403d9b0a5ec2`; only EA-1 governance docs were added at `a6532c7`.

## Actual results

| Classification | Count | Meaning |
| --- | ---: | --- |
| Independently checked, cell-level local `PASS` | **11** | Concrete stored log, exact SHA, per-entrypoint assertion and Store backend reviewed |
| `UNRUN` with some existing evidence recorded | **21** | The provided test proves only a subset of the frozen cell's timing/identity/backend vectors |
| `UNRUN` without cell-specific evidence | **112** | No cell-level verification has been executed or attached yet |
| N/A approvals | **0** | N/A is not inferred from a label |
| Overall | **NO-GO** | 133 cells not approved; 47 explicit coverage gaps (G-codes) remain in the frozen matrix |

The `PASS` values apply **only to the exact test SHA above**. They are not a release-level PASS for a future changed code head or an automatic claim that the entire EA-01..EA-12 rule set is satisfied. The final reviewed PR head requires rerunning all applicable matrix cells.

### 11 cell-level signed outcomes

| Cell | Exact verified boundary |
| --- | --- |
| `EA1-WR-S-P2` | Original Root Workflow external Pending leaves a durable physical Grant; direct Cancel and subsequent Resume cannot dispatch it again |
| `EA1-WR-R-P1` | Concurrent same-version Root Workflow Resume enters one original external Effect; second worker rejected while the first callback is active |
| `EA1-WR-R-P5` | Root Resume physical callback defeats competing terminal Cancel; original worker is allowed to finish |
| `EA1-WC-S-P1` | Child Workflow Start Effect holds the parent terminal fence while external callback is physically active |
| `EA1-WC-S-P5` | Parent Cancel fails busy during child Effect but succeeds after trusted completed receipt and original Grant retirement |
| `EA1-WC-W-P5` | Quiescent Child Workflow Wait can release Grant; terminated parent denies later Wait continuation before any external Effect |
| `EA1-S-CTL-P1` | Root/child physical Grant denies unbound self/ancestor Apply and Cancel during original physical ownership |
| `EA1-S-CTL-P2` | Direct Child self-Cancel is denied while a genuine Workflow Effect callback is physically in-flight; Store self/ancestor fencing also passes across backends |
| `EA1-S-CTL-P5` | Rejected child control keeps the original Agent ExecutionLease epoch and physical Grant unchanged |
| `EA1-AC-C-P1` | Child Agent with ExecutionRequired lease refuses self-Cancel while original physical Grant is active |
| `EA1-AC-C-P5` | Child Agent physical cancellation path retains the original lease and Harness owns the correct child fence |

The full backend and invocation mapping for **each** row is in the CSV ledger, not inferred from this table.

### Test backends and execution facts

- **Memory:** actual Root/Child Workflow execution suites plus direct self-fence conformance.
- **GORM PostgreSQL adapter:** actual Root/Child Workflow execution against its GORM-based SQL fixture. This adapter suite is **not** a substitute for native PostgreSQL testing.
- **Native SQLite v3:** same Root/Child state machine plus a real close/reopen ACK-loss test.
- **Isolated PostgreSQL 16:** independent live connections and row locks, the same physical Root/Child suite, self-fence conformance and **five independently constructed stale expected-revision races**.
- **Feature-level tests:** Root concurrency, unknown external Pending, attempt-local no-effect, Child Wait, physically blocked parent/child cancellation, original Agent approval/recovery, and ACK loss.
- **Baseline commands:** all four Runtime modules' `go test -race -count=1 ./...` PASS; the Core run included **26 packages**. All four lint scopes report `0 issues`. `node scripts/check-beta.mjs` passes.
- **External Go consumers:** Runtime's compile/test/Race PASS. Harness's separate external consumer typechecks and runs successfully but reports **[no tests to run]**; treat that result strictly as compilation/ABI evidence, not a runtime assertion.

Every directed test log and global gate output is stored in the `evidence/issue107-ea1-b01/` subdirectory (including `COMMANDS.tsv`, `ORIGIN.txt`, and `SHA256SUMS.txt`). Do not treat `ok ... [no tests to run]` as evidence of an executed security scenario.

### 21 partial cells: specifically why UNRUN was retained

A few representative mapping problems discovered *within the frozen matrix*, without expanding its scope:

- `EA1-WR-W-P0`: frozen `E08` anchors a **Child** Workflow Wait test, not a Root Wait; a Root-specific pre-admission proof is still required.
- `EA1-WR-C-P1`: the compensation test's negative assertion does not independently require that an original Grant is live and that competing Cancel returns `ErrChildExecutionBusy` during the complete undo callback.
- `EA1-WC-C-P1`: `E07` is a Root Cancel/compensation test, not the corresponding Child compensation entrypoint.
- `EA1-WR-S-P3` / `EA1-WC-S-P3`: final successful receipt + retired Grant is observed; no controlled interruption between **receipt commit** and **settlement marker** is yet demonstrated.
- `EA1-WR-Q-P4` / `EA1-WC-Q-P4`: SQLite persisted ACK-loss recovery exists; equivalent Root/Child Workflow recovery in a real PostgreSQL restart cohort and negative foreign-owner vectors remain open.
- `EA1-S-CTL-P0`: stale Revision is demonstrated in a true two-connection PG transaction, but other P0 identity/tenant/foreign-origin variants are not certified by this one test.
- `EA1-AR-S-P0`: Root Agent duplicate-ID rejection before Host middleware is proven, but the entire P0 source/Actor/frozen policy matrix is not.
- `EA1-WR-R-P2`: the attempt-no-effect regression runs on Memory and is not a complete cross-Store receipt/restart matrix.
- `EA1-AR-R-P2`: the available ToolTimeout test does not explicitly demonstrate the public Root Agent `Resume` entrypoint at the required physical timing point.

All remaining partial items have exact notes, observed backends and test commands in the ledger. **They remain UNRUN deliberately.**

## What this batch did NOT verify

This batch does not close all 14 fixed gap targets, does not prove Root Agent Host middleware/ExecutionLease admission before every physical request, does not repair Gaoge ProviderReplay, does not align the Gaoge SDK gitlink and does not run PR-head GitHub CI. The original Gaoge #69 MCP test remains a separate BLOCKED work package.

**Next fixed-scope priority:** close the P0 origin/Attempt vectors, the P3 receipt-before-Mark interruption, and P4 PostgreSQL Workflow recovery using existing gap IDs G03/G08/G09/G12/G17 (only as applicable). Do **not** launch a new open-ended general audit.

## Reproducibility / integrity

```bash
python3 scripts/check-issue107-matrix.py
python3 scripts/check-issue107-matrix.py --strict   # expected FAIL / NO-GO

cd docs/audits/evidence/issue107-ea1-b01
shasum -a 256 -c SHA256SUMS.txt
```

Full test commands and backend labels are in `COMMANDS.tsv`. The real PostgreSQL DSN was deliberately redacted from the committed manifest, and only an isolated local container was used.

**Exit verdict for this batch:** **11 baseline matrix cells locally PASS, 133 remain UNRUN, WP-RUNTIME overall NO-GO**.
