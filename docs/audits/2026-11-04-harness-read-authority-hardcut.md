# Harness read-side Session / Turn ownership — I1 hard cut

## Scope and baseline

- Audited prior HEAD: `25818e81a515aae36d87839d299668f91cf7ae7e` on `governance/harness-agent-minimal-hardcut-20261104`.
- Original `main` comparison: `fb84fc1e56612bf912d8e252b1cd1b8d2625f45a`.
- Remediation code commit: `cf23a20e8ee10120452f8d43ccb3b4363b6e7f80`.
- Independent review identified one grouped, **pre-existing** conditional P1: a custom Store could return a different but persisted Session or Turn for `GetSession(requestedID)` or `GetTurn(requestedID)`, and `Runner.Load` / `Refresh` would display another tenant's Session/Turn. Both variants reproduced on prior HEAD and original main. The built-in Memory and PostgreSQL Stores are not known to produce these mismatches normally.
- This governance scope is **read-side owner proof only**. It does not change public SDK calls, Provider ResponseID, Model/Tool execution, Kernel CAS, Store schema, model replay format or Host intent selection. No compatibility alias or fallback interpreter was introduced.

## Trust boundaries / security invariants

1. **Requested key is authority:** a `GetSession` or `GetTurn` reply must satisfy the exact caller-requested ID and existing object validity rules. A valid foreign record cannot substitute.
2. **Host-derived identity is bound:** for the existing deterministic Host Session (`hs_` + 24 digest chars) and Turn (`ht_` + 24) namespaces, recompute `SessionID(HostThread)` and `TurnID(SessionID, HostTurn)` before accepting the read. A returned Turn cannot silently switch Session or HostTurn while preserving its ID.
3. **Frozen owner links:** for Host-derived Turns, read and prove the Session identity and canonical sealed Config, including `ConfigSnapshot.TurnID == requested Turn.ID`, **before** the caller may reconcile, retry, cancel, resume or dispatch a physical effect. `loadSnapshot` also verifies that its Session and Config belong to the Turn supplied by a caller.
4. **Stand-alone Store IDs:** the Store interface also supports explicitly caller-defined IDs outside the Host-generated deterministic namespace. They still require exact key equality and valid object shape, but do not claim a Host-derived cryptographic ID binding. This is part of the existing Store contract, not a compatibility or migration bridge.
5. **Authority exclusions:** these read checks are not a sandbox for a malicious Store that consistently forges same-key contents, ignores CAS, or returns fabricated physical receipts. `SessionClient` continues to require exact HostThread and Actor identity independently; direct Runner APIs still require Host authorization. There is no new persisted owner or side-channel proof.

## Implementation

- Added `loadVerifiedSession` / `loadVerifiedTurn` in the existing private `store_authority.go`; all 52 direct Harness production `GetSession`/`GetTurn` call sites now use them. Only these two private functions call `Store.GetSession` / `Store.GetTurn` directly.
- `loadVerifiedTurn` performs the Host-derived Session and Config cross-reference using the already established `loadVerifiedSession` and `loadSealedConfig`. No cloned Code path or parallel identity cache; all consumers share the one proof.
- `Runner.Load`, `Refresh`, `Cancel`, `ResumeTurn`, `LoadState`, `ListTimeline`, `SubscribeTurnFeed`, `ResolveApproval`, Invocation/Interaction/Task/Role paths, Context and Budget ownership reads now pass the shared read check before trusting an observed Turn/Session.
- `loadSnapshot` additionally verifies incoming Turn validity and Session/Config Turn binding before presenting Items, Invocation metadata, Model output or charge information. No physical retries are inferred from a failed read.

## RED → GREEN, property and integration evidence

- The independent external `go test -overlay` probes on `25818e8` reproduced **wrong-tenant results** in `Load(A)` and `Refresh(A)` after swapping `GetTurn(A)` to B, and in `Load(A)` after swapping `GetSession(A)` to B. Both tests failed pre-fix and pass post-fix under Race, with no repository test-file injection.
- Permanent `TestHardcutReadTurnAndSessionCannotBorrowForeignTenant` covers six types of Store abnormal reply: `redirect-turn`, `redirect-session`, a same-ID Turn pointed at another Session or another sealed Config, a same-ID Turn with changed HostTurn, and a same-ID Session with changed HostThread. Checks `Load`, `Refresh`, `LoadState`, `ResumeTurn`, `Cancel`, `ListTimeline` and ensures no extra physical Model invocation or mutation of the original completed Turn.
- Permanent `TestHardcutReadAuthorityStateMatrix` uses a fixed seed with 60 samples × 8 read/replay state transitions, randomizing six fault modes and `Load`, `Refresh`, `LoadState`, `ResumeTurn`. Combined regression suite passed five consecutive `go test -race` repeats. Original I2/I3 abnormal Config and Invocation-return tests remain GREEN.
- Harness full unit, Harness `-race`, Harness PostgreSQL module, Vet and Lint (0 issues) passed on the remediation code cohort before commit.
- Final SHA must still run all repository checks, real service integrations and independently audited PR-HEAD CI. Those are **not implied** by a local test result; record precise checked commits and missing gates at final delivery.

## Residual risks / limitations

- A custom Store can still violate its own serialization/transaction contract; an ID-consistent but entirely forged or tampered object cannot be proven against independent facts that were never retained. The new checks cover requested IDs and derived Host identity, not arbitrary malicious database behavior.
- The canonical Host-derived Turn check adds a verified Session and sealed Config read, increasing read-side Store I/O. This is deliberate fail-closed authority enforcement; the Kernel remains sole physical execution CAS authority.
- No guarantee of live Provider/gateway behavior, exactly-once network side effects, or remote production/mobile correctness. Those need separate tests.
- No new schema, compatibility alias, alternative persisted state or version bump; malformed pre-stable records now return `ErrConflict` instead of cross-owner presentation. Documented here under the existing Beta hard-cut policy.
