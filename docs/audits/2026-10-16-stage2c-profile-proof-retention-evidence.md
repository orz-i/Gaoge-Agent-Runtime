# Stage 2C — Proof retention and embedded Profile boundary: evidence-only gate

- **Engineering date:** 2026-10-16
- **Branch:** `governance/kernel-runtime`
- **Baseline:** `5d7a6c691ead14cc6423646250a4f8a715dd736b`
- **Commits:** `1840cc2`, `a4d8e02`, `766a77c`
- **Architecture:** ADR 0008 **Accepted**, ADR 0009 **Proposed**, unreleased SQLite Schema **v1**
- **Decision:** **GO** for these narrowly demonstrated regression contracts; **NO-GO** for full Stage 2C, production proof compaction, or shared multi-Profile authorization.

## Scope and evidence-led decision

The next proposed governance items were (1) an authenticated shared-Profile facade and (2) retention/GC for ambiguous Tool effects. Inspection of the current code did **not** uncover an existing production GC that expires unresolved `ToolDispatchID`, `runtime_commit_proofs` or pending Run state. Embedded V1's **supported default** remains one protected SQLite file per trusted Profile; the current HTTP host adapter already has a host-supplied principal seam and `OwnerRunAuthorizer` for object-level routes. Raw Kernel Run-ID ports remain **trusted internal** and are not authorized multi-Profile endpoints.

Without an identified multi-Profile product host or an actual retention implementation, adding a universal policy service, new tombstone table or compaction state machine would assert safety without a failure proof. Instead this increment locks down three observable properties using existing public behavior and persistence. **No production code was changed.**

| Evidence | Fixture and result | What it does not establish |
| --- | --- | --- |
| **Ambiguous Native COMMIT after simulated extended offline time** | The existing `TestNativeUnknownToolReconcilesAcrossProcessesWithoutReplay` now runs five separate OS test-process phases: start, future-clock offline check, blocked, provider reconciliation, and historical terminal replay. It checks the persisted exact `ToolDispatchID` and SQLite revision proofs, confirms the still-uncertain Tool returns `agent.ErrToolOutcomeUnknown` without executing the Handler, then reconciles using a provider-journal lookup. One physical Tool effect is recorded across all phases. | The clock is **simulated** two years ahead, not two years of actual offline storage. No GC ran; this is **not** proof that a future GC or backup/restore implementation safely retains and replays all outstanding identities. It does not prove an actual Provider's physical exactly-once behavior. |
| **Finite Queue DLQ and explicit operator replay** | `TestNativeUnknownToolDeadLetterRequeueKeepsCommitProof` exercises the real Memory Queue Worker with MaxAttempts=1. Two unverified dispatches Dead Letter, the operator explicitly requeues, and the Native Tool intent/Signal remain authoritative while the parent Run stays running. After the verifier independently confirms COMMIT, the third delivery ACKs and the parent Run completes. Total Tool Handler calls stay at **one**. | This tests the in-process Memory Queue, **not** Redis persistence over power loss, deleted Queue Jobs, or a production operator UI. DLQ is a transport status, not a new durable Kernel success/repair authority. |
| **Default private Profile file boundary** | `TestSeparatePrivateProfilesKeepRunIDsAndRecoveryIndependentAfterReopen` creates two independent protected SQLite databases with the **same Run ID** but different actors, state and events. Both Stores close/reopen independently; overlapping IDs remain isolated, and a Run existing only in Profile A is not visible in Profile B. | This is physical file isolation under the documented **one-file-per-Profile** default. It is **not** a shared-DB authorization test and does not authorize raw SDK `Resume`, `Cancel`, approval, signals, Tool calls or ReadyWork claims for different Profiles. It also does not kill/restart the OS process in this particular test. |

## Verification executed

- **PASS:** Targeted `go test -race -count=30` for Native DLQ replay.
- **PASS:** Targeted SQLite OS-process proof recovery `go test -race -count=8`, followed by `go test -race -count=3` on proof recovery and isolated Profile Store reopen.
- **PASS:** `make metadata fmt-check tidy-check go-vet go-test` across **10 Go modules**.
- **PASS:** `make go-race` on all modules and `make go-lint` (**0 issues for every module**).
- **PASS:** `make ts-lint ts-typecheck ts-test ts-build eval-smoke benchmark-smoke otel-e2e-smoke a2a-product-check release-check`.
- **PASS:** `make integration` using disposable PostgreSQL 16 and Redis 8, including existing real native signal/provider recovery and Store/Queue Conformance. The service containers/volumes were removed. The **new** DLQ and SQLite Profile tests run under the Go checks, not in the PG/Redis integration module.
- Local logs: `/tmp/gaoge-stage2c-proof-profile-{go-base,go-race,go-lint,nongo,integration}.log`. Each command group returned exit 0; no combined `make check` exit code is asserted.

## Code and minimality review

Reviewed the diff against `5d7a6c6`, the Native Agent `ToolDispatchID` / `ReconcileToolOutcome` paths, SQLite commit proofs/ReadyWork schema, Queue Dead Letter semantics, and the HTTP owner authorization boundary. The three added/extended test files are the **only** source modifications; no Runtime/Store API, DDL, Schema version, lifecycle, new dispatcher, persistent queue or generic ControlCommand surface was added. No new P0/P1/P2 production-code defect is established by these tests. This is an evidence-based **manual** review, not a claim of an unavailable independent automated/peer audit.

The existing SQLite `runtime_commit_proofs` are revision-keyed commit fingerprints; they are **not** a Provider-side effect receipt or the authorization authority. Native replay safety in this test comes from the persisted Agent `ToolDispatchID`, Store execution fencing and trusted result verification. Future GC must not assume that a later Run revision or elapsed TTL proves all pending external effects are settled.

## Remaining Stage 2C acceptance and next decision

**Not ready:** a shared SQLite database across mutually untrusted Profiles is explicitly opt-in. It requires a *real host* with authenticated principal/active-Profile mapping and authorization against persisted Run ownership for **every** read, mutation, signal, approval, control, work claim, Tool/catalog and secret access, tested across process restart. A test-only facade would not prove that such a product host exists. The separate-file profile proof cannot be substituted for this gate.

**Not ready:** safe compaction/GC requires an actual retention contract: durable, operation-scoped settlement/tombstone identity, replay/backup/restore behavior, and an authenticated operator repair procedure. No existing feature-specific GC has passed those adversarial tests. Do **not** add a generic tombstone or new durable state before identifying a concrete retention policy and failure window.

**Stage ruling:** this Stage 2C verification increment may close locally (**GO for test coverage**); full Stage 2C and production-grade multi-Profile / multi-worker or exactly-once certification remain **NO-GO**. Do not infer Stage 3 eligibility, accept ADR 0009, or upgrade unreleased SQLite v1. No push, PR or merge is authorized.
