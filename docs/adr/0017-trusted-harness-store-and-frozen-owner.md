# ADR 0017 — Trusted Harness Store and one frozen execution-owner contract

- **Status:** Accepted (unreleased beta.24 source; security trust-boundary decision)
- **Date:** 2026-11-06
- **Scope:** `Harness.Store`, Agent-oriented `SessionClient`, original root `Invocation`, Kernel `Run`
- **Context:** Follow-up to ADR 0015/0016 and the November 5–6 Session owner audit
- **Target review base:** `e929b4853b3129f388a2044c62c836a439227c4e`

## Problem: two Store records do not make independent authenticity

Multiple independent reviews correctly found that a *custom Harness Store* could fake `Session.Actor` on one read and return the real owner on later reads. The previous remediation started checking the frozen root Invocation Actor/Thread as well.

An adversarial case then continuously returned both a foreign `Session.Actor` and a changed root `Invocation.Input.Actor`, computing a matching ordinary SHA-256 `InputHash` and optionally changing invocation-list results. These return values were all supplied by the **same custom Store**. The SDK can observe contradictions when one read is correct, but it cannot cryptographically authenticate two values against each other if the source can forge both. The red test showed cross-actor bounded state and Timeline disclosure under that deliberately corrupted Store. It did **not** compromise the project's Memory or PostgreSQL Store, create an authorized physical Model/Tool call, or establish a remote exploit.

We will not claim that re-reading a continuously malicious Store proves original ownership. Adding another hash, redundant `GetSession`, or independent local state machine would increase complexity without providing a new trust root.

## Decision

1. **Harness Store is a trusted, privileged execution-integrity component (TCB).** It is not a source of arbitrary untrusted Provider/Tool/user-authored values. A supported Store implementation must persist and later return the exact original immutable owner of each Session, Turn, Invocation and Context/Config authority. It may return explicit storage errors; it must not silently rewrite ownership or choose a different caller identity on each read. The Host must not pass a hostile adapter as `Harness.Store`.
2. **Only one logical historical owner.** An authenticated Host chooses a tenant-scoped `HostThread` and `kernel.ActorRef`. The original committed Session is the canonical Host-thread-to-Actor owner for all its Turns. The root Invocation freezes the selected Actor and *Kernel Thread* for that Turn, not a second independently authoritative user identity. The Kernel Run carries the same frozen execution owner for native physical effects. These are consistency constraints across trusted persistence boundaries, **not** three cryptographic proofs that can defeat a fully compromised Store.
3. **Immutable writes and deterministic replay are mandatory.** `CreateSession` atomically rejects replay with different `Session.ID`/`HostThread`/`Actor`. `CreateTurn` atomically rejects changes to `Turn.ID`/`SessionID`/`HostTurn`/`ConfigSnapshotID`. `CreateInvocation` enforces a single root per Turn and rejects altered original `Input`/`InputHash`/`CapabilityKey`/`ExecutionClass`/`DefinitionVersion`. `UpdateInvocation` performs a revision CAS and **must not** change original Input, owner, immutable identifiers, or execution reference within an Attempt. `RetryInvocation` alone may rotate Attempt/Run ID using its existing durable fencing; the root Input/Actor/Thread remains original. All returned mutable bytes must be cloned.
4. **Normal authorization stays at Host entry, durable execution stays at Kernel.** The Host must authenticate the caller and authorize the tenant-scoped HostThread before calling `OpenSession`/any public Harness entrypoint; the Runtime does not do tenant directory lookups. `SessionClient` keeps existing read-time ID/Actor/frozen-root consistency checks, `LoadState` stays bounded and Kernel-free, and physical `Resume` continues to require frozen Invocation plus actual Kernel Run owner checks. Explicit `Runner.ResumeTurn` remains a Host-authorized capability; do not expose it directly to untrusted users.
5. **Trusted Store is part of the deployment security boundary.** Protect DB credentials, write permissions, backups/restores and host access; never grant other tenants raw read/write access to Harness/Kernel execution tables. Shared SQL connections must honor the Store's atomic CAS and uniqueness constraints across concurrent writers and process restarts. Third-party Store adapters must be contract-tested before production adoption.
6. **No alternative identity protocol.** No new signer/key registry, external owner mirror, Session token, Kernel status, durable schema field, compatibility bridge, duplicate Agent loop or per-read independent Kernel query is added.

## Explicitly supported and excluded threat models

**Supported:** User-controlled prompts, Provider SSE/JSON content, Tool arguments/results, wrong Actor/tenant in Host requests, stale or malformed Store receipts, accidental one-shot read inconsistencies, duplicate/reordered Call/Response IDs, colliding idempotent replays, concurrent Store operations, crash/restart, and physical effect outcomes unknown after cancellation or transport interruption. The existing model/Tool receipt and run owner fences continue to apply.

**Not supported:** A fully compromised privileged Host, Kernel Store or Harness Store that persistently fabricates coherent Session and Invocation reads (including rehashing Input), or an administrator/DB attacker changing underlying owner columns outside Store methods. Such an attacker already controls the trusted records that the SDK reads. Failing to prevent disclosure under this out-of-TCB attack model is not grounds for repeatedly claiming a new SessionClient P1. Recover by restoring authenticated owner state and rotating credentials; do not proceed with automatic side effects from suspect persistence.

If an actual product requires a **zero-trust Store**, this ADR must be explicitly superseded by a separately reviewed cross-layer authenticated-owner design with a signer/key holder outside the Store, a typed authenticated owner value and a clear bounded-read tradeoff. This is a separate architecture/security project and cannot be inferred from the current interface.

## Verification / convergence gates

- Added `TestTrustedHarnessStoresFreezeSessionAndRootInvocationOwners` in `go/agent-runtime-harness-postgres/trusted_owner_store_test.go` covering the genuine Memory and SQL-compatible Harness Store. A seeded 96-case matrix rejects changed Actor, Thread and root metadata even with a recomputed SHA-256 `InputHash`; 24 concurrent malicious Session replay attempts must never replace the prior identity. Indexed `GetInvocation` and `GetInvocationByExecutionRefID` return original cloned bytes; valid status-CAS progresses without owner drift.
- Added `TestRealPostgresTrustedSessionRootOwnerAcrossConnections` using isolated real PostgreSQL schema and **two independent Store/DB connections**, including 16 concurrent attempts to rebind Session Actor or root Invocation Input/Hash and subsequent cross-connection reads.
- Keep previous SessionClient fault-injection tests for isolated intermittent malformed/mismatched read receipts and physical Kernel-run protection, but classify the persistent-coordinated malicious Store matrix as **out of supported TCB threat model**, not a hidden pass.
- Run these suites with `-race`, the full project checks, real DB integration, clean-consumer tests, and required GitHub PR-head CI on a locked SHA before requesting merge approval. Do not call the PR merge-ready solely because this ADR is recorded.
- Risks not resolved by this ADR: full DB compromise, external Provider/gateway actual billing, live third-party rate-limit/replay behavior, and CI checks that have not run against the final SHA.

**Exit standard:** No unresolved P0/P1 *inside this explicitly accepted trust model*; targeted invariants and path/exception matrix documented and passing; independent reviewer signs off the frozen actual PR Head; mandatory GitHub checks succeed; missing external validations remain explicitly listed.
