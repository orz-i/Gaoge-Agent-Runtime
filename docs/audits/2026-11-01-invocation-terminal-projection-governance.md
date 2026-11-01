# 2026-11-01 — Round 2: Invocation terminal projection and Store CAS authority

**Branch:** `governance/lifecycle-projection-20261101`
**Base:** `main@34ca9b3` (PR #84 already merged)
**Scope:** one observed Run → child Invocation → Turn/Item lifecycle inconsistency; no new Run states, separate recovery authority or performance claims.
**Status:** local implementation candidate; no push, PR, merge or release.

## Why this is a correctness issue

The Kernel Store owns the physical Run. Harness persists a root Turn and one
Invocation per admitted capability, while Items are immutable execution facts.
These are intentionally distinct status representations, not three versions
of one replaceable authority.

The root path in `runner.go:syncRuntimeSnapshotWithCancellationProof` already
rejected a Runtime observation that contradicted a terminal Invocation.
However `feature_invocation.go:syncChildInvocationSnapshot` reached the shared
`syncInvocationProjection` without a terminal fence. That helper could change
a previously `completed`, `failed`, or `cancelled` Invocation to an
unrelated outcome if supplied an older or conflicting Runtime observation.

The durable Store boundary was weaker too: `MemoryStore.UpdateInvocation`
and SQL `Store.UpdateInvocation` accepted these transitions at the current
expected Revision within the same Attempt. This potentially reopens an
already-settled child in the Harness projection even though the physical
Kernel Run is separately authoritative. It is not evidence that Kernel CAS
itself is broken or that external side effects were duplicated.

### Reproduction before the fix

Two new tests were run against the unchanged baseline and **failed as
expected** (exit 1). The checks used committed terminal Invocations and
attempted an exact-revision projection or Store update:

- `TestLifecycleProjectionRejectsStaleTerminalChild`: 5/5 terminal-to-other
  transitions were improperly accepted by the shared projector.
- `TestStoresFenceTerminalInvocationProjection`: the Memory and
  SQLite-backed SQL conformance implementations both accepted
  `completed → running`, `completed → failed`, `failed → running`, and
  `cancelled → completed`.

The original two red-test logs are retained only in the local temporary
engineering directory, not committed to the SDK.

## Bounded change

1. `syncInvocationProjection` now rejects a contradictory terminal status
   **before any Store update or new lifecycle Item**. Root guards remain
   in place; child callbacks share this fail-closed rule.
2. `MemoryStore.UpdateInvocation` refuses terminal-status rewrites under its
   existing mutex and Revision/identity CAS.
3. PostgreSQL/SQL `UpdateInvocation` adds a single conditional predicate to
   the **same atomic UPDATE CAS**. It accepts an update only when the stored
   Invocation status is nonterminal or matches the proposed terminal status.
   The predicate is not a stale separate `SELECT`.
4. Same-terminal metadata reconciliation remains supported and does not
   rotate an Attempt. `Store.RetryInvocation` remains the **only** explicit
   path for a new Attempt and stable new execution reference, with its
   existing original-effect, parent, child and cancellation fences intact.
5. Two existing tests that manufactured failed retries by directly rewriting
   an already-completed Team child were corrected. One now asserts that such
   a rewrite fails closed; the other creates a genuine child failure proven
   no-effect before checking that a terminated Kernel parent blocks its
   retry. The test-only helper that performed illegal terminal rewrites is
   removed.

**Compatibility:** This is an intentional pre-stable `Store` behavior hard
cut, not a new interface method or persisted format. Custom Harness Store
implementors must enforce the terminal constraint atomically with their
`UpdateInvocation` revision CAS. No schema/DDL/version bump, legacy bypass,
extra status enum, generic FeatureRouter, migration or compatibility shim.

## Validation and acceptance boundaries

- New targeted projection and Memory/SQL conformance tests: **red before
  correction; PASS after correction**.
- Harness and Harness SQL all-package unit tests: **PASS**.
- Existing retry-positive coverage (child Attempt rotation, accepted retry
  recovery, top-level Feature retry, SQL root and child RetryInvocation):
  **PASS**, repeated three times.
- Real PostgreSQL cross-connection regression added as
  `TestRealPostgresTerminalInvocationFenceAcrossConnections`; requires
  `TEST_POSTGRES_DSN` and tests that a second connection cannot overwrite
  terminal state with the correct old Revision.
- Repository `make integration`: **PASS, exit 0**. Started isolated
  PostgreSQL 16 and Redis 8 Docker services, ran the real-engine race,
  cross-connection, cancellation and continuation suites, including
  `TestRealPostgresTerminalInvocationFenceAcrossConnections` (**PASS**),
  then removed the isolated test containers/volumes/network. Existing
  development containers were not modified.
- Repository `make check`: **PASS, exit 0**. Covers all Go module unit/race/
  lint and format/tidy/vet gates, TypeScript lint/typecheck/46 tests/build,
  evaluation smoke, benchmark smoke, OTel smoke, A2A product checks, and
  the local-cohort clean-consumer release gate.
- `git diff --check` and documentation relative link validation: **PASS**.
- **Independent P3 test-coverage follow-up:** Added a permanent public
  `RefreshInvocation` regression in
  `go/agent-runtime-harness/lifecycle_projection_public_test.go`, rather
  than relying only on the private projector unit tests or the temporary
  independent-review Go overlay. Each fresh fixture records a terminal
  Completed, Failed or Cancelled child Invocation while its physical Kernel
  Run still reads Running, then checks two consecutive public Refresh calls
  fail with `ErrConflict` and leave Invocation Revision/Attempt, Turn,
  immutable Items and Kernel Run Revision unchanged. A separate positive
  control checks that an already-completed physical child can be refreshed
  twice without any additional lifecycle Item or Revision. The public API
  test passed **10 repetitions under `-race`**, and the surrounding
  lifecycle/retry tests passed **3 repetitions under `-race`**. A fresh
  repository-wide `make check` after adding this permanent public API
  regression also passed (**exit 0**). No production code or persisted
  status contract was altered by this P3 follow-up.
- No real Model Provider/gateway compatibility calls or `make beta`/release
  publication were part of this bounded lifecycle repair.

**Out of scope:** Forcing one shared Turn/Invocation/Run status enum,
resolving historical already-inconsistent persisted records without Host
proof, altering Kernel Run states, adding a global lifecycle ledger,
inventing automatic Tool retries, changing ProviderReplay, or declaring
physical exactly-once effects.

**Next review focus:** Verify all custom Store deployments implement the
atomic terminal CAS before rolling out this SDK candidate. Maintain
explicit root/child ownership proofs; do not downgrade lifecycle safety
merely to remove duplicated presentation status fields.
