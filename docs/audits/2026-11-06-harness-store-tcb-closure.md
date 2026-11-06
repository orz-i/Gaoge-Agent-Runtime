# 2026-11-06 — Harness Store TCB convergence and owner-integrity acceptance

## Immutable starting point

- Prior independent convergence audit: `governance/agent-provider-quality-20261105@e929b4853b3129f388a2044c62c836a439227c4e`
- Evidence: `/private/tmp/gaoge-convergence-audit-e929b48/INDEPENDENT-CONVERGENCE-AUDIT.md`
- The audit's conditional finding required a privileged custom Harness Store to *consistently fabricate* the original Session Actor and root Invocation Actor/InputHash in returned data. Actual project Memory/PostgreSQL Stores were not compromised by that test. It also found no new Provider receipt/binding regression.
- Existing two untracked, user-owned OpenAI probe scripts are excluded from commits and must not be modified.

## Accepted security boundary

[ADR 0017](../adr/0017-trusted-harness-store-and-frozen-owner.md) selects the simpler, explicit trust model:

- Host is the authenticator for callers and tenant-scoped HostThread, including all public Harness/Runner entrypoints.
- Harness Store is a trusted privileged integrity source for the original immutable Session(ID, HostThread, Actor), Turn owner, and root Invocation Input/Hash/Run association. Memory and PostgreSQL/GORM stores are authorized implementations. The Store contract includes strict idempotent replay, CAS, copy-on-read and unchanged historical owner after crash/restart.
- Kernel Store/Run Actor/Thread and original frozen root Invocation authorize physical native execution; independently verified response ID and Usage govern Provider receipts/charges.
- A custom Store able to **continuously forge coherent Session and Invocation values** violates this privileged interface. The SDK cannot cryptographically recover its historical owner by re-reading two records from that same compromised source. Without an external authenticator/signer and a deliberately changed architecture, this scenario is outside the supported threat model. Do not label that red test a normally reachable cross-tenant P1.
- Defenses against stale, one-shot or inconsistent Store receipts remain intact, as do Agent's physical Kernel Run checks and unknown-effect/no-replay fences. Do not add an alternate Host intent router, signer/key registry, persistence mirror, compatibility shim or extra per-read Kernel query.

## Invariants × paths × exception classes

| Invariant | Paths | Exception / proof |
|---|---|---|
| I1: immutable authenticated Session owner | Create/replay/Load/Resume across Memory, SQL-compatible & real PostgreSQL | Other Tenant/Actor, other HostThread Kind/ID, 24 concurrent hostile replays; no reassignment |
| I2: root Invocation Input/Hash and owner cannot be rewritten | Create/replay/GetByID/GetByExecutionRef/Update-CAS | Seeded 96 cases with altered Actor, Thread or version and correctly recomputed InputHash; rejected by both genuine Store implementations |
| I3: valid execution progress must retain same owner | Root Accepted → Running → retry/restart/terminal | Legal CAS progresses status but never mutates frozen Actor/Thread/Input/Run mapping; stale CAS rejected; prior retry/recovery suites retained |
| I4: response identity & billing receipts | OpenAI/Anthropic unary/stream/reject/partial/agent recovery | Prior independent seeded 1000-case ResponseID/binding fingerprint property suite and receipt/refusal/Usage/replay tests passed at audit base |
| I5: frozen ModelOptions | Host/Role → ConfigSnapshot → Agent → Provider | Prior independent seeded 1024-case JSON duplicate/folded/ordering matrix PASS; same capability/effort checks maintained |
| I6: physical unknown-effect cannot be inferred away | Run, Tool, Model, Session Resume, cancellation and cross-connection retry | Existing normal/fault suites and real PostgreSQL/Redis integration passed, alongside the new owner integrity tests |

The new tests are deliberately run against **real Store implementations** rather than the previous malicious adapter. Those remain in the prior audit evidence as out-of-TCB threat-model probes, not as silently ignored failures.

## Added tests and original evidence

- `go/agent-runtime-harness-postgres/trusted_owner_store_test.go`:
  `TestTrustedHarnessStoresFreezeSessionAndRootInvocationOwners` runs identically against `harness.NewMemoryStore()` and the SQL-compatible store fixture. Covers deterministic ID, 24 concurrent same-ID malicious Session replay attempts, seeded 96-case root Actor/Thread/version mutation with *recomputed* hash, indexed owner reads and clone isolation, valid status CAS and stale revision rejection.
- `go/agent-runtime-harness-postgres/trusted_owner_real_test.go`:
  `TestRealPostgresTrustedSessionRootOwnerAcrossConnections` uses dedicated isolated PostgreSQL schema and two independent GORM connections, runs the Store matrix, additionally validates cross-connection original owner and concurrently attempts 16 unauthorized Session/root owner mutations; all reject without mutating the original persisted identity.
- Full source change is intentionally minimal: only Store-interface security comments and the SessionClient contract comment, **no runtime behavior, status/DDL or wire/schema changes**. Production identity checks added in the earlier correction are retained.

## Verification and admission status

- Focused owner tests: PASS; `go test -race` on Memory/SQL-compatible owner matrix PASS. Full Harness, Provider and Harness-Postgres `go test -race ./... -count=1` also PASS.
- `make metadata fmt-check tidy-check go-vet go-lint go-test`: PASS across all 12 Go modules; 0 reported lint issues.
- Real PostgreSQL/Redis suite: PASS, `make integration` exit 0 on the actual local branch; the new `TestRealPostgresTrustedSessionRootOwnerAcrossConnections` passed. Project-owned Docker test containers, volumes and network cleaned.
- All newly added tests have deterministic random seeds and isolate records by Store/database; they do not need real model API secrets.
- Full quality, coverage, security, GitHub PR-head CI and independent final-commit admission status should be reported only after running those commands on the **locked final commit**. A previous branch's passing CI does not certify this change.

## Explicit residual risks and scope

1. A fully compromised privileged Harness Store/Kernel Store or an untrusted Host with direct DB mutation credentials is outside ADR 0017's security guarantees. Products requiring zero-trust storage need an explicitly superseding ADR and new externally authenticated owner protocol.
2. Live third-party gateway and paid Model API billing/reasoning replay are not established by synthetic HTTP and offline tests; their routes remain individually capability-certified.
3. SDK cannot infer authorization for anonymous HostThread; the Host must authenticate the caller and scope HostThread per tenant.
4. Node audit moderate advisories and Go required-module advisories not reached by current symbol calls remain tracked as previously noted.
5. Missing CI at the **actual immutable PR Head** remains a merge blocker regardless of local tests.

**Merge decision is separate:** local tests and a scoped trust-boundary ADR close the repeated synthetic malicious-Store debate, but a future independent review of the real PR diff and its required GitHub checks is still mandatory before merge.
