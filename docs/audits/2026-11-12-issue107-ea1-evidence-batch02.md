# Issue #107 EA-1 — WP-RUNTIME Evidence Batch 02

**Date:** 2026-11-12
**Verdict:** local fixed-matrix progress, **WP-RUNTIME overall NO-GO**
**Frozen tested Runtime SHA:** `6e25233e80aa30a45b3ba645bda0ececae9266c6`
**Prior EA-1 evidence SHA:** `a6532c705cbe2d2403d3c2522391f1b3b6bd7541`
**Implementation code:** original runtime implementation remains unchanged from `c17eeb49cc1d95b39f16c796edcd403d9b0a5ec2`. This batch adds test-only fixtures and reusable conformance test helpers; it does **not** alter Kernel, Agent or Workflow execution paths.

Matrix: `docs/issue107-execution-acceptance-matrix.md`
Ledger: `docs/issue107-execution-acceptance-ledger.csv`
Durable logs: `docs/audits/evidence/issue107-ea1-b02/`

## Fixed-scope outcomes

| Category | Prior batch | After Batch 02 |
| --- | ---: | ---: |
| Locally certified matrix PASS cells | 11 | **25** |
| UNRUN cells, including partial evidence | 133 | **119** |
| UNRUN cells with retained partial evidence | 21 | **13** |
| Global EA-1 strict acceptance | NO-GO | **NO-GO** |

All 32 previously evidenced matrix cells (11 PASS, 21 partly evidenced UNRUN) were actually re-executed on the new test-only SHA, and their ledger source SHA/log references were updated to Batch 02. The old Batch 01 report remains immutable historical evidence, not a pass on the new SHA.

### 14 newly certified, exact phase-specific cells

| Cells | Verified invariant and controlled timing |
| --- | --- |
| `EA1-S-CTL-P0` | **Original Store pre-admission:** foreign Tenant/Actor/Thread, missing/terminal parent, unsupported Run Kind, duplicate ID, zero/stale/future expected Revision produce no unauthorized Run or Grant. Native real PostgreSQL stale-read interleaving repeated 5×. **Does not claim Host Model/Tool route checks.** |
| `EA1-WR-S-P3`, `EA1-WC-S-P3` | **P3 controlled cut:** actual Root/Child Workflow external Effect receipt is already persisted while original Store `MarkChildExecutionSettled` is paused. No release, physical duplicate or terminal state mutation permitted before Mark; original callback completes after the barrier. |
| `EA1-WR-Q-P3`, `EA1-WC-Q-P3`, `EA1-S-CTL-P3` | **First-party / direct Store P3:** a clean persisted result alone cannot authorize `ReconcileChildPhysicalGrant` or direct `FinishChildExecution`. Original Grant persists before Mark and retires only when the legitimate callback settles. |
| `EA1-WR-S-P4`, `EA1-WC-S-P4` | **P4 Workflow Start:** real PostgreSQL 16 and native SQLite simulate committed Mark + lost ACK; a new independent Store / Workflow Runner recovers the original Root or Child without redispatch. |
| `EA1-WR-Q-P4`, `EA1-WC-Q-P4`, `EA1-S-CTL-P4` | **P4 recovery / Store proof:** fresh PostgreSQL connection and reopened SQLite use only the exact trusted original persisted marker; a forged StartRevision cannot retire it. Child parent cancellation succeeds only after legitimate recovery. |
| `EA1-AC-S-P4`, `EA1-AC-Q-P4` | **Child Agent post-Mark ACK loss:** original Agent Model invocation completed; independent PostgreSQL / SQLite Store and first-party Agent can safely recover without model redispatch. |
| `EA1-WC-S-P2` | **Child physical unknown Pending:** Root/Child conformance on Memory, native SQLite, GORM adapter and real PostgreSQL retains an unknown remote Effect Grant; an actual parent-bound Child Workflow test proves earlier `PhysicalNoEffect` from a different Attempt cannot authorize a later ambiguous send. |

Full commands, exact Store backend, evidence paths and reviewer constraints for each cell are in the CSV ledger.

### Test sources added to the fixed EA-1 matrix (no new review dimensions)

- `go/agent-runtime/conformance/ea1_p0_source_admission.go` — P0 source anomalies and exact revision acceptance on the **same** four Store implementations.
- `go/agent-runtime/workflow/ea1_p0_attempt_origin_test.go` — actual Root and Child Workflow, Attempt 1 true no-effect, Attempt 2 post-send unknown: prior proof must not cross attempts or release the unknown Grant.
- `go/agent-runtime/conformance/ea1_receipt_before_mark.go` — deterministic P3 barrier after receipt persistence but before physical Mark, including premature first-party Reconcile and direct Store Finish denial.
- `go/agent-runtime-postgres/ea1_workflow_ack_recovery_real_test.go` — P4 native PostgreSQL two separate DB connections, Root and Child post-Mark ACK lost, fresh Runner, forged revision denial and no replay.
- Memory, SQLite and GORM adapter test registries plus real PostgreSQL test registry now invoke these conformance suites explicitly.

### Repeatability and baseline

All tests below executed against `6e25233e80aa30a45b3ba645bda0ececae9266c6`, with `-race`:

- P0: Memory, native SQLite, GORM adapter, PostgreSQL 16; native PostgreSQL P0 repeated **3×** and the independently controlled stale-revision transaction repeated **5×**.
- Attempt identity: Root and Child Workflow attempt-local no-effect proof repeated **3×**.
- P3: Memory, native SQLite, GORM adapter, native PostgreSQL 16; real PostgreSQL P3 Root/Child barrier repeated **3×**.
- P4: native PostgreSQL Root/Child lost-Mark-ACK/reopen repeated **3×**; native SQLite Root/Child and Agent recovery also passed.
- The entire Batch 01 directed test battery was rerun on the exact new SHA, not copied from old logs.
- Four Runtime modules' complete `go test -race -count=1 ./...` passed (Core **26 packages**, Harness, PostgreSQL, SQLite); four lint scopes reported **0 issues**, and `node scripts/check-beta.mjs` passed.
- Independent external Go consumers typechecked and passed `-race`; the Harness consumer reports **[no tests to run]**, so it is counted as compile/ABI evidence ONLY.

A disposable local PostgreSQL 16 container was used. No production DB, live provider, billing, PR, push or merge was involved.

## Intentionally open (do not over-certify)

- **Host frozen source/route/Tool/limits/Context** still belongs to EA-1 H-FROZEN and WP-GAOGE; Store P0 cannot certify it.
- Root Workflow `ResolveWait` P0 was not covered by a Root-specific expected-revision controlled interleaving, and a Child Wait test is not a substitute.
- P3 `Resume` or `Cancel`/compensation paths have distinct entrypoints and cannot inherit `StartRun` P3 results.
- Root Agent's long-lived Model/Tool and Host middleware has a specialized ExecutionLease contract and does not become generally certified by Workflow's P3/P4 tests.
- All matrix `Gxx` frozen gap targets continue to exist where an applicable timing or identity vector lacks a direct assertion; the frozen 24-entrypoint × 6-phase acceptance scope is unchanged.
- Gaoge #69 original MCP ProviderReplay failure, SDK gitlink alignment, PR-head required CI and production schema preparation remain separate blockers.

## Reproducibility and evidence integrity

```bash
python3 scripts/check-issue107-matrix.py
python3 scripts/check-issue107-matrix.py --strict   # MUST return NO-GO while cells remain UNRUN
cd docs/audits/evidence/issue107-ea1-b02
shasum -a 256 -c SHA256SUMS.txt
```

`COMMANDS.tsv` lists the original command, phase/backend and frozen SHA for each log. Real PostgreSQL connection details have been redacted; `ORIGIN.txt` binds logs to exact test source and isolation. The repository ignores `*.log` by default, so evidence logs must be **explicitly version-controlled**, not merely mentioned in the ledger.

**Batch 02 result: 25 signed local PASS / 119 UNRUN, WP-RUNTIME remains NO-GO.**
