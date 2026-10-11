# SDK Error Governance P2: Implementation Candidate & Release Handoff

- Date: 2026-10-11
- Status: **SDK code candidate; release/host adoption NOT authorized**
- Branch/worktree: `feat/error-governance-p2-sdk` at `/Users/mini/Coding/Gaoge-Agent-Runtime-error-governance-p2`
- Base: `origin/main@1669ffdc`, published `0.1.0-beta.16`
- Design: [ADR-0007](adr/0007-classified-failures-and-error-diagnostics.md), host Gaoge ADR-0113
- Candidate code HEAD before this audit: `44b8115`
- **No merge, push, tag, publish or Beta version bump has been performed.**

## Completed SDK implementation

| Slice | Commit | Evidence |
| --- | --- | --- |
| Design / boundary | `580cbf0` | SDK ADR-0007, architecture index |
| Typed failure facts | `89048a3` | SDK model opt-in `ModelFailureCode()`, exact allowlist and `NewClassifiedError`, Agent final non-retryable/retry-exhausted code, original error chain preserved |
| Ordinary HTTP protection | `63779bc` | Owner Run/Snapshot/Cancel, Workbench, Harness Turn/Invocation/Subtask, Feed, Kernel Events, item payload and safe failure messages; terminal `cancel.json` fixture and CHANGELOG |
| Workflow trace | `8291702` | Owner-accessible Workflow trace now bounds potentially untrusted plugin errorCode |
| Nested diagnostics | `44b8115` | Remove nested provider errors, HTTP auth headers/credentials and diagnostic objects from SDK HTTP JSON projections |
| This audit | next independent documentation commit | Explicit release dependency and remaining permissions scope |

The Kernel retains original `Run.ErrorDetail` and Harness error facts internally. SDK HTTP projections preserve status, revision, legitimate user goals/content/results, bounded failure code, safe summary, ordinary usage/accounting and CAS identities. They do **not** grant debug access to ordinary owner requests or change physical model retry logic.

## Security boundary verification

- Unauthorized/cross-tenant Run access continues to use the existing object-level policy. The new SDK projections reduce what an **authorized owner** receives, not who is authorized.
- **Authenticated ordinary Run/Workbench/Harness/Invocation/Subtask/Feed/Journal**: public error fields are bounded and free-form `ErrorDetail` / `CancelError` / `debug` bytes are scrubbed at the Go HTTP edge.
- Raw `Run.State`, Workbench Section/Timeline/Topology JSON and Harness item payloads are projected with a recursive reserved diagnostic-field scrub. Normal model-generated content, tool result data and user-authored goal are intentionally **not** indiscriminately redacted; arbitrary user-authored text is not an administrator error log.
- Owner Workflow Trace error code strings are bounded even if an effect adapter inserted arbitrary strings.
- **Privileged diagnostic extraction is NOT implemented as a new HTTP endpoint**. No `debug=true` query flag, route or role-bypass exists; a future admin/debug route requires explicit host role+object authorization, audit and data-retention review. The raw details remain accessible through existing authorized server-side operational processes.
- No full guarantee is asserted for arbitrarily structured business content or Model/Tool outputs; they are a separate intentional data channel. Security testing here targets SDK diagnostic facts and reserved error fields.

## Verification

| Check | Result / reproducible evidence |
| --- | --- |
| `go test ./model ./agent` classified failures | PASS, physical retry count, context preservation, error chain and unknown-code negative cases |
| HTTP Go tests and real Gin owner-route tests | PASS: no injected `Bearer sk-PRIVATE` in Run, Workbench, Kernel journal, Harness snapshot/feed and Workflow trace |
| HTTP wire fixture | PASS; `cancel.json` changed to static safe cancellation summary |
| SDK HTTP lint | PASS, 0 issues |
| SDK targeted Go `-race` | PASS, `.cache/error-p2-sdk-race.log` |
| Full `make check` on `63779bc` | PASS / 0, `.cache/error-p2-sdk-exact.{log,exit}` |
| **Exact final code `44b8115` full `make check`** | **PASS / Exit 0** on exact `HEAD=44b8115`, `.cache/error-p2-sdk-candidate-gate.{log,exit}`; SDK Go/HTTP, lint, TS, wire, Beta.16 isolated consumer |

## Release dependencies and limitations

1. **SDK publication is a separate approval gate**. On explicit authorization: final merge audit → host-compatible SemVer prerelease increment (new Beta), coordinated 9 Go modules and TypeScript client cohort → pack/release gate → push/tag/publish and remote verification.
2. **Gaoge production pin remains Beta.16**. Do not use the worktree SHA, `go mod replace`, local TS symlinks or submodule source copying in a production build. Host can currently validate the Gateway cause partition and UI safety independently; it cannot yet claim durable Text Agent typed cause integration or safe direct SDK HTTP network responses on published Beta.16.
3. **Next host step after SDK release**: use official SDK `model.NewClassifiedError` from the Gaoge Text model adapter when a typed upstream cause is confirmed, update the full SDK cohort pin, validate Gateway authentication/authorization/billing causes and physical request attempts, ensure client-only public Run/Workbench reads are safe, run full production build and mock/live E2E.
4. P3 continues with administrator investigation UI, Provider Request/Invocation/Trace correlation, retention/alerting and physical-attempt vs ledger metrics. This SDK phase adds no duplicate ledger, compatibility bridge or recovery state machine.

## Decision

**SDK candidate: full candidate `make check` PASS, not released.** Independent final SDK merge audit, Beta prerelease preparation and publish still require separate authorization. **Cross-repository P2 must remain RELEASE-BLOCKED** until the coordinated new Beta is published and host integration is demonstrated.