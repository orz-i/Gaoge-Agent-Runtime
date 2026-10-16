# Stage 1 SQLite Audit — Remediation and Corrected Final Contract

- **Status:** Stage 1 single-executor code-audit fixes applied; **not a production/mobile/multi-worker release certification**
- **Date:** 2026-10-15 (project governance date; local Mac timestamps can differ)
- **Base commit:** `main@44785f85d3117e9629c34ed48c7a6b458dbfbfab`, including uncommitted Stage 0/1 work
- **Implementation context:** [Stage 1 SQLite prototype](2026-10-15-minimal-runtime-stage1-prototype.md)
- **Architecture:** [ADR 0008](../adr/0008-minimal-agent-runtime-architecture.md)

## Audit findings and current code

| Issue | Final implementation | Contract verification |
| --- | --- | --- |
| **P1: reopening a missing database silently creates an empty Run Store** | Explicit `CreateNew` initializes a private, unpublished temporary SQLite file and publishes its complete closed/checkpointed main DB **only via atomic no-overwrite hard-link**. `Open`/`OpenExisting` remain reopen-only. Interrupted initialization cannot leave a published version-0 file. Parent directory sync failure is surfaced as uncertain publication durability; callers must inspect/reopen, not overwrite. | `TestCancelledCreateDoesNotPublishAndCanRetry`, `TestConcurrentFirstCreateNeverOverwrites`, `TestPrivateCreateAndFailClosedReopen` |
| **P1: database default permissions `0644`** | Private Profile parent `0700`, database and WAL/SHM `0600`, symlink rejection; initial destination cannot reuse orphan sidecars. Temporary initialization is inside the protected Profile dir. | `TestPrivatePathAndSymlinkRequirements`, `TestCreateNewRejectsOrphanWALWithoutTouchingDatabase` |
| **P1: terminal Run cleanup drops unconsumed Outbox / quarantine history** | **Removed** terminal-status based deletion. Every committed `TransitionOutbox` record—including nonterminal historical wakeups, terminal facts, and quarantined records—remains until its **exact projector ACK**; an advanced Run revision is not proof of projection consumption. | `TestTerminalProgressPreservesUnconsumedOutboxAndProofs`, `TestTerminalRunPreservesQuarantineAndAllUnprojectedFacts`, common Kernel Store Conformance |
| **P2: schema shape not validated** | Exact versioned table/index DDL checked at Open. **Unreleased first Schema stays v1**; neither intermediate development additions nor adding a column requires v2. Incompatible older development layouts with the same v1 label fail closed without destructive rewriting. | `TestStage1SchemaStructuralValidation`, `TestSameDevelopmentV1WithObsoleteLayoutIsRejected`, version and corruption checks |
| **P2: corrupt ReadyWork blocks unrelated valid work** | Persistent *delivery-only* quarantine records invalid metadata/JSON. Eligible records continue without ACK/deletion of corruption. | `TestQuarantineCorruptReadyWithoutBlockingHealthyWork` and `TestPoisonAtPageHeadDoesNotStarveLimitOne` |
| **P2: valid JSON modified to `[]` silently drops required wakeup** | Immutable ReadyWork identity, Run, kind, status, revision, committed time and raw payload have a length-separated SHA-256 fingerprint. Claim checks this fingerprint **and** verifies `NeedsTransitionProjection`, including the exact work ID. Invalid records are retained and quarantined, not ACKed. A checksum detects accidental modification, **not malicious modification by a full DB writer**. | `TestWellFormedButModifiedReadyEventsAreQuarantined` and `TestInvalidProjectionIsIsolatedEvenWithRecomputedFingerprint` |
| **P2: `UnixNano` overflows for far-future timestamps** | Persisted timestamps, wakeup/due time, lease expiry and retry deadlines are range-checked; invalid values fail closed via `ErrInvalidTime`. | `TestFarFutureRetryAndLeaseDoNotOverflow` and `TestLeaseExpirationBeyondNanosecondMaximumIsRejected` |

## Corrected development Schema policy

The original local experiment and this **still-unreleased** Stage 1
module share the same **development format `PRAGMA user_version=1`**.
An intermediate local patch briefly labeled its evolving layout v2;
that bump was **reverted after review**. Schema version increments
are governed by published persistence compatibility and an actual
migration contract, **not by every internal DDL edit**.

Strict Schema definition checks remain mandatory: older private test
databases that bear v1 but lack fields such as `payload_hash` fail
with `ErrUnsupportedSchema`. They are never silently reinterpreted,
deleted or migrated. Back up any experimental data before an
explicitly reviewed conversion. No released user data format was
altered by this development correction.

## Safe ReadyWork consumption

An intermediate local fix attempted to delete all nonterminal
ReadyWork once a Run became terminal, leaving only one final row.
**That optimization was unsafe and has been removed**: it erased
unconsumed projection facts and even already-quarantined incident
history. The correct Stage 1 behavior intentionally matches the
existing Memory/PostgreSQL `TransitionOutbox` semantics.

Consequently, a synchronous completed Agent may have several
unacknowledged historical ReadyWork rows. They are not permission to
re-execute Tools. A future verified projector must determine whether
each exact projection was consumed/superseded before calling
`AckTransition`. Preserving this history can increase storage until
a consumer exists; a Stage 2 progress/claim contract must address
legitimate retention and compaction without event loss.

**Quarantine is still transport-only in Stage 1**. It does not
override Run state, authorize direct `Resume` or implement the
Run-level execution fence/poison workflow required in Stage 2.

## Atomic first initialization

`CreateNew` now creates a unique `.init-*` SQLite database in the
app-controlled private Profile directory, initializes and closes it,
rejects any remaining unpublished WAL/SHM, then atomically publishes
the main file via `os.Link` **without overwriting an existing DB**.
The publication uses the same filesystem and syncs the parent
directory; failure after linking leaves a **complete** existing DB
rather than a partially initialized one. Callers must handle an
uncertain publication result by inspecting/reopening the destination.

If the OS kills the process *before* publication, a private orphan
`.init-*` staging file may remain. It does not occupy the target
path or prevent a new attempt; such orphans require a deliberate,
owned-file cleanup policy, not blind deletion of unknown files.
Hard-link and directory sync support are requirements for the
experimental desktop adapter; this is not a Windows/mobile guarantee.

## Verification and acceptance boundary

The negative tests above are checked in under
`go/agent-runtime-sqlite/final_audit_test.go` and
`ready_integrity_test.go`, alongside the earlier regression and
cross-process recovery tests. The [adapter README](../../go/agent-runtime-sqlite/README.md)
contains current configuration and the explicit first-init API.

The Stage 1 contract remains **single executor, six statuses,
no autonomous local Pump and no Run execution epoch**. A linked
temporary DB, a file checksum, or a lease in the existing
`TransitionOutbox` cannot provide physical exactly-once Tool
semantics across competing runners. Mobile VFS/file protection,
power-loss evidence, full Stage 2 progress gating and Hosted parity
are separate acceptance requirements.

## 2026-10-16 — Merge-preparation P2 closure and branch cleanup

The previous Stage 0/1 working-tree artifacts have been organized on
`governance/minimal-kernel-sqlite-stage1-20261016`, without discarding
any historical research or changing `main`. The user separately
requested **segmented local commits**, not a remote push or release.

- **`92aed38` — Architecture/Stage 0:** ADR 0008, implementation contract,
  driver selection, baseline evidence and reproducible reference fixtures.
- **`f094aac` — Stage 1 prototype:** experimental SQLite Store, regression
  and subprocess recovery tests, documentation, and workspace integration.
- **Follow-up P2 patch:** validate the **entire existing v1 Schema before**
  calling `PRAGMA journal_mode=WAL`, so rejecting malformed development
  files cannot change their persistent journal mode. Add
  `TestIncompatibleV1SchemaRejectedWithoutChangingJournal` and
  `TestCompatibleV1SchemaMaySwitchBackToWAL` as opposing negative/positive
  tests. The still-unreleased Schema stays **v1**.
- **Standalone module P2:** add the published Core module's
  `v0.1.0-beta.22` checksums to `go/agent-runtime-sqlite/go.sum`.
  Keep this adapter **without a temporary Core `replace`** during the
  workspace `tidy-check`, and verify with
  `GOWORK=off GOFLAGS=-mod=readonly go test -count=1 ./...`.

**Final local checks:** `make check` PASS; the independent read-only
SQLite module test with `-race` PASS; `govulncheck v1.9.0` reports
no known reachable vulnerabilities; doc/link checks and
`git diff --check` PASS. The adapter remains an **experimental,
single-executor desktop prototype**, not a production mobile Store,
concurrent Run execution engine or an approved SDK release.

This audit record does not authorize a push, PR merge, public release,
or persisted-schema migration. Local commits were explicitly requested
for this governance branch.
