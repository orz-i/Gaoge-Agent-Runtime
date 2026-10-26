# 2026-10-25 — Agent / Harness / Kernel four-stage recovery correction

- **Baseline:** `main` at `6c88517d8a9b8b7a9616aade6a2c523bcec5932e`
- **Scope:** Harness root admission, direct Agent, typed top-level Features, Application capabilities, terminal projection, and status/Timeline reads
- **Policy:** pre-stable hard cut, no historical compatibility replay or replacement Kernel state machine
- **Execution:** local `governance/harness-recovery-hardcut` branch; no credentialed provider traffic

## Boundary assessment

Kernel remains authoritative for feature-neutral Run CAS, lease/epoch, journal, and transition outbox. Agent owns durable Model/Tool intent and receipts. Harness owns Session/Turn/Invocation/Item projection and explicit application-capability dispatch. A Harness Turn/Invocation lifecycle status must never by itself authorize a second physical Model, Tool, or host effect. Projection repair must be idempotent and derived from already committed evidence.

## Confirmed faults and corrections

| Finding | Reproduction | Corrective invariant |
| --- | --- | --- |
| Missing user/assistant-start Items after admitted Turn and failed Item append | `TestAuditStartReplayRepairsHostMessageItems` | Replay reads frozen HostRefs from ConfigSnapshot and restores stable Item IDs without relying on a mutable Host transcript |
| Changed CallAllowance accepted after Invocation admission, before Agent Run creation | `TestAuditRootReplayRefusesChangedExecutionAllowance` | Turn's frozen Config ID covers the complete admission digest |
| Committed terminal Turn skipped a missing terminal Agent Run Item on Refresh | `TestAuditRefreshRepairsTerminalProjectionCrash` | Terminal status cannot short-circuit harmless Item reconciliation |
| Application Invocation terminal while Turn remained running after projection failure | `TestAuditApplicationTerminalInvocationReconcilesTurnOnRefresh` | Completed Application Invocation is the terminal status authority for a root Application Turn |
| Application executor repeated after external result was lost before Invocation completion CAS | `TestAuditApplicationEffectDoesNotRedispatchUnknownOutcome` | CAS `accepted` → `running` is the only dispatch grant; ambiguous outcome stays blocked until read-only reconciliation |
| Changed ContextSeed accepted after Turn/Invocation admission, before Context checkpoint | `TestAuditAcceptedTurnRejectsChangedContextSeed` | Context identity/content is part of the immutable admission digest, never reconstructed from mutable caller input |

The original fault-injection cases were run with a Go test overlay outside the repository. They are now retained as regular tests with post-correction assertions. Additional cases verify read-only external reconciliation, concurrent Application admission, and bounded status/Timeline access.

## Four stages

1. **External effect safety.** `ApplicationCapabilityExecutor` cannot be re-entered on `running` Invocation. The durable CAS is recorded before execution; ambiguous error or commit loss returns `ErrApplicationOutcomeUnknown`. Optional `ApplicationCapabilityReconciler` observes the same frozen Invocation and does not dispatch side effects. Explicit `ErrApplicationNoEffect` is allowed only when the host proves no effect occurred.
2. **Frozen admission.** Compute the canonical complete StartRequest digest before the first Turn write. Store it in existing JSON ConfigSnapshot and bind it into immutable Config ID. Clone request-owned Context, allowance policies and HostRefs before hashing/dispatch. Changed replay cannot choose a new budget, transcript, message identity, or instructions.
3. **Single recovery explanation.** `Refresh` and root/terminal replays re-project deterministic Items from committed Config, Invocation and Kernel Run. Application terminal Invocation can repair a lagging Turn without re-entering the executor. No independent authoritative recovery object, queue or additional status was introduced.
4. **Bounded reads.** `LoadState` returns Session/Turn/config without full Timeline or Kernel execution; `ListTimeline` validates an ordered bounded Item page. Full `Load` remains available for explicit full-history use.

## Bounded in-memory performance evidence

Apple M4, Go `-benchtime=50x`, a terminal Turn with synthetic historical Items; these are microbenchmarks, **not production latency/SLO**.

| Extra Items | Full `Load` | `LoadState` |
| ---: | ---: | ---: |
| 0 | ~1.96 µs; ~6 KB/op | ~0.25 µs; 0 B/op |
| 100 | ~20.0 µs; ~105 KB/op | ~0.26 µs; 0 B/op |
| 1,000 | ~266 µs; ~1.24 MB/op | ~0.27 µs; 0 B/op |
| 5,000 | ~1.14 ms; ~6.71 MB/op | ~0.26 µs; 0 B/op |

The benchmark proves removal of complete-Item traversal from the new status path, not that database-backed stores have constant latency.

## Local verification and exclusions

- **PASS:** full ten-module Go tests, Go race suites, gofmt, tidy-check, vet and golangci-lint; HTTP/Provider/Kernel and Harness regression suites.
- **PASS:** Go clean-consumer local-cohort release check; TypeScript lint, typecheck, test and build; Beta metadata/release policy checks; deterministic evaluation/A2A/MCP scenario suites and benchmark smoke.
- **PASS:** GORM SQLite-backed Harness PostgreSQL Store round-trip of the admission hash and two frozen host message bindings; different admission digest conflicts against the original Turn.
- **PASS:** a pre-hard-cut running Agent Turn with missing admission proof is rejected before direct Resume dispatch; old records can still be inspected and reconciled without creating effects.
- **Still required for final production admission:** execute new unknown-Application-effect and projection crash tests using real PostgreSQL and cross-process restart (the local SQL codec test used SQLite), plus real Host effect-reconciliation acceptance.
- Real PostgreSQL/Redis/Docker integration remains **not verified** until the Docker daemon is available; do not claim production readiness on offline evidence.
- Host effect idempotency and reconciliation are host contracts, not guaranteed physical exactly-once execution by the SDK.
- Existing in-flight pre-hard-cut Turns cannot be granted an invented `AdmissionHash`; drain or isolate them under the operator-owned release gate in `docs/breaking-changes.md`.
